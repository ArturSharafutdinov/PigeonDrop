-- Users, invitations and sessions. Times come from the caller (never now()) so the server and its
-- tests share one clock.

-- name: CreateUser :one
INSERT INTO users (email, password_hash, language, created_at)
VALUES ($1, $2, $3, $4)
RETURNING id, email, password_hash, plan, language, created_at;

-- name: GetUserByEmail :one
SELECT id, email, password_hash, plan, language, created_at
FROM users
WHERE email = $1;

-- name: GetUserByID :one
SELECT id, email, password_hash, plan, language, created_at
FROM users
WHERE id = $1;

-- name: ListUsers :many
SELECT id, email, plan, language, created_at
FROM users
ORDER BY created_at, email;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = $2 WHERE id = $1;

-- name: CreateInvitation :one
INSERT INTO invitations (code_hash, note, created_at, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id, note, created_at, expires_at;

-- Read-only look, to refuse an unusable code before spending a password hash on it.
-- name: FindUsableInvitation :one
SELECT id
FROM invitations
WHERE code_hash = $1 AND used_at IS NULL AND expires_at > sqlc.arg(now)::timestamptz;

-- Spends the invitation. No row means it was unknown, expired, or already used (possibly a moment ago
-- by someone else) - the caller cannot and need not tell which.
-- name: ConsumeInvitation :one
UPDATE invitations
SET used_at = sqlc.arg(now)::timestamptz, used_by = sqlc.arg(used_by)
WHERE code_hash = sqlc.arg(code_hash) AND used_at IS NULL AND expires_at > sqlc.arg(now)::timestamptz
RETURNING id;

-- name: ListInvitations :many
SELECT i.id, i.note, i.created_at, i.expires_at, i.used_at, u.email AS used_by_email
FROM invitations i
LEFT JOIN users u ON u.id = i.used_by
ORDER BY i.created_at DESC;

-- Only an unused invitation can be withdrawn; a used one stays as the record of who came through it.
-- name: RevokeInvitation :execrows
DELETE FROM invitations WHERE id = $1 AND used_at IS NULL;

-- name: CreateSession :one
INSERT INTO sessions (user_id, token_hash, created_at, last_seen_at, expires_at, absolute_expires_at)
VALUES ($1, $2, $3, $3, $4, $5)
RETURNING id;

-- The session behind a cookie, with its user. No row when it is unknown or has expired.
-- name: GetSessionByTokenHash :one
SELECT s.id AS session_id, s.last_seen_at, s.expires_at, s.absolute_expires_at,
       u.id AS user_id, u.email, u.plan, u.language, u.created_at AS user_created_at
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1
  AND s.expires_at > sqlc.arg(now)::timestamptz AND s.absolute_expires_at > sqlc.arg(now)::timestamptz;

-- Slides the expiry forward, but never past the absolute limit.
-- name: TouchSession :exec
UPDATE sessions
SET last_seen_at = sqlc.arg(now)::timestamptz, expires_at = LEAST(sqlc.arg(new_expires_at)::timestamptz, absolute_expires_at)
WHERE id = $1;

-- name: DeleteSessionByTokenHash :exec
DELETE FROM sessions WHERE token_hash = $1;

-- Used after a password change: every other session of the user ends, the current one stays.
-- name: DeleteOtherSessions :exec
DELETE FROM sessions WHERE user_id = $1 AND id <> $2;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= sqlc.arg(now)::timestamptz OR absolute_expires_at <= sqlc.arg(now)::timestamptz;
