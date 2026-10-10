package httpapi

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/transcribe"
)

// recognition turns the speech recognition queue on for a test and counts the times the worker was woken.
func recognition(maxAudio time.Duration, woken *atomic.Int32) func(*Server) {
	return func(s *Server) {
		s.Transcription = &transcribe.Intake{MaxAudio: maxAudio}
		s.TranscriptionWake = func() { woken.Add(1) }
	}
}

type queueRow struct {
	state string
	code  *string
}

func (e *testEnv) queueEntry(t testing.TB, messageID string) (row queueRow, found bool) {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT state, error_code FROM transcriptions WHERE message_id = $1`, messageID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return queueRow{}, false
	}
	if err := rows.Scan(&row.state, &row.code); err != nil {
		t.Fatal(err)
	}
	return row, true
}

func TestAnUploadPutsTheRecordingInTheRecognitionQueueAndWakesTheWorker(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	token, _ := e.pairDevice(t, e.signUp(t, "alice@example.com"), "watch")

	id := e.uploaded(t, token, "voice_1.opus", fixture(t, "tone.ogg"))["id"].(string)

	row, found := e.queueEntry(t, id)
	if !found || row.state != "pending" || row.code != nil {
		t.Fatalf("queue entry: %+v found=%v", row, found)
	}
	if woken.Load() != 1 {
		t.Errorf("the worker was woken %d times, want 1", woken.Load())
	}
}

func TestAnUploadThatIsRepeatedIsNotQueuedAgain(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	token, _ := e.pairDevice(t, e.signUp(t, "alice@example.com"), "watch")

	first := e.uploaded(t, token, "voice_1.opus", fixture(t, "tone.ogg"))
	again := e.uploaded(t, token, "voice_1.opus", fixture(t, "tone.ogg"))

	if again["duplicate"] != true || again["id"] != first["id"] {
		t.Fatalf("second answer: %v", again)
	}
	if n := e.count(t, `SELECT count(*) FROM transcriptions`); n != 1 {
		t.Fatalf("%d queue entries for one recording", n)
	}
	if woken.Load() != 1 {
		t.Errorf("woken %d times: a repeat brought nothing new", woken.Load())
	}
}

func TestARecordingThatCannotBeDecodedIsKeptButNeverSentToTheModel(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	token, _ := e.pairDevice(t, e.signUp(t, "alice@example.com"), "watch")

	id := e.uploaded(t, token, "noise", junk(500, 7))["id"].(string)

	row, found := e.queueEntry(t, id)
	if !found || row.state != "skipped" || row.code == nil || *row.code != transcribe.CodeNotDecodable {
		t.Fatalf("queue entry: %+v found=%v", row, found)
	}
	if n := e.count(t, `SELECT count(*) FROM messages WHERE id = $1`, id); n != 1 {
		t.Fatal("the recording itself was not kept")
	}
}

func TestARecordingLongerThanTheModelIsMeasuredForIsSkipped(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(10*time.Millisecond, &woken)) // the fixture is longer than this
	token, _ := e.pairDevice(t, e.signUp(t, "alice@example.com"), "watch")

	id := e.uploaded(t, token, "long", fixture(t, "tone.ogg"))["id"].(string)

	row, found := e.queueEntry(t, id)
	if !found || row.state != "skipped" || row.code == nil || *row.code != transcribe.CodeTooLong {
		t.Fatalf("queue entry: %+v found=%v", row, found)
	}
}

func TestWithRecognitionOffNothingIsQueuedAndNobodyIsWoken(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, func(s *Server) { s.TranscriptionWake = func() { woken.Add(1) } }) // Transcription stays nil
	token, _ := e.pairDevice(t, e.signUp(t, "alice@example.com"), "watch")

	e.uploaded(t, token, "voice_1.opus", fixture(t, "tone.ogg"))

	if n := e.count(t, `SELECT count(*) FROM transcriptions`); n != 0 {
		t.Fatalf("%d queue entries with recognition off", n)
	}
	if woken.Load() != 0 {
		t.Errorf("woken %d times with recognition off", woken.Load())
	}
}

func TestDeletingARecordingTakesItsPlaceInTheQueueAndItsText(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	owner := e.signUp(t, "alice@example.com")
	token, _ := e.pairDevice(t, owner, "watch")
	id := e.uploaded(t, token, "voice_1.opus", fixture(t, "tone.ogg"))["id"].(string)
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE transcriptions SET state = 'done', text = 'позвони Ивану', finished_at = now() WHERE message_id = $1`, id); err != nil {
		t.Fatal(err)
	}

	if rec := e.call(http.MethodDelete, "/v1/messages/"+id, "", withCookie(owner)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	if _, found := e.queueEntry(t, id); found {
		t.Fatal("the text outlived its recording")
	}
}

func TestARecordingPushedOutByTheAccountLimitTakesItsQueueEntryWithIt(t *testing.T) {
	var woken atomic.Int32
	limits := smallAccount(1000)
	e := newEnvWith(t, func(s *Server) { limits(s); recognition(time.Minute, &woken)(s) })
	token, _ := e.pairDevice(t, e.signUp(t, "alice@example.com"), "watch")

	var ids []string
	for _, name := range []string{"a", "b", "c", "d"} {
		ids = append(ids, e.uploaded(t, token, name, junk(300, 1))["id"].(string)) // the fourth pushes the first out
	}

	if _, found := e.queueEntry(t, ids[0]); found {
		t.Error("the queue entry of a deleted recording is still there")
	}
	for _, id := range ids[1:] {
		if _, found := e.queueEntry(t, id); !found {
			t.Errorf("the entry of %s is missing", id)
		}
	}
}

// If the queue entry cannot be made, the upload fails as a whole: no message, no file, and the watch tries again.
func TestAnUploadThatCannotJoinTheQueueIsRefusedWholeAndLeavesNothingBehind(t *testing.T) {
	var woken atomic.Int32
	e := newEnvWith(t, recognition(time.Minute, &woken))
	token, _ := e.pairDevice(t, e.signUp(t, "alice@example.com"), "watch")
	if _, err := e.pool.Exec(context.Background(), `ALTER TABLE transcriptions RENAME TO transcriptions_away`); err != nil {
		t.Fatal(err)
	}

	rec := e.uploadAs(token, "voice_1.opus", fixture(t, "tone.ogg"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT count(*) FROM messages`); n != 0 {
		t.Errorf("%d messages were kept from a failed upload", n)
	}
	if names := e.storedNames(t); len(names) != 0 {
		t.Errorf("files left behind: %v", names)
	}
	if woken.Load() != 0 {
		t.Error("the worker was woken for nothing")
	}

	// Put the table back: the same upload now goes through, as the watch's retry would.
	if _, err := e.pool.Exec(context.Background(), `ALTER TABLE transcriptions_away RENAME TO transcriptions`); err != nil {
		t.Fatal(err)
	}
	if rec := e.uploadAs(token, "voice_1.opus", fixture(t, "tone.ogg")); rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
}
