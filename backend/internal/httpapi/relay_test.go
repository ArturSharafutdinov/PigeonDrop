package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// ---- helpers ---------------------------------------------------------------------------------

// envelopeOf makes a well-formed envelope (the server only checks its shape, it has no key). The ciphertext starts with
// the marker, so a test can look for it everywhere. ct is 48 bytes, tag is 32 bytes.
func envelopeOf(marker string) (ct, tag string) {
	ctBytes := []byte(marker + strings.Repeat("c", 48))[:48]
	tagBytes := []byte(marker + strings.Repeat("t", 32))[:32]
	enc := base64.RawURLEncoding
	return enc.EncodeToString(ctBytes), enc.EncodeToString(tagBytes)
}

func relayBody(userCode string, v int, ct, tag string) string {
	return asJSON(map[string]any{"userCode": userCode, "envelope": map[string]any{"v": v, "ct": ct, "tag": tag}})
}

func (e *testEnv) relay(userCode string, ct, tag string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	return e.call(http.MethodPost, "/v1/device/relay", relayBody(userCode, 1, ct, tag), mods...)
}

// dumpDatabase is every row of every table, as text. Whatever must not be stored cannot be in it.
func (e *testEnv) dumpDatabase(t testing.TB) string {
	t.Helper()
	ctx := context.Background()
	rows, err := e.pool.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if len(tables) < 5 { // the schema has far more; fewer means the query above found nothing useful
		t.Fatalf("found only the tables %v", tables)
	}
	var out strings.Builder
	for _, name := range tables {
		rs, err := e.pool.Query(ctx, `SELECT t::text FROM `+pgx.Identifier{name}.Sanitize()+` t`)
		if err != nil {
			t.Fatal(err)
		}
		for rs.Next() {
			var line string
			if err := rs.Scan(&line); err != nil {
				t.Fatal(err)
			}
			out.WriteString(name + ": " + line + "\n")
		}
		rs.Close()
	}
	return out.String()
}

// ---- delivery --------------------------------------------------------------------------------

func TestRelayedSettingsReachTheWatchExactlyOnceAndClosePairing(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	p := e.newPairing(t, "Amazfit Balance")
	ct, tag := envelopeOf("delivery-")

	// Before anything is handed over, a poll is the ordinary pending answer.
	e.advance(10 * time.Second)
	if b := bodyOf(t, e.poll(p.deviceCode)); b["status"] != "pending" || b["envelope"] != nil {
		t.Fatalf("pending answer: %v", b)
	}

	// The phone's browser hands the envelope over. The code may be typed any way (case, dash).
	if rec := e.relay(strings.ToLower(p.userCode), ct, tag); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("relay: %d %s", rec.Code, rec.Body.String())
	}
	if e.server.relays.size() != 1 {
		t.Fatalf("the store holds %d envelopes", e.server.relays.size())
	}

	// The watch's next poll gets it, exactly as it was sent.
	e.advance(10 * time.Second)
	rec := e.poll(p.deviceCode)
	b := bodyOf(t, rec)
	env, _ := b["envelope"].(map[string]any)
	if rec.Code != http.StatusOK || b["status"] != "relayed" || env["v"] != float64(1) || env["ct"] != ct || env["tag"] != tag ||
		b["deviceToken"] != nil || b["deviceId"] != nil {
		t.Fatalf("relayed answer: %d %s", rec.Code, rec.Body.String())
	}
	if e.server.relays.size() != 0 {
		t.Fatal("the envelope is still in memory after it was delivered")
	}

	// Once: a second poll finds the pairing closed, and the code cannot be approved or relayed to any more.
	e.advance(10 * time.Second)
	again := e.poll(p.deviceCode)
	if again.Code != http.StatusBadRequest || codeOf(t, again) != "invalid_device_code" || strings.Contains(again.Body.String(), ct) {
		t.Fatalf("second poll: %d %s", again.Code, again.Body.String())
	}
	if rec := e.approve(owner, p.userCode, ""); rec.Code != http.StatusNotFound || codeOf(t, rec) != "invalid_user_code" {
		t.Fatalf("approve after delivery: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.relay(p.userCode, ct, tag); rec.Code != http.StatusNotFound || codeOf(t, rec) != "invalid_user_code" {
		t.Fatalf("relay after delivery: %d %s", rec.Code, rec.Body.String())
	}
	if e.count(t, `SELECT count(*) FROM device_pairings`) != 0 || e.count(t, `SELECT count(*) FROM devices`) != 0 {
		t.Fatal("delivery left a pairing row behind, or made a device")
	}
}

func TestTheLastEnvelopeSentWins(t *testing.T) {
	e := newEnv(t)
	p := e.newPairing(t, "")
	ct1, tag1 := envelopeOf("first-")
	ct2, tag2 := envelopeOf("second-")

	if rec := e.relay(p.userCode, ct1, tag1); rec.Code != http.StatusNoContent {
		t.Fatalf("first: %d", rec.Code)
	}
	if rec := e.relay(p.userCode, ct2, tag2); rec.Code != http.StatusNoContent {
		t.Fatalf("second: %d", rec.Code)
	}
	if e.server.relays.size() != 1 {
		t.Fatalf("%d envelopes for one pairing", e.server.relays.size())
	}
	e.advance(10 * time.Second)
	env, _ := bodyOf(t, e.poll(p.deviceCode))["envelope"].(map[string]any)
	if env["ct"] != ct2 || env["tag"] != tag2 {
		t.Fatalf("got %v, want the second envelope", env)
	}
}

func TestEnvelopesBelongToTheirOwnPairing(t *testing.T) {
	e := newEnv(t)
	a, b := e.newPairing(t, ""), e.newPairing(t, "")
	ctA, tagA := envelopeOf("for-a-")
	if rec := e.relay(a.userCode, ctA, tagA); rec.Code != http.StatusNoContent {
		t.Fatalf("relay: %d", rec.Code)
	}
	e.advance(10 * time.Second)
	if got := bodyOf(t, e.poll(b.deviceCode)); got["status"] != "pending" || got["envelope"] != nil {
		t.Fatalf("the other watch got: %v", got)
	}
	if env, _ := bodyOf(t, e.poll(a.deviceCode))["envelope"].(map[string]any); env["ct"] != ctA {
		t.Fatalf("the right watch got: %v", env)
	}
}

// An approval can come from anyone who knows the short code; an envelope only from someone who held the QR code. When
// both arrive, the envelope is delivered, and the approval dies with the closed pairing: no device is made.
func TestAnEnvelopeWinsOverAnApproval(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	p := e.newPairing(t, "")
	ct, tag := envelopeOf("wins-")
	e.relay(p.userCode, ct, tag)
	if rec := e.approve(owner, p.userCode, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	e.advance(10 * time.Second)
	if b := bodyOf(t, e.poll(p.deviceCode)); b["status"] != "relayed" {
		t.Fatalf("poll: %v", b)
	}
	e.advance(10 * time.Second)
	if rec := e.poll(p.deviceCode); rec.Code != http.StatusBadRequest || codeOf(t, rec) != "invalid_device_code" {
		t.Fatalf("second poll: %d %s", rec.Code, rec.Body.String())
	}
	if e.count(t, `SELECT count(*) FROM devices`) != 0 {
		t.Fatal("a device was made")
	}
}

// Polls that arrive together: the delete of the pairing's row lets exactly one of them have the envelope.
func TestOnlyOneOfSeveralRacingPollsGetsTheEnvelope(t *testing.T) {
	e := newEnv(t)
	p := e.newPairing(t, "")
	ct, tag := envelopeOf("race-")
	e.relay(p.userCode, ct, tag)
	e.advance(10 * time.Second)

	const polls = 8
	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, polls)
	start := make(chan struct{})
	for i := range polls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = e.poll(p.deviceCode)
		}()
	}
	close(start)
	wg.Wait()

	delivered := 0
	for _, rec := range results {
		switch {
		case rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), ct):
			delivered++
		case rec.Code == http.StatusBadRequest && codeOf(t, rec) == "invalid_device_code":
		default:
			t.Errorf("unexpected answer %d %s", rec.Code, rec.Body.String())
		}
	}
	if delivered != 1 {
		t.Fatalf("%d polls got the envelope", delivered)
	}
}

// A poll that read the pairing before another poll closed it: its delete finds no row and it must not hand out the
// envelope (the other poll has it). The wrapper below makes that happen every time.
type closedByAnotherPoll struct{ dbgen.Querier }

func (c closedByAnotherPoll) DeletePairing(ctx context.Context, id uuid.UUID) (int64, error) {
	if _, err := c.Querier.DeletePairing(ctx, id); err != nil {
		return 0, err
	}
	return c.Querier.DeletePairing(ctx, id) // the second delete finds nothing, as the loser of a race does
}

func TestAPollThatLostTheRaceDoesNotGetTheEnvelope(t *testing.T) {
	e := newEnv(t)
	p := e.newPairing(t, "")
	ct, tag := envelopeOf("lost-race-")
	e.relay(p.userCode, ct, tag)
	e.server.DB = closedByAnotherPoll{e.server.DB}
	e.advance(10 * time.Second)

	rec := e.poll(p.deviceCode)
	if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "invalid_device_code" || strings.Contains(rec.Body.String(), ct) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// ---- refusals --------------------------------------------------------------------------------

func TestRelayRefusesCodesThatAreNotOpen(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	ct, tag := envelopeOf("refuse-")

	approved := e.newPairing(t, "")
	e.approve(owner, approved.userCode, "")
	expired := e.newPairing(t, "")
	e.advance(11 * time.Minute) // past the life of a pairing; the two above are over, too

	for name, code := range map[string]string{
		"unknown": "BCDF-GHJK", "not a code": "hello", "empty": "", "approved": approved.userCode, "expired": expired.userCode,
	} {
		rec := e.relay(code, ct, tag, fromIP("192.0.2.7"))
		if rec.Code != http.StatusNotFound || codeOf(t, rec) != "invalid_user_code" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if e.server.relays.size() != 0 {
		t.Fatal("an envelope for a code that is not open was kept")
	}
}

func TestRelayRefusesAnEnvelopeOfTheWrongShape(t *testing.T) {
	e := newEnv(t)
	p := e.newPairing(t, "")
	enc := base64.RawURLEncoding
	goodCt, goodTag := envelopeOf("shape-")
	blocks := func(n int) string { return enc.EncodeToString(make([]byte, n)) }

	for name, tc := range map[string]struct{ ct, tag string }{
		"ct with padding":             {goodCt + "=", goodTag},
		"ct in standard base64":       {strings.NewReplacer("-", "+", "_", "/").Replace(blocks(48) + "-_"), goodTag},
		"ct not a multiple of 16":     {blocks(47), goodTag},
		"ct one block (no data)":      {blocks(16), goodTag},
		"ct empty":                    {"", goodTag},
		"ct too long":                 {blocks(1040), goodTag},
		"ct not canonical base64":     {strings.TrimSuffix(blocks(32), "A") + "B", goodTag}, // spare bits set
		"tag too short":               {goodCt, enc.EncodeToString(make([]byte, 31))},
		"tag too long":                {goodCt, enc.EncodeToString(make([]byte, 33))},
		"tag empty":                   {goodCt, ""},
		"tag with characters outside": {goodCt, strings.Repeat("!", 43)},
	} {
		rec := e.relay(p.userCode, tc.ct, tc.tag)
		if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "invalid_request" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// The biggest ciphertext allowed is fine.
	if rec := e.relay(p.userCode, blocks(1024), goodTag); rec.Code != http.StatusNoContent {
		t.Errorf("1024 bytes: %d %s", rec.Code, rec.Body.String())
	}

	for name, body := range map[string]string{
		"not JSON":         "hello",
		"an array":         "[]",
		"no body":          "",
		"no envelope":      `{"userCode":"` + p.userCode + `"}`,
		"envelope as text": `{"userCode":"` + p.userCode + `","envelope":"abc"}`,
		"no ct":            `{"userCode":"` + p.userCode + `","envelope":{"v":1,"tag":"` + goodTag + `"}}`,
		"no tag":           `{"userCode":"` + p.userCode + `","envelope":{"v":1,"ct":"` + goodCt + `"}}`,
	} {
		rec := e.call(http.MethodPost, "/v1/device/relay", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestRelayRefusesOtherVersions(t *testing.T) {
	e := newEnv(t)
	p := e.newPairing(t, "")
	ct, tag := envelopeOf("version-")
	for _, v := range []int{0, 2, -1, 99} {
		rec := e.call(http.MethodPost, "/v1/device/relay", relayBody(p.userCode, v, ct, tag))
		if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "unsupported_version" {
			t.Errorf("version %d: %d %s", v, rec.Code, rec.Body.String())
		}
	}
	// A version that is not a number is a malformed request, not a newer format.
	rec := e.call(http.MethodPost, "/v1/device/relay", `{"userCode":"`+p.userCode+`","envelope":{"v":"1","ct":"`+ct+`","tag":"`+tag+`"}}`)
	if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "invalid_request" {
		t.Errorf("version as text: %d %s", rec.Code, rec.Body.String())
	}
	if e.server.relays.size() != 0 {
		t.Fatal("an envelope of another version was kept")
	}
}

func TestRelayRefusesALargeBodyBeforeReadingIt(t *testing.T) {
	e := newEnv(t)
	p := e.newPairing(t, "")
	_, tag := envelopeOf("large-")
	rec := e.relay(p.userCode, strings.Repeat("A", 5000), tag)
	if rec.Code != http.StatusRequestEntityTooLarge || codeOf(t, rec) != "payload_too_large" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestWrongCodesAreLimitedPerAddress(t *testing.T) {
	e := newEnv(t)
	p := e.newPairing(t, "")
	ct, tag := envelopeOf("limit-")
	bad := fromIP("192.0.2.50")

	for i := range 10 {
		if rec := e.relay("BCDF-GHJK", ct, tag, bad); rec.Code != http.StatusNotFound {
			t.Fatalf("guess %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	// The address is blocked, even for the right code ...
	rec := e.relay(p.userCode, ct, tag, bad)
	if rec.Code != http.StatusTooManyRequests || codeOf(t, rec) != "too_many_attempts" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after ten wrong codes: %d %s", rec.Code, rec.Body.String())
	}
	// ... while another address is not, and a right code never counts against anyone.
	if rec := e.relay(p.userCode, ct, tag, fromIP("192.0.2.51")); rec.Code != http.StatusNoContent {
		t.Fatalf("another address: %d %s", rec.Code, rec.Body.String())
	}
	for range 30 {
		if rec := e.relay(p.userCode, ct, tag, fromIP("192.0.2.52")); rec.Code != http.StatusNoContent {
			t.Fatalf("right codes: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestAFullStoreRefusesNewPairingsButNotReplacements(t *testing.T) {
	e := newEnv(t)
	e.server.relays = newRelayStore(1, e.server.now)
	a, b := e.newPairing(t, ""), e.newPairing(t, "")
	ct, tag := envelopeOf("full-")

	if rec := e.relay(a.userCode, ct, tag); rec.Code != http.StatusNoContent {
		t.Fatalf("first: %d", rec.Code)
	}
	rec := e.relay(b.userCode, ct, tag)
	if rec.Code != http.StatusTooManyRequests || codeOf(t, rec) != "too_many_attempts" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("second: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.relay(a.userCode, ct, tag); rec.Code != http.StatusNoContent {
		t.Fatalf("replacing the first: %d", rec.Code)
	}
	// Room again once the watch has collected.
	e.advance(10 * time.Second)
	e.poll(a.deviceCode)
	if rec := e.relay(b.userCode, ct, tag); rec.Code != http.StatusNoContent {
		t.Fatalf("after room was made: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAnEnvelopeNobodyCollectsDiesWithThePairing(t *testing.T) {
	e := newEnv(t)
	p := e.newPairing(t, "")
	ct, tag := envelopeOf("expire-")
	e.relay(p.userCode, ct, tag)

	e.advance(11 * time.Minute)
	rec := e.poll(p.deviceCode)
	if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "expired_token" || strings.Contains(rec.Body.String(), ct) {
		t.Fatalf("poll after the pairing ran out: %d %s", rec.Code, rec.Body.String())
	}
	if e.server.relays.size() != 0 {
		t.Fatal("the envelope outlived its pairing")
	}
}

// ---- nothing is kept -------------------------------------------------------------------------

// The promise of the "Personal server" tab: the encrypted settings are in memory, for as long as the pairing lives,
// and in no database table, no log line and no answer other than the delivery to the watch.
func TestAnEnvelopeIsNeverWrittenToTheDatabaseOrTheLog(t *testing.T) {
	e := newEnv(t)
	p := e.newPairing(t, "Amazfit Balance")
	ct, tag := envelopeOf("UNIQUE-MARKER-4f1c9a-")
	// Also parts of it: a search for the whole string would miss a log that cut it short.
	needles := []string{ct, tag, ct[:24], tag[:24], "UNIQUE-MARKER"}
	needles = append(needles, base64.StdEncoding.EncodeToString([]byte("UNIQUE-MARKER")[:9]))

	check := func(when string) {
		t.Helper()
		dump, logs := e.dumpDatabase(t), e.logs.String()
		for _, needle := range needles {
			if strings.Contains(dump, needle) {
				t.Errorf("%s: the database holds %q", when, needle)
			}
			if strings.Contains(logs, needle) {
				t.Errorf("%s: the log holds %q", when, needle)
			}
		}
	}

	check("before")
	rec := e.relay(p.userCode, ct, tag)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("relay: %d", rec.Code)
	}
	check("while it waits")
	// What the database holds about the pairing is what it held before: nothing says an envelope exists.
	var redeemed, approved *time.Time
	var lastPolled *time.Time
	if err := e.pool.QueryRow(context.Background(), `SELECT redeemed_at, approved_at, last_polled_at FROM device_pairings`).Scan(&redeemed, &approved, &lastPolled); err != nil {
		t.Fatal(err)
	}
	if redeemed != nil || approved != nil || lastPolled != nil {
		t.Fatalf("relaying changed the pairing row: %v %v %v", redeemed, approved, lastPolled)
	}

	// A refused attempt does not echo or log the envelope either.
	e.call(http.MethodPost, "/v1/device/relay", relayBody("BCDF-GHJK", 1, ct, tag), fromIP("192.0.2.77"))
	e.call(http.MethodPost, "/v1/device/relay", relayBody(p.userCode, 2, ct, tag))
	e.call(http.MethodPost, "/v1/device/relay", `{"userCode":"`+p.userCode+`","envelope":{"v":1,"ct":"`+ct+`!","tag":"`+tag+`"}}`)
	check("after refused attempts")

	e.advance(10 * time.Second)
	if delivered := e.poll(p.deviceCode); !strings.Contains(delivered.Body.String(), ct) {
		t.Fatalf("delivery: %d %s", delivered.Code, delivered.Body.String())
	}
	check("after delivery")
	if e.server.relays.size() != 0 {
		t.Fatal("memory still holds the envelope after delivery")
	}

	// And one that is never collected.
	q := e.newPairing(t, "")
	e.relay(q.userCode, ct, tag)
	check("second pairing, waiting")
	e.advance(11 * time.Minute)
	if e.server.relays.size() != 0 {
		t.Fatal("memory still holds the envelope after its time was up")
	}
	check("after expiry")
}

func TestAnEnvelopeCannotBePrintedByAccident(t *testing.T) {
	ct, tag := envelopeOf("print-")
	env := relayEnvelope{ct: ct, tag: tag}
	var logs strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	logger.Info("x", "envelope", env, "pointer", &env)
	logger.Info("y", slog.Any("group", slog.GroupValue(slog.Any("envelope", env))))
	text := slog.New(slog.NewTextHandler(&logs, nil))
	text.Info("z", "envelope", env)

	outputs := []string{
		fmt.Sprint(env), fmt.Sprintf("%v", env), fmt.Sprintf("%+v", env), fmt.Sprintf("%#v", env), fmt.Sprintf("%s", env),
		fmt.Sprintf("%q", env), fmt.Sprint(&env), fmt.Sprintf("%v", []relayEnvelope{env}), fmt.Sprintf("%v", map[string]relayEnvelope{"k": env}),
		fmt.Sprintf("%+v", relayEntry{envelope: env}), fmt.Errorf("failed: %v: %w", env, fmt.Errorf("inner")).Error(), logs.String(),
	}
	for i, out := range outputs {
		if strings.Contains(out, ct) || strings.Contains(out, tag) || strings.Contains(out, ct[:20]) {
			t.Errorf("output %d shows the envelope: %s", i, out)
		}
	}
	if !strings.Contains(logs.String(), "redacted") {
		t.Errorf("the log lines say nothing about the envelope being hidden: %s", logs.String())
	}
}

// ---- the store on its own --------------------------------------------------------------------

func TestRelayStoreHandsOutOnceAndHonoursTime(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := newRelayStore(3, clock)
	id := uuid.New()
	env := relayEnvelope{ct: "c", tag: "t"}

	if store.has(id) {
		t.Fatal("empty store has an envelope")
	}
	if _, ok := store.take(id); ok {
		t.Fatal("empty store handed one out")
	}
	if _, ok := store.put(id, env, now.Add(-time.Second)); !ok || store.size() != 0 {
		t.Fatal("an envelope for a pairing that is over was kept")
	}

	store.put(id, env, now.Add(time.Minute))
	first := store.has(id)
	second := store.has(id) // asking twice: has must not take
	if !first || !second {
		t.Fatal("has must not take")
	}
	got, ok := store.take(id)
	if !ok || got != env || store.has(id) || store.size() != 0 {
		t.Fatalf("take: %v %v", got, ok)
	}
	if _, ok := store.take(id); ok {
		t.Fatal("handed out twice")
	}

	store.put(id, env, now.Add(time.Minute))
	now = now.Add(time.Minute) // exactly its time: gone
	if store.has(id) {
		t.Fatal("has an envelope at its expiry")
	}
	if _, ok := store.take(id); ok {
		t.Fatal("handed out an expired envelope")
	}
}

func TestRelayStoreIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := newRelayStore(2, clock)
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	env := relayEnvelope{ct: "c", tag: "t"}

	store.put(a, env, now.Add(5*time.Minute))
	store.put(b, env, now.Add(2*time.Minute))
	wait, ok := store.put(c, env, now.Add(10*time.Minute))
	if ok || wait != 2*time.Minute {
		t.Fatalf("full store: ok=%v wait=%v, want a refusal that points at the soonest expiry", ok, wait)
	}
	if _, ok := store.put(a, env, now.Add(6*time.Minute)); !ok {
		t.Fatal("replacing must work in a full store")
	}
	now = now.Add(3 * time.Minute) // b has expired: its room is free
	if _, ok := store.put(c, env, now.Add(time.Minute)); !ok {
		t.Fatal("no room made by expiry")
	}
	if store.size() != 2 {
		t.Fatalf("size %d", store.size())
	}
	if _, ok := newRelayStore(1, clock).put(a, env, now.Add(time.Second)); !ok {
		t.Fatal("a store of one refused its first")
	}
}

// With the real clock: the envelope leaves memory by itself when its time is up, even if nobody asks for it.
func TestRelayStoreForgetsOnItsOwn(t *testing.T) {
	store := newRelayStore(10, time.Now)
	gone, replaced := uuid.New(), uuid.New()
	env := relayEnvelope{ct: "c", tag: "t"}

	store.put(gone, env, time.Now().Add(30*time.Millisecond))
	store.put(replaced, env, time.Now().Add(30*time.Millisecond))
	store.put(replaced, env, time.Now().Add(time.Hour)) // the first timer must not delete this one
	time.Sleep(200 * time.Millisecond)

	store.mu.Lock()
	_, goneStill := store.entries[gone]
	_, replacedStill := store.entries[replaced]
	store.mu.Unlock()
	if goneStill {
		t.Fatal("an expired envelope is still in memory")
	}
	if !replacedStill {
		t.Fatal("the timer of a replaced envelope deleted its successor")
	}
}

func TestRelayStoreHandsOutToOneOfManyCallers(t *testing.T) {
	store := newRelayStore(10, time.Now)
	id := uuid.New()
	store.put(id, relayEnvelope{ct: "c", tag: "t"}, time.Now().Add(time.Hour))

	var wg sync.WaitGroup
	var mu sync.Mutex
	got := 0
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := store.take(id); ok {
				mu.Lock()
				got++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if got != 1 {
		t.Fatalf("%d callers got it", got)
	}
}
