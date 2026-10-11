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
