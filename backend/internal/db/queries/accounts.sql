-- name: GetAccount :one
SELECT code, custom_server_url, created_at, updated_at
FROM accounts
WHERE code = $1;

-- Returns no row when the account already exists: creating twice is harmless, the caller then reads it.
-- name: CreateAccount :one
INSERT INTO accounts (code)
VALUES ($1)
ON CONFLICT (code) DO NOTHING
RETURNING code, custom_server_url, created_at, updated_at;

-- Returns no row when there is no such account: this never creates one.
-- name: SetAccountServerURL :one
UPDATE accounts
SET custom_server_url = $2, updated_at = now()
WHERE code = $1
RETURNING code, custom_server_url, created_at, updated_at;
