// Package httpapi implements the HTTP API described in api/openapi.yaml. The routing
// and request/response types are generated into internal/api; this package holds the
// behaviour behind them plus the middleware every request passes through.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/api"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/auth"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/storage"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/telegram"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/transcribe"
)

// MaxBodyBytes is the largest request body the server reads, whatever the route. Uploads of recordings have a
// much smaller limit of their own (see Server.uploadBodyLimit).
const MaxBodyBytes = 8 << 20

// StorageLimits are what one recording and one account may take. A minute of the watch's audio is about 140 KiB
// in the Ogg/Opus file the phone sends.
type StorageLimits struct {
	// MaxAudioBytes is the largest recording, as decoded bytes; a bigger one is refused with 413.
	MaxAudioBytes int
	// AccountBytes is the room an account's recordings may take. When a new recording does not fit, the account's
	// oldest ones are deleted to make room. There is one limit for every plan until there are paid ones.
	AccountBytes int64
	// WarnPercent is the share of AccountBytes from which the server tells the watch that storage is nearly full.
	WarnPercent int
}

// DefaultStorageLimits are what a server runs with unless a test or an operator says otherwise.
var DefaultStorageLimits = StorageLimits{MaxAudioBytes: 512 << 10, AccountBytes: 100 << 20, WarnPercent: 90}

// concurrentDecodes bounds how many recordings are decoded at once (when one is received and when one is played):
// decoding is the one CPU-heavy thing a request can ask for.
const concurrentDecodes = 4

// Pinger is the part of the database pool the health check needs.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Server holds what the handlers depend on.
type Server struct {
	Pinger Pinger
	DB     dbgen.Querier
	// Messages holds the recordings (the original files, one per message).
	Messages storage.Store
	Logger   *slog.Logger
	// DefaultServerURL is this server's own public address: the pairing page's link is built from it.
	DefaultServerURL string
	// Now supplies the time used to name stored files and to date sessions; nil means time.Now. Tests set it.
	Now func() time.Time

	// Tx runs a function in one database transaction (db.InTx). Needed by registration and password changes.
	Tx TxFunc
	// Hasher hashes passwords; NewHandler makes one with the default settings if it is nil.
	Hasher *auth.Hasher
	// Limits are the rate limiters (sign-in, pairing, uploads); NewHandler makes the default ones if they are unset.
	Limits AuthLimits
	// Storage are the size limits of recordings and accounts; NewHandler uses DefaultStorageLimits if it is unset.
	Storage StorageLimits
	// CookieSecure marks the session cookie Secure. On for HTTPS, off only for local development over http.
	CookieSecure bool
	// AllowedOrigins are other web pages (scheme://host[:port]) that the cross-origin check (crossOriginGuard, against
	// CSRF) lets through to the /v1 API. That is all they feed: no CORS headers (Access-Control-*) are ever sent, so
	// a browser still stops those pages from reading the answers, and their requests with a body in JSON are stopped
	// by the browser's own preflight. A front end in development is served from this server's own address (a proxy).
	AllowedOrigins []string
	// TrustProxyHeaders reads the client's address from X-Forwarded-For (only behind a proxy that sets it).
	TrustProxyHeaders bool
	// Transcription puts each new recording in the speech recognition queue; nil means recognition is off and
	// nothing is queued.
	Transcription *transcribe.Intake
	// TranscriptionWake tells the worker that there is something new in the queue (nil is fine).
	TranscriptionWake func()
	// Telegram is the bot people link their chat to; nil means the Telegram bot is off on this server.
	Telegram TelegramBot
	// TelegramDelivery puts each new recording of a linked account in the queue for its Telegram chat; nil means
	// the bot is off and nothing is queued.
	TelegramDelivery *telegram.Intake
	// TelegramWake tells the delivery worker that there is something new in its queue (nil is fine).
	TelegramWake func()

	// userCodes draws the short code of a pairing; nil means auth.NewUserCode. Tests replace it to force collisions.
	userCodes func() (display, canonical string, err error)
	// decodeSlots holds a token for every recording being decoded right now (see concurrentDecodes).
	decodeSlots chan struct{}
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

var _ api.StrictServerInterface = (*Server)(nil)

// GetHealth answers 200 while the database responds and 503 when it does not.
func (s *Server) GetHealth(ctx context.Context, _ api.GetHealthRequestObject) (api.GetHealthResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if err := s.Pinger.Ping(ctx); err != nil {
		s.Logger.Warn("health check: database unreachable", "error", err)
		return api.GetHealth503JSONResponse{Ok: false, Error: "database unavailable"}, nil
	}
	return api.GetHealth200JSONResponse{Ok: true}, nil
}

// NewHandler returns the complete HTTP handler: generated routes, JSON errors for
// everything that is not a route, request logging, and panic recovery. It fills in the
// defaults of the optional Server fields (Hasher, Limits).
func NewHandler(s *Server) http.Handler {
	if s.Hasher == nil {
		s.Hasher = auth.NewHasher(auth.DefaultParams, 4)
	}
	if s.Limits == (AuthLimits{}) {
		s.Limits = defaultLimits(s.now)
	}
	if s.Storage == (StorageLimits{}) {
		s.Storage = DefaultStorageLimits
	}
	s.decodeSlots = make(chan struct{}, concurrentDecodes)

	strict := api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			var tooLarge *http.MaxBytesError
			switch {
			case errors.As(err, &tooLarge):
				if strings.HasPrefix(r.URL.Path, "/v1/") {
					writeCodedError(w, http.StatusRequestEntityTooLarge, api.ErrorCodePayloadTooLarge, "request body too large")
				} else {
					writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
				}
			case strings.Contains(err.Error(), "decode JSON body"):
				writeBadRequest(w, r, "invalid JSON body")
			default:
				writeBadRequest(w, r, err.Error())
			}
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			s.Logger.Error("handler failed", "method", r.Method, "path", r.URL.Path, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		},
	})

	mux := http.NewServeMux()
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{ // registers the generated routes on mux
		BaseRouter: mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeBadRequest(w, r, err.Error())
		},
	})
	// The hand-written routes: they serve files and pages, which the generated JSON handlers are not built for.
	mux.HandleFunc("GET /v1/messages/{id}/audio", s.handleMessageAudio)
	mux.HandleFunc("GET /v1/messages/archive", s.handleMessagesArchive)
	mux.HandleFunc("GET /pair", servePage("text/html; charset=utf-8", pairHTML))
	mux.HandleFunc("GET /pair.js", servePage("text/javascript; charset=utf-8", pairJS))
	mux.HandleFunc("GET /pair.css", servePage("text/css; charset=utf-8", pairCSS))
	// Anything the generated routes do not claim - unknown path or wrong method alike - is a JSON 404.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	return recoverer(s.Logger, logRequests(s.Logger, noStoreV1(s.limitBody(s.crossOriginGuard(s.authContext(mux))))))
}

// noStoreV1 tells browsers and proxies not to keep any answer of the /v1 API - they are about one person's account,
// sessions and recordings, and errors included (it wraps the guards, so their refusals have it too). The handlers
// that serve files set a policy of their own afterwards, which replaces this one.
func noStoreV1(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// limitBody caps how much of a request body any handler can read.
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(MaxBodyBytes)
		if r.Method == http.MethodPost && r.URL.Path == "/v1/messages" {
			limit = s.uploadBodyLimit()
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// uploadBodyLimit is the largest body an upload of a recording may have: the base64 of the largest recording,
// with room for line breaks in it (some clients wrap their base64; in JSON each break is the four characters \r\n,
// which adds up to 6% for lines of 64 characters) and for the rest of the JSON. A client sending more is
// refused before the server has read it all.
func (s *Server) uploadBodyLimit() int64 {
	base64Len := int64(s.Storage.MaxAudioBytes+2) / 3 * 4
	return base64Len + base64Len/10 + 4096
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeErrorBody(w, status, api.Error{Ok: false, Error: message})
}

func writeErrorBody(w http.ResponseWriter, status int, body api.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeBadRequest answers 400. The /v1 API also names the problem with a machine-readable code; the only
// route outside /v1, the health check, takes no input and never gets here.
func writeBadRequest(w http.ResponseWriter, r *http.Request, message string) {
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		writeCodedError(w, http.StatusBadRequest, api.ErrorCodeInvalidRequest, message)
		return
	}
	writeError(w, http.StatusBadRequest, message)
}

// statusRecorder remembers the status code a handler wrote. It must stay transparent to http.ResponseController
// (see Unwrap): the handlers move write deadlines and flush through it.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

// Unwrap gives http.ResponseController the ResponseWriter underneath. Without it SetWriteDeadline, Flush and the like
// find nothing to work on and answer http.ErrNotSupported, which the archive download once ignored: its deadline
// never moved, and a download over a slow link was cut off by the server's WriteTimeout.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) WriteHeader(code int) {
	if !r.written {
		r.status = code
		r.written = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.written {
		r.status = http.StatusOK
		r.written = true
	}
	return r.ResponseWriter.Write(b)
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if r.URL.Path == "/healthz" && rec.status == http.StatusOK {
			level = slog.LevelDebug // health checks run every few seconds; keep them out of the normal log
		}
		logger.Log(r.Context(), level, "request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status, "duration_ms", time.Since(started).Milliseconds())
	})
}

func recoverer(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if v := recover(); v != nil {
				logger.Error("panic in handler", "method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				if !rec.written {
					writeError(w, http.StatusInternalServerError, "internal error")
				}
			}
		}()
		next.ServeHTTP(rec, r)
	})
}
