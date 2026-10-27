package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The calls the "Просмотрено" button needs of the Bot API: buttons under what is sent, and the answers to presses.

const seenKeyboard = `{"inline_keyboard":[[{"callback_data":"seen:abc","text":"Seen"}]]}`

var seenButtons = []Button{{Text: "Seen", Data: "seen:abc"}}

func TestSendVoiceAndDocumentCarryTheButtonsAsReplyMarkup(t *testing.T) {
	client, f := newFakeAPI(t, ok(`{"message_id":1,"chat":{"id":9,"type":"private"},"voice":{"file_id":"X","duration":3}}`))
	if _, err := client.SendVoice(context.Background(), VoiceParams{ChatID: 9, Audio: []byte("OggS"), Buttons: seenButtons}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendDocument(context.Background(), DocumentParams{ChatID: 9, Data: []byte("OggS"), FileName: "r.ogg", Buttons: seenButtons}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendVoice(context.Background(), VoiceParams{ChatID: 9, Audio: []byte("OggS")}); err != nil {
		t.Fatal(err)
	}

	for i, method := range []string{"sendVoice", "sendDocument"} {
		fields, _, _ := parseMultipart(t, f.calls[i])
		if f.calls[i].Method != method || fields["reply_markup"] != seenKeyboard {
			t.Errorf("%s: reply_markup = %q, want %s", method, fields["reply_markup"], seenKeyboard)
		}
	}
	if fields, _, _ := parseMultipart(t, f.calls[2]); func() bool { _, has := fields["reply_markup"]; return has }() {
		t.Errorf("a voice message with no buttons carries a reply_markup: %v", fields)
	}
}

func TestSendMessageCarriesTheButtonsAsReplyMarkup(t *testing.T) {
	client, f := newFakeAPI(t, ok(`{"message_id":55,"chat":{"id":9,"type":"private"},"text":"hi"}`))
	if _, err := client.SendMessage(context.Background(), MessageParams{ChatID: 9, Text: "x", Buttons: seenButtons}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendMessage(context.Background(), MessageParams{ChatID: 9, Text: "x"}); err != nil {
		t.Fatal(err)
	}

	var body struct {
		ReplyMarkup json.RawMessage `json:"reply_markup"`
	}
	if err := json.Unmarshal(f.calls[0].Body, &body); err != nil || string(body.ReplyMarkup) != seenKeyboard {
		t.Errorf("reply_markup = %s (%v), want %s", body.ReplyMarkup, err, seenKeyboard)
	}
	if strings.Contains(string(f.calls[1].Body), "reply_markup") {
		t.Errorf("a message with no buttons carries a reply_markup: %s", f.calls[1].Body)
	}
}

func TestAnswerCallbackQuery(t *testing.T) {
	client, f := newFakeAPI(t, ok(`true`))
	if err := client.AnswerCallbackQuery(context.Background(), "qid", "", false); err != nil {
		t.Fatal(err)
	}
	if err := client.AnswerCallbackQuery(context.Background(), "qid", "Слишком старое", true); err != nil {
		t.Fatal(err)
	}

	if f.calls[0].Method != "answerCallbackQuery" || string(f.calls[0].Body) != `{"callback_query_id":"qid"}` {
		t.Errorf("an answer with nothing to say: %s %s", f.calls[0].Method, f.calls[0].Body)
	}
	var body map[string]any
	if err := json.Unmarshal(f.calls[1].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["callback_query_id"] != "qid" || body["text"] != "Слишком старое" || body["show_alert"] != true {
		t.Errorf("an alert: %v", body)
	}
}

func TestDeleteMessage(t *testing.T) {
	client, f := newFakeAPI(t, ok(`true`))
	if err := client.DeleteMessage(context.Background(), 9, 1234); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(f.calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if f.calls[0].Method != "deleteMessage" || body["chat_id"] != float64(9) || body["message_id"] != float64(1234) || len(body) != 2 {
		t.Errorf("%s %v", f.calls[0].Method, body)
	}
}

func TestRemoveButtonsSendsAnEmptyKeyboard(t *testing.T) {
	client, f := newFakeAPI(t, ok(`{"message_id":1234,"chat":{"id":9,"type":"private"}}`))
	if err := client.RemoveButtons(context.Background(), 9, 1234); err != nil {
		t.Fatal(err)
	}
	var body struct {
		ChatID      int64           `json:"chat_id"`
		MessageID   int64           `json:"message_id"`
		ReplyMarkup json.RawMessage `json:"reply_markup"`
	}
	if err := json.Unmarshal(f.calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if f.calls[0].Method != "editMessageReplyMarkup" || body.ChatID != 9 || body.MessageID != 1234 || string(body.ReplyMarkup) != `{"inline_keyboard":[]}` {
		t.Errorf("%s %+v %s", f.calls[0].Method, body, body.ReplyMarkup)
	}
}

func TestGetUpdatesReadsPressesOnButtons(t *testing.T) {
	client, _ := newFakeAPI(t, ok(`[
		{"update_id":1,"callback_query":{"id":"c1","from":{"id":9,"is_bot":false,"language_code":"ru"},"message":{"message_id":88,"date":1790000000,"chat":{"id":9,"type":"private"},"voice":{"file_id":"X","duration":3}},"data":"seen:abc"}},
		{"update_id":2,"callback_query":{"id":"c2","from":{"id":9,"is_bot":false},"message":{"message_id":89,"date":0,"chat":{"id":9,"type":"private"}},"data":"seen:def"}}
	]`))
	updates, err := client.GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 || updates[0].Message != nil {
		t.Fatalf("updates = %+v", updates)
	}
	q := updates[0].CallbackQuery
	if q == nil || q.ID != "c1" || q.From == nil || q.From.ID != 9 || q.From.LanguageCode != "ru" || q.Data != "seen:abc" || q.Message == nil || q.Message.MessageID != 88 || q.Message.Chat.ID != 9 {
		t.Errorf("press = %+v", q)
	}
	// A message older than 48 hours reaches the bot with its chat and id and nothing else; that is all the bot uses.
	if old := updates[1].CallbackQuery; old == nil || old.Message == nil || old.Message.MessageID != 89 || old.Message.Chat.ID != 9 {
		t.Errorf("old press = %+v", old)
	}
}

func TestWhatDeletingAMessageAnswersWhenItCannotBeDone(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		want        Kind
		alreadyGone bool
	}{
		{"too old", 400, `{"ok":false,"error_code":400,"description":"Bad Request: message can't be deleted"}`, Rejected, false},
		{"too old, the older wording", 400, `{"ok":false,"error_code":400,"description":"Bad Request: message can't be deleted for everyone"}`, Rejected, false},
		{"the person deleted it", 400, `{"ok":false,"error_code":400,"description":"Bad Request: message to delete not found"}`, Rejected, true},
		{"blocked", 403, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`, Blocked, false},
		{"too many requests", 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`, RateLimited, false},
		{"telegram is down", 502, `<html>Bad Gateway</html>`, Unavailable, false},
		{"a server error: deleting twice is harmless, so it is not an unclear failure", 500, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`, Unavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request, _ fakeCall) { reply(w, tc.status, tc.body) })

			err := client.DeleteMessage(context.Background(), 9, 1)

			if kind, isCall := KindOf(err); !isCall || kind != tc.want {
				t.Errorf("kind = %v, %v, want %s (%v)", kind, isCall, tc.want, err)
			}
			if AlreadyGone(err) != tc.alreadyGone {
				t.Errorf("AlreadyGone = %v, want %v", AlreadyGone(err), tc.alreadyGone)
			}
		})
	}
}

func TestADeleteWhoseAnswerIsLostIsNotAnUnclearFailure(t *testing.T) {
	client, _ := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request, _ fakeCall) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close() // the request was read, the answer never comes
	})

	err := client.DeleteMessage(context.Background(), 9, 1)

	if kind, _ := KindOf(err); kind != Unavailable {
		t.Errorf("kind = %v (%v): deleting again does no harm, so a lost answer is as good as a lost request", kind, err)
	}
}

func TestAlreadyGoneIsOnlyTheMessageToDeleteNotFoundAnswer(t *testing.T) {
	if AlreadyGone(nil) || AlreadyGone(errors.New("message to delete not found")) {
		t.Error("something that is not Telegram's answer counts as a deleted message")
	}
	if AlreadyGone(&Error{Kind: Blocked, Description: "message to delete not found"}) || AlreadyGone(&Error{Kind: Unavailable, Description: "message to delete not found"}) {
		t.Error("an answer of another kind counts as a deleted message")
	}
	if AlreadyGone(&Error{Kind: Rejected, Description: "Bad Request: chat not found"}) || AlreadyGone(&Error{Kind: Rejected, Description: "Bad Request: message can't be deleted"}) {
		t.Error("another refusal counts as a deleted message")
	}
	if !AlreadyGone(&Error{Kind: Rejected, Description: "Bad Request: MESSAGE TO DELETE NOT FOUND"}) {
		t.Error("the case of the words matters")
	}
}
