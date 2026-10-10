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

-- What a person may see of one of their own recordings' transcription: no row when the recording is not theirs or
-- was never queued.
-- name: GetOwnTranscription :one
SELECT t.state, t.text, t.language, t.engine, t.finished_at, t.error_code
FROM transcriptions t
JOIN messages m ON m.id = t.message_id
WHERE t.message_id = sqlc.arg(message_id) AND m.user_id = sqlc.arg(user_id);

-- A person asks for another try at a recording that was given up on. It goes to the back of the queue with its
-- attempts reset. One row when it was theirs and failed; none otherwise (the caller says why).
-- name: RetryOwnTranscription :execrows
UPDATE transcriptions t
SET state = 'pending', attempts = 0, run_after = sqlc.arg(now)::timestamptz, enqueued_at = sqlc.arg(now)::timestamptz,
    locked_until = NULL, started_at = NULL, finished_at = NULL, error_code = NULL, last_error = NULL
FROM messages m
WHERE t.message_id = sqlc.arg(message_id) AND m.id = t.message_id AND m.user_id = sqlc.arg(user_id) AND t.state = 'failed';

-- The operator's counts by state, for `pigeondrop stt status`.
-- name: CountTranscriptions :many
SELECT state, count(*)::bigint AS total FROM transcriptions GROUP BY state ORDER BY state;

-- When the oldest recording still waiting (pending, or running) was queued. No row when nothing waits.
-- name: OldestWaitingTranscription :one
SELECT enqueued_at FROM transcriptions WHERE state IN ('pending', 'running') ORDER BY enqueued_at, message_id LIMIT 1;

-- Recordings given up on since a moment.
-- name: CountFailedTranscriptionsSince :one
SELECT count(*)::bigint AS total FROM transcriptions WHERE state = 'failed' AND finished_at >= sqlc.arg(since)::timestamptz;

-- The latest failure, with the model's own words about it.
-- name: LatestFailedTranscription :one
SELECT message_id, finished_at, error_code, last_error FROM transcriptions WHERE state = 'failed' ORDER BY finished_at DESC, message_id LIMIT 1;

-- Puts every recording that has no row in the queue yet into it (the ones received before speech recognition was
-- switched on), oldest first, behind whatever is queued now. A recording that cannot go to the model is recorded
-- as skipped, like a new one. Returns the state of each row added.
-- name: EnqueueMissingTranscriptions :many
INSERT INTO transcriptions (message_id, state, run_after, enqueued_at, finished_at, error_code)
SELECT m.id,
       CASE WHEN NOT m.has_wav OR m.duration_ms > sqlc.arg(max_audio_ms)::integer THEN 'skipped' ELSE 'pending' END,
       sqlc.arg(now)::timestamptz,
       sqlc.arg(now)::timestamptz + ROW_NUMBER() OVER (ORDER BY m.received_at, m.id) * interval '1 microsecond',
       CASE WHEN NOT m.has_wav OR m.duration_ms > sqlc.arg(max_audio_ms)::integer THEN sqlc.arg(now)::timestamptz END,
       CASE WHEN NOT m.has_wav THEN 'not_decodable' WHEN m.duration_ms > sqlc.arg(max_audio_ms)::integer THEN 'too_long' END
FROM messages m
WHERE NOT EXISTS (SELECT 1 FROM transcriptions t WHERE t.message_id = m.id)
ON CONFLICT (message_id) DO NOTHING
RETURNING state;

-- Gives every recording that was given up on another try, oldest failure first, behind whatever is queued now.
-- name: RetryFailedTranscriptions :execrows
WITH picked AS (
    SELECT message_id, ROW_NUMBER() OVER (ORDER BY finished_at, message_id) AS place
    FROM transcriptions WHERE state = 'failed'
)
UPDATE transcriptions t
SET state = 'pending', attempts = 0, run_after = sqlc.arg(now)::timestamptz,
    enqueued_at = sqlc.arg(now)::timestamptz + picked.place * interval '1 microsecond',
    locked_until = NULL, started_at = NULL, finished_at = NULL, error_code = NULL, last_error = NULL
FROM picked
WHERE t.message_id = picked.message_id AND t.state = 'failed';
