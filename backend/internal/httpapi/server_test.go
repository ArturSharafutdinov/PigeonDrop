package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/testdb"
)

type failingPinger struct{}

func (failingPinger) Ping(context.Context) error { return errors.New("connection refused") }

type panickingPinger struct{}

func (panickingPinger) Ping(context.Context) error { panic("boom") }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func do(t *testing.T, h http.Handler, method, path string) (int, map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body, rec.Header().Get("Content-Type")
}

func TestHealthWithRealDatabase(t *testing.T) {
	h := NewHandler(&Server{Pinger: testdb.New(t), Logger: quietLogger()})

	status, body, contentType := do(t, h, http.MethodGet, "/healthz")
	if status != http.StatusOK || body["ok"] != true || !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("got %d %v %q", status, body, contentType)
	}
}

func TestHealthReportsUnavailableDatabase(t *testing.T) {
	h := NewHandler(&Server{Pinger: failingPinger{}, Logger: quietLogger()})

	status, body, _ := do(t, h, http.MethodGet, "/healthz")
	if status != http.StatusServiceUnavailable || body["ok"] != false || body["error"] != "database unavailable" {
		t.Fatalf("got %d %v", status, body)
	}
}

func TestUnknownRoutesAreJSON404(t *testing.T) {
	h := NewHandler(&Server{Pinger: failingPinger{}, Logger: quietLogger()})

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/nope"},
		{http.MethodPost, "/healthz"}, // known path, wrong method
		{http.MethodDelete, "/"},
		// The routes of the Node prototype were removed on 2026-10-04; they must not come back by accident.
		{http.MethodPost, "/messages"},
		{http.MethodPost, "/accounts"},
		{http.MethodGet, "/accounts/abc"},
		{http.MethodPut, "/accounts/abc"},
		{http.MethodGet, "/setup"},
		{http.MethodGet, "/uploads"},
		{http.MethodGet, "/uploads/2026-10-03_07-23-36-048.opus"},
	} {
		status, body, contentType := do(t, h, tc.method, tc.path)
		if status != http.StatusNotFound || body["ok"] != false || body["error"] != "not found" || !strings.HasPrefix(contentType, "application/json") {
			t.Errorf("%s %s: got %d %v %q", tc.method, tc.path, status, body, contentType)
		}
	}
}

func TestPanicBecomesJSON500(t *testing.T) {
	h := NewHandler(&Server{Pinger: panickingPinger{}, Logger: quietLogger()})

	status, body, _ := do(t, h, http.MethodGet, "/healthz")
	if status != http.StatusInternalServerError || body["error"] != "internal error" {
		t.Fatalf("got %d %v", status, body)
	}
	// and the server keeps answering afterwards
	if status, _, _ := do(t, h, http.MethodGet, "/nope"); status != http.StatusNotFound {
		t.Fatalf("handler is broken after a panic: %d", status)
	}
}
