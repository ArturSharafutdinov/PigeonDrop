package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// DefaultAPIURL is the Bot API of Telegram itself. Another address is for tests and for a Bot API server of
	// one's own.
	DefaultAPIURL = "https://api.telegram.org"

	// maxResponseBytes bounds what is read from Telegram: its answers are a few hundred bytes, a hundred updates a
	// few hundred kilobytes at most.
	maxResponseBytes = 4 << 20
	// maxRetryAfter caps what Telegram may ask us to wait.
	maxRetryAfter = time.Hour
)

// Client calls the Bot API for one bot. It is safe for concurrent use.
type Client struct {
	base    string
	token   string
	timeout time.Duration
	client  *http.Client
}

// NewClient returns a client for the bot with the given token. apiURL is scheme://host[:port] with nothing after
// it; empty means DefaultAPIURL. timeout limits one call (long polling gets its own wait on top, see GetUpdates).
// Errors from here and from every call leave the token out of their text.
func NewClient(apiURL, token string, timeout time.Duration) (*Client, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("the bot token is empty")
	}
	if strings.ContainsAny(token, "/ \t\r\n") {
		return nil, errors.New("the bot token has characters a token cannot have") // not echoed: it is a secret
	}
	if strings.TrimSpace(apiURL) == "" {
		apiURL = DefaultAPIURL
	}
	u, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("the Bot API address must be an http(s) URL, got %q", apiURL)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("the Bot API address must be scheme://host[:port] only, got %q", apiURL)
	}
	if timeout <= 0 {
		return nil, errors.New("the Telegram timeout must be positive")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	// One connection per call. Sending is not safe to repeat, and a kept connection that the other side has since
	// closed would fail a send in a way that cannot be told from a real failure.
	transport.DisableKeepAlives = true
	return &Client{
		base:    strings.TrimRight(u.String(), "/"),
		token:   token,
		timeout: timeout,
		client: &http.Client{
			Transport: transport,
			// A redirect means the address is wrong; following it would hand the token over to someone else.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// envelope is the shape of every answer of the Bot API.
type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// scrub removes the token from a text that is going to be shown or logged.
func (c *Client) scrub(s string) string { return strings.ReplaceAll(s, c.token, "<token>") }

// call makes one request. safeToRepeat is true for calls that change nothing (reading), where a failure after the
// request went out is as harmless as one before it; for sending it is false, and such a failure is Failed, because
// the message may have arrived. extra is added to the timeout (a long poll's own wait). The answer's result is
// decoded into result when it is not nil.
func (c *Client) call(ctx context.Context, method string, safeToRepeat bool, extra time.Duration, contentType string, body []byte, result any) error {
	fail := func(kind Kind, code int, description string, retryAfter time.Duration, err error) error {
		e := &Error{Kind: kind, Method: method, Code: code, Description: c.scrub(description), RetryAfter: retryAfter}
		if err != nil {
			e.Err = errors.New(c.scrub(err.Error()))
		}
		return e
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout+extra)
	defer cancel()

	// Whether the request was sent to the end decides what a failure means: if it was not, nothing was delivered.
	var written atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			written.Store(true)
		}
	}}
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(callCtx, trace), http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fail(Unavailable, 0, "", 0, fmt.Errorf("cannot build the request: %w", err))
	}
	request.Header.Set("Content-Type", contentType)

	response, err := c.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // the caller's own end, not Telegram's failure
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // the url.Error repeats the request's address, which holds the token
		}
		if safeToRepeat || !written.Load() {
			return fail(Unavailable, 0, "", 0, fmt.Errorf("the request did not reach Telegram: %w", err))
		}
		return fail(Failed, 0, "", 0, fmt.Errorf("no answer after the request was sent: %w", err))
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		kind := Failed
		if safeToRepeat {
			kind = Unavailable
		}
		return fail(kind, 0, "", 0, fmt.Errorf("the answer was cut off: %w", err))
	}
	if len(raw) > maxResponseBytes {
		return fail(Failed, 0, "", 0, fmt.Errorf("the answer is over %d bytes", maxResponseBytes))
	}

	var env envelope
	parsed := json.Unmarshal(raw, &env) == nil // a proxy's page is not JSON; the status alone then speaks
	if response.StatusCode == http.StatusOK && parsed && env.OK {
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(env.Result, result); err != nil {
			return fail(Failed, 0, "", 0, fmt.Errorf("the answer is not what the Bot API describes: %w", err))
		}
		return nil
	}

	code := env.ErrorCode
	if code == 0 {
		code = response.StatusCode
	}
	description := env.Description
	if description == "" {
		description = strings.TrimSpace(http.StatusText(response.StatusCode))
	}
	var retryAfter time.Duration
	if env.Parameters != nil && env.Parameters.RetryAfter > 0 {
		retryAfter = min(time.Duration(env.Parameters.RetryAfter)*time.Second, maxRetryAfter)
	} else if seconds, err := strconv.Atoi(strings.TrimSpace(response.Header.Get("Retry-After"))); err == nil && seconds > 0 {
		retryAfter = min(time.Duration(seconds)*time.Second, maxRetryAfter)
	}
	return fail(classify(code, description, safeToRepeat), code, description, retryAfter, nil)
}

// classify decides what an error answer means. code is Telegram's error_code (or the HTTP status), description
// its words; the 400 answers are told apart by them, since the Bot API has no other way to say it.
func classify(code int, description string, safeToRepeat bool) Kind {
	text := strings.ToLower(description)
	switch {
	case code == http.StatusTooManyRequests:
		return RateLimited
	case code == http.StatusUnauthorized || code == http.StatusNotFound:
		return Unavailable // the token or the address is wrong: nothing about this message
	case code == http.StatusForbidden:
		return Blocked
	case code == http.StatusConflict:
		return Conflict
	case code == http.StatusBadRequest:
		switch {
		case strings.Contains(text, "voice_messages_forbidden"):
			return VoiceForbidden
		case strings.Contains(text, "chat not found"), strings.Contains(text, "peer_id_invalid"):
			return Blocked
		}
		return Rejected
	case code >= 300 && code < 400:
		return Unavailable // a redirect: the address is wrong
	case code >= 400 && code < 500:
		return Rejected
	case code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout:
		return Unavailable
	case code >= 500 && safeToRepeat:
		return Unavailable
	}
	return Failed
}

func jsonBody(v any) ([]byte, error) { return json.Marshal(v) }

// GetMe asks who the bot is. It is also the cheapest way to find out that the token and the network work.
func (c *Client) GetMe(ctx context.Context) (Me, error) {
	var me Me
	err := c.call(ctx, "getMe", true, 0, "application/json", []byte("{}"), &me)
	return me, err
}

// GetWebhookInfo says whether a webhook is set (getUpdates does not work while one is).
func (c *Client) GetWebhookInfo(ctx context.Context) (WebhookInfo, error) {
	var info WebhookInfo
	err := c.call(ctx, "getWebhookInfo", true, 0, "application/json", []byte("{}"), &info)
	return info, err
}

// GetUpdates waits up to wait for messages to the bot. offset is the id of the first update wanted: every update
// before it is taken as received and Telegram forgets it, so pass the last id seen plus one. Only messages and
// presses on buttons are asked for.
func (c *Client) GetUpdates(ctx context.Context, offset int64, wait time.Duration) ([]Update, error) {
	body, err := jsonBody(map[string]any{
		"offset": offset, "timeout": int(wait / time.Second), "limit": 100, "allowed_updates": []string{"message", "callback_query"},
	})
	if err != nil {
		return nil, err
	}
	var updates []Update
	err = c.call(ctx, "getUpdates", true, wait, "application/json", body, &updates)
	return updates, err
}

// SetMyCommands sets the menu of commands the bot shows.
func (c *Client) SetMyCommands(ctx context.Context, commands []Command, languageCode string) error {
	payload := map[string]any{"commands": commands}
	if languageCode != "" {
		payload["language_code"] = languageCode
	}
	body, err := jsonBody(payload)
	if err != nil {
		return err
	}
	return c.call(ctx, "setMyCommands", true, 0, "application/json", body, nil)
}

// SendVoice sends a recording as a voice message. The Bot API takes it as one when it is an Ogg file with Opus; the
// returned Sent.Kind says whether it did.
func (c *Client) SendVoice(ctx context.Context, p VoiceParams) (Sent, error) {
	fields := []field{{"chat_id", strconv.FormatInt(p.ChatID, 10)}}
	if p.Caption != "" {
		fields = append(fields, field{"caption", p.Caption})
	}
	if seconds := int(p.Duration.Round(time.Second) / time.Second); seconds > 0 {
		fields = append(fields, field{"duration", strconv.Itoa(seconds)})
	}
	if markup := keyboard(p.Buttons); markup != "" {
		fields = append(fields, field{"reply_markup", markup})
	}
	return c.sendFile(ctx, "sendVoice", fields, "voice", "voice.ogg", "audio/ogg", p.Audio)
}

// SendDocument sends a file as it is.
func (c *Client) SendDocument(ctx context.Context, p DocumentParams) (Sent, error) {
	fields := []field{{"chat_id", strconv.FormatInt(p.ChatID, 10)}}
	if p.Caption != "" {
		fields = append(fields, field{"caption", p.Caption})
	}
	if markup := keyboard(p.Buttons); markup != "" {
		fields = append(fields, field{"reply_markup", markup})
	}
	name := strings.NewReplacer("\r", "", "\n", "", "/", "_", "\\", "_").Replace(p.FileName)
	if name == "" {
		name = "file"
	}
	return c.sendFile(ctx, "sendDocument", fields, "document", name, "application/octet-stream", p.Data)
}

// SendMessage sends a text message. No link preview is made of links in the text: it is a transcript, and what is in
// it is not ours to have fetched.
func (c *Client) SendMessage(ctx context.Context, p MessageParams) (Sent, error) {
	payload := map[string]any{"chat_id": p.ChatID, "text": p.Text, "link_preview_options": map[string]any{"is_disabled": true}}
	if p.ReplyTo != 0 {
		payload["reply_parameters"] = map[string]any{"message_id": p.ReplyTo, "allow_sending_without_reply": true}
	}
	if len(p.Buttons) > 0 {
		payload["reply_markup"] = inlineKeyboard(p.Buttons)
	}
	body, err := jsonBody(payload)
	if err != nil {
		return Sent{}, err
	}
	var message Message
	if err := c.call(ctx, "sendMessage", false, 0, "application/json", body, &message); err != nil {
		return Sent{}, err
	}
	return sentFrom(message), nil
}

// AnswerCallbackQuery answers a press on a button. Telegram shows the person a progress bar until it is answered,
// so every press is, even when there is nothing to say. text (up to 200 characters) appears at the top of the chat,
// or, with alert, in a window the person has to close.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string, alert bool) error {
	payload := map[string]any{"callback_query_id": id}
	if text != "" {
		payload["text"] = text
		payload["show_alert"] = alert
	}
	body, err := jsonBody(payload)
	if err != nil {
		return err
	}
	return c.call(ctx, "answerCallbackQuery", true, 0, "application/json", body, nil)
}

// DeleteMessage deletes a message of the bot's from a private chat. Telegram only allows it for 48 hours after the
// message was sent; a later try is a Rejected error, as is one for a message that is no longer there (see
// AlreadyGone). Doing it twice is harmless, so a failure after the request went out is an Unavailable one.
func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	body, err := jsonBody(map[string]any{"chat_id": chatID, "message_id": messageID})
	if err != nil {
		return err
	}
	return c.call(ctx, "deleteMessage", true, 0, "application/json", body, nil)
}

// RemoveButtons takes the buttons off a message and leaves the message as it is. Telegram answers "message is not
// modified" (Rejected) when there were none.
func (c *Client) RemoveButtons(ctx context.Context, chatID, messageID int64) error {
	body, err := jsonBody(map[string]any{"chat_id": chatID, "message_id": messageID, "reply_markup": inlineKeyboard(nil)})
	if err != nil {
		return err
	}
	return c.call(ctx, "editMessageReplyMarkup", true, 0, "application/json", body, nil)
}

// inlineKeyboard is the reply_markup for buttons in one row; no buttons make an empty keyboard, which removes it.
func inlineKeyboard(buttons []Button) map[string]any {
	row := make([]map[string]string, 0, len(buttons))
	for _, b := range buttons {
		row = append(row, map[string]string{"text": b.Text, "callback_data": b.Data})
	}
	rows := [][]map[string]string{}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return map[string]any{"inline_keyboard": rows}
}

// keyboard is inlineKeyboard as the JSON text a multipart request carries it in; empty when there are no buttons.
func keyboard(buttons []Button) string {
	if len(buttons) == 0 {
		return ""
	}
	raw, err := json.Marshal(inlineKeyboard(buttons))
	if err != nil {
		return ""
	}
	return string(raw)
}

// AlreadyGone says whether err is Telegram's answer to deleting a message that is not there (the person deleted it
// themselves): for what the bot wants, the message is deleted.
func AlreadyGone(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == Rejected && strings.Contains(strings.ToLower(e.Description), "message to delete not found")
}

type field struct{ name, value string }

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

func (c *Client) sendFile(ctx context.Context, method string, fields []field, fileField, fileName, fileType string, data []byte) (Sent, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range fields {
		if err := w.WriteField(f.name, f.value); err != nil {
			return Sent{}, err
		}
	}
	header := textproto.MIMEHeader{}
	// Written out, in the form every browser and HTTP library uses (name first, both values in quotes): mime.FormatMediaType
	// would leave a value without quotes when it is a plain word, which is valid but not what a server's parser is most
	// likely to have been tried with.
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, quoteEscaper.Replace(fileField), quoteEscaper.Replace(fileName)))
	header.Set("Content-Type", fileType)
	part, err := w.CreatePart(header)
	if err != nil {
		return Sent{}, err
	}
	if _, err := part.Write(data); err != nil {
		return Sent{}, err
	}
	if err := w.Close(); err != nil {
		return Sent{}, err
	}

	var message Message
	if err := c.call(ctx, method, false, 0, w.FormDataContentType(), buf.Bytes(), &message); err != nil {
		return Sent{}, err
	}
	return sentFrom(message), nil
}

func sentFrom(m Message) Sent {
	sent := Sent{MessageID: m.MessageID, Kind: "text"}
	for _, a := range []struct {
		kind string
		file *Attachment
	}{{"voice", m.Voice}, {"audio", m.Audio}, {"document", m.Document}} {
		if a.file != nil {
			sent.Kind, sent.DurationSec, sent.MimeType = a.kind, a.file.Duration, a.file.MimeType
			break
		}
	}
	return sent
}
