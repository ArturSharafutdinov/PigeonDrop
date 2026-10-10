package transcribe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

const testToken = "tok-3n-s3cret"

var testWAV = []byte("RIFF....WAVEfmt fake wav bytes for the tests")

// service starts a fake recognition service and a client for it.
func service(t *testing.T, handler http.HandlerFunc) *HTTPTranscriber {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewHTTP(server.URL, testToken, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func reply(status int, body string, headers ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// wantKind checks that err is a transcription error of the given kind.
func wantKind(t *testing.T, err error, kind Kind) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want a *Error of kind %s, got %T: %v", kind, err, err)
	}
	if e.Kind != kind {
		t.Fatalf("kind = %s, want %s (%v)", e.Kind, kind, err)
	}
	if e.RequestID == "" {
		t.Errorf("the error carries no request id: %v", err)
	}
	return e
}

func TestSuccessSendsTheProtocolAndReadsTheAnswer(t *testing.T) {
	var got struct {
		method, path, contentType, auth, requestID string
		body                                       []byte
		closes                                     bool
	}
	client := service(t, func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path = r.Method, r.URL.Path
		got.contentType, got.auth, got.requestID = r.Header.Get("Content-Type"), r.Header.Get("Authorization"), r.Header.Get("X-Request-Id")
		got.body, _ = io.ReadAll(r.Body)
		got.closes = r.Close
		_, _ = io.WriteString(w, `{"text":"позвони Ивану","language":"ru","engine":"parakeet-tdt-0.6b-v3-int8","audio_ms":4880,"stt_ms":420}`)
	})

	result, err := client.Transcribe(context.Background(), testWAV)
	if err != nil {
		t.Fatal(err)
	}

	if got.method != http.MethodPost || got.path != "/v1/transcribe" || got.contentType != "audio/wav" {
		t.Errorf("request = %s %s %s", got.method, got.path, got.contentType)
	}
	if got.auth != "Bearer "+testToken {
		t.Errorf("Authorization = %q", got.auth)
	}
	if !got.closes {
		t.Error("the call did not ask for the connection to be closed: a kept-alive connection the service has since closed would fail a later call in a way that looks like a real failure")
	}
	if !bytes.Equal(got.body, testWAV) {
		t.Errorf("the body was not the recording as given")
	}
	if _, err := uuid.Parse(got.requestID); err != nil || got.requestID != result.RequestID {
		t.Errorf("X-Request-Id = %q, result.RequestID = %q", got.requestID, result.RequestID)
	}
	want := Result{Text: "позвони Ивану", Language: "ru", Engine: "parakeet-tdt-0.6b-v3-int8", AudioMs: 4880, SttMs: 420, RequestID: got.requestID}
	if result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestSilenceIsAnAnswerAndAMissingTextIsNot(t *testing.T) {
	client := service(t, reply(200, `{"text":"","language":null,"engine":"e","audio_ms":10000,"stt_ms":90}`))
	result, err := client.Transcribe(context.Background(), testWAV)
	if err != nil || result.Text != "" || result.Language != "" || result.AudioMs != 10000 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}

	for name, body := range map[string]string{
		"no text":        `{"engine":"e"}`,
		"text is null":   `{"text":null}`,
		"text is number": `{"text":5}`,
		"not json":       `<html>Bad gateway page</html>`,
		"empty":          ``,
		"a list":         `["x"]`,
		"cut":            `{"text":"abc`,
	} {
		t.Run(name, func(t *testing.T) {
			client := service(t, reply(200, body))
			_, err := client.Transcribe(context.Background(), testWAV)
			wantKind(t, err, Failed)
		})
	}
}

func TestNoTokenSendsNoAuthorizationHeader(t *testing.T) {
	var header = "unset"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"text":"x"}`)
	}))
	defer server.Close()
	client, err := NewHTTP(server.URL, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Transcribe(context.Background(), testWAV); err != nil {
		t.Fatal(err)
	}
	if header != "" {
		t.Errorf("Authorization = %q, want none", header)
	}
}

// What each answer of the service means for the recording is the contract the queue relies on.
func TestStatusesAreSortedByWhoIsAtFault(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		headers    []string
		kind       Kind
		retryAfter time.Duration
	}{
		{"busy", 429, `{"code":"busy","message":"another recording is being recognized"}`, []string{"Retry-After", "5"}, Busy, 5 * time.Second},
		{"busy without a hint", 429, `{"code":"busy"}`, nil, Busy, 0},
		{"busy with a date hint", 429, `{"code":"busy"}`, []string{"Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT"}, Busy, 0},
		{"busy asks for too long", 429, `{"code":"busy"}`, []string{"Retry-After", "99999"}, Busy, time.Minute},
		{"bad audio", 400, `{"code":"bad_audio","message":"the WAV must be mono"}`, nil, Rejected, 0},
		{"too long", 413, `{"code":"too_long"}`, nil, Rejected, 0},
		{"unprocessable", 422, ``, nil, Rejected, 0},
		{"loading", 503, `{"code":"loading"}`, []string{"Retry-After", "10"}, Unavailable, 10 * time.Second},
		{"bad gateway", 502, `<html>proxy</html>`, nil, Unavailable, 0},
		{"gateway timeout", 504, ``, nil, Unavailable, 0},
		{"wrong token", 401, `{"code":"unauthorized"}`, nil, Unavailable, 0},
		{"forbidden", 403, ``, nil, Unavailable, 0},
		{"wrong address", 404, `{"code":"not_found"}`, nil, Unavailable, 0},
		{"wrong method", 405, ``, nil, Unavailable, 0},
		{"length required", 411, ``, nil, Unavailable, 0},
		{"wrong media type", 415, ``, nil, Unavailable, 0},
		{"some other 4xx", 418, ``, nil, Unavailable, 0},
		{"internal", 500, `{"code":"internal"}`, nil, Failed, 0},
		{"not implemented", 501, ``, nil, Failed, 0},
		{"out of storage", 507, ``, nil, Failed, 0},
		{"no content", 204, ``, nil, Unavailable, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := service(t, reply(tt.status, tt.body, tt.headers...))
			_, err := client.Transcribe(context.Background(), testWAV)
			e := wantKind(t, err, tt.kind)
			if e.RetryAfter != tt.retryAfter {
				t.Errorf("RetryAfter = %v, want %v", e.RetryAfter, tt.retryAfter)
			}
		})
	}
}

func TestTheServicesReasonReachesTheError(t *testing.T) {
	client := service(t, reply(400, `{"code":"bad_audio","message":"the WAV must be mono, it has 2 channels"}`))
	_, err := client.Transcribe(context.Background(), testWAV)
	if !strings.Contains(err.Error(), "bad_audio") || !strings.Contains(err.Error(), "2 channels") {
		t.Errorf("error = %v", err)
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	var elsewhere atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Store(true) }))
	defer other.Close()
	client := service(t, reply(302, "", "Location", other.URL))

	_, err := client.Transcribe(context.Background(), testWAV)

	wantKind(t, err, Unavailable)
	if elsewhere.Load() {
		t.Error("the recording and the token were sent to the redirect target")
	}
}

func TestAServiceThatIsNotThereIsUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close() // nothing listens there now: connection refused

	client, err := NewHTTP("http://"+address, testToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Transcribe(context.Background(), testWAV)
	wantKind(t, err, Unavailable)

	client, _ = NewHTTP("http://no-such-host.invalid", testToken, time.Second)
	_, err = client.Transcribe(context.Background(), testWAV)
	wantKind(t, err, Unavailable)
}

// A model that hangs or dies on a recording must count against that recording, or it would be tried forever.
func TestNoAnswerAfterTheRequestWasSentIsAFailureOfTheRecording(t *testing.T) {
	t.Run("hangs", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer server.Close()
		client, _ := NewHTTP(server.URL, testToken, 100*time.Millisecond)

		started := time.Now()
		_, err := client.Transcribe(context.Background(), testWAV)

		wantKind(t, err, Failed)
		if took := time.Since(started); took > 3*time.Second {
			t.Errorf("the call took %v, the timeout is 100ms", took)
		}
	})

	t.Run("the connection is cut after the request", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			connection, _, _ := w.(http.Hijacker).Hijack()
			connection.Close() // the container was killed while it was recognizing
		}))
		defer server.Close()
		client, _ := NewHTTP(server.URL, testToken, 5*time.Second)

		_, err := client.Transcribe(context.Background(), testWAV)

		wantKind(t, err, Failed)
	})

	t.Run("the answer breaks off in the middle", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Length", "500")
			w.WriteHeader(200)
			_, _ = io.WriteString(w, `{"text":"half`)
		}))
		defer server.Close()
		client, _ := NewHTTP(server.URL, testToken, 5*time.Second)

		_, err := client.Transcribe(context.Background(), testWAV)

		wantKind(t, err, Failed)
	})
}

// The reverse: when the request could not be sent to the end, the model never saw the recording.
func TestARequestThatCouldNotBeSentIsNotTheRecordingsFault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, _, _ := w.(http.Hijacker).Hijack()
		connection.Close() // closes without reading the body
	}))
	defer server.Close()
	client, _ := NewHTTP(server.URL, testToken, 10*time.Second)

	// Bigger than every buffer between the two ends, so the writing fails before it is finished.
	_, err := client.Transcribe(context.Background(), make([]byte, 64<<20))

	wantKind(t, err, Unavailable)
}

// hanging is a service that takes the recording and never answers; entered is closed once it has the whole request.
func hanging(t *testing.T) (client *HTTPTranscriber, entered <-chan struct{}) {
	t.Helper()
	signal := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(signal) })
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	client, err := NewHTTP(server.URL, testToken, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client, signal
}

func TestTheCallersContextEndingIsNotAServiceFailure(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		client, entered := hanging(t)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-entered
			cancel()
		}()
		_, err := client.Transcribe(ctx, testWAV)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if _, isService := KindOf(err); isService {
			t.Error("a cancelled call must not look like a failure of the service")
		}
	})

	t.Run("the callers deadline", func(t *testing.T) {
		client, _ := hanging(t)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := client.Transcribe(ctx, testWAV)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if _, isService := KindOf(err); isService {
			t.Error("the caller's deadline must not look like a failure of the service")
		}
	})
}

func TestAnAnswerOverTheLimitIsRefused(t *testing.T) {
	client := service(t, reply(200, `{"text":"`+strings.Repeat("а", maxResponseBytes)+`"}`))
	_, err := client.Transcribe(context.Background(), testWAV)
	e := wantKind(t, err, Failed)
	if !strings.Contains(e.Error(), "the answer is over") {
		t.Errorf("error = %v, want it to say that the answer is over the limit", e)
	}
}

func TestTheTokenNeverAppearsInAnError(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"401":     reply(401, `{"code":"unauthorized","message":"wrong token"}`),
		"500":     reply(500, ``),
		"garbage": reply(200, `nonsense`),
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			client := service(t, handler)
			_, err := client.Transcribe(context.Background(), testWAV)
			if err == nil || strings.Contains(err.Error(), testToken) {
				t.Errorf("err = %v", err)
			}
		})
	}
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	address := listener.Addr().String()
	listener.Close()
	client, _ := NewHTTP("http://"+address, testToken, time.Second)
	if _, err := client.Transcribe(context.Background(), testWAV); err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("err = %v", err)
	}
}

func TestEachCallHasItsOwnRequestID(t *testing.T) {
	client := service(t, reply(200, `{"text":"x"}`))
	first, _ := client.Transcribe(context.Background(), testWAV)
	second, _ := client.Transcribe(context.Background(), testWAV)
	if first.RequestID == "" || first.RequestID == second.RequestID {
		t.Errorf("request ids: %q, %q", first.RequestID, second.RequestID)
	}
}

func TestCallsMayRunAtTheSameTime(t *testing.T) {
	client := service(t, reply(200, `{"text":"x"}`))
	errs := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := client.Transcribe(context.Background(), testWAV)
			errs <- err
		}()
	}
	for range 8 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestNewHTTPChecksTheAddress(t *testing.T) {
	good := map[string]string{
		"http://stt:8000":             "http://stt:8000/v1/transcribe",
		"http://stt:8000/":            "http://stt:8000/v1/transcribe",
		"  https://stt.internal  ":    "https://stt.internal/v1/transcribe",
		"http://127.0.0.1:18000":      "http://127.0.0.1:18000/v1/transcribe",
		"http://pigeondrop-stt-ab12/": "http://pigeondrop-stt-ab12/v1/transcribe",
	}
	for address, endpoint := range good {
		client, err := NewHTTP(address, "", time.Second)
		if err != nil {
			t.Errorf("%q: %v", address, err)
			continue
		}
		if client.endpoint != endpoint {
			t.Errorf("%q: endpoint = %q, want %q", address, client.endpoint, endpoint)
		}
	}
	for _, address := range []string{"", "stt:8000", "ftp://stt", "http://", "http://user:pass@stt", "http://stt/some/path", "http://stt?x=1", "http://stt#frag", "://bad"} {
		if _, err := NewHTTP(address, "", time.Second); err == nil {
			t.Errorf("%q was accepted", address)
		}
	}
	if _, err := NewHTTP("http://stt", "", 0); err == nil {
		t.Error("a zero timeout was accepted")
	}
}

func TestKindOfAndStrings(t *testing.T) {
	err := &Error{Kind: Busy, Err: errors.New("x")}
	wrapped := errors.Join(errors.New("context"), err)
	if kind, ok := KindOf(wrapped); !ok || kind != Busy {
		t.Errorf("KindOf = %v, %v", kind, ok)
	}
	if _, ok := KindOf(errors.New("plain")); ok {
		t.Error("a plain error has no kind")
	}
	for kind, want := range map[Kind]string{Unavailable: "unavailable", Busy: "busy", Failed: "failed", Rejected: "rejected", Kind(9): "kind(9)"} {
		if kind.String() != want {
			t.Errorf("%d = %q, want %q", kind, kind.String(), want)
		}
	}
}
