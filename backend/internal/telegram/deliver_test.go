package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/storage"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/testdb"
)

// The queue that sends recordings to a linked chat, on a real database and a real directory, with a Telegram that does
// what the test says and a clock the test moves.

var deliveryStart = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// sentCall is one call the delivery made to Telegram.
type sentCall struct {
	Method   string // sendVoice, sendDocument or sendMessage
	Voice    VoiceParams
	Document DocumentParams
	Message  MessageParams
	At       time.Time
}

func (c sentCall) chat() int64 {
	switch c.Method {
	case "sendVoice":
		return c.Voice.ChatID
	case "sendDocument":
		return c.Document.ChatID
	}
	return c.Message.ChatID
}

// fakeSender is a Telegram that remembers its calls and, when the test gives it a script, fails them.
type fakeSender struct {
	mu    sync.Mutex
	calls []sentCall
	// script, when set, decides the answer to the n-th call (counting from 0): a nil error is a success.
	script func(n int, c sentCall) (Sent, error)
}

func (f *fakeSender) record(c sentCall) (Sent, error) {
	f.mu.Lock()
	c.At = time.Now()
	n := len(f.calls)
	f.calls = append(f.calls, c)
	script := f.script
	f.mu.Unlock()
	if script != nil {
		if sent, err := script(n, c); sent != (Sent{}) || err != nil {
			return sent, err
		}
	}
	id := int64(1000 + n)
	switch c.Method {
	case "sendVoice":
		return Sent{MessageID: id, Kind: "voice", DurationSec: 3, MimeType: "audio/ogg"}, nil
	case "sendDocument":
		return Sent{MessageID: id, Kind: "document"}, nil
	}
	return Sent{MessageID: id, Kind: "text"}, nil
}

func (f *fakeSender) SendVoice(_ context.Context, p VoiceParams) (Sent, error) {
	return f.record(sentCall{Method: "sendVoice", Voice: p})
}

func (f *fakeSender) SendDocument(_ context.Context, p DocumentParams) (Sent, error) {
	return f.record(sentCall{Method: "sendDocument", Document: p})
}

func (f *fakeSender) SendMessage(_ context.Context, p MessageParams) (Sent, error) {
	return f.record(sentCall{Method: "sendMessage", Message: p})
}

func (f *fakeSender) all() []sentCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentCall(nil), f.calls...)
}

func (f *fakeSender) methods() []string {
	var out []string
	for _, c := range f.all() {
		out = append(out, c.Method)
	}
	return out
}

func newTestLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type syncLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type deliveryEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *dbgen.Queries
	files   *storage.Disk
	sender  *fakeSender
	d       *Deliverer
	logs    *syncLog
	intake  Intake
	mu      sync.Mutex
	clock   time.Time
	devices map[uuid.UUID]uuid.UUID
	users   int
}

const recordingBytes = "OggS\x00pretend this is a recording"

func newDeliveryEnv(t *testing.T, settings DeliverySettings) *deliveryEnv {
	t.Helper()
	pool := testdb.New(t)
	files, err := storage.NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &deliveryEnv{t: t, pool: pool, q: dbgen.New(pool), files: files, sender: &fakeSender{}, logs: &syncLog{},
		intake: Intake{Wait: 3 * time.Minute}, clock: deliveryStart, devices: map[uuid.UUID]uuid.UUID{}}
	if settings.MinGap == 0 {
		settings.MinGap = time.Millisecond
	}
	if settings.Poll == 0 {
		settings.Poll = 5 * time.Millisecond
	}
	e.d = NewDeliverer(e.q, files, e.sender, newTestLogger(e.logs), settings)
	e.d.Now = e.now
	return e
}

func (e *deliveryEnv) now() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clock
}

func (e *deliveryEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock = e.clock.Add(d)
}

func (e *deliveryEnv) user(language string) uuid.UUID {
	e.t.Helper()
	e.users++
	u, err := e.q.CreateUser(context.Background(), dbgen.CreateUserParams{
		Email: fmt.Sprintf("user%d@example.com", e.users), PasswordHash: "x", Language: language, CreatedAt: deliveryStart,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

// link links the user to a chat, as the bot does.
func (e *deliveryEnv) link(user uuid.UUID, chat int64) {
	e.t.Helper()
	if err := e.q.UpsertTelegramLink(context.Background(), dbgen.UpsertTelegramLinkParams{UserID: user, ChatID: chat, Now: deliveryStart}); err != nil {
		e.t.Fatal(err)
	}
}

// recording stores a message of the user the way an upload does, with its file, and puts it in the queue as the
// upload would. format is ogg-opus or zepp.
func (e *deliveryEnv) recording(user uuid.UUID, format string, durationMs int32) uuid.UUID {
	e.t.Helper()
	ctx := context.Background()
	device, ok := e.devices[user]
	if !ok {
		d, err := e.q.CreateDevice(ctx, dbgen.CreateDeviceParams{UserID: user, Name: "watch", TokenHash: []byte(user.String()), CreatedAt: deliveryStart})
		if err != nil {
			e.t.Fatal(err)
		}
		device = d.ID
		e.devices[user] = device
	}
	id := uuid.New()
	if _, err := e.q.InsertMessage(ctx, dbgen.InsertMessageParams{
		ID: id, UserID: user, DeviceID: device, ClientMessageID: id.String(), Format: format, BytesReceived: int64(len(recordingBytes)),
		DurationMs: &durationMs, HasWav: true, ReceivedAt: e.now(),
	}); err != nil {
		e.t.Fatal(err)
	}
	if err := e.files.Put(storage.RecordingName(id), []byte(recordingBytes)); err != nil {
		e.t.Fatal(err)
	}
	if err := e.intake.Enqueue(ctx, e.q, id, e.now()); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// transcript gives the recording an entry in the speech recognition queue in the given state.
func (e *deliveryEnv) transcript(id uuid.UUID, state, text string) {
	e.t.Helper()
	var query string
	var args []any
	switch state {
	case "pending":
		query = `INSERT INTO transcriptions (message_id, state, run_after, enqueued_at) VALUES ($1, 'pending', $2, $2)`
		args = []any{id, e.now()}
	case "running":
		query = `INSERT INTO transcriptions (message_id, state, attempts, run_after, enqueued_at, locked_until) VALUES ($1, 'running', 1, $2, $2, $2::timestamptz + interval '1 hour')`
		args = []any{id, e.now()}
	case "done":
		query = `INSERT INTO transcriptions (message_id, state, run_after, enqueued_at, finished_at, text, engine) VALUES ($1, 'done', $2, $2, $2, $3, 'test')`
		args = []any{id, e.now(), text}
	case "failed", "skipped":
		query = `INSERT INTO transcriptions (message_id, state, run_after, enqueued_at, finished_at, error_code) VALUES ($1, '` + state + `', $2, $2, $2, 'too_long')`
		args = []any{id, e.now()}
	default:
		e.t.Fatalf("no such state %q", state)
	}
	if _, err := e.pool.Exec(context.Background(), query, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *deliveryEnv) setTranscriptState(id uuid.UUID, state, text string) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM transcriptions WHERE message_id = $1`, id); err != nil {
		e.t.Fatal(err)
	}
	e.transcript(id, state, text)
}

func (e *deliveryEnv) delivery(id uuid.UUID) dbgen.TelegramDelivery {
	e.t.Helper()
	row, err := e.q.GetTelegramDelivery(context.Background(), id)
	if err != nil {
		e.t.Fatalf("no delivery for %s: %v", id, err)
	}
	return row
}

func (e *deliveryEnv) hasDelivery(id uuid.UUID) bool {
	e.t.Helper()
	_, err := e.q.GetTelegramDelivery(context.Background(), id)
	return err == nil
}

// runOnce takes one ready delivery and says whether there was one.
func (e *deliveryEnv) runOnce() bool {
	e.t.Helper()
	did, err := e.d.RunOnce(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return did
}

func (e *deliveryEnv) mustSend(id uuid.UUID) dbgen.TelegramDelivery {
	e.t.Helper()
	if !e.runOnce() {
		e.t.Fatal("no delivery was ready")
	}
	return e.delivery(id)
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefS(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// --- joining the queue

func TestOnlyAnAccountWithAWorkingLinkGetsADelivery(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	notLinked := e.user("en")
	linked := e.user("en")
	blocked := e.user("en")
	e.link(linked, 100)
	e.link(blocked, 200)
	if _, err := e.q.BlockTelegramLink(context.Background(), dbgen.BlockTelegramLinkParams{UserID: blocked, ChatID: 200, Now: deliveryStart}); err != nil {
		t.Fatal(err)
	}

	a, b, c := e.recording(notLinked, "ogg-opus", 3000), e.recording(linked, "ogg-opus", 3000), e.recording(blocked, "ogg-opus", 3000)

	if e.hasDelivery(a) || e.hasDelivery(c) {
		t.Error("an account without a working link was given a delivery")
	}
	row := e.delivery(b)
	if row.State != StatePending || row.Attempts != 0 || !row.RunAfter.Equal(deliveryStart) || !row.WaitUntil.Equal(deliveryStart.Add(3*time.Minute)) || !row.EnqueuedAt.Equal(deliveryStart) {
		t.Errorf("row = %+v", row)
	}
	// Repeating the upload (the same message again) changes nothing.
	e.advance(time.Minute)
	if err := e.intake.Enqueue(context.Background(), e.q, b, e.now()); err != nil {
		t.Fatal(err)
	}
	if again := e.delivery(b); !again.EnqueuedAt.Equal(deliveryStart) || !again.WaitUntil.Equal(row.WaitUntil) {
		t.Errorf("the second Enqueue changed the entry: %+v", again)
	}
}

func TestADeliveryGoesWithItsRecording(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)

	if _, err := e.pool.Exec(context.Background(), `DELETE FROM messages WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	if e.hasDelivery(id) {
		t.Error("the delivery outlived its recording")
	}
	if e.runOnce() {
		t.Error("a deleted recording was taken")
	}
	if len(e.sender.all()) != 0 {
		t.Error("something was sent")
	}
}

func TestUnlinkingTheAccountKeepsTheDeliveriesAlreadyQueuedButTheyAreNotSent(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	if _, err := e.q.DeleteTelegramLink(context.Background(), user); err != nil {
		t.Fatal(err)
	}

	row := e.mustSend(id)

	if row.State != StateSkipped || derefS(row.ErrorCode) != CodeUnlinked || len(e.sender.all()) != 0 {
		t.Errorf("row = %+v, sent %v", row, e.sender.methods())
	}
}

func TestADeliveryForAChatThatBlockedTheBotIsNotSent(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	first := e.recording(user, "ogg-opus", 3000)
	e.advance(time.Millisecond) // so that the queue has an order
	second := e.recording(user, "ogg-opus", 3000)
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		return Sent{}, &Error{Kind: Blocked, Method: "sendVoice", Code: 403, Description: "Forbidden: bot was blocked by the user"}
	}

	if row := e.mustSend(first); row.State != StateSkipped || derefS(row.ErrorCode) != CodeBlocked {
		t.Fatalf("first = %+v", row)
	}
	callsAfterFirst := len(e.sender.all())
	e.advance(time.Second)
	if row := e.mustSend(second); row.State != StateSkipped || derefS(row.ErrorCode) != CodeBlocked {
		t.Errorf("second = %+v", row)
	}
	if len(e.sender.all()) != callsAfterFirst {
		t.Error("a delivery to a chat known to be blocked was tried")
	}
	link, err := e.q.GetTelegramLink(context.Background(), user)
	if err != nil || link.BlockedAt == nil {
		t.Errorf("the link was not switched off: %+v %v", link, err)
	}
	// A recording that arrives now is not even queued.
	if third := e.recording(user, "ogg-opus", 3000); e.hasDelivery(third) {
		t.Error("a recording of a blocked link was queued")
	}
	// Linking again (the person presses Start) clears the block.
	e.link(user, 100)
	if fourth := e.recording(user, "ogg-opus", 3000); !e.hasDelivery(fourth) {
		t.Error("linking again did not bring deliveries back")
	}
}

func TestABlockOfAnOldChatDoesNotBlockTheNewOne(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		e.link(user, 200) // the person links another chat while the send is in flight
		return Sent{}, &Error{Kind: Blocked, Method: "sendVoice", Code: 403}
	}

	e.mustSend(id)

	link, err := e.q.GetTelegramLink(context.Background(), user)
	if err != nil || link.ChatID != 200 || link.BlockedAt != nil {
		t.Errorf("the new chat was blocked for the old one's sake: %+v %v", link, err)
	}
}

// --- waiting for the text

func TestADeliveryWaitsForTheTextOfItsRecording(t *testing.T) {
	for _, state := range []string{"pending", "running"} {
		t.Run(state, func(t *testing.T) {
			e := newDeliveryEnv(t, DeliverySettings{})
			user := e.user("en")
			e.link(user, 100)
			id := e.recording(user, "ogg-opus", 3000)
			e.transcript(id, state, "")

			e.advance(3*time.Minute - time.Second)
			if e.runOnce() {
				t.Fatal("the delivery did not wait for the text")
			}

			e.advance(time.Second)
			row := e.mustSend(id)
			if row.State != StateSent || row.WithText == nil || *row.WithText {
				t.Errorf("row = %+v: after the wait the recording goes without text", row)
			}
			if calls := e.sender.all(); len(calls) != 1 || calls[0].Method != "sendVoice" || calls[0].Voice.Caption != "" {
				t.Errorf("calls = %+v", calls)
			}
		})
	}
}

func TestADeliveryGoesAsSoonAsTheTextIsFinal(t *testing.T) {
	for _, tc := range []struct {
		name, state, text, caption string
	}{
		{"done", "done", "Привет, мир", "Привет, мир"},
		{"silence", "done", "", ""},
		{"failed", "failed", "", ""},
		{"skipped", "skipped", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDeliveryEnv(t, DeliverySettings{})
			user := e.user("en")
			e.link(user, 100)
			id := e.recording(user, "ogg-opus", 3000)
			e.transcript(id, "pending", "")
			if e.runOnce() {
				t.Fatal("taken while its text was being made")
			}

			e.setTranscriptState(id, tc.state, tc.text)

			row := e.mustSend(id)
			calls := e.sender.all()
			if row.State != StateSent || len(calls) != 1 || calls[0].Voice.Caption != tc.caption {
				t.Errorf("row %+v calls %+v", row, calls)
			}
			if row.WithText == nil || *row.WithText != (tc.caption != "") {
				t.Errorf("with_text = %v", row.WithText)
			}
		})
	}
}

func TestWithRecognitionOffTheRecordingGoesAtOnce(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000) // no entry in the recognition queue

	row := e.mustSend(id)

	if row.State != StateSent || row.WithText == nil || *row.WithText {
		t.Errorf("row = %+v", row)
	}
}

func TestTheOldestReadyDeliveryGoesFirstAndAWaitingOneDoesNotBlockTheOthers(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	waiting := e.recording(user, "ogg-opus", 3000)
	e.transcript(waiting, "pending", "")
	e.advance(time.Second)
	second := e.recording(user, "ogg-opus", 3000)
	e.advance(time.Second)
	third := e.recording(user, "ogg-opus", 3000)

	e.mustSend(second)
	e.mustSend(third)

	if row := e.delivery(waiting); row.State != StatePending {
		t.Errorf("the one waiting for its text is %s", row.State)
	}
	if e.runOnce() {
		t.Error("the waiting one was taken")
	}
}

func TestADeliveryIsNotTakenBeforeItsPauseIsOver(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	if _, err := e.pool.Exec(context.Background(), `UPDATE telegram_deliveries SET run_after = $1`, deliveryStart.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if e.runOnce() {
		t.Fatal("taken before run_after")
	}
	e.advance(time.Minute)
	if e.mustSend(id).State != StateSent {
		t.Error("not sent when its time came")
	}
}

// --- what is sent

func TestAShortTextGoesUnderTheVoiceMessage(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("ru")
	e.link(user, 4242)
	id := e.recording(user, "ogg-opus", 15280)
	e.transcript(id, "done", "  Проверка распознавания записи.\n")

	row := e.mustSend(id)

	calls := e.sender.all()
	if len(calls) != 1 || calls[0].Method != "sendVoice" {
		t.Fatalf("calls = %v", e.sender.methods())
	}
	v := calls[0].Voice
	if v.ChatID != 4242 || v.Caption != "Проверка распознавания записи." || v.Duration != 15280*time.Millisecond || string(v.Audio) != recordingBytes {
		t.Errorf("voice = %+v", v)
	}
	if row.State != StateSent || row.WithText == nil || !*row.WithText || deref64(row.ChatID) != 4242 || deref64(row.VoiceMessageID) != 1000 || row.FinishedAt == nil || row.LockedUntil != nil || row.ErrorCode != nil {
		t.Errorf("row = %+v", row)
	}
}

func TestTheLengthOfACaptionIsCountedInUTF16Units(t *testing.T) {
	for _, tc := range []struct {
		name        string
		text        string
		inCaption   bool
		wantMessage string
	}{
		{"exactly the limit", strings.Repeat("я", MaxCaptionUnits), true, ""},
		{"one over", strings.Repeat("я", MaxCaptionUnits+1), false, strings.Repeat("я", MaxCaptionUnits+1)},
		{"an emoji counts as two", strings.Repeat("я", MaxCaptionUnits-2) + "😀", true, ""},
		{"an emoji that is one too many", strings.Repeat("я", MaxCaptionUnits-1) + "😀", false, strings.Repeat("я", MaxCaptionUnits-1) + "😀"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDeliveryEnv(t, DeliverySettings{})
			user := e.user("en")
			e.link(user, 100)
			id := e.recording(user, "ogg-opus", 60000)
			e.transcript(id, "done", tc.text)

			row := e.mustSend(id)

			calls := e.sender.all()
			if tc.inCaption {
				if len(calls) != 1 || calls[0].Voice.Caption != tc.text {
					t.Fatalf("calls = %v", e.sender.methods())
				}
			} else {
				if len(calls) != 2 || calls[0].Method != "sendVoice" || calls[0].Voice.Caption != "" || calls[1].Method != "sendMessage" {
					t.Fatalf("calls = %v", e.sender.methods())
				}
				if m := calls[1].Message; m.Text != tc.wantMessage || m.ChatID != 100 || m.ReplyTo != 1000 {
					t.Errorf("message = %+v: the text must answer the voice message", m)
				}
			}
			if row.State != StateSent || row.WithText == nil || !*row.WithText {
				t.Errorf("row = %+v", row)
			}
		})
	}
}

func TestATextTooLongForAMessageIsCutShort(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 60000)
	e.transcript(id, "done", strings.Repeat("слово ", 2000))

	e.mustSend(id)

	m := e.sender.all()[1].Message
	if Units(m.Text) > MaxMessageUnits || !strings.HasSuffix(m.Text, "…") {
		t.Errorf("message of %d units, ends %q", Units(m.Text), m.Text[len(m.Text)-10:])
	}
}

func TestARecordingInTheOldFormatGoesAsTextOrNotAtAll(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	withText := e.recording(user, "zepp", 5000)
	e.advance(time.Millisecond) // so that the queue has an order
	silent := e.recording(user, "zepp", 5000)
	e.transcript(withText, "done", "Это старая запись.")
	e.transcript(silent, "done", "")

	rowWithText := e.mustSend(withText)
	e.advance(time.Second)
	rowSilent := e.mustSend(silent)

	calls := e.sender.all()
	if len(calls) != 1 || calls[0].Method != "sendMessage" || calls[0].Message.Text != "Это старая запись." || calls[0].Message.ReplyTo != 0 {
		t.Fatalf("calls = %+v", calls)
	}
	if rowWithText.State != StateSent || rowWithText.WithText == nil || !*rowWithText.WithText || rowWithText.VoiceMessageID != nil {
		t.Errorf("row with text = %+v", rowWithText)
	}
	if rowSilent.State != StateSkipped || derefS(rowSilent.ErrorCode) != CodeNoAudio {
		t.Errorf("silent row = %+v", rowSilent)
	}
}

func TestWhenVoiceMessagesAreForbiddenTheRecordingGoesAsAFile(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	e.transcript(id, "done", "Текст.")
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		if c.Method == "sendVoice" {
			return Sent{}, &Error{Kind: VoiceForbidden, Method: "sendVoice", Code: 400, Description: "Bad Request: VOICE_MESSAGES_FORBIDDEN"}
		}
		return Sent{}, nil
	}

	row := e.mustSend(id)

	calls := e.sender.all()
	if len(calls) != 2 || calls[0].Method != "sendVoice" || calls[1].Method != "sendDocument" {
		t.Fatalf("calls = %v", e.sender.methods())
	}
	if d := calls[1].Document; d.ChatID != 100 || d.Caption != "Текст." || string(d.Data) != recordingBytes || d.FileName != "recording.ogg" {
		t.Errorf("document = %+v", d)
	}
	if row.State != StateSent || row.WithText == nil || !*row.WithText || deref64(row.VoiceMessageID) != 1001 {
		t.Errorf("row = %+v", row)
	}
}

func TestARecordingThatBecameADocumentIsStillSentAndTheOperatorIsTold(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	e.sender.script = func(n int, c sentCall) (Sent, error) { return Sent{MessageID: 77, Kind: "document"}, nil }

	row := e.mustSend(id)

	if row.State != StateSent || deref64(row.VoiceMessageID) != 77 {
		t.Errorf("row = %+v", row)
	}
	if !strings.Contains(e.logs.String(), "did not take the recording as a voice message") || !strings.Contains(e.logs.String(), `"taken_as":"document"`) {
		t.Errorf("the operator was not told:\n%s", e.logs.String())
	}
}

func TestMessagesToOneChatKeepTheGapAndOtherChatsAreNotHeldBack(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{MinGap: 400 * time.Millisecond})
	a, b := e.user("en"), e.user("en")
	e.link(a, 100)
	e.link(b, 200)
	first := e.recording(a, "ogg-opus", 3000)
	e.advance(time.Millisecond)
	second := e.recording(a, "ogg-opus", 3000)
	e.advance(time.Second)
	other := e.recording(b, "ogg-opus", 3000)

	e.mustSend(first)
	e.mustSend(second)
	e.mustSend(other)

	calls := e.sender.all()
	if len(calls) != 3 {
		t.Fatalf("calls = %v", e.sender.methods())
	}
	if gap := calls[1].At.Sub(calls[0].At); gap < 400*time.Millisecond {
		t.Errorf("the second message to a chat came %s after the first", gap)
	}
	if gap := calls[2].At.Sub(calls[1].At); gap > 300*time.Millisecond {
		t.Errorf("a message to another chat waited %s", gap)
	}
}

func TestTheLogHasNeitherTheTextNorTheChat(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 987654321)
	id := e.recording(user, "ogg-opus", 3000)
	e.transcript(id, "done", "секретная фраза")

	e.mustSend(id)

	logs := e.logs.String()
	if !strings.Contains(logs, "recording sent to telegram") {
		t.Fatalf("nothing was logged:\n%s", logs)
	}
	for _, secret := range []string{"секретная", "987654321"} {
		if strings.Contains(logs, secret) {
			t.Errorf("%q is in the log:\n%s", secret, logs)
		}
	}
}

// --- when Telegram fails

func TestAnOutageDoesNotUseUpAttemptsAndTheWaitGrows(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{OutageMin: 15 * time.Second, OutageMax: 40 * time.Second})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	down := true
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		if down {
			return Sent{}, &Error{Kind: Unavailable, Method: "sendVoice", Err: errors.New("no route to host")}
		}
		return Sent{}, nil
	}

	for i, pause := range []time.Duration{15 * time.Second, 30 * time.Second, 40 * time.Second, 40 * time.Second} {
		row := e.mustSend(id)
		if row.State != StatePending || row.Attempts != 0 || !row.RunAfter.Equal(e.now().Add(pause)) || row.LockedUntil != nil || !strings.Contains(derefS(row.LastError), "no route to host") {
			t.Fatalf("failure %d: %+v (want a pause of %s)", i+1, row, pause)
		}
		e.advance(pause)
	}
	if !e.d.outage.blocked(e.now().Add(-time.Second)) {
		t.Error("the worker does not know that there is an outage")
	}

	down = false
	row := e.mustSend(id)
	if row.State != StateSent || e.d.outage.pause != 0 {
		t.Errorf("row = %+v, outage pause %s", row, e.d.outage.pause)
	}
	if !strings.Contains(e.logs.String(), "telegram is reachable again") {
		t.Error("the end of the outage was not logged")
	}
}

func TestARefusedTokenIsLoudInTheLogAndWaitsLikeAnOutage(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		return Sent{}, &Error{Kind: Unavailable, Method: "sendVoice", Code: 401, Description: "Unauthorized"}
	}

	row := e.mustSend(id)

	if row.State != StatePending || row.Attempts != 0 {
		t.Errorf("row = %+v", row)
	}
	if logs := e.logs.String(); !strings.Contains(logs, `"level":"ERROR"`) || !strings.Contains(logs, "TELEGRAM_BOT_TOKEN") {
		t.Errorf("a refused token is not reported as an error:\n%s", logs)
	}
}

func TestTheRateLimitPausesTheWholeWorkerAndDoesNotUseUpAnAttempt(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	a, b := e.user("en"), e.user("en")
	e.link(a, 100)
	e.link(b, 200)
	first := e.recording(a, "ogg-opus", 3000)
	e.advance(time.Millisecond)
	e.recording(b, "ogg-opus", 3000)
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		return Sent{}, &Error{Kind: RateLimited, Method: "sendVoice", Code: 429, RetryAfter: 7 * time.Second}
	}

	row := e.mustSend(first)

	if row.State != StatePending || row.Attempts != 0 || !row.RunAfter.Equal(e.now().Add(7*time.Second)) {
		t.Errorf("row = %+v", row)
	}
	if !e.d.pausedUntil.Equal(e.now().Add(7 * time.Second)) {
		t.Errorf("the worker is paused until %s", e.d.pausedUntil)
	}
}

func TestARateLimitWithoutASaneWaitStillWaitsASecond(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		return Sent{}, &Error{Kind: RateLimited, Method: "sendVoice", Code: 429}
	}
	if row := e.mustSend(id); !row.RunAfter.Equal(e.now().Add(time.Second)) {
		t.Errorf("run_after = %s", row.RunAfter)
	}
}

func TestAMessageTelegramRefusesIsGivenUpOn(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		return Sent{}, &Error{Kind: Rejected, Method: "sendVoice", Code: 400, Description: "Bad Request: wrong file"}
	}

	row := e.mustSend(id)

	if row.State != StateFailed || derefS(row.ErrorCode) != CodeRejected || !strings.Contains(derefS(row.LastError), "wrong file") || row.FinishedAt == nil || row.LockedUntil != nil {
		t.Errorf("row = %+v", row)
	}
	e.advance(time.Hour)
	if e.runOnce() {
		t.Error("a failed delivery was taken again")
	}
}

func TestAFailureWithAnUnknownOutcomeCountsAndIsTriedAgain(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{MaxAttempts: 3, Backoff: []time.Duration{30 * time.Second, 2 * time.Minute}})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		return Sent{}, &Error{Kind: Failed, Method: "sendVoice", Err: errors.New("timeout")}
	}

	first := e.mustSend(id)
	if first.State != StatePending || first.Attempts != 1 || !first.RunAfter.Equal(e.now().Add(30*time.Second)) {
		t.Fatalf("first = %+v", first)
	}
	if e.runOnce() {
		t.Fatal("taken before its pause was over")
	}
	e.advance(30 * time.Second)
	second := e.mustSend(id)
	if second.State != StatePending || second.Attempts != 2 || !second.RunAfter.Equal(e.now().Add(2*time.Minute)) {
		t.Fatalf("second = %+v", second)
	}
	e.advance(2 * time.Minute)
	third := e.mustSend(id)
	if third.State != StateFailed || derefS(third.ErrorCode) != CodeTooManyFailures || third.Attempts != 3 {
		t.Errorf("third = %+v", third)
	}
}

func TestAnErrorThatIsNotTelegramsIsReadAsAFailureOfUnknownOutcome(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	e.sender.script = func(n int, c sentCall) (Sent, error) { return Sent{}, errors.New("something odd") }

	if row := e.mustSend(id); row.State != StatePending || row.Attempts != 1 {
		t.Errorf("row = %+v", row)
	}
}

func TestStoppingTheServerPutsTheDeliveryBackAsIfItWasNeverTaken(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	ctx, cancel := context.WithCancel(context.Background())
	sender := &blockingSender{started: make(chan struct{})}
	e.d.api = sender
	done := make(chan error, 1)
	go func() {
		_, err := e.d.RunOnce(ctx)
		done <- err
	}()
	<-sender.started
	cancel()

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	row := e.delivery(id)
	if row.State != StatePending || row.Attempts != 0 || !row.RunAfter.Equal(e.now()) || row.LockedUntil != nil {
		t.Errorf("row = %+v", row)
	}
}

type blockingSender struct{ started chan struct{} }

func (b *blockingSender) SendVoice(ctx context.Context, p VoiceParams) (Sent, error) {
	close(b.started)
	<-ctx.Done()
	return Sent{}, ctx.Err()
}

func (b *blockingSender) SendDocument(ctx context.Context, p DocumentParams) (Sent, error) {
	return Sent{}, ctx.Err()
}

func (b *blockingSender) SendMessage(ctx context.Context, p MessageParams) (Sent, error) {
	return Sent{}, ctx.Err()
}

func TestALeaseThatRunsOutLetsAnotherWorkerTakeTheDeliveryAndTheFirstCannotFinishIt(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{Lease: time.Minute})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	ctx := context.Background()
	claimed, err := e.q.ClaimTelegramDelivery(ctx, dbgen.ClaimTelegramDeliveryParams{Now: e.now(), LeaseUntil: e.now().Add(time.Minute)})
	if err != nil || claimed.Attempts != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if e.runOnce() {
		t.Fatal("a delivery that is being worked on was taken")
	}

	e.advance(time.Minute + time.Second) // the first worker died
	row := e.mustSend(id)

	if row.State != StateSent || row.Attempts != 2 {
		t.Fatalf("row = %+v", row)
	}
	// The first worker comes back to life and tries to finish what it started.
	for name, call := range map[string]func() (int64, error){
		"complete": func() (int64, error) {
			return e.q.CompleteTelegramDelivery(ctx, dbgen.CompleteTelegramDeliveryParams{MessageID: id, Attempt: 1, Now: e.now()})
		},
		"fail": func() (int64, error) {
			return e.q.FailTelegramDelivery(ctx, dbgen.FailTelegramDeliveryParams{MessageID: id, Attempt: 1, ErrorCode: "x", Now: e.now()})
		},
		"skip": func() (int64, error) {
			return e.q.SkipTelegramDelivery(ctx, dbgen.SkipTelegramDeliveryParams{MessageID: id, Attempt: 1, ErrorCode: "x", Now: e.now()})
		},
		"release": func() (int64, error) {
			return e.q.ReleaseTelegramDelivery(ctx, dbgen.ReleaseTelegramDeliveryParams{MessageID: id, Attempt: 1, RunAfter: e.now()})
		},
		"voice": func() (int64, error) {
			return e.q.RecordTelegramVoice(ctx, dbgen.RecordTelegramVoiceParams{MessageID: id, Attempt: 1, ChatID: 5, VoiceMessageID: 5})
		},
	} {
		if n, err := call(); err != nil || n != 0 {
			t.Errorf("%s by a worker that no longer has the delivery changed %d rows (%v)", name, n, err)
		}
	}
	if after := e.delivery(id); after.State != StateSent {
		t.Errorf("the stale worker changed the delivery: %+v", after)
	}
}

func TestADeliveryThatKeepsKillingItsWorkerIsGivenUpOn(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{MaxAttempts: 2, Lease: time.Minute})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	ctx := context.Background()
	for range 2 { // two workers take it and die without a word
		if _, err := e.q.ClaimTelegramDelivery(ctx, dbgen.ClaimTelegramDeliveryParams{Now: e.now(), LeaseUntil: e.now().Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		e.advance(2 * time.Minute)
	}

	row := e.mustSend(id)

	if row.State != StateFailed || derefS(row.ErrorCode) != CodeTooManyFailures || len(e.sender.all()) != 0 {
		t.Errorf("row = %+v, sent %v", row, e.sender.methods())
	}
}

func TestTheVoiceMessageIsNotSentAgainWhenOnlyTheTextFailed(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{Backoff: []time.Duration{time.Minute}})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 60000)
	long := strings.Repeat("слово ", 300) // too long for a caption
	e.transcript(id, "done", long)
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		if c.Method == "sendMessage" && n == 1 {
			return Sent{}, &Error{Kind: Failed, Method: "sendMessage", Err: errors.New("timeout")}
		}
		return Sent{}, nil
	}

	first := e.mustSend(id)

	if first.State != StatePending || first.Attempts != 1 || deref64(first.VoiceMessageID) != 1000 || deref64(first.ChatID) != 100 {
		t.Fatalf("after the failure: %+v", first)
	}
	// Meanwhile the text is made short (it was made again): it must still reach the chat, and the voice must not be sent again.
	e.setTranscriptState(id, "done", "Теперь коротко.")
	e.advance(time.Minute)
	second := e.mustSend(id)

	calls := e.sender.all()
	if got := strings.Join(e.sender.methods(), ","); got != "sendVoice,sendMessage,sendMessage" {
		t.Fatalf("calls = %s", got)
	}
	if m := calls[2].Message; m.Text != "Теперь коротко." || m.ReplyTo != 1000 || m.ChatID != 100 {
		t.Errorf("the text that followed = %+v", m)
	}
	if second.State != StateSent || second.WithText == nil || !*second.WithText {
		t.Errorf("second = %+v", second)
	}
}

func TestWhenTheDeliveryIsLostWhileTheVoiceIsBeingSentNothingFollowsIt(t *testing.T) {
	// The lease ran out and another worker took the delivery: it is that worker's to finish, not this one's.
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 60000)
	e.transcript(id, "done", strings.Repeat("слово ", 300))
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		if _, err := e.pool.Exec(context.Background(), `UPDATE telegram_deliveries SET attempts = attempts + 1`); err != nil {
			t.Error(err)
		}
		return Sent{}, nil
	}

	row := e.mustSend(id)

	if got := strings.Join(e.sender.methods(), ","); got != "sendVoice" {
		t.Errorf("calls = %s: nothing may follow a voice message that this worker no longer owns", got)
	}
	if row.State != StateRunning || row.Attempts != 2 || row.VoiceMessageID != nil {
		t.Errorf("the other worker's delivery was touched: %+v", row)
	}
	if !strings.Contains(e.logs.String(), "no longer ours") {
		t.Error("the operator was not told")
	}
}

func TestTheRestOfADeliveryIsNotSentToAChatThePersonHasLeft(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{Backoff: []time.Duration{time.Minute}})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 60000)
	e.transcript(id, "done", strings.Repeat("слово ", 300))
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		if c.Method == "sendMessage" {
			return Sent{}, &Error{Kind: Failed, Method: "sendMessage", Err: errors.New("timeout")}
		}
		return Sent{}, nil
	}
	e.mustSend(id)
	e.link(user, 200) // linked to another chat before the retry
	e.advance(time.Minute)

	row := e.mustSend(id)

	if row.State != StateSkipped || derefS(row.ErrorCode) != CodeUnlinked {
		t.Errorf("row = %+v", row)
	}
	for _, c := range e.sender.all() {
		if c.chat() == 200 {
			t.Error("something went to the new chat")
		}
	}
}

func TestAMissingFileIsAProblemOfItsOwn(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	if err := e.files.Remove(storage.RecordingName(id)); err != nil {
		t.Fatal(err)
	}

	row := e.mustSend(id)

	if row.State != StateFailed || derefS(row.ErrorCode) != CodeFileMissing || len(e.sender.all()) != 0 {
		t.Errorf("row = %+v", row)
	}
}

func TestAnUnreadableTextRecordingDoesNotNeedItsFile(t *testing.T) {
	// A recording in the old format with text goes as a message; its file is not needed for that.
	e := newDeliveryEnv(t, DeliverySettings{})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "zepp", 3000)
	e.transcript(id, "done", "Текст")
	if err := e.files.Remove(storage.RecordingName(id)); err != nil {
		t.Fatal(err)
	}

	if row := e.mustSend(id); row.State != StateSent {
		t.Errorf("row = %+v", row)
	}
}

// --- the loop

func TestRunSendsWhatBecomesReadyAndStopsWithItsContext(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{Poll: 10 * time.Millisecond})
	user := e.user("en")
	e.link(user, 100)
	waiting := e.recording(user, "ogg-opus", 3000)
	e.transcript(waiting, "pending", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.d.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	}()

	time.Sleep(50 * time.Millisecond)
	if len(e.sender.all()) != 0 {
		t.Fatal("sent before the text was ready")
	}
	e.setTranscriptState(waiting, "done", "Готово.")
	waitUntil(t, "the recording to be sent", func() bool { return len(e.sender.all()) == 1 })

	later := e.recording(user, "ogg-opus", 3000)
	e.d.Wake()
	waitUntil(t, "the next recording to be sent", func() bool { return e.delivery(later).State == StateSent })
	if got := e.sender.all()[0].Voice.Caption; got != "Готово." {
		t.Errorf("caption = %q", got)
	}
}

func TestRunDoesNotTakeAnythingWhileTelegramIsDownOrAskedToWait(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{Poll: 5 * time.Millisecond})
	user := e.user("en")
	e.link(user, 100)
	id := e.recording(user, "ogg-opus", 3000)
	e.d.pausedUntil = e.now().Add(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.d.Run(ctx) }()
	defer func() { cancel(); <-done }()

	time.Sleep(60 * time.Millisecond)
	if e.delivery(id).State != StatePending || len(e.sender.all()) != 0 {
		t.Fatal("something was taken during the pause")
	}
	e.advance(time.Minute + time.Second)
	waitUntil(t, "the delivery after the pause", func() bool { return e.delivery(id).State == StateSent })
}

func TestRunLeavesTheQueueAloneWhileTelegramIsDown(t *testing.T) {
	e := newDeliveryEnv(t, DeliverySettings{Poll: 5 * time.Millisecond, OutageMin: time.Minute})
	user := e.user("en")
	e.link(user, 100)
	first := e.recording(user, "ogg-opus", 3000)
	e.advance(time.Millisecond)
	e.recording(user, "ogg-opus", 3000)
	e.sender.script = func(n int, c sentCall) (Sent, error) {
		return Sent{}, &Error{Kind: Unavailable, Method: "sendVoice", Err: errors.New("no route to host")}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.d.Run(ctx) }()
	defer func() { cancel(); <-done }()

	waitUntil(t, "the first failed try", func() bool { return len(e.sender.all()) == 1 })
	time.Sleep(100 * time.Millisecond) // twenty polls
	if n := len(e.sender.all()); n != 1 {
		t.Fatalf("Telegram was tried %d times during its outage: the queue must wait the pause out, not try every recording", n)
	}
	if row := e.delivery(first); row.Attempts != 0 {
		t.Errorf("attempts = %d", row.Attempts)
	}

	e.advance(time.Minute + time.Second)
	waitUntil(t, "the next try after the pause", func() bool { return len(e.sender.all()) >= 2 })
}

func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSplitText(t *testing.T) {
	long := strings.Repeat("a", MaxCaptionUnits+1)
	for _, tc := range []struct{ name, text, caption, message string }{
		{"empty", "", "", ""},
		{"short", "hi", "hi", ""},
		{"at the limit", strings.Repeat("a", MaxCaptionUnits), strings.Repeat("a", MaxCaptionUnits), ""},
		{"over the limit", long, "", long},
		{"too long for a message", strings.Repeat("a", MaxMessageUnits+5), "", Fit(strings.Repeat("a", MaxMessageUnits+5), MaxMessageUnits)},
	} {
		caption, message := splitText(tc.text)
		if caption != tc.caption || message != tc.message {
			t.Errorf("%s: %d-unit caption %d-unit message, want %d and %d", tc.name, Units(caption), Units(message), Units(tc.caption), Units(tc.message))
		}
	}
}

func TestPauseAfterFailure(t *testing.T) {
	backoff := []time.Duration{time.Second, time.Minute}
	for attempt, want := range map[int]time.Duration{0: time.Second, 1: time.Second, 2: time.Minute, 3: time.Minute, 99: time.Minute} {
		if got := pauseAfterFailure(backoff, attempt); got != want {
			t.Errorf("attempt %d: %s, want %s", attempt, got, want)
		}
	}
	if pauseAfterFailure(nil, 1) != 0 {
		t.Error("no backoff, no pause")
	}
}

func TestShorten(t *testing.T) {
	if got := shorten("short"); got != "short" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("я", 600)
	got := shorten(long)
	if r := []rune(got); len(r) != 500 || r[499] != '…' {
		t.Errorf("%d runes, ends %q", len(r), string(r[len(r)-1]))
	}
}
