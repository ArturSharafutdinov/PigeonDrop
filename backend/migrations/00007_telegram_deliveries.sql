-- +goose Up
-- The queue that sends recordings to the Telegram chat their owner linked: one row per recording, made in the same
-- transaction that stores the recording (and only for an account that is linked at that moment), so a recording that
-- was accepted is never lost on its way to the chat, and one that is sent is never sent twice by a restart. The row
-- goes with its message (a deleted recording, or one the account's storage limit pushed out, takes its place in the
-- queue with it). Design: TELEGRAM_BOT_PLAN.md.
--
-- States: pending (waiting, from run_after on, and, while the recording's text is still being made, until wait_until),
-- running (taken by a worker until locked_until), sent, failed (gave up, error_code says why), skipped (not sent,
-- error_code says why).

CREATE TABLE telegram_deliveries (
    message_id   uuid        PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
    state        text        NOT NULL CHECK (state IN ('pending', 'running', 'sent', 'failed', 'skipped')),
    -- Attempts that count against the delivery: it was taken by a worker and Telegram did not give a clear answer.
    -- An outage of Telegram, or its rate limit, does not count (the attempt is given back).
    attempts     integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    -- Not taken before this moment: the pause between attempts.
    run_after    timestamptz NOT NULL,
    -- Until this moment the delivery waits for the recording's text; after it, the recording goes without.
    wait_until   timestamptz NOT NULL,
    -- While a worker has the delivery; when it passes, another worker may take it (the first one died).
    locked_until timestamptz,
    enqueued_at  timestamptz NOT NULL,
    finished_at  timestamptz,
    -- The chat the first send went to: later attempts go to the same one even if the account was linked to another.
    chat_id      bigint,
    -- The voice message in Telegram, once it is sent: an attempt that follows a failure of the text message that
    -- goes after it does not send the voice again.
    voice_message_id bigint,
    -- For a sent delivery: whether the text went with it.
    with_text    boolean,
    -- Why a delivery failed or was skipped: blocked, unlinked, rejected, too_many_failures, file_missing, no_audio.
    error_code   text,
    -- The last problem seen, for the operator; never shown to the person.
    last_error   text        CHECK (char_length(last_error) <= 500),
    CHECK ((state = 'running') = (locked_until IS NOT NULL)),
    CHECK ((state IN ('failed', 'skipped')) = (error_code IS NOT NULL)),
    CHECK ((state = 'sent') = (with_text IS NOT NULL)),
    CHECK (state NOT IN ('sent', 'failed', 'skipped') OR finished_at IS NOT NULL)
);

-- The queue is what is not finished, oldest first.
CREATE INDEX telegram_deliveries_queue_idx ON telegram_deliveries (enqueued_at, message_id) WHERE state IN ('pending', 'running');

-- +goose Down
DROP TABLE telegram_deliveries;
