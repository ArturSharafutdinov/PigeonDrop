package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// What people see of the speech recognition queue: the transcript of a message, the retry, the texts in the
// archive. The worker is not running in these tests; they put a recording's queue entry in each state by hand and
// check what the API shows and allows.

// setQueue changes the queue entry of a message, bypassing the worker.
func (e *testEnv) setQueue(t testing.TB, id, set string, args ...any) {
	t.Helper()
	tag, err := e.pool.Exec(context.Background(), `UPDATE transcriptions SET `+set+` WHERE message_id = $1`, append([]any{id}, args...)...)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("set %q on %s: %v (rows %d)", set, id, err, tag.RowsAffected())
	}
}

// queueFacts reads what the tests compare: state, attempts, error code and when the entry was queued.
func (e *testEnv) queueFacts(t testing.TB, id string) (state string, attempts int, code, lastError *string, enqueued time.Time) {
	t.Helper()
	err := e.pool.QueryRow(context.Background(),
		`SELECT state, attempts, error_code, last_error, enqueued_at FROM transcriptions WHERE message_id = $1`, id).
		Scan(&state, &attempts, &code, &lastError, &enqueued)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func (e *testEnv) retry(id string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	return e.call(http.MethodPost, "/v1/messages/"+id+"/transcribe", "", mods...)
}

// transcriptFromList is the transcript the list shows for a message, or nil when the message has none.
func transcriptFromList(t testing.TB, e *testEnv, cookie *http.Cookie, id string) map[string]any {
	t.Helper()
	rec := e.listMessages(cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	for _, m := range bodyOf(t, rec)["messages"].([]any) {
		message := m.(map[string]any)
		if message["id"] == id {
			transcript, _ := message["transcript"].(map[string]any)
			return transcript
		}
	}
	t.Fatalf("message %s is not in the list", id)
	return nil
}

func TestTheListShowsEachStateOfTheTranscript(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	ogg := fixture(t, "tone.ogg")
	ids := map[string]string{}
	for _, name := range []string{"pending", "running", "done", "silence", "failed", "skipped"} {
		ids[name] = e.uploaded(t, token, name, ogg)["id"].(string)
	}
	e.setQueue(t, ids["running"], `state = 'running', attempts = 1, locked_until = now() + interval '2 minutes'`)
	e.setQueue(t, ids["done"], `state = 'done', text = 'Позвони Ивану в пять.', engine = 'parakeet-tdt-0.6b-v3-int8', finished_at = '2026-10-10T09:00:00Z'`)
	e.setQueue(t, ids["silence"], `state = 'done', text = '', engine = 'parakeet-tdt-0.6b-v3-int8', finished_at = '2026-10-10T09:01:00Z'`)
	e.setQueue(t, ids["failed"], `state = 'failed', attempts = 5, error_code = 'too_many_failures', last_error = 'the model hung', finished_at = '2026-10-10T09:02:00Z'`)
	e.setQueue(t, ids["skipped"], `state = 'skipped', error_code = 'too_long', finished_at = '2026-10-10T09:03:00Z'`)

	want := map[string]map[string]any{
		"pending": {"state": "pending"},
		"running": {"state": "running"},
		"done": {"state": "done", "text": "Позвони Ивану в пять.", "engine": "parakeet-tdt-0.6b-v3-int8",
			"finishedAt": "2026-10-10T09:00:00Z"},
		"silence": {"state": "done", "text": "", "engine": "parakeet-tdt-0.6b-v3-int8", "finishedAt": "2026-10-10T09:01:00Z"},
		"failed":  {"state": "failed", "errorCode": "too_many_failures", "finishedAt": "2026-10-10T09:02:00Z"},
		"skipped": {"state": "skipped", "errorCode": "too_long", "finishedAt": "2026-10-10T09:03:00Z"},
	}
	for name, expected := range want {
		got := transcriptFromList(t, e, owner, ids[name])
		if len(got) != len(expected) {
			t.Errorf("%s: transcript %v, want %v", name, got, expected)
			continue
		}
		for key, value := range expected {
			if got[key] != value {
				t.Errorf("%s: %s is %v, want %v (all: %v)", name, key, got[key], value, got)
			}
		}
	}
}

func TestTheModelsOwnWordsAboutAFailureStayOnTheServer(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	id := e.uploaded(t, token, "voice", fixture(t, "tone.ogg"))["id"].(string)
	e.setQueue(t, id, `state = 'failed', attempts = 5, error_code = 'rejected', last_error = 'internal path /srv/model/x', finished_at = now()`)

	rec := e.listMessages(owner, "")
	if bytes.Contains(rec.Body.Bytes(), []byte("/srv/model/x")) || bytes.Contains(rec.Body.Bytes(), []byte("last")) {
		t.Fatalf("the list leaks the service's error: %s", rec.Body.String())
	}
}

func TestAMessageThatWasNeverQueuedHasNoTranscript(t *testing.T) {
	// Received while recognition was off; it is on now. Nothing queued it, and nothing shows a state for it.
	var woken atomic.Int32
	e := newEnvWith(t, func(s *Server) { s.TranscriptionWake = func() { woken.Add(1) } })
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	id := e.uploaded(t, token, "old", fixture(t, "tone.ogg"))["id"].(string)

	if got := transcriptFromList(t, e, owner, id); got != nil {
		t.Fatalf("transcript of a recording that was never queued: %v", got)
	}
	if bytes.Contains(e.listMessages(owner, "").Body.Bytes(), []byte("transcript")) {
		t.Error("the field is there although there is nothing to say")
	}
}

func TestTextsStillShowWhenRecognitionIsSwitchedOffLater(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	id := e.uploaded(t, token, "voice", fixture(t, "tone.ogg"))["id"].(string)
	e.setQueue(t, id, `state = 'done', text = 'готово', engine = 'm', finished_at = now()`)
	e.server.Transcription = nil // the operator cleared STT_URL

	if got := transcriptFromList(t, e, owner, id); got["text"] != "готово" {
		t.Fatalf("a text that exists was hidden: %v", got)
	}
}

func TestOnePersonNeverSeesAnothersText(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	alice, mallory := e.signUp(t, "alice@example.com"), e.signUp(t, "mallory@example.com")
	aliceToken, _ := e.pairDevice(t, alice, "watch")
	id := e.uploaded(t, aliceToken, "voice", fixture(t, "tone.ogg"))["id"].(string)
	e.setQueue(t, id, `state = 'done', text = 'пароль от сейфа 1234', engine = 'm', finished_at = now()`)

	rec := e.listMessages(mallory, "")
	if rec.Code != http.StatusOK || bytes.Contains(rec.Body.Bytes(), []byte("1234")) {
		t.Fatalf("mallory's list: %d %s", rec.Code, rec.Body.String())
	}
}

// --- retry

func TestRetryPutsAFailedRecordingBackAtTheEndOfTheQueue(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	failed := e.uploaded(t, token, "failed", fixture(t, "tone.ogg"))["id"].(string)
	later := e.uploaded(t, token, "later", fixture(t, "tone.ogg"))["id"].(string)
	e.setQueue(t, failed, `state = 'failed', attempts = 5, error_code = 'too_many_failures', last_error = 'hung', started_at = now(), finished_at = now()`)
	_, _, _, _, laterQueued := e.queueFacts(t, later)
	woken.Store(0)

	rec := e.retry(failed, withCookie(owner))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	if got := bodyOf(t, rec); got["state"] != "pending" || got["errorCode"] != nil || got["finishedAt"] != nil {
		t.Errorf("answer: %v", got)
	}
	state, attempts, code, lastError, queued := e.queueFacts(t, failed)
	if state != "pending" || attempts != 0 || code != nil || lastError != nil {
		t.Errorf("entry: %s, %d attempts, code %v, last error %v", state, attempts, code, lastError)
	}
	if !queued.After(laterQueued) {
		t.Errorf("queued at %v, not behind the recording that was waiting (%v)", queued, laterQueued)
	}
	if woken.Load() != 1 {
		t.Errorf("worker woken %d times, want 1", woken.Load())
	}
	if got := transcriptFromList(t, e, owner, failed); got["state"] != "pending" {
		t.Errorf("the list still shows %v", got)
	}
}

func TestOnlyAFailedRecordingCanBeRetried(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")

	for _, set := range []string{
		`state = 'pending'`,
		`state = 'running', attempts = 1, locked_until = now() + interval '2 minutes'`,
		`state = 'done', text = 'готово', engine = 'm', finished_at = now()`,
		`state = 'skipped', error_code = 'not_decodable', finished_at = now()`,
	} {
		id := e.uploaded(t, token, set, fixture(t, "tone.ogg"))["id"].(string)
		e.setQueue(t, id, set)
		before, attemptsBefore, _, _, _ := e.queueFacts(t, id)
		woken.Store(0)

		rec := e.retry(id, withCookie(owner))

		if rec.Code != http.StatusConflict || codeOf(t, rec) != "conflict" {
			t.Errorf("%q: %d %s", set, rec.Code, rec.Body.String())
		}
		if after, attemptsAfter, _, _, _ := e.queueFacts(t, id); after != before || attemptsAfter != attemptsBefore {
			t.Errorf("%q: the entry changed to %s, %d attempts", set, after, attemptsAfter)
		}
		if woken.Load() != 0 {
			t.Errorf("%q: the worker was woken for nothing", set)
		}
	}
}

func TestAnOldRecordingWithoutAQueueEntryCannotBeRetried(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, func(s *Server) { recognition(time.Minute, &woken)(s) })
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	id := e.uploaded(t, token, "old", fixture(t, "tone.ogg"))["id"].(string)
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM transcriptions WHERE message_id = $1`, id); err != nil {
		t.Fatal(err)
	}

	rec := e.retry(id, withCookie(owner))

	if rec.Code != http.StatusConflict || codeOf(t, rec) != "conflict" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if _, found := e.queueEntry(t, id); found {
		t.Error("a retry queued a recording that the operator did not queue")
	}
}

func TestRetryWithRecognitionOffChangesNothing(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	id := e.uploaded(t, token, "voice", fixture(t, "tone.ogg"))["id"].(string)
	e.setQueue(t, id, `state = 'failed', attempts = 5, error_code = 'too_many_failures', finished_at = now()`)
	e.server.Transcription = nil
	woken.Store(0)

	rec := e.retry(id, withCookie(owner))

	if rec.Code != http.StatusConflict || codeOf(t, rec) != "conflict" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if state, _, _, _, _ := e.queueFacts(t, id); state != "failed" {
		t.Errorf("the entry is %s: with nobody to work the queue it would wait for ever", state)
	}
}

func TestRetryIsForTheOwnerOnly(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	alice, mallory := e.signUp(t, "alice@example.com"), e.signUp(t, "mallory@example.com")
	aliceToken, _ := e.pairDevice(t, alice, "watch")
	id := e.uploaded(t, aliceToken, "voice", fixture(t, "tone.ogg"))["id"].(string)
	e.setQueue(t, id, `state = 'failed', attempts = 5, error_code = 'too_many_failures', finished_at = now()`)

	if rec := e.retry(id, withCookie(mallory)); rec.Code != http.StatusNotFound || codeOf(t, rec) != "not_found" {
		t.Errorf("another user: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.retry(id); rec.Code != http.StatusUnauthorized || codeOf(t, rec) != "unauthorized" {
		t.Errorf("no session: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.retry(id, bearer(aliceToken)); rec.Code != http.StatusUnauthorized {
		t.Errorf("a watch's token: %d", rec.Code)
	}
	if rec := e.retry("00000000-0000-4000-8000-000000000000", withCookie(alice)); rec.Code != http.StatusNotFound {
		t.Errorf("no such message: %d", rec.Code)
	}
	if rec := e.retry("not-a-uuid", withCookie(alice)); rec.Code != http.StatusBadRequest {
		t.Errorf("not an id: %d", rec.Code)
	}
	if state, _, _, _, _ := e.queueFacts(t, id); state != "failed" {
		t.Errorf("the entry is %s after refused retries", state)
	}
}

// The handler checks ownership first, but the queries carry the check themselves too, so that a handler written
// later cannot forget it.
func TestTheQueriesCarryTheOwnershipCheckThemselves(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	alice := e.signUp(t, "alice@example.com")
	e.signUp(t, "mallory@example.com")
	aliceToken, _ := e.pairDevice(t, alice, "watch")
	id := e.uploaded(t, aliceToken, "voice", fixture(t, "tone.ogg"))["id"].(string)
	e.setQueue(t, id, `state = 'failed', attempts = 5, error_code = 'too_many_failures', finished_at = now()`)
	var malloryID uuid.UUID
	if err := e.pool.QueryRow(context.Background(), `SELECT id FROM users WHERE email = 'mallory@example.com'`).Scan(&malloryID); err != nil {
		t.Fatal(err)
	}
	messageID := uuid.MustParse(id)

	n, err := e.db.RetryOwnTranscription(context.Background(), dbgen.RetryOwnTranscriptionParams{MessageID: messageID, UserID: malloryID, Now: time.Now()})
	if err != nil || n != 0 {
		t.Errorf("another user's retry changed %d rows (%v)", n, err)
	}
	if _, err := e.db.GetOwnTranscription(context.Background(), dbgen.GetOwnTranscriptionParams{MessageID: messageID, UserID: malloryID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("another user read the transcription: %v", err)
	}
	if state, _, _, _, _ := e.queueFacts(t, id); state != "failed" {
		t.Errorf("the entry is %s", state)
	}
}

func TestRetryNeedsTheSameOriginAsTheOtherActions(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	id := e.uploaded(t, token, "voice", fixture(t, "tone.ogg"))["id"].(string)
	e.setQueue(t, id, `state = 'failed', attempts = 5, error_code = 'too_many_failures', finished_at = now()`)

	rec := e.retry(id, withCookie(owner), withHeader("Origin", "https://evil.example"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("a page of another site: %d %s", rec.Code, rec.Body.String())
	}
	if state, _, _, _, _ := e.queueFacts(t, id); state != "failed" {
		t.Errorf("the entry is %s", state)
	}
}

// --- the archive

func unzip(t testing.TB, data []byte) (names []string, files map[string]*zip.File, contents map[string]string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files, contents = map[string]*zip.File{}, map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, f.Name)
		files[f.Name], contents[f.Name] = f, string(body)
	}
	return
}

func TestTheArchiveHasTheTextNextToTheAudio(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, func(s *Server) {
		recognition(time.Minute, &woken)(s)
		fixed := time.Date(2026, 10, 8, 12, 30, 15, 0, time.UTC)
		s.Now = func() time.Time { return fixed } // all five in one second: the names are numbered
	})
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	ogg := fixture(t, "tone.ogg")
	texts := map[string]string{"one": "Не забудь купить молоко.", "two": "Позвони Ивану в пять.", "three": "Встреча в четверг."}
	for name, text := range texts {
		id := e.uploaded(t, token, name, ogg)["id"].(string)
		e.setQueue(t, id, `state = 'done', text = $2, engine = 'm', finished_at = now()`, text)
	}
	silence := e.uploaded(t, token, "silence", ogg)["id"].(string)
	failed := e.uploaded(t, token, "failed", ogg)["id"].(string)
	e.setQueue(t, silence, `state = 'done', text = '', engine = 'm', finished_at = now()`)
	e.setQueue(t, failed, `state = 'failed', attempts = 5, error_code = 'too_many_failures', finished_at = now()`)

	res := e.archiveOf(owner)

	names, files, contents := unzip(t, res.Body.Bytes())
	var opus, txt []string
	for _, name := range names {
		switch {
		case strings.HasSuffix(name, ".opus"):
			opus = append(opus, name)
		case strings.HasSuffix(name, ".txt"):
			txt = append(txt, name)
		}
	}
	// Five recordings in one second get five numbered names; which one is which depends on their ids, so the check
	// is on the pairing: a text for each of the three that have words (none for the silent and the failed one), every
	// text beside an audio of the same name, and no two files of one name (an unpacker would overwrite one).
	if len(opus) != 5 || len(txt) != 3 || len(names) != len(unique(names)) {
		t.Fatalf("files: %v", names)
	}
	var got []string
	for _, name := range txt {
		stem := strings.TrimSuffix(name, ".txt")
		if _, ok := files[stem+".opus"]; !ok {
			t.Errorf("%q has no audio of the same name beside it: %v", name, names)
		}
		if f := files[name]; f.Method != zip.Deflate || !f.Modified.Equal(time.Date(2026, 10, 8, 12, 30, 15, 0, time.UTC)) {
			t.Errorf("%s: method %d, modified %v", name, f.Method, f.Modified)
		}
		if !strings.HasSuffix(contents[name], "\n") || strings.Count(contents[name], "\n") != 1 {
			t.Errorf("%s is %q: one line, ended by a newline", name, contents[name])
		}
		got = append(got, strings.TrimSuffix(contents[name], "\n"))
	}
	slices.Sort(got)
	want := []string{texts["one"], texts["two"], texts["three"]}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("texts in the archive: %q, want %q", got, want)
	}
	// The audio is untouched.
	for _, name := range opus {
		if !bytes.Equal([]byte(contents[name]), ogg) {
			t.Errorf("%s is not the recording as received", name)
		}
	}
}

func unique(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func TestTheArchiveNeverHoldsAnothersText(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	alice, mallory := e.signUp(t, "alice@example.com"), e.signUp(t, "mallory@example.com")
	aliceToken, _ := e.pairDevice(t, alice, "watch")
	malloryToken, _ := e.pairDevice(t, mallory, "watch")
	secret := e.uploaded(t, aliceToken, "secret", fixture(t, "tone.ogg"))["id"].(string)
	e.setQueue(t, secret, `state = 'done', text = 'секрет Алисы', engine = 'm', finished_at = now()`)
	e.uploaded(t, malloryToken, "mine", fixture(t, "tone.ogg"))

	names, _, contents := unzip(t, e.archiveOf(mallory).Body.Bytes())

	if len(names) != 1 {
		t.Fatalf("files: %v", names)
	}
	for _, body := range contents {
		if bytes.Contains([]byte(body), []byte("секрет")) {
			t.Fatal("another user's text is in the archive")
		}
	}
}

func TestTheArchiveWithoutRecognitionIsAsItWas(t *testing.T) {
	e := newEnv(t)
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	e.uploaded(t, token, "one", fixture(t, "tone.ogg"))

	names, _, _ := unzip(t, e.archiveOf(owner).Body.Bytes())

	if len(names) != 1 || !strings.HasSuffix(names[0], ".opus") {
		t.Fatalf("files: %v", names)
	}
}
