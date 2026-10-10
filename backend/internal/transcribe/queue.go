package transcribe

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/internal/db/dbgen"
)

// The values of transcriptions.error_code.
const (
	CodeNotDecodable    = "not_decodable"     // the recording could not be turned into audio
	CodeTooLong         = "too_long"          // longer than the model is measured for
	CodeRejected        = "rejected"          // the service refused the recording as it is
	CodeTooManyFailures = "too_many_failures" // the model did not answer on every attempt
	CodeFileMissing     = "file_missing"      // the message exists but its file does not
)

// The values of transcriptions.state.
const (
	StatePending = "pending"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
	StateSkipped = "skipped"
)

// DefaultBackoff is the pause before the next attempt after the first, second, ... failure of one recording; the
// last value repeats. It is for failures that may be the recording's own (the model hung or died on it): an
// outage of the service has its own, shorter pauses and does not use up attempts.
var DefaultBackoff = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour}

// Intake is what the upload does with a new recording: it either joins the queue or is marked as not going to the
// model, and either way it is recorded in the transaction that stores the message.
type Intake struct {
	// MaxAudio is the longest recording sent to the model. The server takes files of up to 512 KiB (about 3.7
	// minutes), but memory was measured only up to the watch's 60 seconds, so anything much longer is not sent.
	MaxAudio time.Duration
}

// Enqueue records a new message. q must belong to the transaction that inserted the message, so that the queue
// entry and the message appear (or do not) together. Repeating it for the same message changes nothing.
func (in Intake) Enqueue(ctx context.Context, q dbgen.Querier, messageID uuid.UUID, decodable bool, durationMs *int32, now time.Time) error {
	skip := func(code string) error {
		return q.EnqueueSkippedTranscription(ctx, dbgen.EnqueueSkippedTranscriptionParams{MessageID: messageID, Now: now, ErrorCode: code})
	}
	switch {
	case !decodable:
		return skip(CodeNotDecodable)
	case durationMs != nil && time.Duration(*durationMs)*time.Millisecond > in.MaxAudio:
		return skip(CodeTooLong)
	}
	return q.EnqueueTranscription(ctx, dbgen.EnqueueTranscriptionParams{MessageID: messageID, Now: now})
}

// pauseAfterFailure is the wait before the next attempt, when attempt (counting from 1) has just failed.
func pauseAfterFailure(backoff []time.Duration, attempt int) time.Duration {
	if len(backoff) == 0 {
		return 0
	}
	return backoff[min(max(attempt-1, 0), len(backoff)-1)]
}
