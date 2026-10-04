package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/audio"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/auth"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/storage"
)

func (e *testEnv) uploadAs(token, messageID string, data []byte, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	if token != "" {
		mods = append(mods, bearer(token))
	}
	return e.call(http.MethodPost, "/v1/messages", asJSON(map[string]any{
		"messageId": messageID, "audio": base64.StdEncoding.EncodeToString(data),
	}), mods...)
}

func (e *testEnv) listMessages(c *http.Cookie, query string) *httptest.ResponseRecorder {
	return e.call(http.MethodGet, "/v1/messages"+query, "", withCookie(c))
}

func (e *testEnv) audioOf(c *http.Cookie, id, query string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	if c != nil {
		mods = append(mods, withCookie(c))
	}
	return e.call(http.MethodGet, "/v1/messages/"+id+"/audio"+query, "", mods...)
}

func (e *testEnv) storedNames(t testing.TB) []string {
	t.Helper()
	entries, err := e.messages.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

func fileBytes(t testing.TB, e *testEnv, name string) []byte {
	t.Helper()
	f, err := e.messages.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(f); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var storedName = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.(opus|wav)$`)

func TestUploadStoresTheRecordingForTheDevicesOwner(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, deviceID := e.pairDevice(t, owner, "Alice's watch")
	ogg := fixture(t, "tone.ogg")
	wav, err := audio.ToWAV(ogg)
	if err != nil {
		t.Fatal(err)
	}

	rec := e.uploadAs(token, "voice_1791004922779.opus", ogg)
	got := bodyOf(t, rec)
	if rec.Code != http.StatusOK || got["ok"] != true || got["duplicate"] != false || got["messageId"] != "voice_1791004922779.opus" ||
		got["bytesReceived"] != float64(len(ogg)) || got["receivedAt"] == nil {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body.String())
	}
	id := got["id"].(string)

	// Both files are there under the message's id, and nothing else is.
	if !bytes.Equal(fileBytes(t, e, id+".opus"), ogg) || !bytes.Equal(fileBytes(t, e, id+".wav"), wav) {
		t.Fatal("the stored files differ from what was sent and what the converter makes")
	}
	if names := e.storedNames(t); len(names) != 2 {
		t.Fatalf("stored files: %v", names)
	}

	// The row says whose it is, which device sent it, and how long it is.
	var userID, devID, format string
	var durationMs *int32
	var hasWav bool
	if err := e.pool.QueryRow(context.Background(), `SELECT user_id::text, device_id::text, format, duration_ms, has_wav FROM messages WHERE id = $1`, id).
		Scan(&userID, &devID, &format, &durationMs, &hasWav); err != nil {
		t.Fatal(err)
	}
	if userID != mustUser(t, e, "alice@example.com").ID.String() || devID != deviceID || format != "ogg-opus" || !hasWav ||
		durationMs == nil || int(*durationMs) != (len(wav)-44)/32 {
		t.Fatalf("row: user=%s device=%s format=%s duration=%v wav=%v", userID, devID, format, durationMs, hasWav)
	}

	// The owner sees it in their list.
	list := bodyOf(t, e.listMessages(owner, ""))
	messages := list["messages"].([]any)
	if len(messages) != 1 || list["nextCursor"] != nil {
		t.Fatalf("list: %v", list)
	}
	m := messages[0].(map[string]any)
	if m["id"] != id || m["messageId"] != "voice_1791004922779.opus" || m["deviceId"] != deviceID || m["deviceName"] != "Alice's watch" ||
		m["bytesReceived"] != float64(len(ogg)) || m["durationMs"] != float64((len(wav)-44)/32) || m["hasWav"] != true {
		t.Fatalf("list entry: %v", m)
	}
}

func TestUploadNeedsAValidDeviceToken(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, deviceID := e.pairDevice(t, owner, "watch")
	ogg := fixture(t, "tone.ogg")
	e.call(http.MethodDelete, "/v1/devices/"+deviceID, "", withCookie(owner))

	other, _, _ := newDeviceTokenForTest()
	for name, mods := range map[string][]func(*http.Request){
		"no credentials":        nil,
		"a session cookie only": {withCookie(owner)},
		"an unknown token":      {bearer(other)},
		"a garbage token":       {withHeader("Authorization", "Bearer nonsense")},
		"a revoked device":      {bearer(token)},
	} {
		rec := e.uploadAs("", "x", ogg, mods...)
		if rec.Code != http.StatusUnauthorized || codeOf(t, rec) != "unauthorized" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if names := e.storedNames(t); len(names) != 0 || e.count(t, `SELECT count(*) FROM messages`) != 0 {
		t.Fatalf("refused uploads left %v behind", names)
	}
}

func TestUploadIsIdempotentWithinOneDevice(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	first, _ := e.pairDevice(t, owner, "first")
	second, _ := e.pairDevice(t, owner, "second")
	ogg := fixture(t, "tone.ogg")

	one := bodyOf(t, e.uploadAs(first, "voice_1.opus", ogg))
	again := bodyOf(t, e.uploadAs(first, "voice_1.opus", ogg))
	if again["duplicate"] != true || again["id"] != one["id"] || again["receivedAt"] != one["receivedAt"] || again["bytesReceived"] != one["bytesReceived"] {
		t.Fatalf("a repeat: %v (first: %v)", again, one)
	}
	// Even a different recording under the same name gets the stored result: the name is the key.
	if other := bodyOf(t, e.uploadAs(first, "voice_1.opus", fixture(t, "tone.zepp"))); other["id"] != one["id"] || other["duplicate"] != true {
		t.Fatalf("a different body under the same name: %v", other)
	}
	if n := e.count(t, `SELECT count(*) FROM messages`); n != 1 || len(e.storedNames(t)) != 2 {
		t.Fatalf("%d rows and %d files after repeats", n, len(e.storedNames(t)))
	}

	// Another device may use the same name: it is another message, not a duplicate.
	third := bodyOf(t, e.uploadAs(second, "voice_1.opus", ogg))
	if third["duplicate"] != false || third["id"] == one["id"] {
		t.Fatalf("the same name from another device: %v", third)
	}
	if n := e.count(t, `SELECT count(*) FROM messages`); n != 2 {
		t.Fatalf("%d rows", n)
	}
}

func TestTwoUsersDevicesNeverCollideOnNames(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.signUp(t, "alice@example.com"), e.signUp(t, "bob@example.com")
	aliceToken, _ := e.pairDevice(t, alice, "a")
	bobToken, _ := e.pairDevice(t, bob, "b")
	ogg := fixture(t, "tone.ogg")

	a := bodyOf(t, e.uploadAs(aliceToken, "voice_1.opus", ogg))
	b := bodyOf(t, e.uploadAs(bobToken, "voice_1.opus", ogg))
	if a["duplicate"] != false || b["duplicate"] != false || a["id"] == b["id"] {
		t.Fatalf("alice %v, bob %v", a, b)
	}
}

// gateStore holds every writer of a recording at its first Put until n of them have arrived. Each upload checks
// for a duplicate before it writes anything, so once all n are inside Put every one has passed that check and
// the race is real: only the database's uniqueness can pick the winner.
type gateStore struct {
	storage.Store
	n       int
	mu      sync.Mutex
	arrived int
	open    chan struct{}
}

func newGateStore(inner storage.Store, n int) *gateStore {
	return &gateStore{Store: inner, n: n, open: make(chan struct{})}
}

func (g *gateStore) Put(name string, data []byte) error {
	if strings.HasSuffix(name, ".opus") {
		g.mu.Lock()
		g.arrived++
		if g.arrived == g.n {
			close(g.open)
		}
		g.mu.Unlock()
		select {
		case <-g.open:
		case <-time.After(20 * time.Second):
		}
	}
	return g.Store.Put(name, data)
}

func TestRacingDuplicateUploadsStoreOneMessage(t *testing.T) {
	const n = 8
	var gate *gateStore
	e := newEnvWith(t, func(s *Server) {
		gate = newGateStore(s.Messages, n)
		s.Messages = gate
	})
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	ogg := fixture(t, "tone.ogg")

	ids := make([]string, n)
	statuses := make([]int, n)
	duplicates := make([]bool, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := e.uploadAs(token, "voice_race.opus", ogg)
			statuses[i] = rec.Code
			got := bodyOf(t, rec)
			ids[i], _ = got["id"].(string)
			duplicates[i], _ = got["duplicate"].(bool)
		}()
	}
	wg.Wait()

	if gate.arrived != n {
		t.Fatalf("only %d of %d uploads reached the point of the race", gate.arrived, n)
	}
	winners := 0
	for i := range n {
		if statuses[i] != http.StatusOK || ids[i] != ids[0] {
			t.Fatalf("request %d: %d %q (request 0: %q)", i, statuses[i], ids[i], ids[0])
		}
		if !duplicates[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("%d uploads claim to have stored the message", winners)
	}
	if rows, files := e.count(t, `SELECT count(*) FROM messages`), e.storedNames(t); rows != 1 || len(files) != 2 {
		t.Fatalf("%d rows, files %v: the losers left files behind", rows, files)
	}
}

func TestUploadRefusesMalformedRequests(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	post := func(body string) *httptest.ResponseRecorder {
		return e.call(http.MethodPost, "/v1/messages", body, bearer(token))
	}

	for name, body := range map[string]string{
		"empty body":         "",
		"not JSON":           "{",
		"no fields":          "{}",
		"no audio":           `{"messageId":"a"}`,
		"no messageId":       `{"audio":"AAAA"}`,
		"empty audio":        `{"messageId":"a","audio":""}`,
		"bad base64":         `{"messageId":"a","audio":"***not base64***"}`,
		"messageId too long": asJSON(map[string]any{"messageId": strings.Repeat("x", 256), "audio": "AAAA"}),
		"wrong types":        `{"messageId":5,"audio":true}`,
	} {
		rec := post(body)
		if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "invalid_request" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if names := e.storedNames(t); len(names) != 0 || e.count(t, `SELECT count(*) FROM messages`) != 0 {
		t.Fatalf("refused uploads left %v behind", names)
	}

	// 255 characters is the longest name; base64 with line breaks is tolerated, as the prototype tolerated it.
	if rec := e.uploadAs(token, strings.Repeat("x", 255), fixture(t, "tone.ogg")); rec.Code != http.StatusOK {
		t.Errorf("255-character name: %d %s", rec.Code, rec.Body.String())
	}
	wrapped := regexp.MustCompile(`(.{76})`).ReplaceAllString(base64.StdEncoding.EncodeToString(fixture(t, "tone.zepp")), "$1\r\n")
	if rec := post(asJSON(map[string]any{"messageId": "wrapped", "audio": wrapped})); rec.Code != http.StatusOK {
		t.Errorf("wrapped base64: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUploadTooLargeIsRefused(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")

	rec := e.uploadAs(token, "huge", bytes.Repeat([]byte{1}, MaxBodyBytes)) // base64 makes it a third larger than the limit
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d", rec.Code)
	}
	if names := e.storedNames(t); len(names) != 0 {
		t.Fatalf("an oversized upload left %v", names)
	}
}

func TestARecordingThatCannotBeConvertedIsStillKept(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	junk := []byte("this is not audio at all, just some bytes")

	rec := e.uploadAs(token, "junk", junk)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	id := bodyOf(t, rec)["id"].(string)
	if names := e.storedNames(t); len(names) != 1 || names[0] != id+".opus" {
		t.Fatalf("stored files: %v", names)
	}

	m := bodyOf(t, e.listMessages(owner, ""))["messages"].([]any)[0].(map[string]any)
	if m["hasWav"] != false || m["durationMs"] != nil {
		t.Fatalf("list entry: %v", m)
	}
	// The original is what plays by default, and it is honest about not being something a browser can play.
	res := e.audioOf(owner, id, "")
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), junk) || res.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("default audio: %d %s %q", res.Code, res.Header().Get("Content-Type"), res.Body.String())
	}
	if res := e.audioOf(owner, id, "?format=wav"); res.Code != http.StatusNotFound || codeOf(t, res) != "not_found" {
		t.Fatalf("wav of an unconvertible message: %d %s", res.Code, res.Body.String())
	}
}

func TestTheOlderZeppFormatIsAcceptedAndConverted(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	zepp := fixture(t, "tone.zepp")

	id := bodyOf(t, e.uploadAs(token, "old-style", zepp))["id"].(string)
	var format string
	var hasWav bool
	e.pool.QueryRow(context.Background(), `SELECT format, has_wav FROM messages WHERE id = $1`, id).Scan(&format, &hasWav)
	if format != "zepp" || !hasWav {
		t.Fatalf("format %q, has_wav %v", format, hasWav)
	}
	// Played, it comes out as WAV; the original is a Zepp file that browsers cannot play.
	if res := e.audioOf(owner, id, ""); res.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("default audio: %s", res.Header().Get("Content-Type"))
	}
	if res := e.audioOf(owner, id, "?format=original"); res.Header().Get("Content-Type") != "application/octet-stream" || !bytes.Equal(res.Body.Bytes(), zepp) {
		t.Fatalf("original: %s", res.Header().Get("Content-Type"))
	}
}

func TestAudioIsServedToItsOwnerWithRanges(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	ogg := fixture(t, "tone.ogg")
	wav, _ := audio.ToWAV(ogg)
	id := bodyOf(t, e.uploadAs(token, "tone", ogg))["id"].(string)

	res := e.audioOf(owner, id, "")
	h := res.Header()
	if res.Code != http.StatusOK || h.Get("Content-Type") != "audio/wav" || !bytes.Equal(res.Body.Bytes(), wav) {
		t.Fatalf("default: %d %s", res.Code, h.Get("Content-Type"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Cache-Control"), "private") ||
		!regexp.MustCompile(`^inline; filename="PigeonDrop-\d{4}-\d\d-\d\d_\d\d-\d\d-\d\d\.wav"$`).MatchString(h.Get("Content-Disposition")) {
		t.Fatalf("headers: %v", h)
	}
	if res := e.audioOf(owner, id, "?format=wav"); !bytes.Equal(res.Body.Bytes(), wav) {
		t.Fatal("format=wav differs from the default")
	}
	orig := e.audioOf(owner, id, "?format=original")
	if orig.Code != http.StatusOK || orig.Header().Get("Content-Type") != "audio/ogg" || !bytes.Equal(orig.Body.Bytes(), ogg) ||
		!strings.HasSuffix(orig.Header().Get("Content-Disposition"), `.opus"`) {
		t.Fatalf("original: %d %s %s", orig.Code, orig.Header().Get("Content-Type"), orig.Header().Get("Content-Disposition"))
	}

	// A browser's player seeks with Range.
	part := e.audioOf(owner, id, "", withHeader("Range", "bytes=44-107"))
	if part.Code != http.StatusPartialContent || !bytes.Equal(part.Body.Bytes(), wav[44:108]) ||
		part.Header().Get("Content-Range") != fmt.Sprintf("bytes 44-107/%d", len(wav)) {
		t.Fatalf("range: %d %q", part.Code, part.Header().Get("Content-Range"))
	}
}

func TestAudioRefusesWhatItShould(t *testing.T) {
	e := newEnv(t)
	alice, mallory := e.signUp(t, "alice@example.com"), e.signUp(t, "mallory@example.com")
	token, _ := e.pairDevice(t, alice, "watch")
	id := bodyOf(t, e.uploadAs(token, "tone", fixture(t, "tone.ogg")))["id"].(string)

	for name, tc := range map[string]struct {
		cookie *http.Cookie
		id     string
		query  string
		status int
		code   string
	}{
		"no session":            {nil, id, "", http.StatusUnauthorized, "unauthorized"},
		"another user's":        {mallory, id, "", http.StatusNotFound, "not_found"},
		"unknown id":            {alice, "00000000-0000-0000-0000-000000000000", "", http.StatusNotFound, "not_found"},
		"malformed id":          {alice, "not-a-uuid", "", http.StatusNotFound, "not_found"},
		"traversal in the id":   {alice, "..%2F..%2Fetc%2Fpasswd", "", http.StatusNotFound, "not_found"},
		"unknown format":        {alice, id, "?format=mp3", http.StatusBadRequest, "invalid_request"},
		"another user's wav":    {mallory, id, "?format=wav", http.StatusNotFound, "not_found"},
		"another user's origin": {mallory, id, "?format=original", http.StatusNotFound, "not_found"},
	} {
		res := e.audioOf(tc.cookie, tc.id, tc.query)
		if res.Code != tc.status || codeOf(t, res) != tc.code {
			t.Errorf("%s: %d %s", name, res.Code, res.Body.String())
		}
	}
	// A device token is not a user: it cannot read recordings back.
	if res := e.audioOf(nil, id, "", bearer(token)); res.Code != http.StatusUnauthorized {
		t.Errorf("device token: %d", res.Code)
	}
}

func TestAMessageIsInvisibleToEveryoneButItsOwner(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.signUp(t, "alice@example.com"), e.signUp(t, "bob@example.com")
	aliceToken, _ := e.pairDevice(t, alice, "a")
	bobToken, _ := e.pairDevice(t, bob, "b")
	ogg := fixture(t, "tone.ogg")
	mine := bodyOf(t, e.uploadAs(aliceToken, "m1", ogg))["id"].(string)
	e.uploadAs(bobToken, "m1", ogg)

	bobsList := bodyOf(t, e.listMessages(bob, ""))["messages"].([]any)
	if len(bobsList) != 1 || bobsList[0].(map[string]any)["id"] == mine || bobsList[0].(map[string]any)["deviceName"] != "b" {
		t.Fatalf("bob's list: %v", bobsList)
	}
	if rec := e.call(http.MethodDelete, "/v1/messages/"+mine, "", withCookie(bob)); rec.Code != http.StatusNotFound {
		t.Fatalf("bob deleting alice's message: %d", rec.Code)
	}
	if n := e.count(t, `SELECT count(*) FROM messages`); n != 2 || len(e.storedNames(t)) != 4 {
		t.Fatalf("a refused delete removed something (%d rows)", n)
	}
	if rec := e.call(http.MethodGet, "/v1/messages", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("list without a session: %d", rec.Code)
	}
}

func TestListIsNewestFirstAndPaged(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	watch, _ := e.pairDevice(t, owner, "watch")
	phoneSide, _ := e.pairDevice(t, owner, "second")
	ogg := fixture(t, "tone.ogg")

	var want []string // newest first
	for i := range 7 {
		token := watch
		if i%2 == 1 {
			token = phoneSide
		}
		name := "voice_" + strconv.Itoa(i)
		e.uploadAs(token, name, ogg)
		want = slices.Insert(want, 0, name)
		e.advance(time.Second)
	}

	var got []string
	query := "?limit=3"
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging does not end")
		}
		page := bodyOf(t, e.listMessages(owner, query))
		for _, m := range page["messages"].([]any) {
			got = append(got, m.(map[string]any)["messageId"].(string))
		}
		next, _ := page["nextCursor"].(string)
		if next == "" {
			if len(page["messages"].([]any)) == 0 && pages == 0 {
				t.Fatal("no messages")
			}
			break
		}
		if len(page["messages"].([]any)) != 3 {
			t.Fatalf("a page with a next cursor has %d messages", len(page["messages"].([]any)))
		}
		query = "?limit=3&before=" + next
	}
	if !slices.Equal(got, want) {
		t.Fatalf("paged order %v, want %v", got, want)
	}

	// A page that ends exactly at the last message has no next cursor.
	if page := bodyOf(t, e.listMessages(owner, "?limit=7")); page["nextCursor"] != nil || len(page["messages"].([]any)) != 7 {
		t.Fatalf("exact page: %v", page["nextCursor"])
	}
	if page := bodyOf(t, e.listMessages(owner, "")); len(page["messages"].([]any)) != 7 {
		t.Fatal("the default page size is too small")
	}
}

func TestMessagesWithTheSameTimeStillPageWithoutLossOrRepeats(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	ogg := fixture(t, "tone.ogg")
	for i := range 6 {
		e.uploadAs(token, "m"+strconv.Itoa(i), ogg)
	}
	// Equal received_at: only the id can order them.
	e.pool.Exec(context.Background(), `UPDATE messages SET received_at = '2026-10-03T12:00:00Z'`)

	seen := map[string]bool{}
	query := "?limit=2"
	for range 4 {
		page := bodyOf(t, e.listMessages(owner, query))
		for _, m := range page["messages"].([]any) {
			id := m.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("%s listed twice", id)
			}
			seen[id] = true
		}
		next, _ := page["nextCursor"].(string)
		if next == "" {
			break
		}
		query = "?limit=2&before=" + next
	}
	if len(seen) != 6 {
		t.Fatalf("%d of 6 messages listed", len(seen))
	}
}

func TestListRefusesBadParameters(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	for name, query := range map[string]string{
		"limit 0":        "?limit=0",
		"limit 101":      "?limit=101",
		"negative limit": "?limit=-1",
		"limit text":     "?limit=many",
		"cursor text":    "?before=hello",
		"cursor garbage": "?before=" + base64.RawURLEncoding.EncodeToString([]byte("not|a cursor")),
		"cursor no id":   "?before=" + base64.RawURLEncoding.EncodeToString([]byte("2026-10-03T12:00:00Z")),
	} {
		rec := e.listMessages(owner, query)
		if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "invalid_request" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestDeletingAMessageRemovesItsRowAndFiles(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	with := bodyOf(t, e.uploadAs(token, "with-wav", fixture(t, "tone.ogg")))["id"].(string)
	without := bodyOf(t, e.uploadAs(token, "without-wav", []byte("not audio")))["id"].(string)
	keep := bodyOf(t, e.uploadAs(token, "keep", fixture(t, "tone.ogg")))["id"].(string)

	for _, id := range []string{with, without} {
		if rec := e.call(http.MethodDelete, "/v1/messages/"+id, "", withCookie(owner)); rec.Code != http.StatusNoContent {
			t.Fatalf("delete %s: %d %s", id, rec.Code, rec.Body.String())
		}
		if res := e.audioOf(owner, id, ""); res.Code != http.StatusNotFound {
			t.Errorf("audio after delete: %d", res.Code)
		}
		if rec := e.call(http.MethodDelete, "/v1/messages/"+id, "", withCookie(owner)); rec.Code != http.StatusNotFound || codeOf(t, rec) != "not_found" {
			t.Errorf("deleting twice: %d %s", rec.Code, rec.Body.String())
		}
	}
	if names := e.storedNames(t); !slices.Equal(names, []string{keep + ".opus", keep + ".wav"}) {
		t.Fatalf("files left: %v", names)
	}
	if n := e.count(t, `SELECT count(*) FROM messages`); n != 1 {
		t.Fatalf("%d rows left", n)
	}
	// With the row gone, the same name can be uploaded again as a new message.
	if again := bodyOf(t, e.uploadAs(token, "with-wav", fixture(t, "tone.ogg"))); again["duplicate"] != false || again["id"] == with {
		t.Fatalf("re-upload after delete: %v", again)
	}
	if rec := e.call(http.MethodDelete, "/v1/messages/"+keep, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("delete without a session: %d", rec.Code)
	}
	if rec := e.call(http.MethodDelete, "/v1/messages/not-a-uuid", "", withCookie(owner)); rec.Code != http.StatusBadRequest {
		t.Fatalf("delete with a malformed id: %d", rec.Code)
	}
}

func TestClientChosenNamesNeverBecomePaths(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	ogg := fixture(t, "tone.ogg")

	for _, name := range []string{"../../etc/passwd", "a/b/c.opus", `..\..\windows`, "voice.opus;.wav", ".", "..", "%2e%2e%2f", "ж"} {
		if rec := e.uploadAs(token, name, ogg); rec.Code != http.StatusOK {
			t.Errorf("%q: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// Control characters (NUL included: PostgreSQL cannot store it) are refused, not turned into an internal error.
	for _, name := range []string{"voice.opus\x00.wav", "line\nbreak", "tab\there", "bell\x07", "\x7f"} {
		if rec := e.uploadAs(token, name, ogg); rec.Code != http.StatusBadRequest || codeOf(t, rec) != "invalid_request" {
			t.Errorf("%q: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	for _, name := range e.storedNames(t) {
		if !storedName.MatchString(name) {
			t.Errorf("a file is named %q, not after a message id", name)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM messages`); n != 8 {
		t.Fatalf("%d messages", n)
	}
}

func TestRevokingADeviceKeepsItsMessagesButEndsItsUploads(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, deviceID := e.pairDevice(t, owner, "lost watch")
	id := bodyOf(t, e.uploadAs(token, "before", fixture(t, "tone.ogg")))["id"].(string)

	e.call(http.MethodDelete, "/v1/devices/"+deviceID, "", withCookie(owner))
	if rec := e.uploadAs(token, "after", fixture(t, "tone.ogg")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("upload after revocation: %d", rec.Code)
	}
	list := bodyOf(t, e.listMessages(owner, ""))["messages"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != id || list[0].(map[string]any)["deviceName"] != "lost watch" {
		t.Fatalf("list after revocation: %v", list)
	}
	if res := e.audioOf(owner, id, ""); res.Code != http.StatusOK {
		t.Fatalf("audio after revocation: %d", res.Code)
	}
}

func TestAFailedInsertLeavesNoFilesBehind(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")

	// The look-up before the upload still works, but every insert is refused (a check no row can pass; NOT VALID
	// skips the existing rows and still applies to new ones): a database error that is not a duplicate.
	if _, err := e.pool.Exec(context.Background(), `ALTER TABLE messages ADD CONSTRAINT never CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	rec := e.uploadAs(token, "x", fixture(t, "tone.ogg"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if names := e.storedNames(t); len(names) != 0 {
		t.Fatalf("a failed upload left %v behind", names)
	}
}

// newDeviceTokenForTest returns a well-formed token no device owns.
func newDeviceTokenForTest() (token string, hash []byte, err error) { return auth.NewDeviceToken() }
