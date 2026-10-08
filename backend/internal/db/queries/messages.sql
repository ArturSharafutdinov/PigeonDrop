-- Recordings received from paired watches. Everything a person can reach is scoped by user_id here, in
-- the query, so a handler cannot forget the ownership check.

-- name: GetMessageByClientID :one
SELECT id, device_id, client_message_id, format, bytes_received, duration_ms, has_wav, received_at
FROM messages
WHERE device_id = $1 AND client_message_id = $2;

-- Returns no row when this device already uploaded this name: a request with the same name won the race.
-- name: InsertMessage :one
INSERT INTO messages (id, user_id, device_id, client_message_id, format, bytes_received, duration_ms, has_wav, received_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (device_id, client_message_id) DO NOTHING
RETURNING id, device_id, client_message_id, format, bytes_received, duration_ms, has_wav, received_at;

-- The caller's messages, newest first, starting after the cursor (the received_at and id of the last message
-- of the previous page; both null for the first page).
-- name: ListMessages :many
SELECT m.id, m.device_id, d.name AS device_name, m.client_message_id, m.bytes_received, m.duration_ms, m.has_wav, m.received_at
FROM messages m
JOIN devices d ON d.id = m.device_id
WHERE m.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(before_at)::timestamptz IS NULL OR (m.received_at, m.id) < (sqlc.narg(before_at)::timestamptz, sqlc.narg(before_id)::uuid))
ORDER BY m.received_at DESC, m.id DESC
LIMIT sqlc.arg(max_rows);

-- Every message of the user, oldest first, for the archive of all recordings: only what is needed to find and
-- name the files.
-- name: ListMessageFiles :many
SELECT id, received_at FROM messages WHERE user_id = $1 ORDER BY received_at, id;

-- name: GetMessage :one
SELECT id, device_id, client_message_id, format, bytes_received, duration_ms, has_wav, received_at
FROM messages
WHERE id = $1 AND user_id = $2;

-- No row when the user has no such message. The files are removed by the caller afterwards.
-- name: DeleteMessage :one
DELETE FROM messages WHERE id = $1 AND user_id = $2
RETURNING id, has_wav;

-- Everything that changes how much room a user's recordings take (an upload counts, makes room and inserts)
-- runs under this lock, so two uploads of one user cannot both see free room that only one of them gets.
-- NO KEY UPDATE conflicts with itself but not with the key-share locks that inserts referencing the user take.
-- name: LockUserStorage :exec
SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE;

-- What the user's recordings take. Only the original files count; the WAV played in a browser is made on demand.
-- name: SumMessageBytes :one
SELECT COALESCE(SUM(bytes_received), 0)::bigint AS used FROM messages WHERE user_id = $1;

-- Deletes the user's oldest messages, never keep_id, until the newest ones that are left fit in room bytes
-- (the limit minus the message being kept, whatever its place in time). The files are removed by the caller.
-- name: PruneMessages :many
WITH ranked AS (
    SELECT id, SUM(bytes_received) OVER (ORDER BY received_at DESC, id DESC) AS newest_first_total
    FROM messages
    WHERE messages.user_id = sqlc.arg(owner)::uuid AND messages.id <> sqlc.arg(keep_id)::uuid
)
DELETE FROM messages
WHERE id IN (SELECT id FROM ranked WHERE newest_first_total > sqlc.arg(room)::bigint)
RETURNING id;

-- name: DeleteAllMessages :many
DELETE FROM messages WHERE user_id = $1 RETURNING id;
