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
)

// MaxBodyBytes is the largest request body the server reads. A 60-second recording is about
// 180 KB as base64; this leaves room for much longer ones without letting a client stream
// unbounded data into memory.
const MaxBodyBytes = 8 << 20

// Pinger is the part of the database pool the health check needs.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Server holds what the handlers depend on.
type Server struct {
	Pinger Pinger
	DB     dbgen.Querier
	Files  storage.Store
	Logger *slog.Logger
	// DefaultServerURL is this server's own public address: what an account resolves to when it has no server of its own.
	DefaultServerURL string
	// Now supplies the time used to name stored files and to date sessions; nil means time.Now. Tests set it.
	Now func() time.Time

	// Tx runs a function in one database transaction (db.InTx). Needed by registration and password changes.
	Tx TxFunc
	// Hasher hashes passwords; NewHandler makes one with the default settings if it is nil.
	Hasher *auth.Hasher
	// Limits are the sign-in brute-force limiters; NewHandler makes the default ones if they are unset.
	Limits AuthLimits
	// CookieSecure marks the session cookie Secure. On for HTTPS, off only for local development over http.
	CookieSecure bool
	// AllowedOrigins are other web pages (scheme://host[:port]) allowed to call the /v1 API with a session.
	AllowedOrigins []string
	// TrustProxyHeaders reads the client's address from X-Forwarded-For (only behind a proxy that sets it).
	TrustProxyHeaders bool

	// userCodes draws the short code of a pairing; nil means auth.NewUserCode. Tests replace it to force collisions.
	userCodes func() (display, canonical string, err error)
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

	strict := api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			var tooLarge *http.MaxBytesError
			switch {
			case errors.As(err, &tooLarge):
				writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
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
	// The hand-written browser routes (see uploads.go).
	mux.HandleFunc("GET /setup", s.handleSetup)
	mux.HandleFunc("GET /uploads", s.handleListUploads)
	mux.HandleFunc("GET /uploads/{name}", s.handleDownload)
	// Anything the generated routes do not claim - unknown path or wrong method alike -
	// is a JSON 404, the same answer the Node prototype gave.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	return recoverer(s.Logger, logRequests(s.Logger, limitBody(s.crossOriginGuard(s.authContext(mux)))))
}

// limitBody caps how much of a request body any handler can read.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeErrorBody(w, status, api.Error{Ok: false, Error: message})
}

func writeErrorBody(w http.ResponseWriter, status int, body api.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeBadRequest answers 400. The /v1 API also names the problem with a machine-readable code; the older
// routes keep the exact answer the Node prototype gave, so their clients see no change.
func writeBadRequest(w http.ResponseWriter, r *http.Request, message string) {
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		writeCodedError(w, http.StatusBadRequest, api.ErrorCodeInvalidRequest, message)
		return
	}
	writeError(w, http.StatusBadRequest, message)
}

// statusRecorder remembers the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

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
