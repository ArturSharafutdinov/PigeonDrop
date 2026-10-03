-- Device pairing and paired devices. Times come from the caller (never now()).

-- A unique violation on user_code means the random code collided with a live one: the caller draws another.
-- name: CreatePairing :one
INSERT INTO device_pairings (device_code_hash, user_code, client_name, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: GetPairingByDeviceCodeHash :one
SELECT id, user_code, expires_at, last_polled_at, user_id, device_name, approved_at, redeemed_at
FROM device_pairings
WHERE device_code_hash = $1;

-- Only a pairing that can still be approved: unknown, expired and already approved codes are all "no row".
-- name: GetOpenPairingByUserCode :one
SELECT id, user_code, client_name, expires_at
FROM device_pairings
WHERE user_code = $1 AND approved_at IS NULL AND expires_at > sqlc.arg(now)::timestamptz;

-- Approval also gives the watch a few more minutes to collect its token: a code approved in its last
-- seconds must not expire before the next poll.
-- name: ApprovePairing :one
UPDATE device_pairings
SET user_id = sqlc.arg(user_id)::uuid, device_name = sqlc.arg(device_name)::text, approved_at = sqlc.arg(now)::timestamptz,
    expires_at = GREATEST(expires_at, sqlc.arg(collect_until)::timestamptz)
WHERE user_code = sqlc.arg(user_code) AND approved_at IS NULL AND expires_at > sqlc.arg(now)::timestamptz
RETURNING id;

-- name: TouchPairingPoll :exec
UPDATE device_pairings SET last_polled_at = sqlc.arg(now)::timestamptz WHERE id = $1;

-- Hands out the approval exactly once: no row means someone else collected it first, or it was never approved.
-- name: RedeemPairing :one
UPDATE device_pairings
SET redeemed_at = sqlc.arg(now)::timestamptz
WHERE id = $1 AND approved_at IS NOT NULL AND redeemed_at IS NULL
RETURNING user_id, device_name;

-- name: DeleteExpiredPairings :execrows
DELETE FROM device_pairings WHERE expires_at <= sqlc.arg(now)::timestamptz;

-- name: CreateDevice :one
INSERT INTO devices (user_id, name, token_hash, created_at)
VALUES ($1, $2, $3, $4)
RETURNING id, user_id, name, created_at;

-- The device behind a token. No row for an unknown token or a revoked device.
-- name: GetDeviceByTokenHash :one
SELECT id, user_id, name, last_seen_at
FROM devices
WHERE token_hash = $1 AND revoked_at IS NULL;

-- Records that the device was seen, at most once per interval: the caller passes the oldest last_seen_at
-- that still counts as recent.
-- name: TouchDevice :exec
UPDATE devices
SET last_seen_at = sqlc.arg(now)::timestamptz
WHERE id = $1 AND (last_seen_at IS NULL OR last_seen_at < sqlc.arg(recent_after)::timestamptz);

-- name: ListDevices :many
SELECT id, name, created_at, last_seen_at
FROM devices
WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY created_at, id;

-- No row means the user has no such device. Revoking twice is fine: the second time changes nothing.
-- name: RevokeDevice :one
UPDATE devices
SET revoked_at = COALESCE(revoked_at, sqlc.arg(now)::timestamptz)
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id)
RETURNING id;
