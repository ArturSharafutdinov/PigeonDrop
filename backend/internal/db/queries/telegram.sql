-- Linking an account to a Telegram chat (internal/telegram, internal/httpapi/telegram.go). Times come from the
-- caller (never now()).

-- A new code for the person; their earlier unused codes stop working (one live link at a time).
-- name: DeleteUnusedTelegramLinkCodes :exec
DELETE FROM telegram_link_codes WHERE user_id = $1 AND used_at IS NULL;

-- name: CreateTelegramLinkCode :exec
INSERT INTO telegram_link_codes (code_hash, user_id, created_at, expires_at)
VALUES ($1, $2, sqlc.arg(now)::timestamptz, sqlc.arg(expires_at)::timestamptz);

-- The code with what is needed to decide: no row means there never was such a code (or it was purged).
-- name: GetTelegramLinkCode :one
SELECT user_id, expires_at, used_at, used_chat_id FROM telegram_link_codes WHERE code_hash = $1;

-- Uses the code, once: no row means someone used it first, or it has expired.
-- name: UseTelegramLinkCode :execrows
UPDATE telegram_link_codes
SET used_at = sqlc.arg(now)::timestamptz, used_chat_id = sqlc.arg(chat_id)::bigint
WHERE code_hash = $1 AND used_at IS NULL AND expires_at > sqlc.arg(now)::timestamptz;

-- The chat leaves whoever it was linked to (a chat belongs to one account); returns who, if anyone.
-- name: DeleteTelegramLinkByChat :execrows
DELETE FROM telegram_links WHERE chat_id = $1;

-- Links the account to the chat, replacing the account's earlier link and clearing a block.
-- name: UpsertTelegramLink :exec
INSERT INTO telegram_links (user_id, chat_id, username, first_name, linked_at)
VALUES (sqlc.arg(user_id), sqlc.arg(chat_id), sqlc.narg(username), sqlc.narg(first_name), sqlc.arg(now)::timestamptz)
ON CONFLICT (user_id) DO UPDATE
SET chat_id = EXCLUDED.chat_id, username = EXCLUDED.username, first_name = EXCLUDED.first_name,
    linked_at = EXCLUDED.linked_at, blocked_at = NULL;

-- name: GetTelegramLink :one
SELECT user_id, chat_id, username, first_name, linked_at, blocked_at FROM telegram_links WHERE user_id = $1;

-- Which account a chat is linked to, for /stop.
-- name: GetTelegramLinkByChat :one
SELECT user_id, chat_id, username, first_name, linked_at, blocked_at FROM telegram_links WHERE chat_id = $1;

-- Unlinking; no row changed means there was no link (not an error).
-- name: DeleteTelegramLink :execrows
DELETE FROM telegram_links WHERE user_id = $1;

-- The language of the person's account, for the bot's answers once it knows whose chat it is.
-- name: GetUserLanguage :one
SELECT language FROM users WHERE id = $1;

-- name: DeleteExpiredTelegramLinkCodes :execrows
DELETE FROM telegram_link_codes WHERE expires_at <= sqlc.arg(now)::timestamptz;

-- The queue that sends recordings to a linked chat (internal/telegram/deliver.go).

-- Puts a new recording in the queue, if its owner has a link that works. Does nothing if the account is not linked, or the
-- link is blocked, or the recording is already there (an upload that ran twice).
-- name: EnqueueTelegramDelivery :exec
INSERT INTO telegram_deliveries (message_id, state, run_after, wait_until, enqueued_at)
SELECT m.id, 'pending', sqlc.arg(now)::timestamptz, sqlc.arg(wait_until)::timestamptz, sqlc.arg(now)::timestamptz
FROM messages m
JOIN telegram_links l ON l.user_id = m.user_id AND l.blocked_at IS NULL
WHERE m.id = sqlc.arg(message_id)
ON CONFLICT (message_id) DO NOTHING;

-- Takes the oldest delivery that is ready: waiting and past its pause, and either its recording's text is no longer being
-- made (done, failed or skipped, or recognition is off and there is no entry) or the time to wait for it is up; or
-- running with a lease that has run out (its worker died). SKIP LOCKED lets several workers take different ones.
-- The attempt is counted here; the caller gives it back when Telegram was not reached.
-- No row when nothing is ready.
-- name: ClaimTelegramDelivery :one
UPDATE telegram_deliveries
SET state = 'running', attempts = attempts + 1, locked_until = sqlc.arg(lease_until)::timestamptz
WHERE message_id = (
    SELECT d.message_id FROM telegram_deliveries d
    WHERE (d.state = 'pending' AND d.run_after <= sqlc.arg(now)::timestamptz
           AND (d.wait_until <= sqlc.arg(now)::timestamptz
                OR NOT EXISTS (SELECT 1 FROM transcriptions t WHERE t.message_id = d.message_id AND t.state IN ('pending', 'running'))))
       OR (d.state = 'running' AND d.locked_until < sqlc.arg(now)::timestamptz)
    ORDER BY d.enqueued_at, d.message_id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING message_id, attempts;

-- What the worker needs to know to send a recording it took: the recording, the account's link now, what was already
-- done for it, and its text. No row: the recording was deleted meanwhile. The link columns are null when the account
-- has no link any more.
-- name: GetTelegramDeliverySubject :one
SELECT m.id, m.format, m.duration_ms,
       d.chat_id AS delivery_chat_id, d.voice_message_id,
       l.user_id AS link_user_id, l.chat_id AS link_chat_id, l.blocked_at AS link_blocked_at,
       t.state AS transcript_state, t.text AS transcript_text
FROM telegram_deliveries d
JOIN messages m ON m.id = d.message_id
LEFT JOIN telegram_links l ON l.user_id = m.user_id
LEFT JOIN transcriptions t ON t.message_id = m.id
WHERE d.message_id = $1;

-- The voice message is in Telegram: remember where, so that a retry does not send it again. Fenced by the attempt like
-- the others, so only the worker that holds the delivery can.
-- name: RecordTelegramVoice :execrows
UPDATE telegram_deliveries
SET chat_id = sqlc.arg(chat_id)::bigint, voice_message_id = sqlc.arg(voice_message_id)::bigint
WHERE message_id = sqlc.arg(message_id) AND state = 'running' AND attempts = sqlc.arg(attempt);

-- name: CompleteTelegramDelivery :execrows
UPDATE telegram_deliveries
SET state = 'sent', with_text = sqlc.arg(with_text)::boolean, chat_id = COALESCE(chat_id, sqlc.narg(chat_id)),
    finished_at = sqlc.arg(now)::timestamptz, locked_until = NULL, error_code = NULL, last_error = NULL
WHERE message_id = sqlc.arg(message_id) AND state = 'running' AND attempts = sqlc.arg(attempt);

-- Puts the delivery back in the queue, to be taken again from run_after on. give_back is 1 when the attempt must not
-- count (Telegram was not reached, or the server is stopping) and 0 when it must.
-- name: ReleaseTelegramDelivery :execrows
UPDATE telegram_deliveries
SET state = 'pending', run_after = sqlc.arg(run_after)::timestamptz, locked_until = NULL,
    attempts = GREATEST(attempts - sqlc.arg(give_back)::integer, 0), last_error = sqlc.narg(last_error)
WHERE message_id = sqlc.arg(message_id) AND state = 'running' AND attempts = sqlc.arg(attempt);

-- name: FailTelegramDelivery :execrows
UPDATE telegram_deliveries
SET state = 'failed', error_code = sqlc.arg(error_code)::text, last_error = sqlc.narg(last_error),
    finished_at = sqlc.arg(now)::timestamptz, locked_until = NULL
WHERE message_id = sqlc.arg(message_id) AND state = 'running' AND attempts = sqlc.arg(attempt);

-- Decides that the recording is not going to the chat.
-- name: SkipTelegramDelivery :execrows
UPDATE telegram_deliveries
SET state = 'skipped', error_code = sqlc.arg(error_code)::text, last_error = sqlc.narg(last_error),
    finished_at = sqlc.arg(now)::timestamptz, locked_until = NULL
WHERE message_id = sqlc.arg(message_id) AND state = 'running' AND attempts = sqlc.arg(attempt);

-- Telegram says the chat cannot be written to: the link stops being used until the person links again. Only the link
-- to that chat: one made to another chat since is not blocked.
-- name: BlockTelegramLink :execrows
UPDATE telegram_links SET blocked_at = sqlc.arg(now)::timestamptz
WHERE user_id = sqlc.arg(user_id) AND chat_id = sqlc.arg(chat_id) AND blocked_at IS NULL;

-- name: GetTelegramDelivery :one
SELECT message_id, state, attempts, run_after, wait_until, locked_until, enqueued_at, finished_at, chat_id,
       voice_message_id, with_text, error_code, last_error
FROM telegram_deliveries WHERE message_id = $1;

-- The operator's counts by state, for `pigeondrop telegram status`.
-- name: CountTelegramDeliveries :many
SELECT state, count(*)::bigint AS total FROM telegram_deliveries GROUP BY state ORDER BY state;

-- When the oldest delivery still waiting (pending, or running) was queued. No row when nothing waits.
-- name: OldestWaitingTelegramDelivery :one
SELECT enqueued_at FROM telegram_deliveries WHERE state IN ('pending', 'running') ORDER BY enqueued_at, message_id LIMIT 1;

-- Deliveries given up on since a moment.
-- name: CountFailedTelegramDeliveriesSince :one
SELECT count(*)::bigint AS total FROM telegram_deliveries WHERE state = 'failed' AND finished_at >= sqlc.arg(since)::timestamptz;

-- The latest failure, with Telegram's own words about it.
-- name: LatestFailedTelegramDelivery :one
SELECT message_id, finished_at, error_code, last_error FROM telegram_deliveries WHERE state = 'failed' ORDER BY finished_at DESC, message_id LIMIT 1;

-- How many accounts are linked, and how many of those links are blocked.
-- name: CountTelegramLinks :one
SELECT count(*)::bigint AS linked, (count(*) FILTER (WHERE blocked_at IS NOT NULL))::bigint AS blocked FROM telegram_links;
