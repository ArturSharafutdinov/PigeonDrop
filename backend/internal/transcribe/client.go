package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	// maxResponseBytes bounds what is read from the service: its answers are a few hundred bytes plus the text.
	maxResponseBytes = 1 << 20
	// maxRetryAfter caps what the service may ask us to wait.
	maxRetryAfter = time.Minute
)

// HTTPTranscriber asks the service in stt/ (protocol: stt/README.md). It is safe for concurrent use.
type HTTPTranscriber struct {
	endpoint string
	token    string
	timeout  time.Duration
	client   *http.Client
}

// NewHTTP returns a client for the service at baseURL (scheme://host[:port], no credentials in it). An empty token
// sends no Authorization header, which only a service started for experiments accepts. timeout limits one whole call
// (sending the recording and waiting for the text).
func NewHTTP(baseURL, token string, timeout time.Duration) (*HTTPTranscriber, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("the speech recognition service's address must be an http(s) URL, got %q", baseURL)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("the speech recognition service's address must be scheme://host[:port] only, got %q", baseURL)
	}
	if timeout <= 0 {
		return nil, errors.New("the speech recognition timeout must be positive")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	// One connection per call. The service answers and closes after each request, and a connection kept open that the
	// other side has since closed would fail a call in a way that cannot be told from a real failure.
	transport.DisableKeepAlives = true
	return &HTTPTranscriber{
		endpoint: strings.TrimRight(u.String(), "/") + "/v1/transcribe",
		token:    token,
		timeout:  timeout,
		client: &http.Client{
			Transport: transport,
			// A redirect means the address is wrong; following it would send the recording and the token elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// serviceError is the body of the service's refusals: {"code": "...", "message": "..."}.
type serviceError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e serviceError) String() string {
	switch {
	case e.Code != "" && e.Message != "":
		return e.Code + ": " + e.Message
	case e.Code != "":
		return e.Code
	}
	return e.Message
}

// answer is the service's 200 body. text is a pointer so that a missing text is told from an empty one (silence).
type answer struct {
	Text     *string `json:"text"`
	Language *string `json:"language"`
	Engine   string  `json:"engine"`
	AudioMs  int     `json:"audio_ms"`
	SttMs    int     `json:"stt_ms"`
}

func (t *HTTPTranscriber) Transcribe(ctx context.Context, wav []byte) (Result, error) {
	requestID := uuid.NewString()
	fail := func(kind Kind, retryAfter time.Duration, format string, args ...any) (Result, error) {
		return Result{}, &Error{Kind: kind, RetryAfter: retryAfter, RequestID: requestID, Err: fmt.Errorf(format, args...)}
	}

	callCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	// Whether the request was sent to the end decides what a failure means: if it was not, the model never saw the
	// recording and cannot be blamed for what followed.
	var written atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			written.Store(true)
		}
	}}
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(callCtx, trace), http.MethodPost, t.endpoint, bytes.NewReader(wav))
	if err != nil {
		return fail(Unavailable, 0, "cannot build the request: %v", err)
	}
	request.Header.Set("Content-Type", "audio/wav")
	request.Header.Set("X-Request-Id", requestID)
	if t.token != "" {
		request.Header.Set("Authorization", "Bearer "+t.token)
	}

	response, err := t.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err() // the caller's own end, not the service's failure
		}
		if !written.Load() {
			return fail(Unavailable, 0, "the request did not reach the service: %v", err)
		}
		return fail(Failed, 0, "no answer after the request was sent: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return fail(Failed, 0, "the answer was cut off: %v", err)
	}
	if len(body) > maxResponseBytes {
		return fail(Failed, 0, "the answer is over %d bytes", maxResponseBytes)
	}

	if response.StatusCode == http.StatusOK {
		return t.result(body, requestID, fail)
	}

	var refusal serviceError
	_ = json.Unmarshal(body, &refusal) // a proxy's page is not JSON; the status alone then speaks
	detail := refusal.String()
	if detail == "" {
		detail = strings.TrimSpace(http.StatusText(response.StatusCode))
	}
	retryAfter := parseRetryAfter(response.Header.Get("Retry-After"))

	switch code := response.StatusCode; {
	case code == http.StatusTooManyRequests:
		return fail(Busy, retryAfter, "HTTP %d, %s", code, detail)
	case code == http.StatusBadRequest || code == http.StatusRequestEntityTooLarge || code == http.StatusUnprocessableEntity:
		return fail(Rejected, 0, "HTTP %d, %s", code, detail)
	case code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout:
		return fail(Unavailable, retryAfter, "HTTP %d, %s", code, detail)
	case code >= 500:
		return fail(Failed, 0, "HTTP %d, %s", code, detail)
	default:
		// 3xx, a wrong token or address (401, 403, 404, 405, 411, 415) and any other 4xx are not about this
		// recording: they are a misconfiguration or a service of another version, which a retry does not fix but
		// the recording does not deserve to be failed for.
		return fail(Unavailable, 0, "HTTP %d, %s (check the address and the token of the service)", code, detail)
	}
}

func (t *HTTPTranscriber) result(body []byte, requestID string, fail func(Kind, time.Duration, string, ...any) (Result, error)) (Result, error) {
	var a answer
	if err := json.Unmarshal(body, &a); err != nil {
		return fail(Failed, 0, "the answer is not the expected JSON: %v", err)
	}
	if a.Text == nil {
		return fail(Failed, 0, "the answer has no text")
	}
	result := Result{Text: *a.Text, Engine: a.Engine, AudioMs: a.AudioMs, SttMs: a.SttMs, RequestID: requestID}
	if a.Language != nil {
		result.Language = *a.Language
	}
	return result, nil
}

// parseRetryAfter reads the number-of-seconds form of Retry-After. The HTTP-date form is not used by the service
// and is ignored (zero), as is anything unreadable; the value is capped.
func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return min(time.Duration(seconds)*time.Second, maxRetryAfter)
}
