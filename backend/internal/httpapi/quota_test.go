package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/audio"
)

// Limits for the tests that fill an account: small enough to fill with a few bytes, with the real 90% warning.
func smallAccount(accountBytes int64) func(*Server) {
	return func(s *Server) {
		s.Storage = StorageLimits{MaxAudioBytes: 512 << 10, AccountBytes: accountBytes, WarnPercent: 90}
	}
}

// junk is a recording of n bytes that is not audio: it is kept (and counted) all the same.
func junk(n int, fill byte) []byte { return bytes.Repeat([]byte{fill}, n) }

// uploaded uploads data under name and returns the answer, failing the test unless it was accepted.
func (e *testEnv) uploaded(t testing.TB, token, name string, data []byte) map[string]any {
	t.Helper()
	rec := e.uploadAs(token, name, data)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload %s: %d %s", name, rec.Code, rec.Body.String())
	}
	return bodyOf(t, rec)
}

func storageOf(t testing.TB, answer map[string]any) (used, limit float64, state string) {
	t.Helper()
	st, ok := answer["storage"].(map[string]any)
	if !ok {
		t.Fatalf("no storage in %v", answer)
	}
	return st["usedBytes"].(float64), st["limitBytes"].(float64), st["state"].(string)
}

func (e *testEnv) messageIDs(t testing.TB) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT client_message_id FROM messages ORDER BY received_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func TestTheDefaultLimitsAreWhatTheProductPromises(t *testing.T) {
	if DefaultStorageLimits.MaxAudioBytes != 512*1024 || DefaultStorageLimits.AccountBytes != 100*1024*1024 || DefaultStorageLimits.WarnPercent != 90 {
		t.Fatalf("defaults: %+v", DefaultStorageLimits)
	}
	// A server built without settings uses them.
	if e := newEnv(t); e.server.Storage != DefaultStorageLimits {
		t.Fatalf("a new handler runs with %+v", e.server.Storage)
	}
}

func TestARecordingOverTheSizeLimitIsRefusedAndOneAtItIsKept(t *testing.T) {
	ogg := fixture(t, "tone.ogg")
	e := newEnvWith(t, func(s *Server) {
		s.Storage = StorageLimits{MaxAudioBytes: len(ogg), AccountBytes: 100 << 20, WarnPercent: 90}
	})
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")

	e.uploaded(t, token, "exactly-the-limit", ogg)

	rec := e.uploadAs(token, "one-byte-over", append(slices.Clone(ogg), 0))
	if rec.Code != http.StatusRequestEntityTooLarge || codeOf(t, rec) != "payload_too_large" {
		t.Fatalf("one byte over: %d %s", rec.Code, rec.Body.String())
	}
	if names := e.messageIDs(t); !slices.Equal(names, []string{"exactly-the-limit"}) || len(e.storedNames(t)) != 1 {
		t.Fatalf("an oversized recording left something: %v, %v", names, e.storedNames(t))
	}
	// A refused recording is not a stored one: sending it again is not a duplicate.
	if rec := e.uploadAs(token, "one-byte-over", append(slices.Clone(ogg), 0)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("again: %d", rec.Code)
	}
}

func TestTheBodyLimitOfAnUploadFitsTheLargestRecordingAndNothingMuchBigger(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	largest := junk(DefaultStorageLimits.MaxAudioBytes, 7)

	// The largest recording gets through as plain base64 and as base64 broken into lines, as older clients sent it.
	e.uploaded(t, token, "largest", largest)
	wrapped := regexp.MustCompile(`(.{76})`).ReplaceAllString(base64.StdEncoding.EncodeToString(largest), "$1\r\n")
	rec := e.call(http.MethodPost, "/v1/messages", asJSON(map[string]any{"messageId": "largest-wrapped", "audio": wrapped}), bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("the largest recording, wrapped: %d %s", rec.Code, rec.Body.String())
	}

	// One byte more is the handler's refusal, still with the code.
	if rec := e.uploadAs(token, "over", append(slices.Clone(largest), 0)); rec.Code != http.StatusRequestEntityTooLarge || codeOf(t, rec) != "payload_too_large" {
		t.Fatalf("one byte over: %d %s", rec.Code, rec.Body.String())
	}
	// A far larger body is refused before it is read in full, with the code as well.
	huge := e.uploadAs(token, "huge", junk(2<<20, 1))
	if huge.Code != http.StatusRequestEntityTooLarge || codeOf(t, huge) != "payload_too_large" {
		t.Fatalf("2 MiB: %d %s", huge.Code, huge.Body.String())
	}
	if n := e.count(t, `SELECT count(*) FROM messages`); n != 2 {
		t.Fatalf("%d messages stored", n)
	}

	// Other routes keep the larger general limit: the tight one is for uploads only. A megabyte of password is
	// refused for what it is, not for its size.
	if got := e.server.uploadBodyLimit(); got >= MaxBodyBytes || got < 700_000 {
		t.Fatalf("upload body limit %d", got)
	}
	login := e.call(http.MethodPost, "/v1/auth/login", asJSON(map[string]any{"email": "alice@example.com", "password": strings.Repeat("x", 1<<20)}))
	if login.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("a login of 1 MiB was refused for its size: %s", login.Body.String())
	}
}

func TestADeviceMayUploadSixtyTimesAnHour(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "busy watch")
	other, _ := e.pairDevice(t, owner, "quiet watch")

	for i := range 60 {
		// Repeats count too: a watch that repeats itself is the thing being limited.
		name := fmt.Sprintf("m%d", i%50)
		if rec := e.uploadAs(token, name, junk(10, byte(i))); rec.Code != http.StatusOK {
			t.Fatalf("upload %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := e.uploadAs(token, "one-too-many", junk(10, 1))
	if rec.Code != http.StatusTooManyRequests || codeOf(t, rec) != "too_many_attempts" {
		t.Fatalf("the 61st: %d %s", rec.Code, rec.Body.String())
	}
	wait, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || wait < 1 || wait > 3600 {
		t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	if n := e.count(t, `SELECT count(*) FROM messages WHERE client_message_id = 'one-too-many'`); n != 0 {
		t.Fatalf("a refused upload was stored")
	}
	if !strings.Contains(e.logs.String(), "uploading too often") {
		t.Error("the limit left no trace in the log")
	}

	// Only that device is held back: another device of the same person, and the person's own pages, carry on.
	if rec := e.uploadAs(other, "fine", junk(10, 1)); rec.Code != http.StatusOK {
		t.Fatalf("another device: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.listMessages(owner, ""); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}

	// An hour later the device may upload again, and gets its recording stored.
	e.advance(time.Hour)
	if rec := e.uploadAs(token, "one-too-many", junk(10, 1)); rec.Code != http.StatusOK {
		t.Fatalf("after an hour: %d %s", rec.Code, rec.Body.String())
	}
}

func TestStorageStatusInAnUploadAnswer(t *testing.T) {
	e := newEnvWith(t, smallAccount(1000))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")

	for _, step := range []struct {
		name  string
		size  int
		used  float64
		state string
	}{
		{"a", 300, 300, "ok"},
		{"b", 300, 600, "ok"},
		{"c", 289, 889, "ok"},         // 88.9%: still below the warning
		{"d", 11, 900, "nearly_full"}, // exactly 90%
		{"e", 100, 1000, "nearly_full"},
	} {
		answer := e.uploaded(t, token, step.name, junk(step.size, 1))
		used, limit, state := storageOf(t, answer)
		if used != step.used || limit != 1000 || state != step.state || answer["pruned"] != float64(0) {
			t.Errorf("%s: used %v limit %v state %s pruned %v", step.name, used, limit, state, answer["pruned"])
		}
	}
}

func TestWhenTheAccountIsFullTheOldestRecordingsGoAndTheNewOneStays(t *testing.T) {
	e := newEnvWith(t, smallAccount(1000))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	bob := e.signUp(t, "bob@example.com")
	bobToken, _ := e.pairDevice(t, bob, "bob's watch")
	bobsID := e.uploaded(t, bobToken, "bobs", junk(900, 9))["id"].(string)

	ids := map[string]string{}
	for _, name := range []string{"a", "b", "c"} {
		ids[name] = e.uploaded(t, token, name, junk(300, 1))["id"].(string)
	}

	// The fourth does not fit (1200 > 1000): the oldest one goes, and the answer says so.
	d := e.uploaded(t, token, "d", junk(300, 1))
	used, _, state := storageOf(t, d)
	if state != "full" || used != 900 || d["pruned"] != float64(1) {
		t.Fatalf("d: used %v state %s pruned %v", used, state, d["pruned"])
	}
	if names := e.messageIDs(t); !slices.Equal(names, []string{"bobs", "b", "c", "d"}) {
		t.Fatalf("after d: %v", names)
	}
	// The deleted recording is gone completely: its row, its file, its audio.
	if names := e.storedNames(t); slices.Contains(names, ids["a"]+".opus") || len(names) != 4 {
		t.Fatalf("files after d: %v", names)
	}
	if res := e.audioOf(owner, ids["a"], "?format=original"); res.Code != http.StatusNotFound {
		t.Fatalf("audio of the deleted one: %d", res.Code)
	}
	// The name is free again: the watch never learns anything is missing, and a new upload under it is a new message.
	if again := e.uploaded(t, token, "a", junk(100, 2)); again["duplicate"] != false {
		t.Fatalf("a deleted recording's name: %v", again)
	}

	// Someone else's recordings are neither deleted nor counted.
	if n := e.count(t, `SELECT count(*) FROM messages WHERE id = '`+bobsID+`'`); n != 1 {
		t.Fatal("another account's recording was deleted")
	}
	bobsUsed, _, _ := storageOf(t, e.uploaded(t, bobToken, "bobs2", junk(50, 9)))
	if bobsUsed != 950 {
		t.Fatalf("bob uses %v", bobsUsed)
	}
}

func TestOneUploadCanMakeRoomByDeletingSeveralRecordings(t *testing.T) {
	e := newEnvWith(t, smallAccount(1000))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	for _, name := range []string{"a", "b", "c"} {
		e.uploaded(t, token, name, junk(300, 1))
	}

	big := e.uploaded(t, token, "big", junk(700, 1)) // room for the others is 300: only c fits beside it
	used, _, state := storageOf(t, big)
	if used != 1000 || state != "full" || big["pruned"] != float64(2) {
		t.Fatalf("used %v state %s pruned %v", used, state, big["pruned"])
	}
	if names := e.messageIDs(t); !slices.Equal(names, []string{"c", "big"}) {
		t.Fatalf("left: %v", names)
	}
	if len(e.storedNames(t)) != 2 {
		t.Fatalf("files: %v", e.storedNames(t))
	}
}

func TestNothingIsDeletedWhileTheRecordingsFit(t *testing.T) {
	e := newEnvWith(t, smallAccount(1000))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")

	e.uploaded(t, token, "a", junk(500, 1))
	b := e.uploaded(t, token, "b", junk(500, 1)) // exactly the limit
	used, _, state := storageOf(t, b)
	if used != 1000 || state != "nearly_full" || b["pruned"] != float64(0) {
		t.Fatalf("at the limit: used %v state %s pruned %v", used, state, b["pruned"])
	}
	if names := e.messageIDs(t); !slices.Equal(names, []string{"a", "b"}) {
		t.Fatalf("left: %v", names)
	}
}

// A message that is "oldest" by the clock but is the one just received must survive: it is the whole point of
// the upload. (A device's clock does not matter, the server's does, but time can still go backwards.)
func TestTheNewRecordingIsKeptEvenWhenOlderOnesLookNewer(t *testing.T) {
	e := newEnvWith(t, smallAccount(1000))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")

	e.uploaded(t, token, "a", junk(600, 1))
	if _, err := e.pool.Exec(context.Background(), `UPDATE messages SET received_at = received_at + interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	b := e.uploaded(t, token, "b", junk(600, 1))
	if b["pruned"] != float64(1) {
		t.Fatalf("pruned %v", b["pruned"])
	}
	if names := e.messageIDs(t); !slices.Equal(names, []string{"b"}) {
		t.Fatalf("left: %v: the new recording must stay", names)
	}
}

func TestARepeatedUploadReportsTheStorageToo(t *testing.T) {
	e := newEnvWith(t, smallAccount(1000))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	e.uploaded(t, token, "a", junk(500, 1))

	// The watch may have lost the answer that said "nearly full"; the repeat says what is true now.
	e.uploaded(t, token, "b", junk(450, 1))
	again := e.uploaded(t, token, "b", junk(450, 1))
	used, limit, state := storageOf(t, again)
	if again["duplicate"] != true || again["pruned"] != float64(0) || used != 950 || limit != 1000 || state != "nearly_full" {
		t.Fatalf("repeat: %v", again)
	}
	if n := e.count(t, `SELECT count(*) FROM messages`); n != 2 {
		t.Fatalf("%d messages", n)
	}
}

func TestUploadsOfOneAccountNeverExceedItsLimitAtTheSameTime(t *testing.T) {
	e := newEnvWith(t, smallAccount(1000))
	owner := e.signUp(t, "alice@example.com")
	var tokens []string
	for i := range 4 {
		token, _ := e.pairDevice(t, owner, fmt.Sprintf("watch %d", i))
		tokens = append(tokens, token)
	}

	// Many devices upload at once: whatever order they land in, the account ends within its limit, every
	// upload is accepted, and the files on disk are exactly the rows that are left.
	var wg sync.WaitGroup
	statuses := make([]int, 24)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = e.uploadAs(tokens[i%len(tokens)], fmt.Sprintf("m%d", i), junk(300, byte(i))).Code
		}()
	}
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Errorf("upload %d: %d", i, status)
		}
	}
	var used int64
	if err := e.pool.QueryRow(context.Background(), `SELECT COALESCE(sum(bytes_received), 0) FROM messages`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	rows := e.count(t, `SELECT count(*) FROM messages`)
	if used > 1000 || rows != 3 {
		t.Fatalf("%d messages take %d bytes of 1000", rows, used)
	}
	if files := e.storedNames(t); len(files) != rows {
		t.Fatalf("%d files for %d rows: %v", len(files), rows, files)
	}
}

func TestStorageEndpointAndDeleteAll(t *testing.T) {
	e := newEnvWith(t, smallAccount(1000))
	alice, bob := e.signUp(t, "alice@example.com"), e.signUp(t, "bob@example.com")
	aliceToken, _ := e.pairDevice(t, alice, "a")
	bobToken, _ := e.pairDevice(t, bob, "b")

	if rec := e.call(http.MethodGet, "/v1/storage", ""); rec.Code != http.StatusUnauthorized || codeOf(t, rec) != "unauthorized" {
		t.Fatalf("storage without a session: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.call(http.MethodDelete, "/v1/messages", ""); rec.Code != http.StatusUnauthorized || codeOf(t, rec) != "unauthorized" {
		t.Fatalf("delete-all without a session: %d %s", rec.Code, rec.Body.String())
	}
	// A device token is not a person's session.
	if rec := e.call(http.MethodDelete, "/v1/messages", "", bearer(aliceToken)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("delete-all with a device token: %d", rec.Code)
	}

	empty := bodyOf(t, e.call(http.MethodGet, "/v1/storage", "", withCookie(alice)))
	if empty["usedBytes"] != float64(0) || empty["limitBytes"] != float64(1000) || empty["state"] != "ok" {
		t.Fatalf("empty: %v", empty)
	}

	e.uploaded(t, aliceToken, "a1", junk(500, 1))
	e.uploaded(t, aliceToken, "a2", junk(450, 1))
	e.uploaded(t, bobToken, "b1", junk(100, 2))

	filled := bodyOf(t, e.call(http.MethodGet, "/v1/storage", "", withCookie(alice)))
	if filled["usedBytes"] != float64(950) || filled["state"] != "nearly_full" {
		t.Fatalf("filled: %v", filled)
	}
	if mine := bodyOf(t, e.call(http.MethodGet, "/v1/storage", "", withCookie(bob))); mine["usedBytes"] != float64(100) || mine["state"] != "ok" {
		t.Fatalf("bob: %v", mine)
	}

	rec := e.call(http.MethodDelete, "/v1/messages", "", withCookie(alice))
	if got := bodyOf(t, rec); rec.Code != http.StatusOK || got["ok"] != true || got["deleted"] != float64(2) {
		t.Fatalf("delete-all: %d %s", rec.Code, rec.Body.String())
	}
	if after := bodyOf(t, e.call(http.MethodGet, "/v1/storage", "", withCookie(alice))); after["usedBytes"] != float64(0) || after["state"] != "ok" {
		t.Fatalf("after: %v", after)
	}
	if list := bodyOf(t, e.listMessages(alice, ""))["messages"].([]any); len(list) != 0 {
		t.Fatalf("alice still sees %d", len(list))
	}
	// Bob's recording, and its file, are untouched; Alice's files are gone with her rows.
	if names := e.messageIDs(t); !slices.Equal(names, []string{"b1"}) || len(e.storedNames(t)) != 1 {
		t.Fatalf("left: %v, %v", names, e.storedNames(t))
	}
	// Emptying an empty storage is fine, and her device still works.
	if got := bodyOf(t, e.call(http.MethodDelete, "/v1/messages", "", withCookie(alice))); got["deleted"] != float64(0) {
		t.Fatalf("again: %v", got)
	}
	e.uploaded(t, aliceToken, "after", junk(10, 1))
}

func TestTheWavIsMadeWhenPlayedAndNeverStored(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	ogg := fixture(t, "tone.ogg")
	wav, err := audio.ToWAV(ogg)
	if err != nil {
		t.Fatal(err)
	}
	id := e.uploaded(t, token, "tone", ogg)["id"].(string)

	for range 2 { // every request makes it again
		if res := e.audioOf(owner, id, "?format=wav"); res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), wav) {
			t.Fatalf("wav: %d", res.Code)
		}
	}
	if names := e.storedNames(t); !slices.Equal(names, []string{id + ".opus"}) {
		t.Fatalf("playing left files behind: %v", names)
	}
}

func TestAConvertedCopyLeftByAnEarlierVersionIsRemovedWithItsMessage(t *testing.T) {
	e := newEnvWith(t, smallAccount(1000))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	old := e.uploaded(t, token, "old", junk(400, 1))["id"].(string)
	// An earlier version stored a WAV next to the original.
	if err := e.messages.Put(old+".wav", junk(900, 5)); err != nil {
		t.Fatal(err)
	}

	// Making room removes it...
	e.uploaded(t, token, "mid", junk(400, 1))
	e.uploaded(t, token, "new", junk(400, 1))
	if names := e.storedNames(t); slices.Contains(names, old+".wav") || slices.Contains(names, old+".opus") || len(names) != 2 {
		t.Fatalf("after pruning: %v", names)
	}

	// ... and so does deleting by hand, one message or all.
	for _, how := range []string{"one", "all"} {
		id := e.uploaded(t, token, "stale-"+how, junk(10, 1))["id"].(string)
		if err := e.messages.Put(id+".wav", junk(50, 5)); err != nil {
			t.Fatal(err)
		}
		if how == "one" {
			e.call(http.MethodDelete, "/v1/messages/"+id, "", withCookie(owner))
		} else {
			e.call(http.MethodDelete, "/v1/messages", "", withCookie(owner))
		}
		for _, name := range e.storedNames(t) {
			if strings.HasSuffix(name, ".wav") {
				t.Errorf("deleting %s left %s", how, name)
			}
		}
	}
}

func TestDecodingWaitsForAFreeSlotAndGivesUpWhenCancelled(t *testing.T) {
	e := newEnv(t)
	var releases []func()
	for range concurrentDecodes {
		release, err := e.server.acquireDecodeSlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}

	// All slots are taken: a further request waits, and a cancelled one stops waiting.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := e.server.acquireDecodeSlot(ctx); err == nil {
		t.Fatal("got a slot although all are taken")
	}

	got := make(chan error, 1)
	go func() {
		release, err := e.server.acquireDecodeSlot(context.Background())
		if err == nil {
			release()
		}
		got <- err
	}()
	select {
	case <-got:
		t.Fatal("did not wait for a slot")
	case <-time.After(30 * time.Millisecond):
	}
	releases[0]()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	for _, release := range releases[1:] {
		release()
	}
}

func TestEveryRowOfAnAccountCountsOnceInItsUsage(t *testing.T) {
	// The usage is the sum of what was received, measured in the database, not a counter that can drift.
	e := newEnvWith(t, smallAccount(1000))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	for i := range 5 {
		e.uploaded(t, token, "m"+strconv.Itoa(i), junk(100+i, 1))
	}
	e.call(http.MethodDelete, "/v1/messages/"+e.firstMessage(t), "", withCookie(owner))

	var sum int64
	e.pool.QueryRow(context.Background(), `SELECT sum(bytes_received) FROM messages`).Scan(&sum)
	if got := bodyOf(t, e.call(http.MethodGet, "/v1/storage", "", withCookie(owner)))["usedBytes"]; got != float64(sum) || sum != 101+102+103+104 {
		t.Fatalf("usage %v, rows sum to %d", got, sum)
	}
}

func (e *testEnv) firstMessage(t testing.TB) string {
	t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(context.Background(), `SELECT id FROM messages ORDER BY received_at, id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id.String()
}
