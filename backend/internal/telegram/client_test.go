package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "123456789:AAH-test_token0123456789abcdefghijk"

// fakeAPI is a Bot API that answers as the test says. It insists that every request goes to /bot<token>/<method>.
type fakeAPI struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	calls  []fakeCall
}

type fakeCall struct {
	Method      string
	ContentType string
	Body        []byte
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, c fakeCall)

func newFakeAPI(t *testing.T, h handlerFunc) (*Client, *fakeAPI) {
	t.Helper()
	f := &fakeAPI{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/bot" + testToken + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			t.Errorf("request to %q, want a path starting with /bot<token>/", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		c := fakeCall{Method: strings.TrimPrefix(r.URL.Path, prefix), ContentType: r.Header.Get("Content-Type"), Body: body}
		f.mu.Lock()
		f.calls = append(f.calls, c)
		f.mu.Unlock()
		h(w, r, c)
	}))
	t.Cleanup(f.server.Close)
	client, err := NewClient(f.server.URL, testToken, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client, f
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func ok(result string) handlerFunc {
	return func(w http.ResponseWriter, _ *http.Request, _ fakeCall) {
		reply(w, 200, `{"ok":true,"result":`+result+`}`)
	}
}

func TestGetMe(t *testing.T) {
	client, f := newFakeAPI(t, ok(`{"id":777,"is_bot":true,"first_name":"PigeonDrop","username":"pigeondrop_bot"}`))
	bot, err := client.GetMe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bot.ID != 777 || bot.Username != "pigeondrop_bot" || bot.FirstName != "PigeonDrop" {
		t.Errorf("bot = %+v", bot)
	}
	if f.calls[0].Method != "getMe" {
		t.Errorf("method = %q", f.calls[0].Method)
	}
}

func TestGetWebhookInfo(t *testing.T) {
	client, _ := newFakeAPI(t, ok(`{"url":"https://example.com/hook","pending_update_count":3,"last_error_message":"boom"}`))
	info, err := client.GetWebhookInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.URL != "https://example.com/hook" || info.PendingUpdateCount != 3 || info.LastErrorMessage != "boom" {
		t.Errorf("info = %+v", info)
	}
}

func TestGetUpdatesAsksForMessagesAndButtonPresses(t *testing.T) {
	client, f := newFakeAPI(t, ok(`[{"update_id":41,"message":{"message_id":5,"from":{"id":9,"is_bot":false,"first_name":"Ann","username":"ann","language_code":"ru"},"chat":{"id":9,"type":"private","first_name":"Ann"},"text":"/start abc"}},{"update_id":42}]`))
	updates, err := client.GetUpdates(context.Background(), 41, 25*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(f.calls[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["offset"] != float64(41) || sent["timeout"] != float64(25) || sent["limit"] != float64(100) {
		t.Errorf("request = %v", sent)
	}
	if got := fmt.Sprint(sent["allowed_updates"]); got != "[message callback_query]" {
		t.Errorf("allowed_updates = %s", got)
	}
	if len(updates) != 2 || updates[0].UpdateID != 41 || updates[1].UpdateID != 42 || updates[1].Message != nil {
		t.Fatalf("updates = %+v", updates)
	}
	m := updates[0].Message
	if m.Text != "/start abc" || m.Chat.ID != 9 || m.Chat.Type != "private" || m.From == nil || m.From.LanguageCode != "ru" || m.From.Username != "ann" {
		t.Errorf("message = %+v", m)
	}
}

func TestGetUpdatesWaitsLongerThanTheCallTimeout(t *testing.T) {
	// A long poll is silent for up to the wait it asked for; the call's own timeout counts from the end of it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		reply(w, 200, `{"ok":true,"result":[]}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, testToken, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetUpdates(context.Background(), 0, time.Second); err != nil {
		t.Errorf("a poll that answers within its wait failed: %v", err)
	}
	if _, err := client.GetMe(context.Background()); err == nil {
		t.Error("a plain call that is slower than the timeout succeeded")
	}
}

func parseMultipart(t *testing.T, c fakeCall) (fields map[string]string, filePart *multipart.Part, fileBytes []byte) {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(c.ContentType)
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("content type %q", c.ContentType)
	}
	reader := multipart.NewReader(strings.NewReader(string(c.Body)), params["boundary"])
	fields = map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return fields, filePart, fileBytes
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(part)
		if part.FileName() != "" {
			filePart, fileBytes = part, data
			continue
		}
		fields[part.FormName()] = string(data)
	}
}

func TestSendVoice(t *testing.T) {
	client, f := newFakeAPI(t, ok(`{"message_id":88,"chat":{"id":9,"type":"private"},"voice":{"file_id":"X","duration":15,"mime_type":"audio/ogg","file_size":34626}}`))
	audio := []byte("OggS\x00fake audio bytes\x00\xff")
	sent, err := client.SendVoice(context.Background(), VoiceParams{ChatID: 9, Audio: audio, Caption: "Привет, мир", Duration: 15700 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if sent.MessageID != 88 || sent.Kind != "voice" || sent.DurationSec != 15 || sent.MimeType != "audio/ogg" {
		t.Errorf("sent = %+v", sent)
	}
	c := f.calls[0]
	if c.Method != "sendVoice" {
		t.Errorf("method = %q", c.Method)
	}
	fields, part, data := parseMultipart(t, c)
	if fields["chat_id"] != "9" || fields["caption"] != "Привет, мир" || fields["duration"] != "16" {
		t.Errorf("fields = %v", fields)
	}
	if _, has := fields["parse_mode"]; has {
		t.Error("the caption must be plain text: parse_mode was sent")
	}
	if part == nil || part.FormName() != "voice" || part.FileName() != "voice.ogg" || part.Header.Get("Content-Type") != "audio/ogg" {
		t.Fatalf("file part = %+v", part)
	}
	if string(data) != string(audio) {
		t.Errorf("file bytes changed on the way")
	}
	// The way a browser writes it: a parser that only knows that form must read it.
	if got := part.Header.Get("Content-Disposition"); got != `form-data; name="voice"; filename="voice.ogg"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	if !strings.Contains(string(c.Body), "Content-Disposition: form-data; name=\"chat_id\"\r\n") {
		t.Errorf("the ordinary fields are not written the usual way: %q", c.Body[:80])
	}
}

func TestAFileNameWithQuotesCannotBreakOutOfItsHeader(t *testing.T) {
	client, f := newFakeAPI(t, ok(`{"message_id":4,"chat":{"id":9,"type":"private"},"document":{"file_id":"D"}}`))
	// SendDocument turns slashes and backslashes into underscores and drops line breaks; a quote must be escaped, not trusted.
	if _, err := client.SendDocument(context.Background(), DocumentParams{ChatID: 9, Data: []byte("data"), FileName: `a"b\c.opus`}); err != nil {
		t.Fatal(err)
	}
	_, part, _ := parseMultipart(t, f.calls[0])
	if part == nil || part.FileName() != `a"b_c.opus` {
		t.Fatalf("file part %+v", part)
	}
	if !strings.Contains(string(f.calls[0].Body), `filename="a\"b_c.opus"`) {
		t.Errorf("the name is not escaped in the header: %q", f.calls[0].Body)
	}
}

func TestSendVoiceLeavesOutWhatIsEmpty(t *testing.T) {
	client, f := newFakeAPI(t, ok(`{"message_id":1,"chat":{"id":9,"type":"private"},"voice":{"file_id":"X","duration":2}}`))
	if _, err := client.SendVoice(context.Background(), VoiceParams{ChatID: -100123, Audio: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	fields, _, _ := parseMultipart(t, f.calls[0])
	if len(fields) != 1 || fields["chat_id"] != "-100123" {
		t.Errorf("fields = %v, want only chat_id", fields)
	}
}

func TestSendVoiceTelegramMadeItADocument(t *testing.T) {
	client, _ := newFakeAPI(t, ok(`{"message_id":3,"chat":{"id":9,"type":"private"},"document":{"file_id":"D","mime_type":"audio/ogg","file_size":10}}`))
	sent, err := client.SendVoice(context.Background(), VoiceParams{ChatID: 9, Audio: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if sent.Kind != "document" || sent.MessageID != 3 {
		t.Errorf("sent = %+v: the caller must be able to see that it did not become a voice message", sent)
	}
}

func TestSendDocument(t *testing.T) {
	client, f := newFakeAPI(t, ok(`{"message_id":4,"chat":{"id":9,"type":"private"},"document":{"file_id":"D"}}`))
	sent, err := client.SendDocument(context.Background(), DocumentParams{ChatID: 9, Data: []byte("data"), FileName: "../a\"b\r\nc.opus", Caption: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if sent.Kind != "document" {
		t.Errorf("sent = %+v", sent)
	}
	fields, part, data := parseMultipart(t, f.calls[0])
	if fields["caption"] != "text" || string(data) != "data" || part.FormName() != "document" {
		t.Errorf("fields %v part %v data %q", fields, part, data)
	}
	if name := part.FileName(); strings.ContainsAny(name, "/\\\r\n") {
		t.Errorf("file name %q was not made safe", name)
	}
	if _, err := client.SendDocument(context.Background(), DocumentParams{ChatID: 9, Data: []byte("data")}); err != nil {
		t.Fatal(err)
	}
	if _, part, _ := parseMultipart(t, f.calls[1]); part.FileName() == "" {
		t.Error("a file without a name must get one")
	}
}

func TestSendMessage(t *testing.T) {
	client, f := newFakeAPI(t, ok(`{"message_id":55,"chat":{"id":9,"type":"private"},"text":"hi"}`))
	sent, err := client.SendMessage(context.Background(), MessageParams{ChatID: 9, Text: "Привет <b>мир</b> & https://example.com", ReplyTo: 88})
	if err != nil {
		t.Fatal(err)
	}
	if sent.MessageID != 55 || sent.Kind != "text" {
		t.Errorf("sent = %+v", sent)
	}
	var body struct {
		ChatID      int64  `json:"chat_id"`
		Text        string `json:"text"`
		ParseMode   string `json:"parse_mode"`
		ReplyParams struct {
			MessageID                int64 `json:"message_id"`
			AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
		} `json:"reply_parameters"`
		LinkPreview struct {
			Disabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
	}
	if err := json.Unmarshal(f.calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.ChatID != 9 || body.Text != "Привет <b>мир</b> & https://example.com" || body.ParseMode != "" {
		t.Errorf("body = %+v", body)
	}
	if body.ReplyParams.MessageID != 88 || !body.ReplyParams.AllowSendingWithoutReply {
		t.Errorf("reply_parameters = %+v", body.ReplyParams)
	}
	if !body.LinkPreview.Disabled {
		t.Error("link previews must be switched off")
	}

	if _, err := client.SendMessage(context.Background(), MessageParams{ChatID: 9, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.calls[1].Body), "reply_parameters") {
		t.Errorf("a message that answers nothing carries reply_parameters: %s", f.calls[1].Body)
	}
}

func TestSetMyCommands(t *testing.T) {
	client, f := newFakeAPI(t, ok(`true`))
	err := client.SetMyCommands(context.Background(), []Command{{Command: "stop", Description: "Отключить"}}, "ru")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Commands     []Command `json:"commands"`
		LanguageCode string    `json:"language_code"`
	}
	if err := json.Unmarshal(f.calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Commands) != 1 || body.Commands[0].Command != "stop" || body.LanguageCode != "ru" {
		t.Errorf("body = %+v", body)
	}
	if err := client.SetMyCommands(context.Background(), []Command{{Command: "stop", Description: "x"}}, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.calls[1].Body), "language_code") {
		t.Errorf("an empty language code was sent: %s", f.calls[1].Body)
	}
}

func TestErrorKinds(t *testing.T) {
	const retry7 = `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`
	for _, tc := range []struct {
		name       string
		status     int
		header     string
		body       string
		send       bool // sending (not safe to repeat) or reading
		want       Kind
		wantCode   int
		wantRetry  time.Duration
		wantInText string
	}{
		{"rate limit", 429, "", retry7, true, RateLimited, 429, 7 * time.Second, "retry after 7"},
		{"rate limit asked for a very long wait", 429, "", `{"ok":false,"error_code":429,"description":"flood","parameters":{"retry_after":999999}}`, true, RateLimited, 429, time.Hour, "flood"},
		{"rate limit in the header only", 429, "5", `not json`, true, RateLimited, 429, 5 * time.Second, "Too Many Requests"},
		{"rate limit in the header, very long", 429, "999999", `not json`, true, RateLimited, 429, time.Hour, "Too Many Requests"},
		{"blocked by the person", 403, "", `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`, true, Blocked, 403, 0, "blocked by the user"},
		{"deactivated", 403, "", `{"ok":false,"error_code":403,"description":"Forbidden: user is deactivated"}`, true, Blocked, 403, 0, "deactivated"},
		{"chat not found", 400, "", `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, true, Blocked, 400, 0, "chat not found"},
		{"peer invalid", 400, "", `{"ok":false,"error_code":400,"description":"Bad Request: PEER_ID_INVALID"}`, true, Blocked, 400, 0, "PEER_ID_INVALID"},
		{"voice forbidden", 400, "", `{"ok":false,"error_code":400,"description":"Bad Request: VOICE_MESSAGES_FORBIDDEN"}`, true, VoiceForbidden, 400, 0, "VOICE_MESSAGES_FORBIDDEN"},
		{"another bad request", 400, "", `{"ok":false,"error_code":400,"description":"Bad Request: message caption is too long"}`, true, Rejected, 400, 0, "caption is too long"},
		{"too large", 413, "", `<html>Request Entity Too Large</html>`, true, Rejected, 413, 0, "Request Entity Too Large"},
		{"token refused", 401, "", `{"ok":false,"error_code":401,"description":"Unauthorized"}`, true, Unavailable, 401, 0, "Unauthorized"},
		{"unknown method or token", 404, "", `{"ok":false,"error_code":404,"description":"Not Found"}`, true, Unavailable, 404, 0, "Not Found"},
		{"another process polls", 409, "", `{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request"}`, false, Conflict, 409, 0, "terminated by other"},
		{"bad gateway", 502, "", `<html>502 Bad Gateway</html>`, true, Unavailable, 502, 0, "Bad Gateway"},
		{"unavailable", 503, "", ``, true, Unavailable, 503, 0, "Service Unavailable"},
		{"gateway timeout", 504, "", ``, true, Unavailable, 504, 0, "Gateway Timeout"},
		{"server error on a send: the message may have gone", 500, "", `{"ok":false,"error_code":500,"description":"Internal Server Error"}`, true, Failed, 500, 0, "Internal Server Error"},
		{"server error on a read", 500, "", `{"ok":false,"error_code":500,"description":"Internal Server Error"}`, false, Unavailable, 500, 0, "Internal Server Error"},
		{"ok false with status 200", 200, "", `{"ok":false,"description":"strange"}`, true, Failed, 200, 0, "strange"},
		{"not json at all with 200", 200, "", `<html>captive portal</html>`, true, Failed, 200, 0, "OK"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request, _ fakeCall) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				reply(w, tc.status, tc.body)
			})
			var err error
			if tc.send {
				_, err = client.SendMessage(context.Background(), MessageParams{ChatID: 1, Text: "x"})
			} else {
				_, err = client.GetUpdates(context.Background(), 0, 0)
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v, want an *Error", err)
			}
			if e.Kind != tc.want || e.Code != tc.wantCode || e.RetryAfter != tc.wantRetry {
				t.Errorf("kind %s code %d retry %s, want %s %d %s (%v)", e.Kind, e.Code, e.RetryAfter, tc.want, tc.wantCode, tc.wantRetry, err)
			}
			if !strings.Contains(e.Description, tc.wantInText) || !strings.Contains(err.Error(), tc.wantInText) {
				t.Errorf("the failure does not say %q: %v", tc.wantInText, err)
			}
			if kind, isCall := KindOf(err); !isCall || kind != tc.want {
				t.Errorf("KindOf = %v, %v", kind, isCall)
			}
		})
	}
}

func TestAnAnswerThatIsNotWhatTheBotAPIDescribes(t *testing.T) {
	client, _ := newFakeAPI(t, ok(`"a string, not a message"`))
	_, err := client.SendMessage(context.Background(), MessageParams{ChatID: 1, Text: "x"})
	if kind, isCall := KindOf(err); !isCall || kind != Failed {
		t.Errorf("err = %v, want a failed call: the message may have been sent", err)
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the redirect was followed to %s", r.URL)
	}))
	defer elsewhere.Close()
	client, _ := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, _ fakeCall) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
	})
	_, err := client.SendMessage(context.Background(), MessageParams{ChatID: 1, Text: "x"})
	if kind, _ := KindOf(err); kind != Unavailable {
		t.Errorf("err = %v, want unavailable (the address is wrong)", err)
	}
}

func TestTheTokenIsNotInAnyError(t *testing.T) {
	check := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("no error")
		}
		for _, text := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
			if strings.Contains(text, testToken) || strings.Contains(text, "AAH-test_token") {
				t.Errorf("the token is in %q", text)
			}
		}
	}

	t.Run("nothing listens", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := l.Addr().String()
		l.Close()
		client, err := NewClient("http://"+address, testToken, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.SendMessage(context.Background(), MessageParams{ChatID: 1, Text: "x"})
		check(t, err)
		if kind, _ := KindOf(err); kind != Unavailable {
			t.Errorf("a request that never reached Telegram is %v, want unavailable", kind)
		}
		_, err = client.GetMe(context.Background())
		check(t, err)
	})

	t.Run("no answer after the request went out", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer server.Close()
		client, err := NewClient(server.URL, testToken, 200*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.SendMessage(context.Background(), MessageParams{ChatID: 1, Text: "x"})
		check(t, err)
		if kind, _ := KindOf(err); kind != Failed {
			t.Errorf("a send with no answer is %v, want failed (it may have been delivered)", kind)
		}
		_, err = client.GetMe(context.Background())
		check(t, err)
		if kind, _ := KindOf(err); kind != Unavailable {
			t.Errorf("a read with no answer is %v, want unavailable", kind)
		}
	})

	t.Run("the connection is cut after the request", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
		}))
		defer server.Close()
		client, err := NewClient(server.URL, testToken, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.SendVoice(context.Background(), VoiceParams{ChatID: 1, Audio: []byte("x")})
		check(t, err)
		if kind, _ := KindOf(err); kind != Failed {
			t.Errorf("a send whose connection was cut after the request is %v, want failed", kind)
		}
	})

	t.Run("the description repeats the token", func(t *testing.T) {
		client, _ := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request, _ fakeCall) {
			reply(w, 400, `{"ok":false,"error_code":400,"description":"Bad Request: `+testToken+` is not a chat"}`)
		})
		_, err := client.SendMessage(context.Background(), MessageParams{ChatID: 1, Text: "x"})
		check(t, err)
	})
}

func TestTheCallersOwnEndIsNotTelegramsFailure(t *testing.T) {
	started := make(chan struct{})
	client, _ := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, _ fakeCall) {
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	_, err := client.SendMessage(ctx, MessageParams{ChatID: 1, Text: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the context's own", err)
	}
	if _, isCall := KindOf(err); isCall {
		t.Error("a cancelled call was reported as a failure of Telegram")
	}
}

func TestNewClientChecksItsArguments(t *testing.T) {
	for _, tc := range []struct {
		name, api, token string
		timeout          time.Duration
		wantError        string
	}{
		{"empty token", "", "", time.Second, "token is empty"},
		{"blank token", "", "  ", time.Second, "token is empty"},
		{"token with a slash", "", "123:abc/def", time.Second, "characters a token cannot have"},
		{"token with a space inside", "", "123: abc", time.Second, "characters a token cannot have"},
		{"no scheme", "api.telegram.org", testToken, time.Second, "http(s) URL"},
		{"ftp", "ftp://api.telegram.org", testToken, time.Second, "http(s) URL"},
		{"credentials", "https://u:p@api.telegram.org", testToken, time.Second, "scheme://host"},
		{"path", "https://api.telegram.org/bot", testToken, time.Second, "scheme://host"},
		{"query", "https://api.telegram.org?x=1", testToken, time.Second, "scheme://host"},
		{"no timeout", "", testToken, 0, "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(tc.api, tc.token, tc.timeout)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("err = %v, want one with %q", err, tc.wantError)
			}
			if strings.Contains(err.Error(), "abc") || strings.Contains(err.Error(), "AAH-test") {
				t.Errorf("the token is in the error: %v", err)
			}
		})
	}

	client, err := NewClient("  https://api.telegram.org/  ", " "+testToken+" ", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if client.base != "https://api.telegram.org" || client.token != testToken {
		t.Errorf("base %q token %q", client.base, client.token)
	}
	if client, err := NewClient("", testToken, time.Second); err != nil || client.base != DefaultAPIURL {
		t.Errorf("the default address: %v, %v", client, err)
	}
}

func TestKindNames(t *testing.T) {
	for kind, want := range map[Kind]string{
		Unavailable: "unavailable", RateLimited: "rate_limited", Blocked: "blocked", VoiceForbidden: "voice_forbidden",
		Rejected: "rejected", Conflict: "conflict", Failed: "failed", Kind(99): "kind(99)",
	} {
		if got := kind.String(); got != want {
			t.Errorf("Kind(%d) = %q, want %q", int(kind), got, want)
		}
	}
}

func TestAnAnswerCutOffInTheMiddle(t *testing.T) {
	// The headers promise more than arrives. For a send that is "may have been delivered"; for a read, nothing happened.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(200)
		io.WriteString(w, `{"ok":true,`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, testToken, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SendMessage(context.Background(), MessageParams{ChatID: 1, Text: "x"})
	if kind, isCall := KindOf(err); !isCall || kind != Failed {
		t.Errorf("a send with a cut answer: %v, want failed", err)
	}
	_, err = client.GetMe(context.Background())
	if kind, isCall := KindOf(err); !isCall || kind != Unavailable {
		t.Errorf("a read with a cut answer: %v, want unavailable", err)
	}
}

func TestAnAnswerThatIsTooBigIsNotRead(t *testing.T) {
	// A valid answer, only huge: without the limit it would be read and decoded.
	client, _ := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request, _ fakeCall) {
		reply(w, 200, `{"ok":true,"result":{"url":"`+strings.Repeat("a", maxResponseBytes+10)+`"}}`)
	})
	info, err := client.GetWebhookInfo(context.Background())
	if kind, isCall := KindOf(err); !isCall || kind != Failed {
		t.Errorf("err = %v (url of %d bytes), want a failed call", err, len(info.URL))
	}
	if err != nil && !strings.Contains(err.Error(), "is over") {
		t.Errorf("the failure does not say that the answer was too big: %v", err)
	}
}

func TestEveryCallUsesItsOwnConnection(t *testing.T) {
	// A send is not safe to repeat, so a connection that the other side closed meanwhile must not be reused.
	var mu sync.Mutex
	var closing []bool
	client, _ := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, _ fakeCall) {
		mu.Lock()
		closing = append(closing, r.Close)
		mu.Unlock()
		reply(w, 200, `{"ok":true,"result":{"id":1}}`)
	})
	for range 2 {
		if _, err := client.GetMe(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range closing {
		if !c {
			t.Errorf("call %d asked to keep its connection open", i)
		}
	}
}
