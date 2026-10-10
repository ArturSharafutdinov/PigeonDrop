-- The queue of recordings waiting to be turned into text, and the text. The worker (internal/transcribe) is the
-- only caller except the upload, which puts a new recording in the queue. Every time passed in is the caller's
-- clock, not the database's, so that tests can move it.

-- Puts a new recording in the queue. Does nothing if it is already there (an upload that ran twice).
-- name: EnqueueTranscription :exec
INSERT INTO transcriptions (message_id, state, run_after, enqueued_at)
VALUES (sqlc.arg(message_id), 'pending', sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz)
ON CONFLICT (message_id) DO NOTHING;

-- A new recording that is not going to the model (it cannot be decoded, or it is too long); error_code says why.
-- name: EnqueueSkippedTranscription :exec
INSERT INTO transcriptions (message_id, state, run_after, enqueued_at, finished_at, error_code)
VALUES (sqlc.arg(message_id), 'skipped', sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz, sqlc.arg(error_code)::text)
ON CONFLICT (message_id) DO NOTHING;

-- Takes the oldest recording that is ready: waiting and past its pause, or running with a lease that has run out
-- (its worker died). SKIP LOCKED lets several workers take different ones without waiting for each other.
-- The attempt is counted here; the caller gives it back when the model was never reached.
-- No row when nothing is ready.
-- name: ClaimTranscription :one
UPDATE transcriptions
SET state = 'running', attempts = attempts + 1, started_at = sqlc.arg(now)::timestamptz, locked_until = sqlc.arg(lease_until)::timestamptz
WHERE message_id = (
    SELECT t.message_id FROM transcriptions t
    WHERE (t.state = 'pending' AND t.run_after <= sqlc.arg(now)::timestamptz)
       OR (t.state = 'running' AND t.locked_until < sqlc.arg(now)::timestamptz)
    ORDER BY t.enqueued_at, t.message_id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING message_id, attempts;

-- What the worker needs to know about a recording it took. No row: the recording was deleted meanwhile.
-- name: GetTranscriptionSubject :one
SELECT id, duration_ms, has_wav FROM messages WHERE id = $1;

-- Stores the text. Only the worker that took this attempt can: if the lease ran out and another worker took the
-- recording, or the recording was deleted, no row matches and nothing changes (the caller sees 0 rows).
-- name: CompleteTranscription :execrows
UPDATE transcriptions
SET state = 'done', text = sqlc.arg(text)::text, language = sqlc.narg(language), engine = sqlc.arg(engine)::text,
    audio_ms = sqlc.arg(audio_ms)::integer, stt_ms = sqlc.arg(stt_ms)::integer, finished_at = sqlc.arg(now)::timestamptz,
    locked_until = NULL, error_code = NULL, last_error = NULL
WHERE message_id = sqlc.arg(message_id) AND state = 'running' AND attempts = sqlc.arg(attempt);

-- Puts the recording back in the queue, to be taken again from run_after on. give_back is 1 when the attempt
-- must not count (the model was not reached, or the server is stopping) and 0 when it must.
-- name: ReleaseTranscription :execrows
UPDATE transcriptions
SET state = 'pending', run_after = sqlc.arg(run_after)::timestamptz, locked_until = NULL,
    attempts = GREATEST(attempts - sqlc.arg(give_back)::integer, 0), last_error = sqlc.narg(last_error)
WHERE message_id = sqlc.arg(message_id) AND state = 'running' AND attempts = sqlc.arg(attempt);

-- Gives up on the recording.
-- name: FailTranscription :execrows
UPDATE transcriptions
SET state = 'failed', error_code = sqlc.arg(error_code)::text, last_error = sqlc.narg(last_error),
    finished_at = sqlc.arg(now)::timestamptz, locked_until = NULL
WHERE message_id = sqlc.arg(message_id) AND state = 'running' AND attempts = sqlc.arg(attempt);

-- Decides, after the recording was taken, that it is not going to the model.
-- name: SkipTranscription :execrows
UPDATE transcriptions
SET state = 'skipped', error_code = sqlc.arg(error_code)::text, last_error = sqlc.narg(last_error),
    finished_at = sqlc.arg(now)::timestamptz, locked_until = NULL
WHERE message_id = sqlc.arg(message_id) AND state = 'running' AND attempts = sqlc.arg(attempt);

-- name: GetTranscription :one
SELECT message_id, state, attempts, run_after, locked_until, enqueued_at, started_at, finished_at,
       text, language, engine, audio_ms, stt_ms, error_code, last_error
FROM transcriptions
WHERE message_id = $1;
