-- +goose Up
-- The text of a recording and the queue that produces it. One row per message, made in the same transaction that
-- stores the message, so a recording cannot be accepted without its place in the queue. The row goes with its
-- message (a deleted recording, or one the account's storage limit pushed out, takes its text and its place in the
-- queue with it).
--
-- States: pending (waiting, from run_after on), running (taken by a worker until locked_until), done (text is set,
-- possibly empty: silence), failed (gave up, error_code says why), skipped (never sent to the model, error_code
-- says why). Design and the rules of retrying: STT_SERVICE_PLAN.md.

CREATE TABLE transcriptions (
    message_id   uuid        PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
    state        text        NOT NULL CHECK (state IN ('pending', 'running', 'done', 'failed', 'skipped')),
    -- Attempts that count against the recording: it was taken by a worker and the model did not answer. A service
    -- that could not be reached does not count (the attempt is given back).
    attempts     integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    -- Not taken before this moment: the pause between attempts.
    run_after    timestamptz NOT NULL,
    -- While a worker has the recording; when it passes, another worker may take it (the first one died).
    locked_until timestamptz,
    enqueued_at  timestamptz NOT NULL,
    started_at   timestamptz,
    finished_at  timestamptz,
    text         text,
    -- What the model says it heard, when it says.
    language     text,
    -- What made the text (for example parakeet-tdt-0.6b-v3-int8), so that a text can be traced back to its model.
    engine       text,
    audio_ms     integer     CHECK (audio_ms >= 0),
    -- How long the model took.
    stt_ms       integer     CHECK (stt_ms >= 0),
    -- Why a recording failed or was skipped: not_decodable, too_long, rejected, too_many_failures, file_missing.
    error_code   text,
    -- The last problem seen, for the operator; never shown to the person.
    last_error   text        CHECK (char_length(last_error) <= 500),
    CHECK ((state = 'done') = (text IS NOT NULL)),
    CHECK ((state = 'running') = (locked_until IS NOT NULL)),
    CHECK ((state IN ('failed', 'skipped')) = (error_code IS NOT NULL)),
    CHECK (state NOT IN ('done', 'failed', 'skipped') OR finished_at IS NOT NULL)
);

-- The queue is what is not finished, oldest first.
CREATE INDEX transcriptions_queue_idx ON transcriptions (enqueued_at, message_id) WHERE state IN ('pending', 'running');

-- +goose Down
DROP TABLE transcriptions;
