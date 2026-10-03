package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/auth"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

const goodPassword = "correct horse battery staple"

func asJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// call sends a request through the whole handler. mods adjust it (cookies, address, headers) first.
func (e *testEnv) call(method, path, body string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, mod := range mods {
		mod(req)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func bodyOf(t testing.TB, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("answer %d is not a JSON object: %q", rec.Code, rec.Body.String())
	}
	return out
}

func withCookie(c *http.Cookie) func(*http.Request) { return func(r *http.Request) { r.AddCookie(c) } }
func fromIP(addr string) func(*http.Request) {
	return func(r *http.Request) { r.RemoteAddr = addr + ":4321" }
}
func withHeader(k, v string) func(*http.Request) { return func(r *http.Request) { r.Header.Set(k, v) } }

// sessionCookieOf returns the session cookie a response set, or nil.
func sessionCookieOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == secureCookieName || c.Name == cookieName {
			return c
		}
	}
	return nil
}

// invite stores a new invitation, valid for validFor, and returns its code.
func (e *testEnv) invite(t testing.TB, validFor time.Duration) string {
	t.Helper()
	code, hash, err := auth.NewInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	now := e.server.now()
	if _, err := e.db.CreateInvitation(context.Background(), dbgen.CreateInvitationParams{
		CodeHash: hash, Note: "test", CreatedAt: now, ExpiresAt: now.Add(validFor),
	}); err != nil {
		t.Fatal(err)
	}
	return code
}

func (e *testEnv) register(code, email, password string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	return e.call(http.MethodPost, "/v1/auth/register",
		asJSON(map[string]any{"inviteCode": code, "email": email, "password": password}), mods...)
}

func (e *testEnv) login(email, password string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	return e.call(http.MethodPost, "/v1/auth/login", asJSON(map[string]any{"email": email, "password": password}), mods...)
}

// signUp registers a new user with a fresh invitation and returns their session cookie.
func (e *testEnv) signUp(t testing.TB, email string) *http.Cookie {
	t.Helper()
	rec := e.register(e.invite(t, 24*time.Hour), email, goodPassword)
	if rec.Code != http.StatusCreated {
		t.Fatalf("sign up %s: %d %s", email, rec.Code, rec.Body.String())
	}
	return sessionCookieOf(rec)
}

func (e *testEnv) me(c *http.Cookie, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	if c != nil {
		mods = append(mods, withCookie(c))
	}
	return e.call(http.MethodGet, "/v1/me", "", mods...)
}

func (e *testEnv) count(t testing.TB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func codeOf(t testing.TB, rec *httptest.ResponseRecorder) any {
	t.Helper()
	return bodyOf(t, rec)["code"]
}

// slowHasher makes each password hash take on the order of 20 ms, long enough that requests started together
// have all passed their early checks before the first one commits. Race tests need that to really race.
func slowHasher(s *Server) {
	s.Hasher = auth.NewHasher(auth.Params{MemoryKiB: 32 * 1024, Iterations: 1, Parallelism: 1}, 4)
}

// ---- registration ----------------------------------------------------------------------------

func TestRegisterCreatesTheAccountAndSignsIn(t *testing.T) {
	e := newEnv(t)
	code := e.invite(t, 24*time.Hour)

	rec := e.register(code, "  Alice@Example.COM ", goodPassword)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	user := bodyOf(t, rec)
	if _, err := uuid.Parse(user["id"].(string)); err != nil {
		t.Errorf("id %q is not a UUID", user["id"])
	}
	if user["email"] != "alice@example.com" || user["plan"] != "free" || user["language"] != "en" {
		t.Errorf("user: %v", user)
	}
	if _, has := user["passwordHash"]; has || strings.Contains(rec.Body.String(), "argon2") || strings.Contains(rec.Body.String(), goodPassword) {
		t.Errorf("the answer leaks password material: %s", rec.Body.String())
	}

	c := sessionCookieOf(rec)
	if c == nil {
		t.Fatal("no session cookie was set")
	}
	if c.Name != secureCookieName || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" ||
		c.MaxAge != int((30*24*time.Hour).Seconds()) || c.Domain != "" || len(c.Value) != auth.SessionTokenLength {
		t.Errorf("cookie: %+v", c)
	}

	// The cookie signs in.
	me := e.me(c)
	if me.Code != http.StatusOK || me.Body.String() != rec.Body.String() {
		t.Errorf("GET /v1/me: %d %s (registration said %s)", me.Code, me.Body.String(), rec.Body.String())
	}

	// What is stored: a password hash and a token hash, never the secrets themselves.
	stored, err := e.db.GetUserByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored.PasswordHash, "$argon2id$v=19$") || strings.Contains(stored.PasswordHash, goodPassword) {
		t.Errorf("stored password hash: %s", stored.PasswordHash)
	}
	var tokenHash []byte
	if err := e.pool.QueryRow(context.Background(), `SELECT token_hash FROM sessions`).Scan(&tokenHash); err != nil {
		t.Fatal(err)
	}
	if string(tokenHash) == c.Value || string(tokenHash) != string(auth.HashSessionToken(c.Value)) {
		t.Error("the session token is stored in the clear, or hashed differently from lookups")
	}
	invites, _ := e.db.ListInvitations(context.Background())
	if len(invites) != 1 || invites[0].UsedAt == nil || invites[0].UsedByEmail == nil || *invites[0].UsedByEmail != "alice@example.com" {
		t.Errorf("the invitation was not recorded as used by the new user: %+v", invites)
	}
}

func TestRegisterWithoutSecureCookies(t *testing.T) {
	e := newEnvWith(t, func(s *Server) { s.CookieSecure = false })
	c := e.signUp(t, "dev@example.com")
	// Plain http (local development): no Secure flag and no __Host- prefix, which requires Secure.
	if c.Name != cookieName || c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie: %+v", c)
	}
	if rec := e.me(c); rec.Code != http.StatusOK {
		t.Fatalf("the development cookie does not sign in: %d", rec.Code)
	}
}

func TestRegisterLanguage(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		name, language string
		status         int
		want           string
	}{
		{"russian", "ru", http.StatusCreated, "ru"},
		{"english", "en", http.StatusCreated, "en"},
		{"unknown", "de", http.StatusBadRequest, ""},
		{"empty", "", http.StatusBadRequest, ""},
	} {
		code := e.invite(t, time.Hour)
		rec := e.call(http.MethodPost, "/v1/auth/register", asJSON(map[string]any{
			"inviteCode": code, "email": tc.name + "@example.com", "password": goodPassword, "language": tc.language,
		}))
		if rec.Code != tc.status || (tc.want != "" && bodyOf(t, rec)["language"] != tc.want) {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestInvitationIsSingleUse(t *testing.T) {
	e := newEnv(t)
	code := e.invite(t, time.Hour)

	if rec := e.register(code, "first@example.com", goodPassword); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	rec := e.register(code, "second@example.com", goodPassword)
	if rec.Code != http.StatusForbidden || codeOf(t, rec) != "invalid_invite" {
		t.Fatalf("second use: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT count(*) FROM users`); n != 1 {
		t.Fatalf("%d users exist", n)
	}
}

func TestInvitationCodeIsForgivingAboutTyping(t *testing.T) {
	e := newEnv(t)
	code := e.invite(t, time.Hour)
	typed := " " + strings.ToLower(strings.ReplaceAll(code, "-", "")) + "\n"
	if rec := e.register(typed, "typed@example.com", goodPassword); rec.Code != http.StatusCreated {
		t.Fatalf("%q: %d %s", typed, rec.Code, rec.Body.String())
	}
}

func TestUnusableInvitationsAreAllRefusedTheSameWay(t *testing.T) {
	e := newEnv(t)
	expired := e.invite(t, time.Hour)
	e.advance(2 * time.Hour)
	unknown, _, _ := auth.NewInviteCode()
	used := e.invite(t, time.Hour)
	e.register(used, "owner@example.com", goodPassword)

	var answers []string
	for name, code := range map[string]string{
		"expired": expired, "unknown": unknown, "used": used, "malformed": "hello", "empty": "", "too long": strings.Repeat("A", 500),
	} {
		rec := e.register(code, strings.ReplaceAll(name, " ", "-")+"@example.com", goodPassword)
		if rec.Code != http.StatusForbidden || codeOf(t, rec) != "invalid_invite" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		answers = append(answers, rec.Body.String())
	}
	if len(slices.Compact(slices.Sorted(slices.Values(answers)))) != 1 {
		t.Errorf("the answers differ, which tells a guesser what was wrong: %q", answers)
	}
}

func TestInvalidInputIsRefusedAndDoesNotSpendTheInvitation(t *testing.T) {
	e := newEnv(t)
	code := e.invite(t, time.Hour)

	for name, tc := range map[string]struct {
		email, password, want string
	}{
		"no email":          {"", goodPassword, "invalid_email"},
		"not an email":      {"alice", goodPassword, "invalid_email"},
		"name and brackets": {"Alice <alice@example.com>", goodPassword, "invalid_email"},
		"short password":    {"a@example.com", "short", "weak_password"},
		"empty password":    {"a@example.com", "", "weak_password"},
		"huge password":     {"a@example.com", strings.Repeat("x", 300), "weak_password"},
	} {
		rec := e.register(code, tc.email, tc.password)
		if rec.Code != http.StatusBadRequest || codeOf(t, rec) != tc.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := e.register(code, "alice@example.com", goodPassword); rec.Code != http.StatusCreated {
		t.Fatalf("the invitation was spent by refused requests: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMalformedBodiesGetMachineReadableCodesOnV1Only(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/v1/auth/register", "/v1/auth/login"} {
		for _, body := range []string{"", "{", "[]", `"text"`, `{"email": 5}`} {
			rec := e.call(http.MethodPost, path, body)
			if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "invalid_request" {
				t.Errorf("POST %s %q: %d %s", path, body, rec.Code, rec.Body.String())
			}
		}
	}
	// The older routes answer exactly as the Node prototype did: no extra field.
	rec := e.call(http.MethodPost, "/messages", "{")
	if rec.Code != http.StatusBadRequest || rec.Body.String() != `{"error":"invalid JSON body","ok":false}`+"\n" {
		t.Errorf("legacy answer changed: %d %q", rec.Code, rec.Body.String())
	}
}

func TestSameEmailInAnotherCaseIsTakenAndKeepsTheInvitation(t *testing.T) {
	e := newEnv(t)
	e.signUp(t, "alice@example.com")

	code := e.invite(t, time.Hour)
	rec := e.register(code, "ALICE@example.com", goodPassword)
	if rec.Code != http.StatusConflict || codeOf(t, rec) != "email_taken" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT count(*) FROM users`); n != 1 {
		t.Fatalf("%d users exist", n)
	}
	// The code was not spent by the failed attempt.
	if rec := e.register(code, "bob@example.com", goodPassword); rec.Code != http.StatusCreated {
		t.Fatalf("the invitation was spent by a refused registration: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOneInvitationAmongManyRacingRegistrationsMakesOneAccount(t *testing.T) {
	e := newEnvWith(t, slowHasher)
	code := e.invite(t, time.Hour)

	const n = 10
	statuses := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = e.register(code, "racer"+strconv.Itoa(i)+"@example.com", goodPassword).Code
		}()
	}
	wg.Wait()

	created := 0
	for _, s := range statuses {
		switch s {
		case http.StatusCreated:
			created++
		case http.StatusForbidden:
		default:
			t.Errorf("unexpected status %d in %v", s, statuses)
		}
	}
	if created != 1 || e.count(t, `SELECT count(*) FROM users`) != 1 || e.count(t, `SELECT count(*) FROM sessions`) != 1 {
		t.Fatalf("statuses %v; users=%d sessions=%d", statuses, e.count(t, `SELECT count(*) FROM users`), e.count(t, `SELECT count(*) FROM sessions`))
	}
}

func TestOneEmailAmongManyRacingRegistrationsMakesOneAccountAndSpendsOneInvitation(t *testing.T) {
	e := newEnvWith(t, slowHasher)
	const n = 6
	codes := make([]string, n)
	for i := range codes {
		codes[i] = e.invite(t, time.Hour)
	}

	statuses := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = e.register(codes[i], "same@example.com", goodPassword).Code
		}()
	}
	wg.Wait()

	created, taken := 0, 0
	for _, s := range statuses {
		switch s {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			taken++
		}
	}
	unused := e.count(t, `SELECT count(*) FROM invitations WHERE used_at IS NULL`)
	if created != 1 || taken != n-1 || unused != n-1 || e.count(t, `SELECT count(*) FROM users`) != 1 {
		t.Fatalf("statuses %v; unused invitations %d", statuses, unused)
	}
}

func TestRegistrationGuessingIsSlowedPerAddress(t *testing.T) {
	e := newEnv(t)
	guess := func(ip string) *httptest.ResponseRecorder {
		wrong, _, _ := auth.NewInviteCode()
		return e.register(wrong, "guesser@example.com", goodPassword, fromIP(ip))
	}
	for i := range 10 {
		if rec := guess("198.51.100.7"); rec.Code != http.StatusForbidden {
			t.Fatalf("guess %d: %d", i+1, rec.Code)
		}
	}
	rec := guess("198.51.100.7")
	if rec.Code != http.StatusTooManyRequests || codeOf(t, rec) != "too_many_attempts" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("11th guess: %d %s (Retry-After %q)", rec.Code, rec.Body.String(), rec.Header().Get("Retry-After"))
	}
	// Blocked means blocked: even a valid code from that address waits.
	if rec := e.register(e.invite(t, time.Hour), "late@example.com", goodPassword, fromIP("198.51.100.7")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("valid code while blocked: %d", rec.Code)
	}

	// Other addresses are not affected, and the block ends with its window.
	if rec := e.register(e.invite(t, time.Hour), "other@example.com", goodPassword, fromIP("198.51.100.8")); rec.Code != http.StatusCreated {
		t.Fatalf("another address: %d %s", rec.Code, rec.Body.String())
	}
	e.advance(61 * time.Minute)
	if rec := e.register(e.invite(t, time.Hour), "later@example.com", goodPassword, fromIP("198.51.100.7")); rec.Code != http.StatusCreated {
		t.Fatalf("after the window: %d %s", rec.Code, rec.Body.String())
	}
}

// ---- signing in ------------------------------------------------------------------------------

func TestLoginStartsASessionPerSignIn(t *testing.T) {
	e := newEnv(t)
	first := e.signUp(t, "alice@example.com")

	rec := e.login("  ALICE@example.com", goodPassword)
	if rec.Code != http.StatusOK || bodyOf(t, rec)["email"] != "alice@example.com" {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	second := sessionCookieOf(rec)
	if second == nil || second.Value == first.Value {
		t.Fatal("login did not start a new session")
	}
	// Both stay valid: signing in on a second device does not sign out the first.
	if e.me(first).Code != http.StatusOK || e.me(second).Code != http.StatusOK {
		t.Fatal("a session stopped working")
	}
}

func TestEveryLoginFailureLooksTheSame(t *testing.T) {
	e := newEnv(t)
	e.signUp(t, "alice@example.com")

	wrongPassword := e.login("alice@example.com", "not the password at all", fromIP("192.0.2.10"))
	unknownEmail := e.login("nobody@example.com", goodPassword, fromIP("192.0.2.11"))
	notAnEmail := e.login("not-an-email", goodPassword, fromIP("192.0.2.12"))
	emptyPassword := e.login("alice@example.com", "", fromIP("192.0.2.13"))

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"wrong password": wrongPassword, "unknown email": unknownEmail, "not an email": notAnEmail, "empty password": emptyPassword,
	} {
		if rec.Code != http.StatusUnauthorized || codeOf(t, rec) != "invalid_credentials" || rec.Body.String() != wrongPassword.Body.String() {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		if sessionCookieOf(rec) != nil {
			t.Errorf("%s: a failed login set a cookie", name)
		}
	}
}

func TestAnUnknownEmailCostsAboutAsMuchAsAWrongPassword(t *testing.T) {
	// A hash heavy enough (about 10 ms) to stand out from the rest of the request, so that skipping it would show.
	e := newEnvWith(t, func(s *Server) {
		s.Hasher = auth.NewHasher(auth.Params{MemoryKiB: 16 * 1024, Iterations: 1, Parallelism: 1}, 4)
	})
	// The hash is made once, on first use; do that outside the measurement.
	e.signUp(t, "alice@example.com")
	e.login("nobody@example.com", goodPassword, fromIP("192.0.2.1"))

	measure := func(email string) time.Duration {
		var runs []time.Duration
		for i := range 9 {
			ip := "192.0.2." + strconv.Itoa(20+i) // a new address each time keeps the limits out of it
			start := time.Now()
			e.login(email, "a wrong password", fromIP(ip))
			runs = append(runs, time.Since(start))
		}
		slices.Sort(runs)
		return runs[len(runs)/2]
	}
	known, unknown := measure("alice@example.com"), measure("ghost@example.com")
	if unknown < known/3 {
		t.Fatalf("an unknown email answers in %v, a wrong password in %v: the difference shows which emails exist", unknown, known)
	}
}

func TestRepeatedFailuresBlockAnEmailEvenForTheRightPassword(t *testing.T) {
	e := newEnv(t)
	e.signUp(t, "alice@example.com")

	for i := range 5 {
		if rec := e.login("alice@example.com", "wrong wrong wrong", fromIP("192.0.2."+strconv.Itoa(30+i))); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i+1, rec.Code)
		}
	}
	// Different addresses each time: this is the per-email limit, not the per-address one.
	rec := e.login("alice@example.com", goodPassword, fromIP("192.0.2.99"))
	if rec.Code != http.StatusTooManyRequests || codeOf(t, rec) != "too_many_attempts" {
		t.Fatalf("with the right password while blocked: %d %s", rec.Code, rec.Body.String())
	}
	if seconds, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || seconds < 1 || seconds > 900 {
		t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	if sessionCookieOf(rec) != nil {
		t.Fatal("a blocked login set a cookie")
	}
	if rec := e.login("bob@example.com", goodPassword, fromIP("192.0.2.98")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("another email was affected: %d", rec.Code)
	}

	e.advance(16 * time.Minute)
	if rec := e.login("alice@example.com", goodPassword, fromIP("192.0.2.97")); rec.Code != http.StatusOK {
		t.Fatalf("after the window: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUnknownEmailsAreCountedLikeKnownOnes(t *testing.T) {
	e := newEnv(t)
	for i := range 5 {
		e.login("ghost@example.com", goodPassword, fromIP("192.0.2."+strconv.Itoa(40+i)))
	}
	// If only registered addresses were counted, this would still be a plain 401 and so reveal the difference.
	if rec := e.login("ghost@example.com", goodPassword, fromIP("192.0.2.90")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("an unregistered address was never blocked: %d", rec.Code)
	}
}

func TestRepeatedFailuresFromOneAddressBlockThatAddress(t *testing.T) {
	e := newEnv(t)
	e.signUp(t, "alice@example.com")

	for i := range 20 {
		// Every attempt names another email, so only the per-address limit can stop this.
		if rec := e.login("user"+strconv.Itoa(i)+"@example.com", "guess", fromIP("203.0.113.5")); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	if rec := e.login("alice@example.com", goodPassword, fromIP("203.0.113.5")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the 21st attempt from one address: %d", rec.Code)
	}
	if rec := e.login("alice@example.com", goodPassword, fromIP("203.0.113.6")); rec.Code != http.StatusOK {
		t.Fatalf("a neighbouring address was blocked: %d", rec.Code)
	}
}

func TestSigningInClearsTheEmailsFailures(t *testing.T) {
	e := newEnv(t)
	e.signUp(t, "alice@example.com")

	for round := range 3 {
		for range 4 {
			e.login("alice@example.com", "wrong wrong wrong", fromIP("192.0.2.60"))
		}
		if rec := e.login("alice@example.com", goodPassword, fromIP("192.0.2.61")); rec.Code != http.StatusOK {
			t.Fatalf("round %d: four failures then the right password: %d", round, rec.Code)
		}
	}
}

func TestLoginUpgradesAnOutdatedHash(t *testing.T) {
	e := newEnv(t)
	old := auth.NewHasher(auth.Params{MemoryKiB: 32, Iterations: 1, Parallelism: 1}, 1)
	oldHash, _ := old.Hash(context.Background(), goodPassword)
	if _, err := e.db.CreateUser(context.Background(), dbgen.CreateUserParams{
		Email: "old@example.com", PasswordHash: oldHash, Language: "en", CreatedAt: e.server.now(),
	}); err != nil {
		t.Fatal(err)
	}

	if rec := e.login("old@example.com", goodPassword); rec.Code != http.StatusOK {
		t.Fatalf("login with an old-settings hash: %d %s", rec.Code, rec.Body.String())
	}
	user, _ := e.db.GetUserByEmail(context.Background(), "old@example.com")
	if !strings.HasPrefix(user.PasswordHash, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("hash was not upgraded: %s", user.PasswordHash)
	}
	if rec := e.login("old@example.com", goodPassword); rec.Code != http.StatusOK {
		t.Fatalf("login after the upgrade: %d", rec.Code)
	}
	// A wrong password must not upgrade anything.
	other := auth.NewHasher(auth.Params{MemoryKiB: 32, Iterations: 1, Parallelism: 1}, 1)
	otherHash, _ := other.Hash(context.Background(), goodPassword)
	e.db.CreateUser(context.Background(), dbgen.CreateUserParams{Email: "other@example.com", PasswordHash: otherHash, Language: "en", CreatedAt: e.server.now()})
	e.login("other@example.com", "not the password", fromIP("192.0.2.70"))
	user, _ = e.db.GetUserByEmail(context.Background(), "other@example.com")
	if user.PasswordHash != otherHash {
		t.Fatal("a failed login changed the stored hash")
	}
}

// ---- sessions --------------------------------------------------------------------------------

func TestSessionRequiresAValidCookie(t *testing.T) {
	e := newEnv(t)
	c := e.signUp(t, "alice@example.com")

	hashAsToken := base64.RawURLEncoding.EncodeToString(auth.HashSessionToken(c.Value))
	forged := strings.Repeat("A", auth.SessionTokenLength)
	otherName := &http.Cookie{Name: cookieName, Value: c.Value} // the unprefixed name is not accepted when Secure is on

	for name, cookie := range map[string]*http.Cookie{
		"none":                nil,
		"random token":        {Name: secureCookieName, Value: forged},
		"wrong length":        {Name: secureCookieName, Value: c.Value[:10]},
		"empty":               {Name: secureCookieName, Value: ""},
		"the stored hash":     {Name: secureCookieName, Value: hashAsToken},
		"token with a suffix": {Name: secureCookieName, Value: c.Value + "A"},
		"without the prefix":  otherName,
	} {
		rec := e.me(cookie)
		if rec.Code != http.StatusUnauthorized || codeOf(t, rec) != "unauthorized" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if e.me(c).Code != http.StatusOK {
		t.Fatal("the real cookie stopped working")
	}
}

func TestLogoutEndsTheSessionAndClearsTheCookie(t *testing.T) {
	e := newEnv(t)
	c := e.signUp(t, "alice@example.com")
	other := sessionCookieOf(e.login("alice@example.com", goodPassword))

	rec := e.call(http.MethodPost, "/v1/auth/logout", "", withCookie(c))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("logout: %d %q", rec.Code, rec.Body.String())
	}
	cleared := sessionCookieOf(rec)
	if cleared == nil || cleared.Value != "" || cleared.MaxAge >= 0 || !cleared.HttpOnly || !cleared.Secure || cleared.Path != "/" {
		t.Fatalf("the cookie was not cleared properly: %+v", cleared)
	}
	if e.me(c).Code != http.StatusUnauthorized {
		t.Fatal("the session still works after logout")
	}
	if e.me(other).Code != http.StatusOK {
		t.Fatal("logging out ended the user's other session")
	}
	if n := e.count(t, `SELECT count(*) FROM sessions`); n != 1 {
		t.Fatalf("%d sessions stored, want 1", n)
	}
}

func TestLogoutNeverFails(t *testing.T) {
	e := newEnv(t)
	for name, mods := range map[string][]func(*http.Request){
		"no cookie":      nil,
		"unknown cookie": {withCookie(&http.Cookie{Name: secureCookieName, Value: strings.Repeat("B", auth.SessionTokenLength)})},
		"garbage cookie": {withCookie(&http.Cookie{Name: secureCookieName, Value: "x"})},
	} {
		rec := e.call(http.MethodPost, "/v1/auth/logout", "", mods...)
		if rec.Code != http.StatusNoContent || sessionCookieOf(rec) == nil || sessionCookieOf(rec).MaxAge >= 0 {
			t.Errorf("%s: %d, cookie %+v", name, rec.Code, sessionCookieOf(rec))
		}
	}
}

func TestIdleSessionsExpire(t *testing.T) {
	e := newEnv(t)
	c := e.signUp(t, "alice@example.com")

	e.advance(29 * 24 * time.Hour)
	if e.me(c).Code != http.StatusOK {
		t.Fatal("the session ended before its idle limit")
	}
	e.advance(31 * 24 * time.Hour) // unused for 31 days since the last request
	if rec := e.me(c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an idle session still works: %d", rec.Code)
	}
}

func TestSessionsSlideWhileInUseButNeverPastTheAbsoluteLimit(t *testing.T) {
	e := newEnv(t)
	c := e.signUp(t, "alice@example.com")
	const day = 24 * time.Hour

	// Used every 20 days, so the 30-day idle limit never hits: it must keep working, and each use renews the cookie.
	for _, elapsed := range []int{20, 40, 60} {
		e.advance(20 * day)
		rec := e.me(c)
		if rec.Code != http.StatusOK {
			t.Fatalf("day %d: %d", elapsed, rec.Code)
		}
		renewed := sessionCookieOf(rec)
		// (The clock runs a few milliseconds past the sign-in, so the lifetime may come out a second short.)
		if renewed == nil || renewed.Value != c.Value || renewed.MaxAge > int((30*day).Seconds()) || renewed.MaxAge < int((30*day).Seconds())-5 {
			t.Fatalf("day %d: the cookie was not renewed: %+v", elapsed, renewed)
		}
	}
	// Day 80: only 10 days remain until the absolute limit, and the cookie must not promise more.
	e.advance(20 * day)
	rec := e.me(c)
	renewed := sessionCookieOf(rec)
	if rec.Code != http.StatusOK || renewed == nil || renewed.MaxAge > int((10*day).Seconds()) || renewed.MaxAge < int((10*day).Seconds())-5 {
		t.Fatalf("day 80: %d, cookie %+v", rec.Code, renewed)
	}
	// The stored expiry never goes past the absolute limit either.
	if n := e.count(t, `SELECT count(*) FROM sessions WHERE expires_at > absolute_expires_at`); n != 0 {
		t.Fatalf("%d sessions expire later than their absolute limit", n)
	}
	// Day 100: used only 20 days ago, but 90 days have passed since sign-in.
	e.advance(20 * day)
	if rec := e.me(c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a session outlived the absolute limit: %d", rec.Code)
	}
}

func TestSessionRenewalIsThrottled(t *testing.T) {
	e := newEnv(t)
	c := e.signUp(t, "alice@example.com")

	if rec := e.me(c); sessionCookieOf(rec) != nil {
		t.Fatal("renewed right after sign-in")
	}
	e.advance(30 * time.Minute)
	if rec := e.me(c); sessionCookieOf(rec) != nil {
		t.Fatal("renewed within the hour")
	}
	e.advance(31 * time.Minute)
	if rec := e.me(c); sessionCookieOf(rec) == nil {
		t.Fatal("not renewed after an hour")
	}
	if rec := e.me(c); sessionCookieOf(rec) != nil {
		t.Fatal("renewed twice in a row")
	}
}

func TestExpiredSessionsAreRemoved(t *testing.T) {
	e := newEnv(t)
	e.signUp(t, "alice@example.com")
	e.advance(60 * 24 * time.Hour)
	e.signUp(t, "bob@example.com")

	removed, err := e.db.DeleteExpiredSessions(context.Background(), e.server.now())
	if err != nil || removed != 1 {
		t.Fatalf("removed %d, %v", removed, err)
	}
	if n := e.count(t, `SELECT count(*) FROM sessions`); n != 1 {
		t.Fatalf("%d sessions left", n)
	}
}

// ---- changing the password -------------------------------------------------------------------

func (e *testEnv) changePassword(c *http.Cookie, current, next string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	if c != nil {
		mods = append(mods, withCookie(c))
	}
	return e.call(http.MethodPost, "/v1/me/password", asJSON(map[string]any{"currentPassword": current, "newPassword": next}), mods...)
}

func TestChangingThePasswordEndsOtherSessions(t *testing.T) {
	e := newEnv(t)
	current := e.signUp(t, "alice@example.com")
	elsewhere := sessionCookieOf(e.login("alice@example.com", goodPassword))
	stranger := e.signUp(t, "bob@example.com")

	const next = "a brand new passphrase"
	if rec := e.changePassword(current, goodPassword, next); rec.Code != http.StatusNoContent {
		t.Fatalf("change: %d %s", rec.Code, rec.Body.String())
	}
	if e.me(current).Code != http.StatusOK {
		t.Error("the session that changed the password was ended")
	}
	if e.me(elsewhere).Code != http.StatusUnauthorized {
		t.Error("another session of the user survived the change")
	}
	if e.me(stranger).Code != http.StatusOK {
		t.Error("another user's session was ended")
	}
	if rec := e.login("alice@example.com", goodPassword, fromIP("192.0.2.80")); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old password still works: %d", rec.Code)
	}
	if rec := e.login("alice@example.com", next, fromIP("192.0.2.81")); rec.Code != http.StatusOK {
		t.Errorf("the new password does not work: %d", rec.Code)
	}
}

func TestChangingThePasswordNeedsTheCurrentOne(t *testing.T) {
	e := newEnv(t)
	c := e.signUp(t, "alice@example.com")

	rec := e.changePassword(c, "not my password", "a brand new passphrase")
	if rec.Code != http.StatusForbidden || codeOf(t, rec) != "wrong_password" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.login("alice@example.com", goodPassword, fromIP("192.0.2.82")); rec.Code != http.StatusOK {
		t.Fatal("a refused change altered the password")
	}
}

func TestChangingThePasswordIsRefusedForWeakOnesAndWithoutASession(t *testing.T) {
	e := newEnv(t)
	c := e.signUp(t, "alice@example.com")

	rec := e.changePassword(c, goodPassword, "short")
	if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "weak_password" {
		t.Errorf("weak new password: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.changePassword(nil, goodPassword, "a brand new passphrase")
	if rec.Code != http.StatusUnauthorized || codeOf(t, rec) != "unauthorized" {
		t.Errorf("no session: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.login("alice@example.com", goodPassword, fromIP("192.0.2.83")); rec.Code != http.StatusOK {
		t.Fatal("a refused change altered the password")
	}
}

func TestGuessingThePasswordThroughAStolenSessionIsLimited(t *testing.T) {
	e := newEnv(t)
	c := e.signUp(t, "alice@example.com")

	for i := range 5 {
		if rec := e.changePassword(c, "guess number "+strconv.Itoa(i), "a brand new passphrase"); rec.Code != http.StatusForbidden {
			t.Fatalf("guess %d: %d", i+1, rec.Code)
		}
	}
	rec := e.changePassword(c, goodPassword, "a brand new passphrase")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the right password after five wrong ones: %d %s", rec.Code, rec.Body.String())
	}
	// The same count blocks signing in with that email, so the limit cannot be dodged by switching door.
	if rec := e.login("alice@example.com", goodPassword, fromIP("192.0.2.84")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("sign-in after the failures: %d", rec.Code)
	}
}

// ---- routes that need a session --------------------------------------------------------------

// protectedOperations lists the operations api/openapi.yaml declares with `security:`, as "METHOD /path".
func protectedOperations(t *testing.T) []string {
	t.Helper()
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	var path, method string
	pathLine, methodLine := regexp.MustCompile(`^  (/\S*):$`), regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
	for _, line := range strings.Split(string(spec), "\n") {
		switch {
		case pathLine.MatchString(line):
			path, method = pathLine.FindStringSubmatch(line)[1], ""
		case methodLine.MatchString(line):
			method = strings.ToUpper(methodLine.FindStringSubmatch(line)[1])
		case line == "      security:" && path != "" && method != "":
			found = append(found, method+" "+path)
		}
	}
	return found
}

func TestEveryOperationDeclaredWithSecurityRefusesRequestsWithoutASession(t *testing.T) {
	protected := protectedOperations(t)
	if len(protected) < 2 { // /v1/me and /v1/me/password at least; fewer means the scan above broke
		t.Fatalf("found only %v", protected)
	}
	e := newEnv(t)
	e.signUp(t, "alice@example.com") // a user exists, but these requests carry no cookie
	for _, op := range protected {
		method, path, _ := strings.Cut(op, " ")
		// A placeholder such as {id} stands for a well-formed value, or the request would be refused for its
		// shape before anything asks who is sending it.
		path = regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "00000000-0000-0000-0000-000000000000")
		rec := e.call(method, path, "{}")
		if rec.Code != http.StatusUnauthorized || codeOf(t, rec) != "unauthorized" {
			t.Errorf("%s answers %d %s without a session", op, rec.Code, rec.Body.String())
		}
	}
}

// ---- cross-site requests ---------------------------------------------------------------------

func TestCrossSiteRequestsToTheV1ApiAreRefused(t *testing.T) {
	e := newEnvWith(t, func(s *Server) { s.AllowedOrigins = []string{"http://localhost:5173"} })
	e.signUp(t, "alice@example.com")
	host := func(r *http.Request) { r.Host = "pigeon.test" }

	for name, tc := range map[string]struct {
		mods []func(*http.Request)
		want int
	}{
		"foreign origin":         {[]func(*http.Request){host, withHeader("Origin", "https://evil.example")}, http.StatusForbidden},
		"cross-site fetch":       {[]func(*http.Request){host, withHeader("Sec-Fetch-Site", "cross-site")}, http.StatusForbidden},
		"same-site other origin": {[]func(*http.Request){host, withHeader("Sec-Fetch-Site", "same-site")}, http.StatusForbidden},
		"same origin":            {[]func(*http.Request){host, withHeader("Origin", "https://pigeon.test"), withHeader("Sec-Fetch-Site", "same-origin")}, http.StatusOK},
		"no browser headers":     {[]func(*http.Request){host}, http.StatusOK}, // curl, apps
		"allowed origin":         {[]func(*http.Request){host, withHeader("Origin", "http://localhost:5173")}, http.StatusOK},
		"allowed origin by port": {[]func(*http.Request){host, withHeader("Origin", "http://localhost:3000")}, http.StatusForbidden},
	} {
		rec := e.login("alice@example.com", goodPassword, append(tc.mods, fromIP("192.0.2."+strconv.Itoa(100+len(name)%50)))...)
		if rec.Code != tc.want {
			t.Errorf("%s: login answered %d %s, want %d", name, rec.Code, rec.Body.String(), tc.want)
		}
		if tc.want == http.StatusForbidden && (codeOf(t, rec) != "forbidden_origin" || sessionCookieOf(rec) != nil) {
			t.Errorf("%s: %s (cookie %+v)", name, rec.Body.String(), sessionCookieOf(rec))
		}
	}

	// Reading is not a state change: a foreign page may ask, it just gets no answer worth having.
	if rec := e.call(http.MethodGet, "/v1/me", "", withHeader("Origin", "https://evil.example")); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET with a foreign origin: %d", rec.Code)
	}
}

func TestTheOlderRoutesAreNotSubjectToTheOriginCheck(t *testing.T) {
	// The phone's Side Service posts to /messages and may send headers of its own; that contract must not change.
	e := newEnv(t)
	for _, mods := range [][]func(*http.Request){
		{withHeader("Origin", "https://evil.example")},
		{withHeader("Sec-Fetch-Site", "cross-site")},
	} {
		rec := e.call(http.MethodPost, "/accounts", `{"code":"abc"}`, mods...)
		if rec.Code != http.StatusOK {
			t.Errorf("POST /accounts with browser headers: %d %s", rec.Code, rec.Body.String())
		}
		rec = e.call(http.MethodPost, "/messages", "{", mods...)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /messages with browser headers: %d %s", rec.Code, rec.Body.String())
		}
	}
}

// ---- who is the client -----------------------------------------------------------------------

func TestClientKey(t *testing.T) {
	cases := []struct {
		name       string
		trustProxy bool
		remote     string
		xff        string
		want       string
	}{
		{"plain IPv4", false, "203.0.113.9:5555", "", "203.0.113.9"},
		{"proxy header ignored unless trusted", false, "203.0.113.9:5555", "198.51.100.1", "203.0.113.9"},
		{"last proxy hop when trusted", true, "10.0.0.2:4000", "1.2.3.4, 198.51.100.1", "198.51.100.1"},
		{"a client cannot choose its own address", true, "10.0.0.2:4000", "9.9.9.9, 198.51.100.1", "198.51.100.1"},
		{"single hop", true, "10.0.0.2:4000", "198.51.100.1", "198.51.100.1"},
		{"trusted but header absent", true, "203.0.113.9:5555", "", "203.0.113.9"},
		{"trusted but header is garbage", true, "203.0.113.9:5555", "not-an-ip", "203.0.113.9"},
		{"IPv6 is counted by its /64", false, "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:5555", "", "2001:db8:1:2::/64"},
		{"another host in the same /64", false, "[2001:db8:1:2::1]:5555", "", "2001:db8:1:2::/64"},
		{"a different /64", false, "[2001:db8:1:3::1]:5555", "", "2001:db8:1:3::/64"},
		{"IPv4 mapped into IPv6", false, "[::ffff:203.0.113.9]:5555", "", "203.0.113.9"},
		{"IPv6 zone is dropped", false, "[fe80::1%eth0]:5555", "", "fe80::/64"},
		{"no port", false, "203.0.113.9", "", "203.0.113.9"},
	}
	for _, c := range cases {
		s := &Server{TrustProxyHeaders: c.trustProxy}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := s.clientKey(r); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// ---- secrets ---------------------------------------------------------------------------------

func TestSecretsAreNeverLogged(t *testing.T) {
	e := newEnv(t)
	code := e.invite(t, time.Hour)
	reg := e.register(code, "alice@example.com", goodPassword)
	token := sessionCookieOf(reg).Value
	e.login("alice@example.com", goodPassword)
	e.login("alice@example.com", "a wrong password", fromIP("192.0.2.150"))
	e.changePassword(sessionCookieOf(reg), goodPassword, "a brand new passphrase")
	e.call(http.MethodPost, "/v1/auth/logout", "", withCookie(sessionCookieOf(reg)))

	logs := e.logs.String()
	compact := strings.ReplaceAll(code, "-", "")
	for name, secret := range map[string]string{
		"password": goodPassword, "new password": "a brand new passphrase", "wrong password": "a wrong password",
		"session token": token, "invitation code": code, "invitation code without dashes": compact,
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log contains the %s", name)
		}
	}
	if !strings.Contains(logs, "user registered") {
		t.Error("registration left no trace in the log at all")
	}
}
