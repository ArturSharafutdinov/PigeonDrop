// Package httpapi implements the HTTP API described in api/openapi.yaml. The routing
// and request/response types are generated into internal/api; this package holds the
// behaviour behind them plus the middleware every request passes through.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/api"
)

// Pinger is the part of the database pool the health check needs.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Server holds what the handlers depend on.
type Server struct {
	Pinger Pinger
	Logger *slog.Logger
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
// everything that is not a route, request logging, and panic recovery.
func NewHandler(s *Server) http.Handler {
	strict := api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			s.Logger.Error("handler failed", "method", r.Method, "path", r.URL.Path, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		},
	})

	mux := http.NewServeMux()
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{ // registers the generated routes on mux
		BaseRouter: mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, err.Error())
		},
	})
	// Anything the generated routes do not claim - unknown path or wrong method alike -
	// is a JSON 404, the same answer the Node prototype gave.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	return recoverer(s.Logger, logRequests(s.Logger, mux))
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Error{Ok: false, Error: message})
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
