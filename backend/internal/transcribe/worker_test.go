package transcribe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/audio"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/storage"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/testdb"
)

// --- the test environment: a real database, a real directory, a clock the tests move

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *testClock { return &testClock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *dbgen.Queries
	files  *storage.Disk
	clock  *testClock
	logs   *syncBuffer
	user   uuid.UUID
	device uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	files, err := storage.NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pool: pool, q: dbgen.New(pool), files: files, clock: newClock(), logs: &syncBuffer{}}
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ('alice@example.com', 'x') RETURNING id`).Scan(&e.user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO devices (user_id, name, token_hash) VALUES ($1, 'watch', $2) RETURNING id`, e.user, []byte(uuid.NewString())).Scan(&e.device); err != nil {
		t.Fatal(err)
	}
	return e
}

func tone(t testing.TB) []byte {
	t.Helper()
	data, err := os.ReadFile("../audio/testdata/tone.ogg")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type messageOptions struct {
	durationMs int32
	hasWav     bool
	content    []byte
	writeFile  bool
	queued     bool
	at         time.Time
}

type messageOption func(*messageOptions)

func withDuration(ms int32) messageOption   { return func(o *messageOptions) { o.durationMs = ms } }
func withoutWav() messageOption             { return func(o *messageOptions) { o.hasWav = false } }
func withContent(data []byte) messageOption { return func(o *messageOptions) { o.content = data } }
func withoutFile() messageOption            { return func(o *messageOptions) { o.writeFile = false } }
func notQueued() messageOption              { return func(o *messageOptions) { o.queued = false } }

// message stores a recording the way an upload would: the file, the row and a place in the queue.
func (e *env) message(opts ...messageOption) uuid.UUID {
	e.t.Helper()
	o := messageOptions{durationMs: 1000, hasWav: true, content: tone(e.t), writeFile: true, queued: true, at: e.clock.Now()}
	for _, opt := range opts {
		opt(&o)
	}
	id := uuid.New()
	if o.writeFile {
		if err := e.files.Put(storage.RecordingName(id), o.content); err != nil {
			e.t.Fatal(err)
		}
	}
	_, err := e.pool.Exec(context.Background(), `INSERT INTO messages
		(id, user_id, device_id, client_message_id, format, bytes_received, duration_ms, has_wav, received_at)
		VALUES ($1, $2, $3, $4, 'ogg-opus', $5, $6, $7, $8)`, id, e.user, e.device, id.String(), len(o.content), o.durationMs, o.hasWav, o.at)
	if err != nil {
		e.t.Fatal(err)
	}
	if o.queued {
		if err := e.q.EnqueueTranscription(context.Background(), dbgen.EnqueueTranscriptionParams{MessageID: id, Now: o.at}); err != nil {
			e.t.Fatal(err)
		}
	}
	return id
}

func (e *env) row(id uuid.UUID) dbgen.Transcription {
	e.t.Helper()
	row, err := e.q.GetTranscription(context.Background(), id)
	if err != nil {
		e.t.Fatalf("no queue row for %s: %v", id, err)
	}
	return row
}

func (e *env) worker(svc Transcriber, settings Settings) *Worker {
	w := NewWorker(e.q, e.files, svc, slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), settings)
	w.Now = e.clock.Now
	return w
}

// once runs the worker on the queue until it has dealt with one recording, and fails the test on a queue error.
func (e *env) once(w *Worker) bool {
	e.t.Helper()
	processed, err := w.RunOnce(context.Background())
	if err != nil {
		e.t.Fatalf("RunOnce: %v", err)
	}
	return processed
}

// scripted is a recognition service the tests script: handle gets the number of the call (1, 2, ...).
type scripted struct {
	mu     sync.Mutex
	calls  int
	wavs   [][]byte
	handle func(ctx context.Context, n int, wav []byte) (Result, error)
}

func (s *scripted) Transcribe(ctx context.Context, wav []byte) (Result, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.wavs = append(s.wavs, wav)
	s.mu.Unlock()
	return s.handle(ctx, n, wav)
}

func (s *scripted) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func says(text string) *scripted {
	return &scripted{handle: func(context.Context, int, []byte) (Result, error) {
		return Result{Text: text, Language: "ru", Engine: "fake-1", AudioMs: 1000, SttMs: 42, RequestID: "req-1"}, nil
	}}
}

func failing(err error) *scripted {
	return &scripted{handle: func(context.Context, int, []byte) (Result, error) { return Result{}, err }}
}

func scriptedError(kind Kind, retryAfter time.Duration) error {
	return &Error{Kind: kind, RetryAfter: retryAfter, RequestID: "req-x", Err: errors.New("scripted")}
}

func eventually(t *testing.T, what string, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- joining the queue

func TestAMessageJoinsTheQueueInTheSameTransactionThatStoresIt(t *testing.T) {
	e := newEnv(t)
	intake := Intake{MaxAudio: time.Minute}
	enqueue := func(id uuid.UUID) pgx.Tx {
		tx, err := e.pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := intake.Enqueue(context.Background(), dbgen.New(tx), id, true, ptr(int32(1000)), e.clock.Now()); err != nil {
			t.Fatal(err)
		}
		return tx
	}

	// A rolled-back transaction leaves no queue entry.
	rolledBack := e.message(notQueued())
	if err := enqueue(rolledBack).Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.GetTranscription(context.Background(), rolledBack); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a rolled-back transaction left a queue entry: %v", err)
	}

	// A committed one leaves exactly one, and it cannot be seen before the commit.
	committed := e.message(notQueued())
	tx := enqueue(committed)
	if _, err := e.q.GetTranscription(context.Background(), committed); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("the entry was visible before the commit: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if row := e.row(committed); row.State != StatePending {
		t.Fatalf("state = %s", row.State)
	}
}

func ptr[T any](v T) *T { return &v }

func TestIntakeDecidesWhereANewRecordingGoes(t *testing.T) {
	intake := Intake{MaxAudio: 65 * time.Second}
	tests := []struct {
		name       string
		decodable  bool
		durationMs *int32
		state      string
		code       string
	}{
		{"a normal recording", true, ptr(int32(5000)), StatePending, ""},
		{"exactly the longest", true, ptr(int32(65000)), StatePending, ""},
		{"a millisecond over", true, ptr(int32(65001)), StateSkipped, CodeTooLong},
		{"a server-accepted recording of 3 minutes", true, ptr(int32(180000)), StateSkipped, CodeTooLong},
		{"cannot be decoded", false, nil, StateSkipped, CodeNotDecodable},
		{"cannot be decoded, whatever its length", false, ptr(int32(5000)), StateSkipped, CodeNotDecodable},
		{"length unknown but decodable", true, nil, StatePending, ""},
	}
	e := newEnv(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := e.message(notQueued())
			if err := intake.Enqueue(context.Background(), e.q, id, tt.decodable, tt.durationMs, e.clock.Now()); err != nil {
				t.Fatal(err)
			}
			row := e.row(id)
			if row.State != tt.state {
				t.Fatalf("state = %s, want %s", row.State, tt.state)
			}
			if tt.code == "" && row.ErrorCode != nil || tt.code != "" && (row.ErrorCode == nil || *row.ErrorCode != tt.code) {
				t.Fatalf("error code = %v, want %q", row.ErrorCode, tt.code)
			}
			if tt.state == StateSkipped && row.FinishedAt == nil {
				t.Error("a skipped recording has no finish time")
			}
			// Doing it twice (an upload that ran twice) changes nothing and is not an error.
			if err := intake.Enqueue(context.Background(), e.q, id, tt.decodable, tt.durationMs, e.clock.Now().Add(time.Hour)); err != nil {
				t.Fatalf("second time: %v", err)
			}
			if again := e.row(id); !again.EnqueuedAt.Equal(row.EnqueuedAt) || again.State != row.State {
				t.Errorf("the second time changed the row: %+v -> %+v", row, again)
			}
		})
	}
}

// --- the good path

func TestARecordingIsRecognizedAndItsTextStored(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	svc := &scripted{handle: func(context.Context, int, []byte) (Result, error) {
		return Result{Text: "  позвони\x00 Ивану\r\nПетровичу \t", Language: "ru", Engine: "parakeet-tdt-0.6b-v3-int8", AudioMs: 4880, SttMs: 420, RequestID: "req-7"}, nil
	}}

	if !e.once(e.worker(svc, Settings{})) {
		t.Fatal("nothing was taken")
	}

	row := e.row(id)
	if row.State != StateDone || row.Text == nil || *row.Text != "позвони Ивану\nПетровичу" {
		t.Fatalf("state = %s, text = %v", row.State, row.Text)
	}
	if row.Language == nil || *row.Language != "ru" || row.Engine == nil || *row.Engine != "parakeet-tdt-0.6b-v3-int8" ||
		row.AudioMs == nil || *row.AudioMs != 4880 || row.SttMs == nil || *row.SttMs != 420 {
		t.Fatalf("details: %+v", row)
	}
	if row.Attempts != 1 || row.LockedUntil != nil || row.FinishedAt == nil || row.ErrorCode != nil || row.LastError != nil {
		t.Fatalf("bookkeeping: %+v", row)
	}

	// The model got the WAV of the stored original, nothing else.
	want, err := audio.ToWAV(tone(t))
	if err != nil {
		t.Fatal(err)
	}
	if svc.Calls() != 1 || !bytes.Equal(svc.wavs[0], want) {
		t.Fatalf("the service was not given the recording's WAV (calls: %d)", svc.Calls())
	}
}

func TestSilenceIsDoneWithAnEmptyText(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	e.once(e.worker(says(""), Settings{}))
	row := e.row(id)
	if row.State != StateDone || row.Text == nil || *row.Text != "" {
		t.Fatalf("state = %s, text = %v", row.State, row.Text)
	}
}

func TestLanguageIsNullWhenTheModelDoesNotSayAndATextOfOnlyNoiseIsEmpty(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	svc := &scripted{handle: func(context.Context, int, []byte) (Result, error) {
		return Result{Text: " \x00\t\x01 ", Engine: "fake-1"}, nil
	}}
	e.once(e.worker(svc, Settings{}))
	row := e.row(id)
	if row.Language != nil || row.Text == nil || *row.Text != "" {
		t.Fatalf("language = %v, text = %v", row.Language, row.Text)
	}
}

func TestNothingToDoIsNotAnError(t *testing.T) {
	e := newEnv(t)
	svc := says("x")
	if e.once(e.worker(svc, Settings{})) || svc.Calls() != 0 {
		t.Fatal("took something from an empty queue")
	}
}

func TestTheOldestRecordingGoesFirst(t *testing.T) {
	e := newEnv(t)
	first := e.message()
	e.clock.Advance(time.Second)
	second := e.message()
	e.clock.Advance(time.Second)
	third := e.message()
	w := e.worker(says("x"), Settings{})

	all := []uuid.UUID{first, second, third}
	for step := range all {
		e.once(w)
		for i, id := range all {
			if done := e.row(id).State == StateDone; done != (i <= step) {
				t.Fatalf("after %d step(s): recording %d done = %v", step+1, i+1, done)
			}
		}
	}
}

// --- failures: whose fault it is decides what happens

func TestAServiceThatCannotBeReachedDoesNotUseUpAnAttempt(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	w := e.worker(failing(scriptedError(Unavailable, 0)), Settings{})

	e.once(w)

	row := e.row(id)
	if row.State != StatePending || row.Attempts != 0 || row.LockedUntil != nil {
		t.Fatalf("state = %s, attempts = %d, locked = %v", row.State, row.Attempts, row.LockedUntil)
	}
	if want := e.clock.Now().Add(15 * time.Second); !row.RunAfter.Equal(want) {
		t.Errorf("run_after = %v, want %v", row.RunAfter, want)
	}
	if row.LastError == nil || !strings.Contains(*row.LastError, "unavailable") {
		t.Errorf("last_error = %v", row.LastError)
	}
	if e.once(w) {
		t.Error("the recording was taken again before its pause was over")
	}
}

func TestAServiceThatIsBusyIsWaitedForAndNothingIsCounted(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	retryAfter := time.Duration(0)
	svc := &scripted{handle: func(context.Context, int, []byte) (Result, error) {
		return Result{}, scriptedError(Busy, retryAfter)
	}}
	w := e.worker(svc, Settings{})

	e.once(w) // no hint: the default pause
	if row := e.row(id); row.Attempts != 0 || !row.RunAfter.Equal(e.clock.Now().Add(5*time.Second)) {
		t.Fatalf("no hint: attempts = %d, run_after = %v", row.Attempts, row.RunAfter)
	}
	e.clock.Advance(6 * time.Second)

	retryAfter = 20 * time.Second // the service's hint is longer than the default
	e.once(w)
	if row := e.row(id); row.Attempts != 0 || !row.RunAfter.Equal(e.clock.Now().Add(20*time.Second)) {
		t.Fatalf("hint of 20 s: attempts = %d, run_after = %v", row.Attempts, row.RunAfter)
	}
	e.clock.Advance(21 * time.Second)

	retryAfter = time.Second // a hint shorter than the default does not shorten it
	e.once(w)
	if row := e.row(id); !row.RunAfter.Equal(e.clock.Now().Add(5 * time.Second)) {
		t.Fatalf("hint of 1 s: run_after = %v", row.RunAfter)
	}
}

func TestBusyDoesNotMakeTheWorkerWaitForAnOutage(t *testing.T) {
	e := newEnv(t)
	e.message()
	e.message()
	w := e.worker(failing(scriptedError(Busy, 0)), Settings{})
	e.once(w)
	if !e.once(w) {
		t.Fatal("after a 'busy' the worker should go on to the next recording at once: the service is there")
	}
}

func TestARecordingTheModelDoesNotAnswerIsRetriedWithGrowingPausesThenGivenUpOn(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	svc := failing(scriptedError(Failed, 0))
	w := e.worker(svc, Settings{})

	for attempt := 1; attempt <= 4; attempt++ {
		if !e.once(w) {
			t.Fatalf("attempt %d: nothing was taken", attempt)
		}
		row := e.row(id)
		if row.State != StatePending || int(row.Attempts) != attempt {
			t.Fatalf("attempt %d: state = %s, attempts = %d", attempt, row.State, row.Attempts)
		}
		pause := DefaultBackoff[attempt-1]
		if !row.RunAfter.Equal(e.clock.Now().Add(pause)) {
			t.Fatalf("attempt %d: run_after = %v, want now + %v", attempt, row.RunAfter, pause)
		}
		e.clock.Advance(pause - time.Second)
		if e.once(w) {
			t.Fatalf("attempt %d: taken a second before the pause was over", attempt)
		}
		e.clock.Advance(time.Second)
	}

	e.once(w) // the fifth
	row := e.row(id)
	if row.State != StateFailed || row.ErrorCode == nil || *row.ErrorCode != CodeTooManyFailures || row.Attempts != 5 || row.FinishedAt == nil || row.LockedUntil != nil {
		t.Fatalf("after the fifth: %+v", row)
	}
	e.clock.Advance(24 * time.Hour)
	if e.once(w) || svc.Calls() != 5 {
		t.Fatalf("a failed recording was tried again (calls: %d)", svc.Calls())
	}
}

func TestAServiceThatRefusesTheRecordingEndsItAtOnce(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	svc := failing(scriptedError(Rejected, 0))
	e.once(e.worker(svc, Settings{}))
	row := e.row(id)
	if row.State != StateFailed || row.ErrorCode == nil || *row.ErrorCode != CodeRejected || row.Attempts != 1 {
		t.Fatalf("%+v", row)
	}
	e.clock.Advance(time.Hour)
	if e.once(e.worker(svc, Settings{})) || svc.Calls() != 1 {
		t.Fatal("a refused recording was sent again")
	}
}

func TestAnErrorThatSaysNothingAboutItsKindCountsAgainstTheRecording(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	e.once(e.worker(failing(errors.New("something odd")), Settings{}))
	if row := e.row(id); row.State != StatePending || row.Attempts != 1 {
		t.Fatalf("state = %s, attempts = %d", row.State, row.Attempts)
	}
}

func TestTheNumberOfAttemptsIsASetting(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	w := e.worker(failing(scriptedError(Failed, 0)), Settings{MaxAttempts: 2, Backoff: []time.Duration{time.Second}})
	e.once(w)
	e.clock.Advance(2 * time.Second)
	e.once(w)
	if row := e.row(id); row.State != StateFailed || row.Attempts != 2 {
		t.Fatalf("%+v", row)
	}
}

func TestWhatNeverGoesToTheModel(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name string
		opts []messageOption
		code string
	}{
		{"cannot be decoded when it was received", []messageOption{withoutWav()}, CodeNotDecodable},
		{"too long", []messageOption{withDuration(70000)}, CodeTooLong},
		{"a file that is not a recording", []messageOption{withContent([]byte("not a recording at all"))}, CodeNotDecodable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := e.message(tt.opts...)
			svc := says("never")
			e.once(e.worker(svc, Settings{}))
			row := e.row(id)
			if row.State != StateSkipped || row.ErrorCode == nil || *row.ErrorCode != tt.code || row.FinishedAt == nil {
				t.Fatalf("%+v", row)
			}
			if svc.Calls() != 0 {
				t.Error("the model was asked")
			}
		})
	}
}

func TestAFileThatIsMissingUnderALiveRowFailsTheRecording(t *testing.T) {
	e := newEnv(t)
	id := e.message(withoutFile())
	svc := says("never")
	e.once(e.worker(svc, Settings{}))
	row := e.row(id)
	if row.State != StateFailed || row.ErrorCode == nil || *row.ErrorCode != CodeFileMissing || svc.Calls() != 0 {
		t.Fatalf("%+v (calls %d)", row, svc.Calls())
	}
}

// --- crashes, overlaps and the end of the process

func TestARecordingAWorkerDiedOnIsTakenAgainWhenItsLeaseRunsOut(t *testing.T) {
	e := newEnv(t)
	id := e.message()

	// A worker that took it and was killed: the row stays running.
	claimed, err := e.q.ClaimTranscription(context.Background(), dbgen.ClaimTranscriptionParams{Now: e.clock.Now(), LeaseUntil: e.clock.Now().Add(2 * time.Minute)})
	if err != nil || claimed.MessageID != id || claimed.Attempts != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}

	svc := says("recovered")
	w := e.worker(svc, Settings{})
	e.clock.Advance(2*time.Minute - time.Second)
	if e.once(w) {
		t.Fatal("a recording was taken away from a worker whose lease had not run out")
	}
	e.clock.Advance(2 * time.Second)
	if !e.once(w) {
		t.Fatal("a recording was not taken after its lease ran out")
	}
	row := e.row(id)
	if row.State != StateDone || row.Attempts != 2 {
		t.Fatalf("state = %s, attempts = %d", row.State, row.Attempts)
	}
}

func TestARecordingThatKeepsKillingItsWorkerIsGivenUpOnWithoutAskingTheModel(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE transcriptions SET state = 'running', attempts = 5, started_at = $2, locked_until = $2 WHERE message_id = $1`,
		id, e.clock.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	svc := says("never")
	e.once(e.worker(svc, Settings{}))
	row := e.row(id)
	if row.State != StateFailed || row.ErrorCode == nil || *row.ErrorCode != CodeTooManyFailures || row.Attempts != 6 || svc.Calls() != 0 {
		t.Fatalf("%+v (calls %d)", row, svc.Calls())
	}
}

func TestAResultThatArrivesAfterTheRecordingWasTakenOverIsDiscarded(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	entered, gate := make(chan struct{}), make(chan struct{})
	svc := &scripted{handle: func(_ context.Context, n int, _ []byte) (Result, error) {
		if n == 1 {
			close(entered)
			<-gate // worker A hangs in the model
			return Result{Text: "from A, too late", Engine: "fake"}, nil
		}
		return Result{Text: "from B", Engine: "fake"}, nil
	}}
	a, b := e.worker(svc, Settings{}), e.worker(svc, Settings{})

	finished := make(chan error, 1)
	go func() {
		_, err := a.RunOnce(context.Background())
		finished <- err
	}()
	<-entered
	e.clock.Advance(3 * time.Minute) // A's lease is over
	if !e.once(b) {
		t.Fatal("B did not take over")
	}
	close(gate)
	if err := <-finished; err != nil {
		t.Fatalf("A: %v", err)
	}

	row := e.row(id)
	if row.State != StateDone || row.Text == nil || *row.Text != "from B" || row.Attempts != 2 {
		t.Fatalf("state = %s, text = %v, attempts = %d", row.State, row.Text, row.Attempts)
	}
	if !strings.Contains(e.logs.String(), "discarded") {
		t.Error("A's discarded result left no trace in the log")
	}
}

func TestADeletedRecordingTakesItsQueueEntryAndTheResultIsDropped(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	svc := &scripted{handle: func(ctx context.Context, _ int, _ []byte) (Result, error) {
		// The person deletes the recording (or the account's limit pushes it out) while the model works.
		if _, err := e.pool.Exec(ctx, `DELETE FROM messages WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
		return Result{Text: "nobody wants this", Engine: "fake"}, nil
	}}

	e.once(e.worker(svc, Settings{}))

	if _, err := e.q.GetTranscription(context.Background(), id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("the queue entry outlived its message: %v", err)
	}
	var stray int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM transcriptions`).Scan(&stray); err != nil || stray != 0 {
		t.Fatalf("stray entries: %d %v", stray, err)
	}
}

func TestAMessageDeletedBeforeItsTurnIsNeverTaken(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM messages WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if e.once(e.worker(says("x"), Settings{})) {
		t.Fatal("took a deleted message")
	}
}

func TestStoppingTheServerGivesTheRecordingBackAtOnceAndWithItsAttempt(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	ctx, cancel := context.WithCancel(context.Background())
	svc := &scripted{handle: func(ctx context.Context, _ int, _ []byte) (Result, error) {
		cancel() // SIGTERM while the model works
		<-ctx.Done()
		return Result{}, ctx.Err() // what the client returns when its caller's context ends
	}}

	processed, err := e.worker(svc, Settings{}).RunOnce(ctx)
	if err != nil || !processed {
		t.Fatalf("RunOnce = %v, %v", processed, err)
	}

	row := e.row(id)
	if row.State != StatePending || row.Attempts != 0 || row.LockedUntil != nil || !row.RunAfter.Equal(e.clock.Now()) {
		t.Fatalf("not given back cleanly: %+v", row)
	}
	// The next server takes it at once, no waiting for a lease or a pause.
	next := says("done after the restart")
	if !e.once(e.worker(next, Settings{})) {
		t.Fatal("the recording could not be taken again")
	}
	if row := e.row(id); row.State != StateDone || row.Attempts != 1 {
		t.Fatalf("%+v", row)
	}
}

func TestTwoWorkersNeverTakeTheSameRecording(t *testing.T) {
	e := newEnv(t)
	const count = 40
	ids := make([]uuid.UUID, count)
	for i := range ids {
		ids[i] = e.message()
		e.clock.Advance(time.Millisecond)
	}
	svc := &scripted{handle: func(context.Context, int, []byte) (Result, error) {
		time.Sleep(2 * time.Millisecond) // long enough for the two to overlap
		return Result{Text: "x", Engine: "fake"}, nil
	}}

	var wg sync.WaitGroup
	for range 2 {
		w := e.worker(svc, Settings{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				processed, err := w.RunOnce(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if !processed {
					return
				}
			}
		}()
	}
	wg.Wait()

	if svc.Calls() != count {
		t.Errorf("the model was called %d times for %d recordings", svc.Calls(), count)
	}
	for _, id := range ids {
		if row := e.row(id); row.State != StateDone || row.Attempts != 1 {
			t.Fatalf("%s: state = %s, attempts = %d", id, row.State, row.Attempts)
		}
	}
}

func TestAHundredsLongQueueIsWorkedOffInOrderOneByOne(t *testing.T) {
	e := newEnv(t)
	const count = 200
	ids := make([]uuid.UUID, count)
	for i := range ids {
		ids[i] = e.message()
		e.clock.Advance(time.Millisecond)
	}
	svc := says("x")
	w := e.worker(svc, Settings{})
	for e.once(w) {
		e.clock.Advance(time.Millisecond) // so that the moment each one was taken tells the order
	}
	if svc.Calls() != count {
		t.Fatalf("calls = %d", svc.Calls())
	}
	var last time.Time
	for _, id := range ids {
		row := e.row(id)
		if row.State != StateDone || row.Attempts != 1 {
			t.Fatalf("%s: %+v", id, row)
		}
		if !row.StartedAt.After(last) {
			t.Fatalf("recordings were not taken in the order they arrived")
		}
		last = *row.StartedAt
	}
}

// --- the loop: waiting, waking, the outage pauses

func TestWakingTheWorkerStartsItAtOnceInsteadOfAtTheNextPoll(t *testing.T) {
	e := newEnv(t)
	w := e.worker(says("x"), Settings{Poll: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { w.Run(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped }()

	time.Sleep(100 * time.Millisecond) // the worker has looked at the empty queue and is waiting for an hour
	id := e.message()
	time.Sleep(200 * time.Millisecond)
	if e.row(id).State != StatePending {
		t.Fatal("the worker did not wait for its poll (it should have)")
	}

	w.Wake()
	eventually(t, "the woken worker to recognize the recording", 3*time.Second, func() bool { return e.row(id).State == StateDone })
}

func TestAnIdleWorkerStopsAtOnceWhenTheServerStops(t *testing.T) {
	e := newEnv(t)
	w := e.worker(says("x"), Settings{Poll: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { w.Run(ctx); close(stopped) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestWhileTheServiceIsDownItIsAskedOnceInAWhileNotOncePerRecording(t *testing.T) {
	e := newEnv(t)
	first := e.message()
	e.clock.Advance(time.Millisecond)
	e.message()
	e.clock.Advance(time.Millisecond)
	e.message()

	var up bool
	var mu sync.Mutex
	svc := &scripted{handle: func(context.Context, int, []byte) (Result, error) {
		mu.Lock()
		defer mu.Unlock()
		if up {
			return Result{Text: "x", Engine: "fake"}, nil
		}
		return Result{}, scriptedError(Unavailable, 0)
	}}
	w := e.worker(svc, Settings{Poll: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { w.Run(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped }()

	step := func(wantCalls int, wantPause time.Duration) {
		t.Helper()
		eventually(t, fmt.Sprintf("call %d", wantCalls), 3*time.Second, func() bool { return svc.Calls() >= wantCalls })
		time.Sleep(150 * time.Millisecond) // the other two recordings are waiting, yet nobody asks the service again
		if svc.Calls() != wantCalls {
			t.Fatalf("calls = %d while the service is down, want %d", svc.Calls(), wantCalls)
		}
		row := e.row(first)
		if row.Attempts != 0 || !row.RunAfter.Equal(e.clock.Now().Add(wantPause)) {
			t.Fatalf("attempts = %d, run_after = %v (want now + %v)", row.Attempts, row.RunAfter, wantPause)
		}
		e.clock.Advance(wantPause + time.Second)
	}
	step(1, 15*time.Second) // the pauses double: 15 s, 30 s, 1 minute
	step(2, 30*time.Second)
	step(3, time.Minute)

	mu.Lock()
	up = true
	mu.Unlock()
	eventually(t, "all three recordings to be recognized", 3*time.Second, func() bool {
		var done int
		_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM transcriptions WHERE state = 'done'`).Scan(&done)
		return done == 3
	})
	if !strings.Contains(e.logs.String(), "reachable again") {
		t.Error("the end of the outage left no trace in the log")
	}
	if n := strings.Count(e.logs.String(), "cannot be reached; waiting"); n != 1 {
		t.Errorf("the outage was reported %d times, want once", n)
	}
}

func TestAnOutageThatEndedStartsFromTheShortestPauseNextTime(t *testing.T) {
	var o outage
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	shortest, longest := 15*time.Second, 5*time.Minute
	for i, want := range []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		pause, first := o.failed(now, shortest, longest)
		if pause != want || first != (i == 0) {
			t.Fatalf("failure %d: pause = %v first = %v, want %v", i+1, pause, first, want)
		}
		if !o.blocked(now) || o.blocked(now.Add(want)) {
			t.Fatalf("failure %d: blocked wrongly", i+1)
		}
	}
	if !o.recovered() || o.recovered() {
		t.Fatal("recovered should report an outage once")
	}
	if o.blocked(now) {
		t.Fatal("still blocked after the recovery")
	}
	if pause, first := o.failed(now, shortest, longest); pause != shortest || !first {
		t.Fatalf("a new outage began with %v (first = %v)", pause, first)
	}
}

// --- the log

func TestTheLogNamesTheRecordingButNeverQuotesIt(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	e.once(e.worker(says("номер карты 4242 4242 4242 4242"), Settings{}))
	logs := e.logs.String()
	if !strings.Contains(logs, id.String()) || !strings.Contains(logs, "recording recognized") {
		t.Errorf("the log does not say what happened to %s:\n%s", id, logs)
	}
	if strings.Contains(logs, "4242") || strings.Contains(logs, "номер карты") {
		t.Errorf("the recognized text is in the log:\n%s", logs)
	}
}

// --- small things

func TestSettingsDefaults(t *testing.T) {
	s := Settings{}.withDefaults()
	if s.MaxAttempts != 5 || s.MaxAudio != 65*time.Second || s.Poll != 2*time.Second || s.Lease != 2*time.Minute ||
		len(s.Backoff) != 5 || s.BusyPause != 5*time.Second || s.OutageMin != 15*time.Second || s.OutageMax != 5*time.Minute {
		t.Fatalf("defaults: %+v", s)
	}
	custom := Settings{MaxAttempts: 2, OutageMin: time.Hour}.withDefaults()
	if custom.MaxAttempts != 2 || custom.OutageMax < custom.OutageMin {
		t.Fatalf("custom: %+v", custom)
	}
}

func TestThePauseAfterAFailureGrowsAndThenStays(t *testing.T) {
	backoff := []time.Duration{time.Second, time.Minute, time.Hour}
	for attempt, want := range map[int]time.Duration{0: time.Second, 1: time.Second, 2: time.Minute, 3: time.Hour, 4: time.Hour, 99: time.Hour} {
		if got := pauseAfterFailure(backoff, attempt); got != want {
			t.Errorf("after attempt %d: %v, want %v", attempt, got, want)
		}
	}
	if pauseAfterFailure(nil, 3) != 0 {
		t.Error("no schedule, no pause")
	}
}

func TestClampAndShorten(t *testing.T) {
	if clamp32(-5) != 0 || clamp32(7) != 7 || clamp32(1<<40) != 1<<31-1 {
		t.Error("clamp32")
	}
	long := strings.Repeat("я", 900)
	if got := shorten(long); len([]rune(got)) != 500 || !strings.HasSuffix(got, "…") {
		t.Errorf("shorten: %d runes", len([]rune(got)))
	}
	if shorten("short") != "short" || nonEmpty("") != nil || *nonEmpty("x") != "x" {
		t.Error("shorten or nonEmpty")
	}
}

// A claim holds its row for the length of one statement. A worker that meets a row another worker is claiming at
// that very moment must go on to the next one, not wait: this is what SKIP LOCKED is for.
func TestAWorkerGoesPastARecordingAnotherWorkerIsTakingRightNow(t *testing.T) {
	e := newEnv(t)
	taking := e.message() // the older one: its row is locked by the other worker
	e.clock.Advance(time.Second)
	free := e.message()

	tx, err := e.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SELECT message_id FROM transcriptions WHERE message_id = $1 FOR UPDATE`, taking); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	processed, err := e.worker(says("x"), Settings{}).RunOnce(ctx)
	if err != nil || !processed {
		t.Fatalf("RunOnce = %v, %v: the worker waited for a row another worker holds", processed, err)
	}
	if e.row(free).State != StateDone || e.row(taking).State != StatePending {
		t.Fatalf("free: %s, taking: %s", e.row(free).State, e.row(taking).State)
	}
}

// The dangerous late worker is not the one that comes back after the new owner has finished (a finished row is
// protected by its state), but the one that comes back while the new owner is still working: only the attempt
// number tells the two apart.
func TestAWorkerThatComesBackWhileTheNewOwnerIsStillWorkingChangesNothing(t *testing.T) {
	outcomes := map[string]func() (Result, error){
		"with a text":             func() (Result, error) { return Result{Text: "from A, too late", Engine: "fake"}, nil },
		"unreachable (give back)": func() (Result, error) { return Result{}, scriptedError(Unavailable, 0) },
		"the model failed":        func() (Result, error) { return Result{}, scriptedError(Failed, 0) },
		"refused":                 func() (Result, error) { return Result{}, scriptedError(Rejected, 0) },
	}
	for name, aSays := range outcomes {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			id := e.message()
			firstIn, secondIn, gateA, gateB := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			svc := &scripted{handle: func(_ context.Context, n int, _ []byte) (Result, error) {
				if n == 1 {
					close(firstIn)
					<-gateA
					return aSays()
				}
				close(secondIn)
				<-gateB
				return Result{Text: "from B", Engine: "fake"}, nil
			}}
			a, b := e.worker(svc, Settings{}), e.worker(svc, Settings{})

			aDone, bDone := make(chan error, 1), make(chan error, 1)
			go func() { _, err := a.RunOnce(context.Background()); aDone <- err }()
			<-firstIn
			e.clock.Advance(3 * time.Minute) // A's lease is over
			go func() { _, err := b.RunOnce(context.Background()); bDone <- err }()
			<-secondIn // B has the recording now (attempt 2) and is inside the model

			close(gateA) // A comes back, late
			if err := <-aDone; err != nil {
				t.Fatalf("A: %v", err)
			}
			row := e.row(id)
			if row.State != StateRunning || row.Attempts != 2 || row.LockedUntil == nil {
				t.Fatalf("A's late return changed B's recording: %+v", row)
			}

			close(gateB)
			if err := <-bDone; err != nil {
				t.Fatalf("B: %v", err)
			}
			row = e.row(id)
			if row.State != StateDone || row.Text == nil || *row.Text != "from B" || row.Attempts != 2 {
				t.Fatalf("final: %+v", row)
			}
		})
	}
}

// slowGarbageStore is a store whose first read is slow and returns a file that is not a recording.
type slowGarbageStore struct {
	storage.Store
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
}

type readSeekCloser struct{ *bytes.Reader }

func (readSeekCloser) Close() error { return nil }

func (s *slowGarbageStore) Open(name string) (io.ReadSeekCloser, error) {
	first := false
	s.once.Do(func() { first = true })
	if !first {
		return s.Store.Open(name)
	}
	close(s.entered)
	<-s.gate
	return readSeekCloser{bytes.NewReader([]byte("not a recording"))}, nil
}

// The same for the decision to skip: a worker that was slow before it decided (an old server during a redeploy,
// with other settings) must not skip a recording the new server is working on.
func TestAWorkerThatDecidesToSkipLateChangesNothing(t *testing.T) {
	e := newEnv(t)
	id := e.message()
	slow := &slowGarbageStore{Store: e.files, entered: make(chan struct{}), gate: make(chan struct{})}
	entered, gateB := make(chan struct{}), make(chan struct{})
	model := &scripted{handle: func(context.Context, int, []byte) (Result, error) {
		close(entered)
		<-gateB
		return Result{Text: "from B", Engine: "fake"}, nil
	}}

	a := NewWorker(e.q, slow, says("never"), slog.New(slog.NewTextHandler(e.logs, nil)), Settings{})
	a.Now = e.clock.Now
	b := e.worker(model, Settings{})

	aDone, bDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := a.RunOnce(context.Background()); aDone <- err }()
	<-slow.entered // A has the recording (attempt 1) and is waiting for its file
	e.clock.Advance(3 * time.Minute)
	go func() { _, err := b.RunOnce(context.Background()); bDone <- err }()
	<-entered // B has it now (attempt 2) and is inside the model

	close(slow.gate) // A gets its garbage, decides to skip, too late
	if err := <-aDone; err != nil {
		t.Fatalf("A: %v", err)
	}
	if row := e.row(id); row.State != StateRunning || row.Attempts != 2 {
		t.Fatalf("A's late decision changed B's recording: %+v", row)
	}

	close(gateB)
	if err := <-bDone; err != nil {
		t.Fatalf("B: %v", err)
	}
	if row := e.row(id); row.State != StateDone || row.Text == nil || *row.Text != "from B" {
		t.Fatalf("final: %+v", row)
	}
}
