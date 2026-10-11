package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/telegram"
)

// Linking an account to a Telegram chat, as the page does it: the three routes, and the bot that completes the link.

type fakeBot struct {
	mu   sync.Mutex
	name string
}

func (f *fakeBot) Username() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.name
}

func withBot(name string) func(*Server) {
	return func(s *Server) { s.Telegram = &fakeBot{name: name} }
}

func (e *testEnv) telegramStatus(c *http.Cookie) *httptest.ResponseRecorder {
	return e.call(http.MethodGet, "/v1/telegram", "", withCookie(c))
}

func (e *testEnv) telegramLink(c *http.Cookie, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	return e.call(http.MethodPost, "/v1/telegram/link", "", append(mods, withCookie(c))...)
}

func (e *testEnv) telegramUnlink(c *http.Cookie) *httptest.ResponseRecorder {
	return e.call(http.MethodDelete, "/v1/telegram", "", withCookie(c))
}

// userID reads the id of the signed-in user.
func (e *testEnv) userID(t testing.TB, c *http.Cookie) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(bodyOf(t, e.me(c))["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// codeIn pulls the code out of a t.me link the API returned.
func codeIn(t testing.TB, link, bot string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.Host != "t.me" || u.Path != "/"+bot || u.Fragment != "" {
		t.Fatalf("link %q", link)
	}
	query := u.Query()
	code := query.Get("start")
	if len(query) != 1 || !telegram.ValidLinkCode(code) {
		t.Fatalf("link %q has no good code", link)
	}
	return code
}

func TestTelegramRoutesNeedASignedInUser(t *testing.T) {
	e := newEnvWith(t, withBot("pigeondrop_bot"))
	stranger := &http.Cookie{Name: cookieName, Value: "not-a-session"}
	for _, c := range []*http.Cookie{nil, stranger} {
		for _, call := range []struct{ method, path string }{{http.MethodGet, "/v1/telegram"}, {http.MethodPost, "/v1/telegram/link"}, {http.MethodDelete, "/v1/telegram"}} {
			var mods []func(*http.Request)
			if c != nil {
				mods = append(mods, withCookie(c))
			}
			rec := e.call(call.method, call.path, "", mods...)
			if rec.Code != http.StatusUnauthorized || codeOf(t, rec) != "unauthorized" {
				t.Errorf("%s %s with %v: %d %s", call.method, call.path, c, rec.Code, rec.Body.String())
			}
		}
	}
	if n := e.count(t, `SELECT count(*) FROM telegram_link_codes`); n != 0 {
		t.Errorf("%d codes were stored for nobody", n)
	}
}

func TestWithoutABotThereIsNothingToLink(t *testing.T) {
	for name, configure := range map[string]func(*Server){"no bot": nil, "a bot that does not know its name yet": withBot("")} {
		t.Run(name, func(t *testing.T) {
			e := newEnvWith(t, configure)
			c := e.signUp(t, "alice@example.com")

			rec := e.telegramStatus(c)
			body := bodyOf(t, rec)
			if rec.Code != http.StatusOK || body["available"] != false || body["botUsername"] != nil || body["link"] != nil {
				t.Errorf("status: %d %s", rec.Code, rec.Body.String())
			}

			rec = e.telegramLink(c)
			if rec.Code != http.StatusServiceUnavailable || codeOf(t, rec) != "unavailable" {
				t.Errorf("link: %d %s", rec.Code, rec.Body.String())
			}
			if n := e.count(t, `SELECT count(*) FROM telegram_link_codes`); n != 0 {
				t.Errorf("%d codes were stored", n)
			}
			if rec := e.telegramUnlink(c); rec.Code != http.StatusNoContent {
				t.Errorf("unlink: %d", rec.Code)
			}
		})
	}
}

func TestTheStatusOfAnAccountThatIsNotLinked(t *testing.T) {
	e := newEnvWith(t, withBot("pigeondrop_bot"))
	c := e.signUp(t, "alice@example.com")

	rec := e.telegramStatus(c)

	body := bodyOf(t, rec)
	if rec.Code != http.StatusOK || body["available"] != true || body["botUsername"] != "pigeondrop_bot" {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	if _, has := body["link"]; has {
		t.Errorf("a link is shown for an account that has none: %v", body["link"])
	}
}

func TestACodeIsMadeForTheLinkAndOnlyItsHashIsKept(t *testing.T) {
	e := newEnvWith(t, withBot("pigeondrop_bot"))
	c := e.signUp(t, "alice@example.com")
	user := e.userID(t, c)
	before := e.server.now()

	rec := e.telegramLink(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	body := bodyOf(t, rec)
	code := codeIn(t, body["url"].(string), "pigeondrop_bot")
	expires, err := time.Parse(time.RFC3339Nano, body["expiresAt"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if d := expires.Sub(before); d < 10*time.Minute || d > 10*time.Minute+time.Second {
		t.Errorf("the code lives %s, want ten minutes", d)
	}
	row, err := e.db.GetTelegramLinkCode(context.Background(), telegram.HashLinkCode(code))
	if err != nil {
		t.Fatalf("the code's hash is not stored: %v", err)
	}
	if row.UserID != user || row.UsedAt != nil || !row.ExpiresAt.Equal(expires) {
		t.Errorf("row = %+v", row)
	}
	if n := e.count(t, `SELECT count(*) FROM telegram_link_codes WHERE position($1 in encode(code_hash, 'escape')) > 0`, code); n != 0 {
		t.Error("the code itself is stored")
	}
	if strings.Contains(e.logs.String(), code) {
		t.Error("the code is in the log")
	}
}

func TestAskingAgainMakesTheEarlierCodeStopWorking(t *testing.T) {
	e := newEnvWith(t, withBot("pigeondrop_bot"))
	c := e.signUp(t, "alice@example.com")
	other := e.signUp(t, "bob@example.com")
	first := codeIn(t, bodyOf(t, e.telegramLink(c))["url"].(string), "pigeondrop_bot")
	othersCode := codeIn(t, bodyOf(t, e.telegramLink(other))["url"].(string), "pigeondrop_bot")

	second := codeIn(t, bodyOf(t, e.telegramLink(c))["url"].(string), "pigeondrop_bot")

	if first == second {
		t.Fatal("the same code twice")
	}
	if _, err := e.db.GetTelegramLinkCode(context.Background(), telegram.HashLinkCode(first)); err == nil {
		t.Error("the earlier code still works")
	}
	for _, code := range []string{second, othersCode} {
		if _, err := e.db.GetTelegramLinkCode(context.Background(), telegram.HashLinkCode(code)); err != nil {
			t.Errorf("a live code was removed: %v", err)
		}
	}
}

func TestTheStatusShowsTheLinkOfThisAccountOnly(t *testing.T) {
	e := newEnvWith(t, withBot("pigeondrop_bot"))
	alice := e.signUp(t, "alice@example.com")
	bob := e.signUp(t, "bob@example.com")
	linked := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	username, first := "ann", "Ann"
	if err := e.db.UpsertTelegramLink(context.Background(), dbgen.UpsertTelegramLinkParams{
		UserID: e.userID(t, alice), ChatID: 777, Username: &username, FirstName: &first, Now: linked,
	}); err != nil {
		t.Fatal(err)
	}

	got := bodyOf(t, e.telegramStatus(alice))
	link, _ := got["link"].(map[string]any)
	if link == nil || link["username"] != "ann" || link["firstName"] != "Ann" || link["blocked"] != false || link["linkedAt"] != "2026-10-11T09:00:00Z" {
		t.Fatalf("alice sees %v", got)
	}
	if _, leaked := link["chatId"]; leaked || strings.Contains(e.telegramStatus(alice).Body.String(), "777") {
		t.Error("the chat's number is shown")
	}
	if _, has := bodyOf(t, e.telegramStatus(bob))["link"]; has {
		t.Error("bob sees alice's link")
	}

	if _, err := e.pool.Exec(context.Background(), `UPDATE telegram_links SET blocked_at = now()`); err != nil {
		t.Fatal(err)
	}
	if link := bodyOf(t, e.telegramStatus(alice))["link"].(map[string]any); link["blocked"] != true {
		t.Errorf("a blocked link is not shown as blocked: %v", link)
	}
}

func TestAnAccountWithoutNameOrUsernameStillShowsItsLink(t *testing.T) {
	e := newEnvWith(t, withBot("pigeondrop_bot"))
	c := e.signUp(t, "alice@example.com")
	if err := e.db.UpsertTelegramLink(context.Background(), dbgen.UpsertTelegramLinkParams{UserID: e.userID(t, c), ChatID: 1, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	link := bodyOf(t, e.telegramStatus(c))["link"].(map[string]any)
	if link["username"] != nil || link["firstName"] != nil {
		t.Errorf("link = %v", link)
	}
}

func TestUnlinkingEndsTheLinkAndTheCodesNotYetUsed(t *testing.T) {
	e := newEnvWith(t, withBot("pigeondrop_bot"))
	alice := e.signUp(t, "alice@example.com")
	bob := e.signUp(t, "bob@example.com")
	aliceID, bobID := e.userID(t, alice), e.userID(t, bob)
	for chat, u := range map[int64]uuid.UUID{101: aliceID, 102: bobID} {
		if err := e.db.UpsertTelegramLink(context.Background(), dbgen.UpsertTelegramLinkParams{UserID: u, ChatID: chat, Now: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	e.telegramLink(alice)
	e.telegramLink(bob)

	for range 2 { // twice: there being nothing to unlink is not an error
		if rec := e.telegramUnlink(alice); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("%d %q", rec.Code, rec.Body.String())
		}
	}

	if _, has := bodyOf(t, e.telegramStatus(alice))["link"]; has {
		t.Error("alice is still linked")
	}
	if n := e.count(t, `SELECT count(*) FROM telegram_link_codes WHERE user_id = $1`, aliceID); n != 0 {
		t.Errorf("%d unused codes of alice are left", n)
	}
	if _, has := bodyOf(t, e.telegramStatus(bob))["link"]; !has {
		t.Error("bob was unlinked by alice")
	}
	if n := e.count(t, `SELECT count(*) FROM telegram_link_codes WHERE user_id = $1`, bobID); n != 1 {
		t.Errorf("bob has %d codes, want 1", n)
	}
}

func TestLinksAreLimitedPerUserAndSuccessCounts(t *testing.T) {
	e := newEnvWith(t, withBot("pigeondrop_bot"))
	alice := e.signUp(t, "alice@example.com")
	bob := e.signUp(t, "bob@example.com")

	for i := range 10 {
		if rec := e.telegramLink(alice); rec.Code != http.StatusOK {
			t.Fatalf("link %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := e.telegramLink(alice)

	if rec.Code != http.StatusTooManyRequests || codeOf(t, rec) != "too_many_attempts" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("the eleventh: %d %s %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if n := e.count(t, `SELECT count(*) FROM telegram_link_codes`); n != 1 {
		t.Errorf("%d codes, want alice's one", n)
	}
	if rec := e.telegramLink(bob); rec.Code != http.StatusOK {
		t.Errorf("bob was held back by alice's limit: %d", rec.Code)
	}
	if rec := e.telegramStatus(alice); rec.Code != http.StatusOK {
		t.Errorf("reading the status is limited too: %d", rec.Code)
	}
	e.advance(time.Hour + time.Minute)
	if rec := e.telegramLink(alice); rec.Code != http.StatusOK {
		t.Errorf("the limit never lifts: %d", rec.Code)
	}
}

// scriptedTelegram is a Telegram that hands the bot the updates the test gives it, once.
type scriptedTelegram struct {
	mu      sync.Mutex
	updates chan []telegram.Update
	sent    []telegram.MessageParams
}

func (s *scriptedTelegram) GetMe(context.Context) (telegram.Me, error) {
	return telegram.Me{ID: 1, Username: "pigeondrop_bot"}, nil
}

func (s *scriptedTelegram) GetUpdates(ctx context.Context, _ int64, _ time.Duration) ([]telegram.Update, error) {
	select {
	case u := <-s.updates:
		return u, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *scriptedTelegram) SendMessage(_ context.Context, p telegram.MessageParams) (telegram.Sent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, p)
	return telegram.Sent{MessageID: 1, Kind: "text"}, nil
}

func (s *scriptedTelegram) SetMyCommands(context.Context, []telegram.Command, string) error {
	return nil
}

func TestTheLinkFromThePageLinksTheChatThroughTheBot(t *testing.T) {
	// The whole path: the page asks for a link, the person presses Start, the bot links the chat, the page sees it.
	var bot *telegram.Bot
	api := &scriptedTelegram{updates: make(chan []telegram.Update, 1)}
	e := newEnvWith(t, func(s *Server) {
		bot = telegram.NewBot(api, s.DB, telegram.TxFunc(s.Tx), slog.New(slog.NewTextHandler(io.Discard, nil)), telegram.BotSettings{PageURL: defaultServerURL + "/pair"})
		s.Telegram = bot
	})
	bot.Now = e.server.now // the codes are dated by the test clock
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); bot.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "the bot to learn its name", func() bool { return bot.Username() != "" })

	c := e.signUp(t, "alice@example.com")
	code := codeIn(t, bodyOf(t, e.telegramLink(c))["url"].(string), "pigeondrop_bot")
	api.updates <- []telegram.Update{{UpdateID: 1, Message: &telegram.Message{
		From: &telegram.User{ID: 42, FirstName: "Ann", Username: "ann", LanguageCode: "ru"},
		Chat: telegram.Chat{ID: 42, Type: "private"}, Text: "/start " + code,
	}}}

	waitFor(t, "the link to appear on the page", func() bool { _, has := bodyOf(t, e.telegramStatus(c))["link"]; return has })
	link := bodyOf(t, e.telegramStatus(c))["link"].(map[string]any)
	if link["username"] != "ann" || link["blocked"] != false {
		t.Errorf("link = %v", link)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sent) != 1 || api.sent[0].ChatID != 42 {
		t.Errorf("sent = %+v", api.sent)
	}
}

func waitFor(t testing.TB, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- the upload and the queue for Telegram

// telegramDelivery turns the queue for Telegram on for a test and counts the times the worker was woken.
func telegramDelivery(wait time.Duration, woken *atomic.Int32) func(*Server) {
	return func(s *Server) {
		s.TelegramDelivery = &telegram.Intake{Wait: wait}
		s.TelegramWake = func() { woken.Add(1) }
	}
}

type deliveryRow struct {
	state     string
	waitUntil time.Time
	enqueued  time.Time
}

func (e *testEnv) deliveryEntry(t testing.TB, messageID string) (row deliveryRow, found bool) {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT state, wait_until, enqueued_at FROM telegram_deliveries WHERE message_id = $1`, messageID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return deliveryRow{}, false
	}
	if err := rows.Scan(&row.state, &row.waitUntil, &row.enqueued); err != nil {
		t.Fatal(err)
	}
	return row, true
}

func TestAnUploadOfALinkedAccountJoinsTheQueueForTelegramAndWakesTheWorker(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, telegramDelivery(3*time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	if err := e.db.UpsertTelegramLink(context.Background(), dbgen.UpsertTelegramLinkParams{UserID: e.userID(t, owner), ChatID: 42, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}

	id := e.uploaded(t, token, "voice_1.opus", fixture(t, "tone.ogg"))["id"].(string)

	row, found := e.deliveryEntry(t, id)
	if !found || row.state != "pending" {
		t.Fatalf("queue entry: %+v found=%v", row, found)
	}
	if d := row.waitUntil.Sub(row.enqueued); d != 3*time.Minute {
		t.Errorf("the delivery waits %s for the text, want 3 minutes", d)
	}
	if woken.Load() != 1 {
		t.Errorf("the worker was woken %d times, want 1", woken.Load())
	}
}

func TestAnUploadOfAnAccountThatIsNotLinkedIsNotQueuedForTelegram(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, telegramDelivery(3*time.Minute, &woken))
	linked := e.signUp(t, "alice@example.com")
	stranger := e.signUp(t, "bob@example.com")
	linkedToken, _ := e.pairDevice(t, linked, "watch")
	strangerToken, _ := e.pairDevice(t, stranger, "watch")
	if err := e.db.UpsertTelegramLink(context.Background(), dbgen.UpsertTelegramLinkParams{UserID: e.userID(t, linked), ChatID: 42, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}

	e.uploaded(t, linkedToken, "voice_1.opus", fixture(t, "tone.ogg"))
	other := e.uploaded(t, strangerToken, "voice_1.opus", fixture(t, "tone.ogg"))["id"].(string)

	if _, found := e.deliveryEntry(t, other); found {
		t.Error("a recording of an account that is not linked was queued for Telegram")
	}
	if n := e.count(t, `SELECT count(*) FROM telegram_deliveries`); n != 1 {
		t.Errorf("%d queue entries, want only the linked account's", n)
	}
}

func TestAnUploadThatIsRepeatedIsNotQueuedForTelegramAgain(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, telegramDelivery(time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	if err := e.db.UpsertTelegramLink(context.Background(), dbgen.UpsertTelegramLinkParams{UserID: e.userID(t, owner), ChatID: 42, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}

	first := e.uploaded(t, token, "voice_1.opus", fixture(t, "tone.ogg"))
	again := e.uploaded(t, token, "voice_1.opus", fixture(t, "tone.ogg"))

	if again["duplicate"] != true || again["id"] != first["id"] {
		t.Fatalf("second answer: %v", again)
	}
	if n := e.count(t, `SELECT count(*) FROM telegram_deliveries`); n != 1 {
		t.Fatalf("%d queue entries for one recording", n)
	}
	if woken.Load() != 1 {
		t.Errorf("woken %d times: a repeat brought nothing new", woken.Load())
	}
}

func TestWithTheBotOffNothingIsQueuedForTelegramAndNobodyIsWoken(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, func(s *Server) { s.TelegramWake = func() { woken.Add(1) } }) // TelegramDelivery stays nil
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	if err := e.db.UpsertTelegramLink(context.Background(), dbgen.UpsertTelegramLinkParams{UserID: e.userID(t, owner), ChatID: 42, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}

	e.uploaded(t, token, "voice_1.opus", fixture(t, "tone.ogg"))

	if n := e.count(t, `SELECT count(*) FROM telegram_deliveries`); n != 0 {
		t.Fatalf("%d queue entries with the bot off", n)
	}
	if woken.Load() != 0 {
		t.Errorf("woken %d times with the bot off", woken.Load())
	}
}

func TestARecordingIsNeverAcceptedWithoutItsPlaceInTheQueueForTelegram(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, telegramDelivery(time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	if err := e.db.UpsertTelegramLink(context.Background(), dbgen.UpsertTelegramLinkParams{UserID: e.userID(t, owner), ChatID: 42, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(context.Background(), `ALTER TABLE telegram_deliveries ADD CONSTRAINT never CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}

	rec := e.uploadAs(token, "voice_1.opus", fixture(t, "tone.ogg"))

	if rec.Code < 500 {
		t.Fatalf("the upload answered %d although the recording could not be queued", rec.Code)
	}
	if n := e.count(t, `SELECT count(*) FROM messages`); n != 0 {
		t.Errorf("%d recordings were accepted without their place in the queue", n)
	}
	if names := e.storedNames(t); len(names) != 0 {
		t.Errorf("files left behind: %v", names)
	}
	if woken.Load() != 0 {
		t.Error("the worker was woken for nothing")
	}
}

func TestDeletingARecordingTakesItsPlaceInTheQueueForTelegram(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, telegramDelivery(time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	if err := e.db.UpsertTelegramLink(context.Background(), dbgen.UpsertTelegramLinkParams{UserID: e.userID(t, owner), ChatID: 42, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	id := e.uploaded(t, token, "voice_1.opus", fixture(t, "tone.ogg"))["id"].(string)

	if rec := e.call(http.MethodDelete, "/v1/messages/"+id, "", withCookie(owner)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	if _, found := e.deliveryEntry(t, id); found {
		t.Fatal("the delivery outlived its recording")
	}
}
