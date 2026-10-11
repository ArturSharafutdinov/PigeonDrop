package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/testdb"
)

var botNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

const botPage = "https://pigeon.example.com/pair"

// fakeTelegram is a Telegram that does what the test says and remembers what the bot did.
type fakeTelegram struct {
	mu          sync.Mutex
	me          Me
	meErrors    []error // answers to the first calls of GetMe, before it works
	meCalls     int
	batches     [][]Update // what the next calls of GetUpdates return, one batch each; then they wait
	pollErrors  []error    // returned before any batch
	offsets     []int64
	sent        []MessageParams
	commands    map[string][]Command // by language code
	sendError   error
	updatesDone chan struct{}
}

func newFakeTelegram() *fakeTelegram {
	return &fakeTelegram{me: Me{ID: 1, Username: "pigeondrop_bot"}, commands: map[string][]Command{}, updatesDone: make(chan struct{}, 100)}
}

func (f *fakeTelegram) GetMe(ctx context.Context) (Me, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meCalls++
	if len(f.meErrors) > 0 {
		err := f.meErrors[0]
		f.meErrors = f.meErrors[1:]
		return Me{}, err
	}
	return f.me, nil
}

func (f *fakeTelegram) GetUpdates(ctx context.Context, offset int64, wait time.Duration) ([]Update, error) {
	f.mu.Lock()
	f.offsets = append(f.offsets, offset)
	if len(f.pollErrors) > 0 {
		err := f.pollErrors[0]
		f.pollErrors = f.pollErrors[1:]
		f.mu.Unlock()
		f.updatesDone <- struct{}{}
		return nil, err
	}
	if len(f.batches) > 0 {
		batch := f.batches[0]
		f.batches = f.batches[1:]
		f.mu.Unlock()
		f.updatesDone <- struct{}{}
		return batch, nil
	}
	f.mu.Unlock()
	f.updatesDone <- struct{}{}
	<-ctx.Done() // a long poll with nothing to say
	return nil, ctx.Err()
}

func (f *fakeTelegram) SendMessage(ctx context.Context, p MessageParams) (Sent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendError != nil {
		return Sent{}, f.sendError
	}
	f.sent = append(f.sent, p)
	return Sent{MessageID: int64(len(f.sent)), Kind: "text"}, nil
}

func (f *fakeTelegram) SetMyCommands(ctx context.Context, commands []Command, languageCode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands[languageCode] = commands
	return nil
}

func (f *fakeTelegram) replies() []MessageParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]MessageParams(nil), f.sent...)
}

func (f *fakeTelegram) lastReply(t *testing.T) MessageParams {
	t.Helper()
	r := f.replies()
	if len(r) == 0 {
		t.Fatal("the bot said nothing")
	}
	return r[len(r)-1]
}

type botEnv struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *dbgen.Queries
	api  *fakeTelegram
	bot  *Bot
}

func newBotEnv(t *testing.T) *botEnv {
	t.Helper()
	pool := testdb.New(t)
	q := dbgen.New(pool)
	api := newFakeTelegram()
	bot := NewBot(api, q, db.InTx(pool), slog.New(slog.NewTextHandler(io.Discard, nil)), BotSettings{
		PageURL: botPage, Poll: time.Second, StartRetry: time.Millisecond, StartRetryMax: 4 * time.Millisecond,
		ErrorPause: time.Millisecond, ErrorPauseMax: 4 * time.Millisecond, ConflictPause: time.Millisecond,
	})
	bot.Now = func() time.Time { return botNow }
	bot.username = "pigeondrop_bot"
	return &botEnv{t: t, pool: pool, q: q, api: api, bot: bot}
}

func (e *botEnv) user(email, language string) uuid.UUID {
	e.t.Helper()
	u, err := e.q.CreateUser(context.Background(), dbgen.CreateUserParams{Email: email, PasswordHash: "x", Language: language, CreatedAt: botNow})
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

// code stores a fresh code for the user that lives for the given time from botNow, and returns it.
func (e *botEnv) code(user uuid.UUID, lives time.Duration) string {
	e.t.Helper()
	code, hash, err := NewLinkCode()
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.q.CreateTelegramLinkCode(context.Background(), dbgen.CreateTelegramLinkCodeParams{CodeHash: hash, UserID: user, Now: botNow, ExpiresAt: botNow.Add(lives)}); err != nil {
		e.t.Fatal(err)
	}
	return code
}

func privateMessage(chat int64, text string) Message {
	return Message{MessageID: 1, From: &User{ID: chat, FirstName: "Ann", Username: "ann", LanguageCode: "en"}, Chat: Chat{ID: chat, Type: "private"}, Text: text}
}

func (e *botEnv) say(m Message) { e.bot.handle(context.Background(), m) }

func (e *botEnv) link(user uuid.UUID) (dbgen.TelegramLink, bool) {
	e.t.Helper()
	row, err := e.q.GetTelegramLink(context.Background(), user)
	if err != nil {
		return dbgen.TelegramLink{}, false
	}
	return row, true
}

func (e *botEnv) codeState(code string) dbgen.GetTelegramLinkCodeRow {
	e.t.Helper()
	row, err := e.q.GetTelegramLinkCode(context.Background(), HashLinkCode(code))
	if err != nil {
		e.t.Fatal(err)
	}
	return row
}

func TestStartWithACodeLinksTheChat(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "ru")
	code := e.code(user, 10*time.Minute)

	e.say(privateMessage(100, "/start "+code))

	link, ok := e.link(user)
	if !ok {
		t.Fatal("the chat was not linked")
	}
	if link.ChatID != 100 || link.Username == nil || *link.Username != "ann" || link.FirstName == nil || *link.FirstName != "Ann" || !link.LinkedAt.Equal(botNow) || link.BlockedAt != nil {
		t.Errorf("link = %+v", link)
	}
	if state := e.codeState(code); state.UsedAt == nil || state.UsedChatID == nil || *state.UsedChatID != 100 {
		t.Errorf("the code was not used up: %+v", state)
	}
	reply := e.api.lastReply(t)
	// The person's Telegram speaks English, the account Russian: once the account is known, it decides.
	if reply.ChatID != 100 || !strings.Contains(reply.Text, "Готово") || !strings.Contains(reply.Text, "/stop") {
		t.Errorf("reply = %+v", reply)
	}
}

func TestTheAnswerFollowsTheAccountsLanguage(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)
	m := privateMessage(100, "/start "+code)
	m.From.LanguageCode = "ru"

	e.say(m)

	if reply := e.api.lastReply(t); !strings.Contains(reply.Text, "Done") {
		t.Errorf("reply = %q", reply.Text)
	}
}

func TestALinkWithoutAUsernameOrNameStillWorks(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)
	m := privateMessage(100, "/start "+code)
	m.From.Username, m.From.FirstName = "", ""

	e.say(m)

	link, ok := e.link(user)
	if !ok || link.Username != nil || link.FirstName != nil {
		t.Errorf("link = %+v, %v", link, ok)
	}
}

func TestTheSameStartAgainIsAnsweredTheSame(t *testing.T) {
	// The bot was restarted while it handled a press of Start, so Telegram hands the same one over again.
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)

	e.say(privateMessage(100, "/start "+code))
	first := e.codeState(code)
	e.bot.Now = func() time.Time { return botNow.Add(time.Minute) }
	e.say(privateMessage(100, "/start "+code))

	replies := e.api.replies()
	if len(replies) != 2 || replies[0].Text != replies[1].Text || !strings.Contains(replies[1].Text, "Done") {
		t.Fatalf("replies = %+v", replies)
	}
	if again := e.codeState(code); !again.UsedAt.Equal(*first.UsedAt) {
		t.Error("the second Start changed when the code was used")
	}
	if link, _ := e.link(user); !link.LinkedAt.Equal(botNow) {
		t.Error("the second Start changed the link")
	}
}

func TestAUsedCodeIsRefusedToAnotherChat(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)
	e.say(privateMessage(100, "/start "+code))

	e.say(privateMessage(200, "/start "+code))

	if reply := e.api.lastReply(t); reply.ChatID != 200 || !strings.Contains(reply.Text, "not valid") {
		t.Errorf("reply = %+v", reply)
	}
	if link, _ := e.link(user); link.ChatID != 100 {
		t.Errorf("the chat is %d, want the first one", link.ChatID)
	}
}

func TestAnExpiredCodeIsRefused(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)
	e.bot.Now = func() time.Time { return botNow.Add(10*time.Minute + time.Second) }

	e.say(privateMessage(100, "/start "+code))

	if _, ok := e.link(user); ok {
		t.Error("an expired code linked a chat")
	}
	if reply := e.api.lastReply(t); !strings.Contains(reply.Text, "not valid or has expired") || !strings.Contains(reply.Text, botPage) {
		t.Errorf("reply = %q", reply.Text)
	}
	if e.codeState(code).UsedAt != nil {
		t.Error("an expired code was marked as used")
	}
}

func TestACodeIsGoodUntilTheMomentItExpires(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)
	e.bot.Now = func() time.Time { return botNow.Add(10*time.Minute - time.Second) }

	e.say(privateMessage(100, "/start "+code))

	if _, ok := e.link(user); !ok {
		t.Error("a code was refused before it expired")
	}
}

func TestUnknownAndMalformedCodesAreRefused(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	unknown, _, _ := NewLinkCode()
	for _, text := range []string{"/start " + unknown, "/start short", "/start !!!!!!!!!!!!!!!!!!!!!!", "/start " + strings.Repeat("A", 64), "/start a b"} {
		e.say(privateMessage(100, text))
		if reply := e.api.lastReply(t); !strings.Contains(reply.Text, "not valid or has expired") {
			t.Errorf("%q: reply = %q", text, reply.Text)
		}
	}
	if _, ok := e.link(user); ok {
		t.Error("a chat was linked")
	}
}

func TestStartWithoutACodeExplains(t *testing.T) {
	e := newBotEnv(t)

	e.say(privateMessage(100, "/start"))
	e.say(privateMessage(100, "/start   "))

	for _, reply := range e.api.replies() {
		if !strings.Contains(reply.Text, botPage) || !strings.Contains(reply.Text, "To get your watch recordings") || strings.Contains(reply.Text, "not valid") {
			t.Errorf("reply = %q", reply.Text)
		}
	}
	if len(e.api.replies()) != 2 {
		t.Errorf("%d replies", len(e.api.replies()))
	}
}

func TestAChatLeavesTheAccountItWasLinkedToWhenAnotherLinksIt(t *testing.T) {
	e := newBotEnv(t)
	alice := e.user("alice@example.com", "en")
	bob := e.user("bob@example.com", "en")
	e.say(privateMessage(100, "/start "+e.code(alice, 10*time.Minute)))

	e.say(privateMessage(100, "/start "+e.code(bob, 10*time.Minute)))

	if _, ok := e.link(alice); ok {
		t.Error("the chat is still linked to the first account")
	}
	if link, ok := e.link(bob); !ok || link.ChatID != 100 {
		t.Errorf("the second account is not linked: %+v %v", link, ok)
	}
}

func TestLinkingAgainReplacesTheChatAndClearsTheBlock(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	e.say(privateMessage(100, "/start "+e.code(user, 10*time.Minute)))
	if _, err := e.pool.Exec(context.Background(), `UPDATE telegram_links SET blocked_at = $1`, botNow); err != nil {
		t.Fatal(err)
	}

	e.say(privateMessage(200, "/start "+e.code(user, 10*time.Minute)))

	link, _ := e.link(user)
	if link.ChatID != 200 || link.BlockedAt != nil {
		t.Errorf("link = %+v", link)
	}
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM telegram_links`).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d links (%v), want 1", n, err)
	}
}

func TestStopUnlinksTheChat(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "ru")
	e.say(privateMessage(100, "/start "+e.code(user, 10*time.Minute)))

	e.say(privateMessage(100, "/stop"))

	if _, ok := e.link(user); ok {
		t.Error("the chat is still linked")
	}
	if reply := e.api.lastReply(t); !strings.Contains(reply.Text, "Отключено") || !strings.Contains(reply.Text, botPage) {
		t.Errorf("reply = %q", reply.Text)
	}
}

func TestStopInAChatThatIsNotLinked(t *testing.T) {
	e := newBotEnv(t)
	other := e.user("other@example.com", "en")
	e.say(privateMessage(300, "/start "+e.code(other, 10*time.Minute)))

	e.say(privateMessage(100, "/stop"))

	if reply := e.api.lastReply(t); reply.ChatID != 100 || !strings.Contains(reply.Text, "not linked") {
		t.Errorf("reply = %+v", reply)
	}
	if _, ok := e.link(other); !ok {
		t.Error("someone else's link was removed")
	}
}

func TestGroupsAreRefused(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)

	for _, kind := range []string{"group", "supergroup", "channel"} {
		m := privateMessage(-500, "/start "+code)
		m.Chat.Type = kind
		e.say(m)
		if reply := e.api.lastReply(t); reply.ChatID != -500 || !strings.Contains(reply.Text, "private messages") {
			t.Errorf("%s: reply = %+v", kind, reply)
		}
	}
	if _, ok := e.link(user); ok {
		t.Error("a group was linked")
	}
	if e.codeState(code).UsedAt != nil {
		t.Error("a code was spent in a group")
	}

	before := len(e.api.replies())
	m := privateMessage(-500, "hello everyone")
	m.Chat.Type = "group"
	e.say(m)
	if len(e.api.replies()) != before {
		t.Error("the bot answered ordinary talk in a group")
	}
}

func TestBotsAndStrangersAreIgnored(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)

	fromBot := privateMessage(100, "/start "+code)
	fromBot.From.IsBot = true
	noSender := privateMessage(100, "/start "+code)
	noSender.From = nil
	forAnotherBot := privateMessage(100, "/start@some_other_bot "+code)
	sticker := privateMessage(100, "")
	for _, m := range []Message{fromBot, noSender, forAnotherBot, sticker} {
		e.say(m)
	}
	if n := len(e.api.replies()); n != 0 {
		t.Errorf("the bot said %d things: %+v", n, e.api.replies())
	}
	if _, ok := e.link(user); ok {
		t.Error("a chat was linked")
	}

	e.say(privateMessage(100, "/Start@Pigeondrop_Bot "+code)) // addressed to this bot, in any case
	if _, ok := e.link(user); !ok {
		t.Error("a command addressed to this bot by name, in capitals, was not obeyed")
	}
}

func TestACodeCanBeUsedOnlyOnceAndNotAfterItExpires(t *testing.T) {
	// The bot looks at the code before it uses it; the use itself must still refuse a second one, since two presses
	// of Start can be handled at the same moment.
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)
	use := func(at time.Time, chat int64) int64 {
		n, err := e.q.UseTelegramLinkCode(context.Background(), dbgen.UseTelegramLinkCodeParams{CodeHash: HashLinkCode(code), Now: at, ChatID: chat})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := use(botNow.Add(10*time.Minute), 1); n != 0 {
		t.Error("a code was used at the moment it expires")
	}
	if n := use(botNow.Add(10*time.Minute-time.Second), 1); n != 1 {
		t.Error("a good code was refused")
	}
	if n := use(botNow.Add(time.Minute), 2); n != 0 {
		t.Error("a code was used twice")
	}
	if state := e.codeState(code); state.UsedChatID == nil || *state.UsedChatID != 1 {
		t.Errorf("the second use changed the chat: %+v", state)
	}
}

func TestOrdinaryTalkGetsHelp(t *testing.T) {
	e := newBotEnv(t)
	for _, text := range []string{"hello", "/help", "/unknown", "/START"} {
		e.say(privateMessage(100, text))
		if reply := e.api.lastReply(t); !strings.Contains(reply.Text, botPage) {
			t.Errorf("%q: reply = %q", text, reply.Text)
		}
	}
}

func TestADatabaseFailureIsAnsweredAndLinksNothing(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)
	e.bot.tx = func(ctx context.Context, fn func(q dbgen.Querier) error) error {
		return errors.New("the database is gone")
	}

	e.say(privateMessage(100, "/start "+code))

	if reply := e.api.lastReply(t); !strings.Contains(reply.Text, "try again") {
		t.Errorf("reply = %q", reply.Text)
	}
	if _, ok := e.link(user); ok {
		t.Error("a chat was linked")
	}
	if e.codeState(code).UsedAt != nil {
		t.Error("the code was used up although nothing was linked")
	}
}

func TestAFailedLinkRollsBackTheWholeThing(t *testing.T) {
	// The code is used and the link is stored together or not at all.
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)
	if _, err := e.pool.Exec(context.Background(), `ALTER TABLE telegram_links ADD CONSTRAINT never CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}

	e.say(privateMessage(100, "/start "+code))

	if e.codeState(code).UsedAt != nil {
		t.Error("the code was used although the link could not be stored")
	}
	if reply := e.api.lastReply(t); !strings.Contains(reply.Text, "try again") {
		t.Errorf("reply = %q", reply.Text)
	}
}

func TestAnAnswerThatCannotBeSentDoesNotBreakTheBot(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)
	e.api.sendError = &Error{Kind: Blocked, Method: "sendMessage", Code: 403}

	e.say(privateMessage(100, "/start "+code))

	if _, ok := e.link(user); !ok {
		t.Error("the link depends on the answer being delivered")
	}
}

// --- Run

func (e *botEnv) run(t *testing.T) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.bot.Run(ctx)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not stop")
		}
	}
}

// polls waits until GetUpdates has been called n times in all.
func (f *fakeTelegram) polls(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-f.updatesDone:
		case <-time.After(5 * time.Second):
			t.Fatal("the bot did not poll")
		}
	}
}

func TestRunLearnsItsNameMenuAndLinksChats(t *testing.T) {
	e := newBotEnv(t)
	e.bot.username = ""
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)
	e.api.batches = [][]Update{
		{{UpdateID: 10, Message: ptr(privateMessage(100, "/start "+code))}, {UpdateID: 11}},
		{{UpdateID: 12, Message: ptr(privateMessage(100, "hello"))}},
	}
	stop := e.run(t)
	defer stop()

	e.api.polls(t, 3) // the third call is the one that finds nothing and waits

	if e.bot.Username() != "pigeondrop_bot" {
		t.Errorf("username = %q", e.bot.Username())
	}
	if _, ok := e.link(user); !ok {
		t.Error("the chat was not linked")
	}
	e.api.mu.Lock()
	defer e.api.mu.Unlock()
	if got := e.api.offsets; len(got) != 3 || got[0] != 0 || got[1] != 12 || got[2] != 13 {
		t.Errorf("offsets = %v, want 0, 12, 13: every update seen is confirmed with the next call", got)
	}
	if len(e.api.commands[""]) != 2 || len(e.api.commands["ru"]) != 2 || e.api.commands["ru"][0].Description == e.api.commands[""][0].Description {
		t.Errorf("menus = %+v", e.api.commands)
	}
}

func ptr[T any](v T) *T { return &v }

func TestRunKeepsTryingToFindOutWhoItIs(t *testing.T) {
	e := newBotEnv(t)
	e.bot.username = ""
	e.api.meErrors = []error{&Error{Kind: Unavailable, Method: "getMe"}, &Error{Kind: Unavailable, Method: "getMe"}, &Error{Kind: Unavailable, Method: "getMe"}}
	stop := e.run(t)
	defer stop()

	e.api.polls(t, 1)

	if e.api.meCalls != 4 || e.bot.Username() != "pigeondrop_bot" {
		t.Errorf("getMe was called %d times, username %q", e.api.meCalls, e.bot.Username())
	}
}

func TestRunStopsWhileItCannotFindOutWhoItIs(t *testing.T) {
	e := newBotEnv(t)
	e.bot.username = ""
	e.api.meErrors = make([]error, 1000)
	for i := range e.api.meErrors {
		e.api.meErrors[i] = &Error{Kind: Unavailable, Method: "getMe"}
	}
	e.bot.settings.StartRetry, e.bot.settings.StartRetryMax = time.Hour, time.Hour
	stop := e.run(t)
	time.Sleep(20 * time.Millisecond)
	stop() // must not wait an hour
	if e.bot.Username() != "" {
		t.Error("the bot has a name it never learned")
	}
}

func TestRunWaitsOutTroubleAndGoesOn(t *testing.T) {
	e := newBotEnv(t)
	user := e.user("ann@example.com", "en")
	code := e.code(user, 10*time.Minute)
	e.api.pollErrors = []error{
		&Error{Kind: Conflict, Method: "getUpdates", Code: 409},
		&Error{Kind: Unavailable, Method: "getUpdates"},
		&Error{Kind: Failed, Method: "getUpdates"},
		&Error{Kind: RateLimited, Method: "getUpdates", Code: 429, RetryAfter: time.Millisecond},
		errors.New("something else entirely"),
	}
	e.api.batches = [][]Update{{{UpdateID: 1, Message: ptr(privateMessage(100, "/start "+code))}}}
	stop := e.run(t)
	defer stop()

	e.api.polls(t, 7) // five failures, the batch, and the wait

	if _, ok := e.link(user); !ok {
		t.Error("the bot did not recover")
	}
	e.api.mu.Lock()
	defer e.api.mu.Unlock()
	for i, offset := range e.api.offsets[:6] {
		if offset != 0 {
			t.Errorf("poll %d asked for offset %d: a failed poll must not move it", i, offset)
		}
	}
}

func TestRunDoesNotLoseTheMessagesAfterOneItCouldNotHandle(t *testing.T) {
	e := newBotEnv(t)
	good := e.user("ann@example.com", "en")
	goodCode := e.code(good, 10*time.Minute)
	e.api.batches = [][]Update{{
		{UpdateID: 1, Message: ptr(privateMessage(100, "/start !!!"))},
		{UpdateID: 2, Message: &Message{Chat: Chat{ID: 5, Type: "private"}, Text: "no sender"}},
		{UpdateID: 3, Message: ptr(privateMessage(101, "/start "+goodCode))},
	}}
	stop := e.run(t)
	defer stop()

	e.api.polls(t, 2)

	if _, ok := e.link(good); !ok {
		t.Error("a message after the odd ones was lost")
	}
}

func TestRunStopsWhenItsContextEnds(t *testing.T) {
	e := newBotEnv(t)
	stop := e.run(t)
	e.api.polls(t, 1)
	stop()
}
