package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/auth"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

func bearer(token string) func(*http.Request) {
	return withHeader("Authorization", "Bearer "+token)
}

// startPairing is what a watch does first. clientName may be empty.
func (e *testEnv) startPairing(clientName string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	body := "{}"
	if clientName != "" {
		body = asJSON(map[string]any{"clientName": clientName})
	}
	return e.call(http.MethodPost, "/v1/device/code", body, mods...)
}

func (e *testEnv) poll(deviceCode string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	return e.call(http.MethodPost, "/v1/device/token", asJSON(map[string]any{"deviceCode": deviceCode}), mods...)
}

func (e *testEnv) approve(c *http.Cookie, userCode, name string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	body := map[string]any{"userCode": userCode}
	if name != "" {
		body["name"] = name
	}
	if c != nil {
		mods = append(mods, withCookie(c))
	}
	return e.call(http.MethodPost, "/v1/device/approve", asJSON(body), mods...)
}

// pairing is one pairing's two codes, as the watch received them.
type pairing struct{ deviceCode, userCode string }

func (e *testEnv) newPairing(t testing.TB, clientName string, mods ...func(*http.Request)) pairing {
	t.Helper()
	rec := e.startPairing(clientName, mods...)
	if rec.Code != http.StatusOK {
		t.Fatalf("start pairing: %d %s", rec.Code, rec.Body.String())
	}
	b := bodyOf(t, rec)
	return pairing{deviceCode: b["deviceCode"].(string), userCode: b["userCode"].(string)}
}

// pairDevice goes through the whole flow for a signed-in user and returns the device's token and id.
func (e *testEnv) pairDevice(t testing.TB, c *http.Cookie, name string) (token, id string) {
	t.Helper()
	p := e.newPairing(t, "")
	if rec := e.approve(c, p.userCode, name); rec.Code != http.StatusNoContent {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	e.advance(10 * time.Second) // past the poll spacing
	rec := e.poll(p.deviceCode)
	b := bodyOf(t, rec)
	if rec.Code != http.StatusOK || b["status"] != "approved" {
		t.Fatalf("poll after approval: %d %s", rec.Code, rec.Body.String())
	}
	return b["deviceToken"].(string), b["deviceId"].(string)
}

func TestPairingFromStartToToken(t *testing.T) {
	e := newEnvWith(t, func(s *Server) { s.DefaultServerURL = "https://pigeon.test" })
	owner := e.signUp(t, "alice@example.com")

	// 1. The watch asks for codes.
	start := e.startPairing("Amazfit Balance")
	if start.Code != http.StatusOK {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	got := bodyOf(t, start)
	userCode, deviceCode := got["userCode"].(string), got["deviceCode"].(string)
	if !strings.Contains(userCode, "-") || len(userCode) != 9 || len(deviceCode) != auth.DeviceCodeLength {
		t.Fatalf("codes: %q %q", userCode, deviceCode)
	}
	if got["verificationUri"] != "https://pigeon.test/pair" || got["verificationUriComplete"] != "https://pigeon.test/pair?code="+userCode ||
		got["expiresIn"] != float64(600) || got["interval"] != float64(5) {
		t.Fatalf("start answer: %v", got)
	}
	if strings.Contains(start.Header().Get("Set-Cookie"), "session") {
		t.Fatal("starting a pairing set a cookie")
	}

	// 2. Nobody has approved yet.
	pending := bodyOf(t, e.poll(deviceCode))
	if pending["status"] != "pending" || pending["interval"] != float64(5) || pending["deviceToken"] != nil {
		t.Fatalf("pending answer: %v", pending)
	}

	// 3. The person looks the code up and approves it.
	info := e.call(http.MethodGet, "/v1/device/pairing/"+strings.ToLower(userCode), "", withCookie(owner))
	if info.Code != http.StatusOK || bodyOf(t, info)["clientName"] != "Amazfit Balance" || bodyOf(t, info)["userCode"] != userCode {
		t.Fatalf("lookup: %d %s", info.Code, info.Body.String())
	}
	if rec := e.approve(owner, userCode, "My watch"); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}

	// 4. The next poll collects the token, once.
	e.advance(6 * time.Second)
	done := e.poll(deviceCode)
	b := bodyOf(t, done)
	token, _ := b["deviceToken"].(string)
	if done.Code != http.StatusOK || b["status"] != "approved" || len(token) != auth.DeviceTokenLength || b["deviceId"] == nil {
		t.Fatalf("approved answer: %d %s", done.Code, done.Body.String())
	}
	e.advance(6 * time.Second)
	again := e.poll(deviceCode)
	if again.Code != http.StatusBadRequest || codeOf(t, again) != "invalid_device_code" || strings.Contains(again.Body.String(), token) {
		t.Fatalf("the token was offered twice: %d %s", again.Code, again.Body.String())
	}

	// 5. The device exists, belongs to the person, carries the name they gave, and only the token's hash is stored.
	devices, _ := e.db.ListDevices(context.Background(), mustUser(t, e, "alice@example.com").ID)
	if len(devices) != 1 || devices[0].Name != "My watch" || devices[0].ID.String() != b["deviceId"] {
		t.Fatalf("devices: %+v", devices)
	}
	var stored []byte
	e.pool.QueryRow(context.Background(), `SELECT token_hash FROM devices`).Scan(&stored)
	if string(stored) == token || string(stored) != string(auth.HashDeviceToken(token)) {
		t.Fatal("the device token is stored in the clear, or hashed differently from lookups")
	}
}

func mustUser(t testing.TB, e *testEnv, email string) dbgen.User {
	t.Helper()
	u, err := e.db.GetUserByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestApprovedNameDefaultsToTheWatchsOwnAndThenToWatch(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	userID := mustUser(t, e, "alice@example.com").ID

	for _, tc := range []struct{ clientName, given, want string }{
		{"Amazfit Balance", "", "Amazfit Balance"},
		{"Amazfit Balance", "  Kitchen  ", "Kitchen"},
		{"", "", "Watch"},
		{"", "Mine", "Mine"},
	} {
		p := e.newPairing(t, tc.clientName)
		if rec := e.approve(owner, p.userCode, tc.given); rec.Code != http.StatusNoContent {
			t.Fatalf("%+v: approve %d %s", tc, rec.Code, rec.Body.String())
		}
		e.advance(10 * time.Second)
		e.poll(p.deviceCode)
	}
	devices, _ := e.db.ListDevices(context.Background(), userID)
	var names []string
	for _, d := range devices {
		names = append(names, d.Name)
	}
	if strings.Join(names, "|") != "Amazfit Balance|Kitchen|Watch|Mine" {
		t.Fatalf("names: %q", names)
	}
}

func TestNamesAreCheckedAtBothEnds(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")

	for name, clientName := range map[string]string{
		"too long": strings.Repeat("x", 65), "control character": "bad\x07name", "newline": "two\nlines",
	} {
		if rec := e.startPairing(clientName, fromIP("192.0.2."+strconv.Itoa(len(name)))); rec.Code != http.StatusBadRequest || codeOf(t, rec) != "invalid_request" {
			t.Errorf("client name %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// Sixty-four characters is fine, counted as characters and not bytes.
	if rec := e.startPairing(strings.Repeat("ж", 64)); rec.Code != http.StatusOK {
		t.Errorf("64 multi-byte characters: %d %s", rec.Code, rec.Body.String())
	}

	p := e.newPairing(t, "")
	for name, given := range map[string]string{"too long": strings.Repeat("x", 65), "control": "bad\x00name", "newline": "a\nb"} {
		if rec := e.approve(owner, p.userCode, given); rec.Code != http.StatusBadRequest || codeOf(t, rec) != "invalid_request" {
			t.Errorf("name %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := e.approve(owner, p.userCode, "fine"); rec.Code != http.StatusNoContent {
		t.Errorf("a refused name used up the pairing: %d", rec.Code)
	}
}

func TestPairingNeedsASessionToLookUpOrApprove(t *testing.T) {
	e := newEnv(t)
	p := e.newPairing(t, "")

	if rec := e.approve(nil, p.userCode, ""); rec.Code != http.StatusUnauthorized || codeOf(t, rec) != "unauthorized" {
		t.Errorf("approve without a session: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.call(http.MethodGet, "/v1/device/pairing/"+p.userCode, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("lookup without a session: %d", rec.Code)
	}
	// A device token is not a user: it cannot approve pairings.
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "first")
	if rec := e.approve(nil, p.userCode, "", bearer(token)); rec.Code != http.StatusUnauthorized {
		t.Errorf("approve with a device token: %d", rec.Code)
	}
	// And nobody was paired by those attempts.
	e.advance(10 * time.Second)
	if st := bodyOf(t, e.poll(p.deviceCode))["status"]; st != "pending" {
		t.Errorf("the pairing is %v", st)
	}
}

func TestUnknownExpiredAndUsedUserCodesAreAllRefusedTheSameWay(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")

	expired := e.newPairing(t, "")
	e.advance(11 * time.Minute)
	used := e.newPairing(t, "")
	e.approve(owner, used.userCode, "")

	var answers []string
	i := 0
	for name, code := range map[string]string{
		"unknown": "BBBB-BBBB", "expired": expired.userCode, "already approved": used.userCode, "malformed": "hello", "vowels": "AEIO-AEIO", "empty": "",
	} {
		i++
		other := e.signUp(t, "user"+strconv.Itoa(i)+"@example.com") // a user each, so the per-user limit stays out of it
		rec := e.approve(other, code, "")
		if rec.Code != http.StatusNotFound || codeOf(t, rec) != "invalid_user_code" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		answers = append(answers, rec.Body.String())
		lookup := e.call(http.MethodGet, "/v1/device/pairing/"+code, "", withCookie(other))
		if code != "" && (lookup.Code != http.StatusNotFound || lookup.Body.String() != rec.Body.String()) {
			t.Errorf("%s: lookup answered %d %s", name, lookup.Code, lookup.Body.String())
		}
	}
	for _, a := range answers[1:] {
		if a != answers[0] {
			t.Errorf("the answers differ, which tells a guesser what was wrong: %q vs %q", a, answers[0])
		}
	}
}

func TestWrongUserCodesAreLimitedPerUser(t *testing.T) {
	e := newEnv(t)
	guesser := e.signUp(t, "mallory@example.com")
	honest := e.signUp(t, "alice@example.com")
	real := e.newPairing(t, "")

	// Each guess is from a different address: this is the per-user limit.
	for i := range 10 {
		if rec := e.approve(guesser, "BBBB-BBB"+string("CDFGHJKLMN"[i]), "", fromIP("192.0.2."+strconv.Itoa(50+i))); rec.Code != http.StatusNotFound {
			t.Fatalf("guess %d: %d", i+1, rec.Code)
		}
	}
	rec := e.approve(guesser, real.userCode, "")
	if rec.Code != http.StatusTooManyRequests || codeOf(t, rec) != "too_many_attempts" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("the right code after ten wrong ones: %d %s", rec.Code, rec.Body.String())
	}
	if lookup := e.call(http.MethodGet, "/v1/device/pairing/"+real.userCode, "", withCookie(guesser)); lookup.Code != http.StatusTooManyRequests {
		t.Fatalf("lookup while blocked: %d", lookup.Code)
	}
	// Someone else is unaffected, and the block ends with its window.
	if rec := e.call(http.MethodGet, "/v1/device/pairing/"+real.userCode, "", withCookie(honest)); rec.Code != http.StatusOK {
		t.Fatalf("another user: %d", rec.Code)
	}
	e.advance(16 * time.Minute)
	real = e.newPairing(t, "")
	if rec := e.approve(guesser, real.userCode, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("after the window: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOnlyOneUserCanApproveAPairing(t *testing.T) {
	e := newEnv(t)
	alice := e.signUp(t, "alice@example.com")
	bob := e.signUp(t, "bob@example.com")
	p := e.newPairing(t, "")

	if rec := e.approve(alice, p.userCode, "Alice's"); rec.Code != http.StatusNoContent {
		t.Fatalf("first: %d", rec.Code)
	}
	if rec := e.approve(bob, p.userCode, "Bob's"); rec.Code != http.StatusNotFound {
		t.Fatalf("second approval: %d", rec.Code)
	}
	e.advance(10 * time.Second)
	b := bodyOf(t, e.poll(p.deviceCode))
	if b["status"] != "approved" {
		t.Fatalf("poll: %v", b)
	}
	aliceDevices, _ := e.db.ListDevices(context.Background(), mustUser(t, e, "alice@example.com").ID)
	bobDevices, _ := e.db.ListDevices(context.Background(), mustUser(t, e, "bob@example.com").ID)
	if len(aliceDevices) != 1 || len(bobDevices) != 0 {
		t.Fatalf("the device went to alice=%d bob=%d", len(aliceDevices), len(bobDevices))
	}
}

func TestUsersRacingToApproveOnePairingEndUpWithOneWinner(t *testing.T) {
	e := newEnv(t)
	const n = 6
	users := make([]*http.Cookie, n)
	for i := range users {
		users[i] = e.signUp(t, "user"+strconv.Itoa(i)+"@example.com")
	}
	p := e.newPairing(t, "")

	statuses := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = e.approve(users[i], p.userCode, "mine "+strconv.Itoa(i)).Code
		}()
	}
	wg.Wait()

	approved := 0
	for _, s := range statuses {
		switch s {
		case http.StatusNoContent:
			approved++
		case http.StatusNotFound:
		default:
			t.Errorf("unexpected status %d in %v", s, statuses)
		}
	}
	if approved != 1 {
		t.Fatalf("%d users approved one pairing (statuses %v)", approved, statuses)
	}
	e.advance(10 * time.Second)
	if rec := e.poll(p.deviceCode); bodyOf(t, rec)["status"] != "approved" {
		t.Fatalf("poll: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT count(*) FROM devices`); n != 1 {
		t.Fatalf("%d devices created", n)
	}
	var owner string
	e.pool.QueryRow(context.Background(), `SELECT name FROM devices`).Scan(&owner)
	winner := -1
	for i, s := range statuses {
		if s == http.StatusNoContent {
			winner = i
		}
	}
	if owner != "mine "+strconv.Itoa(winner) {
		t.Fatalf("the device is named %q, but user %d won", owner, winner)
	}
}

func TestPairingExpires(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	p := e.newPairing(t, "")

	e.advance(9 * time.Minute)
	if st := bodyOf(t, e.poll(p.deviceCode))["status"]; st != "pending" {
		t.Fatalf("after nine minutes: %v", st)
	}
	e.advance(2 * time.Minute)
	rec := e.poll(p.deviceCode)
	if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "expired_token" {
		t.Fatalf("after eleven minutes: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.approve(owner, p.userCode, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("approving an expired pairing: %d", rec.Code)
	}
}

func TestAnApprovalInTheLastMomentsStillLeavesTimeToCollectTheToken(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	p := e.newPairing(t, "")

	e.advance(9*time.Minute + 55*time.Second) // five seconds before it runs out
	if rec := e.approve(owner, p.userCode, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("approve: %d", rec.Code)
	}
	e.advance(time.Minute) // the watch's next poll comes after the nominal expiry
	if rec := e.poll(p.deviceCode); rec.Code != http.StatusOK || bodyOf(t, rec)["status"] != "approved" {
		t.Fatalf("poll after the nominal expiry: %d %s", rec.Code, rec.Body.String())
	}

	// But the grace is bounded: a long-forgotten approval does not keep a token waiting forever.
	p = e.newPairing(t, "")
	e.advance(9*time.Minute + 55*time.Second)
	e.approve(owner, p.userCode, "")
	e.advance(3 * time.Minute) // more than the two minutes of grace
	if rec := e.poll(p.deviceCode); rec.Code != http.StatusBadRequest || codeOf(t, rec) != "expired_token" {
		t.Fatalf("poll long after approval: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPollingTooFastIsRefusedAndCostsNothing(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	p := e.newPairing(t, "")

	e.advance(5 * time.Second)
	if rec := e.poll(p.deviceCode); rec.Code != http.StatusOK {
		t.Fatalf("first poll: %d", rec.Code)
	}
	rec := e.poll(p.deviceCode) // immediately again
	if rec.Code != http.StatusTooManyRequests || codeOf(t, rec) != "too_many_attempts" {
		t.Fatalf("second poll at once: %d %s", rec.Code, rec.Body.String())
	}
	if seconds, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || seconds < 1 || seconds > 3 {
		t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	// A refused poll does not count as one: the clock for the next starts at the last real poll.
	e.advance(3 * time.Second)
	if rec := e.poll(p.deviceCode); rec.Code != http.StatusOK {
		t.Fatalf("poll after the minimum gap: %d %s", rec.Code, rec.Body.String())
	}

	// Approval is not held back by the spacing of earlier polls.
	e.approve(owner, p.userCode, "")
	e.advance(3 * time.Second)
	if rec := e.poll(p.deviceCode); bodyOf(t, rec)["status"] != "approved" {
		t.Fatalf("poll after approval: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTheTokenIsHandedOutOnceEvenWhenPollsRace(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	p := e.newPairing(t, "")
	e.approve(owner, p.userCode, "")
	e.advance(10 * time.Second)
	// The polls below arrive together. Nothing has polled this pairing, so the spacing rule does not refuse
	// them, and the only thing standing between them and several tokens is the atomic redemption.

	const n = 8
	codes := make([]int, n)
	tokens := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := e.poll(p.deviceCode, fromIP("192.0.2."+strconv.Itoa(100+i)))
			codes[i] = rec.Code
			if rec.Code == http.StatusOK {
				tokens[i], _ = bodyOf(t, rec)["deviceToken"].(string)
			}
		}()
	}
	wg.Wait()

	granted := 0
	for i := range n {
		if tokens[i] != "" {
			granted++
		}
	}
	devices := e.count(t, `SELECT count(*) FROM devices`)
	if granted != 1 || devices != 1 {
		t.Fatalf("%d tokens handed out and %d devices created (statuses %v)", granted, devices, codes)
	}
}

func TestWrongDeviceCodesAreRefusedAndLimitedPerAddress(t *testing.T) {
	e := newEnv(t)
	real := e.newPairing(t, "")

	for name, code := range map[string]string{
		"unknown": strings.Repeat("A", auth.DeviceCodeLength), "empty": "", "short": "abc", "long": strings.Repeat("A", 500),
		"the user code": real.userCode,
	} {
		rec := e.poll(code, fromIP("198.51.100."+strconv.Itoa(len(name))))
		if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "invalid_device_code" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	for i := range 30 {
		if rec := e.poll(strings.Repeat("B", auth.DeviceCodeLength), fromIP("203.0.113.9")); rec.Code != http.StatusBadRequest {
			t.Fatalf("guess %d: %d", i+1, rec.Code)
		}
	}
	if rec := e.poll(real.deviceCode, fromIP("203.0.113.9")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a real code from a guessing address: %d", rec.Code)
	}
	e.advance(3 * time.Second)
	if rec := e.poll(real.deviceCode, fromIP("203.0.113.10")); rec.Code != http.StatusOK {
		t.Fatalf("another address: %d", rec.Code)
	}
}

func TestStartingPairingsIsLimitedPerAddress(t *testing.T) {
	e := newEnv(t)
	for i := range 10 {
		if rec := e.startPairing("", fromIP("203.0.113.20")); rec.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i+1, rec.Code)
		}
	}
	rec := e.startPairing("", fromIP("203.0.113.20"))
	if rec.Code != http.StatusTooManyRequests || codeOf(t, rec) != "too_many_attempts" {
		t.Fatalf("11th request: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.startPairing("", fromIP("203.0.113.21")); rec.Code != http.StatusOK {
		t.Fatalf("another address: %d", rec.Code)
	}
	e.advance(11 * time.Minute)
	if rec := e.startPairing("", fromIP("203.0.113.20")); rec.Code != http.StatusOK {
		t.Fatalf("after the window: %d", rec.Code)
	}
	if n := e.count(t, `SELECT count(*) FROM device_pairings`); n != 12 {
		t.Fatalf("%d pairings stored", n)
	}
}

func TestOnlyHashesOfTheCodesAreStored(t *testing.T) {
	e := newEnv(t)
	p := e.newPairing(t, "")
	var dump string
	if err := e.pool.QueryRow(context.Background(), `SELECT d::text FROM device_pairings d`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dump, p.deviceCode) {
		t.Fatal("the device code is stored in the clear")
	}
}

// ---- devices ---------------------------------------------------------------------------------

func TestDeviceListIsPerUserAndHidesRevoked(t *testing.T) {
	e := newEnv(t)
	alice := e.signUp(t, "alice@example.com")
	bob := e.signUp(t, "bob@example.com")
	_, first := e.pairDevice(t, alice, "first")
	_, second := e.pairDevice(t, alice, "second")
	e.pairDevice(t, bob, "bobs")

	list := func(c *http.Cookie) []any {
		rec := e.call(http.MethodGet, "/v1/devices", "", withCookie(c))
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		return bodyOf(t, rec)["devices"].([]any)
	}
	devices := list(alice)
	if len(devices) != 2 || devices[0].(map[string]any)["name"] != "first" || devices[1].(map[string]any)["name"] != "second" {
		t.Fatalf("alice's devices: %v", devices)
	}
	d := devices[0].(map[string]any)
	if d["id"] != first || d["lastSeenAt"] != nil || d["createdAt"] == nil || strings.Contains(asJSON(d), "token") {
		t.Fatalf("a device entry: %v", d)
	}

	if rec := e.call(http.MethodDelete, "/v1/devices/"+first, "", withCookie(alice)); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if devices := list(alice); len(devices) != 1 || devices[0].(map[string]any)["id"] != second {
		t.Fatalf("after revoking: %v", devices)
	}
	if len(list(bob)) != 1 {
		t.Fatal("revoking alice's device changed bob's list")
	}
	if rec := e.call(http.MethodGet, "/v1/devices", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("list without a session: %d", rec.Code)
	}
}

func TestRevokingIsForTheOwnerOnlyAndRepeatable(t *testing.T) {
	e := newEnv(t)
	alice := e.signUp(t, "alice@example.com")
	mallory := e.signUp(t, "mallory@example.com")
	_, id := e.pairDevice(t, alice, "alices")

	for name, path := range map[string]string{
		"someone else's device": "/v1/devices/" + id,
		"unknown id":            "/v1/devices/00000000-0000-0000-0000-000000000000",
	} {
		rec := e.call(http.MethodDelete, path, "", withCookie(mallory))
		if rec.Code != http.StatusNotFound || codeOf(t, rec) != "not_found" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := e.call(http.MethodDelete, "/v1/devices/not-a-uuid", "", withCookie(alice)); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed id: %d", rec.Code)
	}
	if rec := e.call(http.MethodDelete, "/v1/devices/"+id, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a session: %d", rec.Code)
	}
	if n := e.count(t, `SELECT count(*) FROM devices WHERE revoked_at IS NULL`); n != 1 {
		t.Fatalf("a refused revocation revoked something (%d active)", n)
	}

	for range 2 {
		if rec := e.call(http.MethodDelete, "/v1/devices/"+id, "", withCookie(alice)); rec.Code != http.StatusNoContent {
			t.Fatalf("owner revoking: %d %s", rec.Code, rec.Body.String())
		}
	}
	var revokedAt time.Time
	e.pool.QueryRow(context.Background(), `SELECT revoked_at FROM devices`).Scan(&revokedAt)
	e.advance(time.Hour)
	e.call(http.MethodDelete, "/v1/devices/"+id, "", withCookie(alice))
	var again time.Time
	e.pool.QueryRow(context.Background(), `SELECT revoked_at FROM devices`).Scan(&again)
	if !again.Equal(revokedAt) {
		t.Fatal("revoking again moved the revocation time")
	}
}

func TestADeviceTokenIdentifiesTheDeviceUntilItIsRevoked(t *testing.T) {
	e := newEnv(t)
	alice := e.signUp(t, "alice@example.com")
	token, id := e.pairDevice(t, alice, "alices")

	// Which handler answers is not the point here: the context a token creates is. Use a helper route that
	// reports it.
	if d := deviceFromRequest(t, e, token); d == nil || d.ID.String() != id || d.UserID != mustUser(t, e, "alice@example.com").ID || d.Name != "alices" {
		t.Fatalf("device for the token: %+v", d)
	}
	for name, header := range map[string]string{
		"no header":      "",
		"another token":  "Bearer " + func() string { tk, _, _ := auth.NewDeviceToken(); return tk }(),
		"basic scheme":   "Basic " + token,
		"truncated":      "Bearer " + token[:len(token)-1],
		"padded":         "Bearer " + token + "x",
		"a session id":   "Bearer " + strings.Repeat("A", auth.SessionTokenLength),
		"the token only": token,
	} {
		if d := deviceFromHeader(t, e, header); d != nil {
			t.Errorf("%s was accepted as device %s", name, d.Name)
		}
	}

	if rec := e.call(http.MethodDelete, "/v1/devices/"+id, "", withCookie(alice)); rec.Code != http.StatusNoContent {
		t.Fatal("revoke failed")
	}
	if d := deviceFromRequest(t, e, token); d != nil {
		t.Fatal("a revoked device's token still works")
	}
}

// deviceFromHeader runs a request with the given Authorization header through the same middleware as the API
// and reports the device it resolved to.
func deviceFromHeader(t testing.TB, e *testEnv, header string) *device {
	t.Helper()
	var got *device
	probe := e.server.authContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = deviceFrom(r.Context()) }))
	req := httptest.NewRequest(http.MethodGet, "/v1/probe", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	probe.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("probe answered %d %s", rec.Code, rec.Body.String())
	}
	return got
}

func deviceFromRequest(t testing.TB, e *testEnv, token string) *device {
	return deviceFromHeader(t, e, "Bearer "+token)
}

func TestDeviceActivityIsRecordedAtMostOnceAnHour(t *testing.T) {
	e := newEnv(t)
	alice := e.signUp(t, "alice@example.com")
	token, id := e.pairDevice(t, alice, "alices")

	seen := func() *time.Time {
		var at *time.Time
		e.pool.QueryRow(context.Background(), `SELECT last_seen_at FROM devices WHERE id = $1`, id).Scan(&at)
		return at
	}
	if seen() != nil {
		t.Fatal("a device that never called the API has a last-seen time")
	}
	deviceFromRequest(t, e, token)
	first := seen()
	if first == nil {
		t.Fatal("the first call was not recorded")
	}
	e.advance(30 * time.Minute)
	deviceFromRequest(t, e, token)
	if got := seen(); !got.Equal(*first) {
		t.Fatalf("recorded again within the hour: %v then %v", first, got)
	}
	e.advance(31 * time.Minute)
	deviceFromRequest(t, e, token)
	if got := seen(); !got.After(*first) {
		t.Fatalf("not recorded after the hour: %v then %v", first, got)
	}
}

func TestExpiredPairingsAreRemoved(t *testing.T) {
	e := newEnv(t)
	e.newPairing(t, "")
	e.advance(30 * time.Minute)
	e.newPairing(t, "")

	removed, err := e.db.DeleteExpiredPairings(context.Background(), e.server.now())
	if err != nil || removed != 1 {
		t.Fatalf("removed %d, %v", removed, err)
	}
	if n := e.count(t, `SELECT count(*) FROM device_pairings`); n != 1 {
		t.Fatalf("%d pairings left", n)
	}
}

// scriptedUserCodes returns a generator that yields the given canonical codes in turn, then random ones.
func scriptedUserCodes(canonical ...string) func() (string, string, error) {
	var mu sync.Mutex
	return func() (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(canonical) == 0 {
			return auth.NewUserCode()
		}
		c := canonical[0]
		canonical = canonical[1:]
		return auth.DisplayUserCode(c), c, nil
	}
}

func TestACollidingUserCodeIsRedrawn(t *testing.T) {
	e := newEnv(t)
	first := e.newPairing(t, "")
	taken, _ := auth.ParseUserCode(first.userCode)

	// The generator offers the live code twice more before a fresh one.
	e.server.userCodes = scriptedUserCodes(taken, taken, "ZZZZZZZZ")
	rec := e.startPairing("")
	if rec.Code != http.StatusOK || bodyOf(t, rec)["userCode"] != "ZZZZ-ZZZZ" {
		t.Fatalf("after two collisions: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT count(DISTINCT user_code) FROM device_pairings`); n != 2 {
		t.Fatalf("%d distinct codes", n)
	}
}

func TestStartingAPairingGivesUpWhenEveryCodeCollides(t *testing.T) {
	e := newEnv(t)
	first := e.newPairing(t, "")
	taken, _ := auth.ParseUserCode(first.userCode)

	e.server.userCodes = func() (string, string, error) { return auth.DisplayUserCode(taken), taken, nil }
	rec := e.startPairing("")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT count(*) FROM device_pairings`); n != 1 {
		t.Fatalf("%d pairings stored", n)
	}
}
