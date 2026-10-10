package transcribe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/audio"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/storage"
)

// maxFileBytes bounds what is read from a recording's file: uploads are limited to 512 KiB, so anything bigger
// is not a recording this server stored.
const maxFileBytes = 8 << 20

// Settings of a Worker. Zero values mean the defaults (see withDefaults).
type Settings struct {
	// MaxAttempts is how many attempts that count a recording gets; the last failure gives up (default 5).
	MaxAttempts int
	// MaxAudio is the longest recording sent to the model (default 65 s).
	MaxAudio time.Duration
	// Poll is how long an idle worker waits before it looks at the queue again (default 2 s); a new recording wakes
	// it sooner.
	Poll time.Duration
	// Lease is how long a worker may hold a recording before another may take it; it must be longer than a call of
	// the service (default 2 minutes).
	Lease time.Duration
	// Backoff is the pause before the next attempt after each failure that counts (default DefaultBackoff).
	Backoff []time.Duration
	// BusyPause is the wait after "the service is busy" when it does not say how long (default 5 s).
	BusyPause time.Duration
	// OutageMin and OutageMax are the first and the longest pause while the service cannot be reached; the pause
	// doubles with each failed try (defaults 15 s and 5 minutes).
	OutageMin, OutageMax time.Duration
}

func (s Settings) withDefaults() Settings {
	if s.MaxAttempts <= 0 {
		s.MaxAttempts = 5
	}
	if s.MaxAudio <= 0 {
		s.MaxAudio = 65 * time.Second
	}
	if s.Poll <= 0 {
		s.Poll = 2 * time.Second
	}
	if s.Lease <= 0 {
		s.Lease = 2 * time.Minute
	}
	if len(s.Backoff) == 0 {
		s.Backoff = DefaultBackoff
	}
	if s.BusyPause <= 0 {
		s.BusyPause = 5 * time.Second
	}
	if s.OutageMin <= 0 {
		s.OutageMin = 15 * time.Second
	}
	if s.OutageMax < s.OutageMin {
		s.OutageMax = max(5*time.Minute, s.OutageMin)
	}
	return s
}

// Worker takes recordings from the queue one at a time and turns them into text. Run it in a goroutine; to run
// several, make one Worker for each (a Worker is not safe for concurrent use, the queue is).
type Worker struct {
	db       dbgen.Querier
	files    storage.Store
	service  Transcriber
	logger   *slog.Logger
	settings Settings
	// Now supplies the time; nil means time.Now. Tests set it.
	Now func() time.Time

	wake   chan struct{}
	outage outage
}

// NewWorker returns a worker for the queue in db, reading recordings from files and asking service for the text.
func NewWorker(db dbgen.Querier, files storage.Store, service Transcriber, logger *slog.Logger, settings Settings) *Worker {
	return &Worker{db: db, files: files, service: service, logger: logger, settings: settings.withDefaults(), wake: make(chan struct{}, 1)}
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Wake tells an idle worker that there is something new in the queue. It never blocks.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run works until ctx ends. A recording in hand when it ends is put back in the queue at once, with its attempt
// given back, so that stopping the server (a redeploy) costs the recording nothing.
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		processed := false
		if !w.outage.blocked(w.now()) {
			var err error
			processed, err = w.RunOnce(ctx)
			if err != nil {
				if ctx.Err() == nil {
					w.logger.Error("speech recognition queue", "error", err)
				}
				processed = false // do not spin on an error that repeats
			}
		}
		if processed {
			continue // there may be more waiting
		}
		select {
		case <-ctx.Done():
		case <-w.wake:
		case <-time.After(w.settings.Poll):
		}
	}
}

// RunOnce takes the oldest recording that is ready and deals with it. It reports whether there was one. An error
// means the queue itself could not be used (the database); what happens to a recording, good or bad, is not an
// error here.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	now := w.now()
	claimed, err := w.db.ClaimTranscription(ctx, dbgen.ClaimTranscriptionParams{Now: now, LeaseUntil: now.Add(w.settings.Lease)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("take a recording from the queue: %w", err)
	}
	return true, w.process(ctx, claimed.MessageID, claimed.Attempts)
}

func (w *Worker) process(ctx context.Context, id uuid.UUID, attempt int32) error {
	log := w.logger.With("message_id", id, "attempt", attempt)

	if int(attempt) > w.settings.MaxAttempts {
		// Taken again after its lease ran out, more often than a recording may be tried: whatever happens to it
		// in the middle (it takes the worker down, say) is not going to stop by trying again.
		log.Warn("giving up on a recording whose attempts ran out without an answer")
		return w.fail(ctx, id, attempt, CodeTooManyFailures, "every earlier attempt ended without an answer")
	}

	subject, err := w.db.GetTranscriptionSubject(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Info("the recording was deleted before it was recognized")
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up the recording: %w", err)
	}
	if !subject.HasWav {
		return w.skip(ctx, id, attempt, CodeNotDecodable, "the recording could not be decoded when it was received")
	}
	if subject.DurationMs != nil && time.Duration(*subject.DurationMs)*time.Millisecond > w.settings.MaxAudio {
		return w.skip(ctx, id, attempt, CodeTooLong, fmt.Sprintf("the recording is %d ms, the limit is %s", *subject.DurationMs, w.settings.MaxAudio))
	}

	data, err := w.read(id)
	if errors.Is(err, storage.ErrNotFound) {
		// The recording may have been deleted just now (row and file go together); only a file missing under a
		// row that is still there is a problem.
		if _, err := w.db.GetTranscriptionSubject(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			log.Info("the recording was deleted before it was recognized")
			return nil
		}
		log.Error("the recording's file is missing")
		return w.fail(ctx, id, attempt, CodeFileMissing, "the file of the recording is missing")
	}
	if err != nil {
		return fmt.Errorf("read the recording: %w", err) // the lease runs out and the recording is taken again
	}

	wav, err := audio.ToWAV(data)
	if err != nil {
		return w.skip(ctx, id, attempt, CodeNotDecodable, "the recording cannot be decoded: "+err.Error())
	}

	started := time.Now()
	result, err := w.service.Transcribe(ctx, wav)
	if err != nil {
		return w.afterFailure(ctx, log, id, attempt, err)
	}
	if w.outage.recovered() {
		log.Info("the speech recognition service is reachable again")
	}
	text := CleanText(result.Text)
	stored, err := w.db.CompleteTranscription(ctx, dbgen.CompleteTranscriptionParams{
		MessageID: id, Attempt: attempt, Now: w.now(), Text: text, Engine: result.Engine,
		Language: nonEmpty(result.Language), AudioMs: clamp32(result.AudioMs), SttMs: clamp32(result.SttMs),
	})
	if err != nil {
		return fmt.Errorf("store the text: %w", err)
	}
	if stored == 0 {
		// The lease ran out and someone else has it, or the recording was deleted while the model worked.
		log.Info("the text was discarded: the recording is no longer ours", "request_id", result.RequestID)
		return nil
	}
	log.Info("recording recognized", "request_id", result.RequestID, "engine", result.Engine,
		"audio_ms", result.AudioMs, "stt_ms", result.SttMs, "ms", time.Since(started).Milliseconds(), "chars", len([]rune(text)))
	return nil
}

// afterFailure decides what a failed call means for the recording (see Kind).
func (w *Worker) afterFailure(ctx context.Context, log *slog.Logger, id uuid.UUID, attempt int32, err error) error {
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		// The server is stopping. The recording is put back as if it had never been taken.
		return w.release(ctx, id, attempt, 1, w.now(), "")
	}

	var failure *Error
	if !errors.As(err, &failure) {
		failure = &Error{Kind: Failed, Err: err} // a Transcriber that does not say: the safe reading is "it may be the recording"
	}
	detail := failure.Error()
	now := w.now()

	switch failure.Kind {
	case Unavailable:
		pause, first := w.outage.failed(now, w.settings.OutageMin, w.settings.OutageMax)
		if first {
			log.Warn("the speech recognition service cannot be reached; waiting", "pause", pause, "error", detail)
		} else {
			log.Debug("the speech recognition service still cannot be reached", "pause", pause, "error", detail)
		}
		return w.release(ctx, id, attempt, 1, now.Add(pause), detail)
	case Busy:
		w.outage.recovered() // it answered
		pause := max(failure.RetryAfter, w.settings.BusyPause)
		log.Debug("the speech recognition service is busy", "pause", pause)
		return w.release(ctx, id, attempt, 1, now.Add(pause), detail)
	case Rejected:
		w.outage.recovered()
		log.Warn("the service refused the recording", "error", detail)
		return w.fail(ctx, id, attempt, CodeRejected, detail)
	default:
		w.outage.recovered()
		if int(attempt) >= w.settings.MaxAttempts {
			log.Warn("giving up on a recording: the model did not answer on every attempt", "error", detail)
			return w.fail(ctx, id, attempt, CodeTooManyFailures, detail)
		}
		pause := pauseAfterFailure(w.settings.Backoff, int(attempt))
		log.Warn("the model did not answer; trying the recording again later", "pause", pause, "error", detail)
		return w.release(ctx, id, attempt, 0, now.Add(pause), detail)
	}
}

func (w *Worker) read(id uuid.UUID) ([]byte, error) {
	file, err := w.files.Open(storage.RecordingName(id))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("the file is over %d bytes", maxFileBytes)
	}
	return data, nil
}

// The four ways a recording leaves the running state. Each is fenced by the attempt: if the recording is no longer
// this worker's, no row changes and the call is a no-op.

func (w *Worker) fail(ctx context.Context, id uuid.UUID, attempt int32, code, detail string) error {
	ctx, done := finishing(ctx)
	defer done()
	_, err := w.db.FailTranscription(ctx, dbgen.FailTranscriptionParams{
		MessageID: id, Attempt: attempt, ErrorCode: code, LastError: nonEmpty(shorten(detail)), Now: w.now(),
	})
	if err != nil {
		return fmt.Errorf("mark the recording as failed: %w", err)
	}
	return nil
}

func (w *Worker) skip(ctx context.Context, id uuid.UUID, attempt int32, code, detail string) error {
	ctx, done := finishing(ctx)
	defer done()
	_, err := w.db.SkipTranscription(ctx, dbgen.SkipTranscriptionParams{
		MessageID: id, Attempt: attempt, ErrorCode: code, LastError: nonEmpty(shorten(detail)), Now: w.now(),
	})
	if err != nil {
		return fmt.Errorf("mark the recording as skipped: %w", err)
	}
	return nil
}

// release puts the recording back in the queue. giveBack is 1 when the attempt must not count.
func (w *Worker) release(ctx context.Context, id uuid.UUID, attempt int32, giveBack int32, runAfter time.Time, detail string) error {
	ctx, done := finishing(ctx)
	defer done()
	_, err := w.db.ReleaseTranscription(ctx, dbgen.ReleaseTranscriptionParams{
		MessageID: id, Attempt: attempt, GiveBack: giveBack, RunAfter: runAfter, LastError: nonEmpty(shorten(detail)),
	})
	if err != nil {
		return fmt.Errorf("put the recording back in the queue: %w", err)
	}
	return nil
}

// finishing returns the context for a write that ends an attempt, and the function that releases it. Such a write
// must go through even when the server is being stopped (that is when the recording has to be given back), so a
// context that has ended is replaced by one that ignores the cancellation, within a limit.
func finishing(ctx context.Context) (context.Context, func()) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// outage tracks the pauses while the service cannot be reached, so that a service that is down is asked about
// once in a while and not once for every recording in the queue.
type outage struct {
	pause time.Duration
	until time.Time
}

func (o *outage) blocked(now time.Time) bool { return !o.until.IsZero() && now.Before(o.until) }

// failed records one more failed try and returns the pause that follows; first is true for the first one of an
// outage (which is the one worth a warning in the log).
func (o *outage) failed(now time.Time, shortest, longest time.Duration) (pause time.Duration, first bool) {
	first = o.pause == 0
	if first {
		o.pause = shortest
	} else {
		o.pause = min(o.pause*2, longest)
	}
	o.until = now.Add(o.pause)
	return o.pause, first
}

// recovered ends an outage; it reports whether there was one.
func (o *outage) recovered() bool {
	was := o.pause > 0
	o.pause, o.until = 0, time.Time{}
	return was
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// shorten fits an error text in the column meant for the operator.
func shorten(s string) string {
	const limit = 500
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

func clamp32(n int) int32 { return int32(min(max(n, 0), math.MaxInt32)) }
