package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// The "Просмотрено" button: it is put under every message of a recording, and pressing it deletes them from the chat.
// The deliveries run on a real database with a Telegram that remembers its calls (deliver_test.go); the presses go to a
// bot on the same database with a Telegram that remembers what it was asked to delete (bot_test.go).

type seenEnv struct {
	*deliveryEnv
	api *fakeTelegram
	bot *Bot
}

func newSeenEnv(t *testing.T) *seenEnv {
	t.Helper()
	e := newDeliveryEnv(t, DeliverySettings{})
	api := newFakeTelegram()
	bot := NewBot(api, e.q, db.InTx(e.pool), slog.New(slog.NewTextHandler(io.Discard, nil)), BotSettings{PageURL: botPage})
	return &seenEnv{deliveryEnv: e, api: api, bot: bot}
}

func longText() string { return strings.Repeat("слово ", 300) } // 1800 units: too long for a caption, fine for a message

// press is the person pressing the button under message `message` of chat `chat`, as `from`.
func (e *seenEnv) press(from, chat, message int64, data string) {
	e.t.Helper()
	e.bot.handleCallback(context.Background(), CallbackQuery{
		ID: "q1", From: &User{ID: from, LanguageCode: "en"},
		Message: &Message{MessageID: message, Chat: Chat{ID: chat, Type: "private"}}, Data: data,
	})
}

func (e *seenEnv) tries() [][2]int64 {
	e.api.mu.Lock()
	defer e.api.mu.Unlock()
	return append([][2]int64(nil), e.api.deleteTries...)
}

func (e *seenEnv) answer(t *testing.T) callbackAnswer {
	t.Helper()
	e.api.mu.Lock()
	defer e.api.mu.Unlock()
	if len(e.api.answers) != 1 {
		t.Fatalf("the press was answered %d times, want once: %+v", len(e.api.answers), e.api.answers)
	}
	return e.api.answers[0]
}

// sentLong delivers a recording whose text is too long for a caption: a voice message (1000) and its text (1001).
func (e *seenEnv) sentLong(language string, chat int64) uuid.UUID {
	e.t.Helper()
	user := e.user(language)
	e.link(user, chat)
	id := e.recording(user, "ogg-opus", 5000)
	e.transcript(id, "done", longText())
	e.mustSend(id)
	return id
}

// --- the button under every message of a recording

func TestEveryMessageOfARecordingHasTheButtonInTheLanguageOfTheAccount(t *testing.T) {
	for _, tc := range []struct {
		language, label string
	}{{"ru", "Просмотрено"}, {"en", "Seen"}} {
		t.Run(tc.language, func(t *testing.T) {
			e := newSeenEnv(t)
			id := e.sentLong(tc.language, 100)

			calls := e.sender.all()
			if len(calls) != 2 {
				t.Fatalf("calls = %v", e.sender.methods())
			}
			want := []Button{{Text: tc.label, Data: "seen:" + id.String()}}
			if got := calls[0].Voice.Buttons; len(got) != 1 || got[0] != want[0] {
				t.Errorf("voice message buttons = %+v, want %+v", got, want)
			}
			if got := calls[1].Message.Buttons; len(got) != 1 || got[0] != want[0] {
				t.Errorf("text message buttons = %+v, want %+v", got, want)
			}
			if n := len(want[0].Data); n > 64 {
				t.Errorf("the button's data is %d bytes; Telegram takes 64", n)
			}
		})
	}
}

func TestTheButtonIsOnAVoiceMessageWithItsTextAsCaptionToo(t *testing.T) {
	e := newSeenEnv(t)
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	e.transcript(id, "done", "Коротко.")
	e.mustSend(id)

	calls := e.sender.all()
	if len(calls) != 1 || len(calls[0].Voice.Buttons) != 1 || calls[0].Voice.Buttons[0].Data != SeenData(id) {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestTheButtonIsOnTheFileWhenVoiceMessagesAreForbiddenAndOnATextOnlyMessage(t *testing.T) {
	e := newSeenEnv(t)
	user := e.user("en")
	e.link(user, 100)
	asFile := e.recording(user, "ogg-opus", 3000)
	e.advance(time.Millisecond)
	oldFormat := e.recording(user, "zepp", 3000)
	e.transcript(asFile, "done", "Файл.")
	e.transcript(oldFormat, "done", "Старая запись.")
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		if c.Method == "sendVoice" {
			return Sent{}, &Error{Kind: VoiceForbidden, Method: "sendVoice", Code: 400, Description: "Bad Request: VOICE_MESSAGES_FORBIDDEN"}
		}
		return Sent{}, nil
	}

	e.mustSend(asFile)
	e.advance(time.Second)
	e.mustSend(oldFormat)

	calls := e.sender.all()
	if len(calls) != 3 || calls[1].Method != "sendDocument" || calls[2].Method != "sendMessage" {
		t.Fatalf("calls = %v", e.sender.methods())
	}
	if b := calls[1].Document.Buttons; len(b) != 1 || b[0].Data != SeenData(asFile) {
		t.Errorf("the file has buttons %+v", b)
	}
	if b := calls[2].Message.Buttons; len(b) != 1 || b[0].Data != SeenData(oldFormat) {
		t.Errorf("the text of an old recording has buttons %+v", b)
	}
}

func TestTheTextMessageIsRememberedForTheButton(t *testing.T) {
	e := newSeenEnv(t)
	long := e.sentLong("en", 100)
	user := e.user("en")
	e.link(user, 200)
	short := e.recording(user, "ogg-opus", 3000)
	e.transcript(short, "done", "Коротко.")
	e.advance(time.Second)
	oldFormat := e.recording(user, "zepp", 3000)
	e.advance(time.Second)
	e.transcript(oldFormat, "done", "Старая запись.")
	e.mustSend(short)
	e.advance(time.Second)
	e.mustSend(oldFormat)

	if row := e.delivery(long); deref64(row.VoiceMessageID) != 1000 || deref64(row.TextMessageID) != 1001 {
		t.Errorf("long: voice %d, text %d, want 1000 and 1001", deref64(row.VoiceMessageID), deref64(row.TextMessageID))
	}
	if row := e.delivery(short); deref64(row.VoiceMessageID) != 1002 || row.TextMessageID != nil {
		t.Errorf("short: voice %d, text %v: the text is the caption, not a message", deref64(row.VoiceMessageID), row.TextMessageID)
	}
	if row := e.delivery(oldFormat); row.VoiceMessageID != nil || deref64(row.TextMessageID) != 1003 {
		t.Errorf("old format: voice %v, text %d: the text message is the only one", row.VoiceMessageID, deref64(row.TextMessageID))
	}
}

// --- pressing it

func TestPressingTheButtonUnderAVoiceMessageWithACaptionDeletesIt(t *testing.T) {
	e := newSeenEnv(t)
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	e.transcript(id, "done", "Коротко.")
	e.mustSend(id)

	e.press(100, 100, 1000, SeenData(id))

	if got := e.tries(); len(got) != 1 || got[0] != [2]int64{100, 1000} {
		t.Errorf("deleted %v, want only the voice message", got)
	}
	if a := e.answer(t); a.ID != "q1" || a.Text != "" || a.Alert {
		t.Errorf("answer = %+v: the press must be answered, with nothing to say", a)
	}
	if len(e.api.unbuttoned) != 0 {
		t.Error("buttons were taken off a message that was deleted")
	}
}

func TestPressingTheButtonUnderEitherMessageDeletesBothAndThePressedOneLast(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pressed int64
		want    [][2]int64
	}{
		{"the voice message", 1000, [][2]int64{{100, 1001}, {100, 1000}}},
		{"the text", 1001, [][2]int64{{100, 1000}, {100, 1001}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSeenEnv(t)
			id := e.sentLong("en", 100)

			e.press(100, 100, tc.pressed, SeenData(id))

			got := e.tries()
			if len(got) != len(tc.want) || got[0] != tc.want[0] || got[1] != tc.want[1] {
				t.Errorf("deleted %v, want %v", got, tc.want)
			}
			if a := e.answer(t); a.Text != "" {
				t.Errorf("answer = %+v", a)
			}
		})
	}
}

func TestSomeoneElsesChatCannotDeleteTheMessagesOfARecording(t *testing.T) {
	e := newSeenEnv(t)
	id := e.sentLong("en", 100)

	// Another person presses a button that carries this recording's id (they could not have got one from the bot,
	// but nothing may rest on that): the ids stored for the recording belong to chat 100 and mean nothing in chat 777.
	e.press(777, 777, 55, SeenData(id))

	if got := e.tries(); len(got) != 1 || got[0] != [2]int64{777, 55} {
		t.Errorf("deleted %v: only the message pressed, in the chat it was pressed in", got)
	}
}

func TestAPressNotInThePersonsOwnChatDoesNothing(t *testing.T) {
	e := newSeenEnv(t)
	id := e.sentLong("en", 100)

	e.press(100, -5000, 1000, SeenData(id)) // a group: the chat's id is not the person's

	if got := e.tries(); len(got) != 0 {
		t.Errorf("deleted %v", got)
	}
	e.answer(t)
}

func TestARecordingThatIsGoneFromTheServerLeavesTheButtonDeletingItsOwnMessage(t *testing.T) {
	e := newSeenEnv(t)
	id := e.sentLong("en", 100)
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM messages WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	e.press(100, 100, 1001, SeenData(id))

	if got := e.tries(); len(got) != 1 || got[0] != [2]int64{100, 1001} {
		t.Errorf("deleted %v: with no record of the recording, only the message pressed", got)
	}
	if a := e.answer(t); a.Text != "" {
		t.Errorf("answer = %+v", a)
	}
}

func TestAMessageThePersonDeletedThemselvesDoesNotStopTheRest(t *testing.T) {
	e := newSeenEnv(t)
	id := e.sentLong("en", 100)
	e.api.deleteError = map[int64]error{1000: &Error{Kind: Rejected, Method: "deleteMessage", Code: 400, Description: "Bad Request: message to delete not found"}}

	e.press(100, 100, 1001, SeenData(id))

	if got := e.tries(); len(got) != 2 {
		t.Errorf("tried %v", got)
	}
	e.api.mu.Lock()
	deleted := append([][2]int64(nil), e.api.deleted...)
	e.api.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != [2]int64{100, 1001} {
		t.Errorf("deleted %v: the pressed message must go", deleted)
	}
	if a := e.answer(t); a.Text != "" || a.Alert {
		t.Errorf("answer = %+v: a message that is already gone is not a problem", a)
	}
}

// A second tap on the button, or a tap on a message the person just deleted by hand: the message is gone, which is what
// they wanted, and not "too old".
func TestPressingTheButtonUnderAMessageThatIsAlreadyGoneIsNotAnError(t *testing.T) {
	e := newSeenEnv(t)
	id := e.sentLong("en", 100)
	gone := &Error{Kind: Rejected, Method: "deleteMessage", Code: 400, Description: "Bad Request: message to delete not found"}
	e.api.deleteError = map[int64]error{1000: gone, 1001: gone}

	e.press(100, 100, 1001, SeenData(id))

	if a := e.answer(t); a.Text != "" || a.Alert {
		t.Errorf("answer = %+v: nothing is wrong", a)
	}
	if len(e.api.unbuttoned) != 0 {
		t.Errorf("buttons taken off %v", e.api.unbuttoned)
	}
}

func TestAMessageTooOldToDeleteLosesItsButtonAndThePersonIsToldWhy(t *testing.T) {
	for _, tc := range []struct {
		language, wantText string
	}{
		{"ru", "Telegram не позволяет боту удалять сообщения старше 48 часов. Удалите это сообщение сами."},
		{"en", "Telegram does not let a bot delete messages older than 48 hours. Please delete this message yourself."},
	} {
		t.Run(tc.language, func(t *testing.T) {
			e := newSeenEnv(t)
			id := e.sentLong(tc.language, 100)
			tooOld := &Error{Kind: Rejected, Method: "deleteMessage", Code: 400, Description: "Bad Request: message can't be deleted"}
			e.api.deleteError = map[int64]error{1000: tooOld, 1001: tooOld}

			e.press(100, 100, 1001, SeenData(id))

			a := e.answer(t)
			if a.Text != tc.wantText || !a.Alert {
				t.Errorf("answer = %+v, want an alert saying %q", a, tc.wantText)
			}
			if len(e.api.unbuttoned) != 1 || e.api.unbuttoned[0] != [2]int64{100, 1001} {
				t.Errorf("buttons taken off %v, want the pressed message's", e.api.unbuttoned)
			}
		})
	}
}

func TestAnOldSiblingDoesNotSpoilADeletionThatWorked(t *testing.T) {
	e := newSeenEnv(t)
	id := e.sentLong("en", 100)
	e.api.deleteError = map[int64]error{1000: &Error{Kind: Rejected, Method: "deleteMessage", Code: 400, Description: "Bad Request: message can't be deleted"}}

	e.press(100, 100, 1001, SeenData(id))

	if a := e.answer(t); a.Text != "" || a.Alert {
		t.Errorf("answer = %+v: the pressed message went", a)
	}
	if len(e.api.unbuttoned) != 0 {
		t.Errorf("buttons taken off %v", e.api.unbuttoned)
	}
}

func TestATelegramThatCannotBeReachedLeavesTheButtonToPressAgain(t *testing.T) {
	for name, failure := range map[string]error{
		"unreachable":  &Error{Kind: Unavailable, Method: "deleteMessage"},
		"rate limited": &Error{Kind: RateLimited, Method: "deleteMessage", Code: 429, RetryAfter: time.Second},
		"unclear":      &Error{Kind: Failed, Method: "deleteMessage"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSeenEnv(t)
			id := e.sentLong("ru", 100)
			e.api.deleteError = map[int64]error{1000: failure}

			e.press(100, 100, 1001, SeenData(id))

			a := e.answer(t)
			if a.Text != say(langRU, phraseTryAgain, botPage) || a.Alert {
				t.Errorf("answer = %+v: the person is told to try again", a)
			}
			if len(e.api.unbuttoned) != 0 {
				t.Error("the button was taken off a message that can still be deleted")
			}

			// Pressing again, once Telegram answers, finishes the job: what is gone already does not matter.
			e.api.mu.Lock()
			e.api.deleteError = map[int64]error{1000: &Error{Kind: Rejected, Method: "deleteMessage", Code: 400, Description: "Bad Request: message to delete not found"}}
			e.api.answers = nil
			e.api.mu.Unlock()
			e.press(100, 100, 1001, SeenData(id))
			if a := e.answer(t); a.Text != "" {
				t.Errorf("second answer = %+v", a)
			}
		})
	}
}

func TestADatabaseThatFailsLeavesTheButtonToPressAgain(t *testing.T) {
	e := newSeenEnv(t)
	id := e.sentLong("en", 100)
	e.bot.db = brokenLookup{Querier: e.q}

	e.press(100, 100, 1001, SeenData(id))

	if got := e.tries(); len(got) != 0 {
		t.Errorf("deleted %v without knowing which messages belong together", got)
	}
	if a := e.answer(t); a.Text != say(langEN, phraseTryAgain, botPage) {
		t.Errorf("answer = %+v", a)
	}
}

type brokenLookup struct{ dbgen.Querier }

func (brokenLookup) GetTelegramDeliveryMessages(context.Context, dbgen.GetTelegramDeliveryMessagesParams) (dbgen.GetTelegramDeliveryMessagesRow, error) {
	return dbgen.GetTelegramDeliveryMessagesRow{}, errors.New("the database is away")
}

func TestPressesThatAreNotOurButtonsAreAnsweredAndDeleteNothing(t *testing.T) {
	e := newSeenEnv(t)
	id := e.sentLong("en", 100)
	good := id.String()
	for name, data := range map[string]string{
		"empty":           "",
		"no id":           "seen:",
		"not an id":       "seen:not-a-uuid",
		"another prefix":  "other:" + good,
		"upper case":      "seen:" + strings.ToUpper(good),
		"braces":          "seen:{" + good + "}",
		"no hyphens":      "seen:" + strings.ReplaceAll(good, "-", ""),
		"text after":      "seen:" + good + "x",
		"urn":             "seen:urn:uuid:" + good,
		"the id alone":    good,
		"another keyword": "SEEN:" + good,
	} {
		t.Run(name, func(t *testing.T) {
			e.api.mu.Lock()
			e.api.answers, e.api.deleteTries = nil, nil
			e.api.mu.Unlock()

			e.press(100, 100, 1001, data)

			if got := e.tries(); len(got) != 0 {
				t.Errorf("deleted %v", got)
			}
			if a := e.answer(t); a.Text != "" {
				t.Errorf("answer = %+v", a)
			}
		})
	}
}

func TestAPressWithoutAMessageOrASenderIsAnsweredAndIgnored(t *testing.T) {
	e := newSeenEnv(t)
	id := e.sentLong("en", 100)
	ctx := context.Background()

	e.bot.handleCallback(ctx, CallbackQuery{ID: "a", From: &User{ID: 100}, Data: SeenData(id)}) // an inline message
	e.bot.handleCallback(ctx, CallbackQuery{ID: "b", Message: &Message{MessageID: 1, Chat: Chat{ID: 100}}, Data: SeenData(id)})

	if got := e.tries(); len(got) != 0 {
		t.Errorf("deleted %v", got)
	}
	e.api.mu.Lock()
	defer e.api.mu.Unlock()
	if len(e.api.answers) != 2 {
		t.Errorf("answers = %+v: Telegram shows a progress bar until a press is answered", e.api.answers)
	}
}

func TestAPressIsDealtWithEvenIfItsAnswerCannotBeSent(t *testing.T) {
	e := newSeenEnv(t)
	id := e.sentLong("en", 100)
	e.api.answerError = &Error{Kind: Unavailable, Method: "answerCallbackQuery"}

	e.press(100, 100, 1001, SeenData(id))

	if got := e.tries(); len(got) != 2 {
		t.Errorf("deleted %v: the messages go whether or not the answer does", got)
	}
}

func TestRunHandlesPressesOnButtons(t *testing.T) {
	e := newBotEnv(t)
	id := uuid.New() // a recording the server does not know: the message pressed is the only one deleted
	e.api.batches = [][]Update{{
		{UpdateID: 20, CallbackQuery: &CallbackQuery{ID: "z", From: &User{ID: 100}, Message: &Message{MessageID: 7, Chat: Chat{ID: 100, Type: "private"}}, Data: SeenData(id)}},
		{UpdateID: 21, Message: ptr(privateMessage(100, "hello"))},
	}}
	stop := e.run(t)
	defer stop()

	e.api.polls(t, 2)

	e.api.mu.Lock()
	defer e.api.mu.Unlock()
	if len(e.api.deleted) != 1 || e.api.deleted[0] != [2]int64{100, 7} || len(e.api.answers) != 1 || e.api.answers[0].ID != "z" {
		t.Errorf("deleted %v, answers %+v", e.api.deleted, e.api.answers)
	}
	if got := e.api.offsets; len(got) < 2 || got[1] != 22 {
		t.Errorf("offsets = %v: a press is an update like another", got)
	}
}
