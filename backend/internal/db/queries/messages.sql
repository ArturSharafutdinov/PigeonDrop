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

-- name: GetMessage :one
SELECT id, device_id, client_message_id, format, bytes_received, duration_ms, has_wav, received_at
FROM messages
WHERE id = $1 AND user_id = $2;

-- No row when the user has no such message. The files are removed by the caller afterwards.
-- name: DeleteMessage :one
DELETE FROM messages WHERE id = $1 AND user_id = $2
RETURNING id, has_wav;
