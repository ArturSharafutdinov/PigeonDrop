-- +goose Up
-- People who use the service: users, the invitations that let them register, and their web sessions.
-- (`accounts` from the first migration is a different thing: a watch's device code in the old contract.)
-- Needs PostgreSQL 13 or newer for gen_random_uuid().

CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Stored lower-cased and trimmed, so one address cannot register twice by changing its case.
    email         text        NOT NULL UNIQUE CHECK (email = lower(email) AND char_length(email) <= 254),
    -- A PHC string ($argon2id$v=19$m=...,t=...,p=...$salt$hash); the parameters travel with the hash.
    password_hash text        NOT NULL,
    plan          text        NOT NULL DEFAULT 'free' CHECK (plan IN ('free', 'paid')),
    language      text        NOT NULL DEFAULT 'en' CHECK (language IN ('en', 'ru')),
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Registration is by invitation. Only a hash of the code is kept, so a copy of the database does not
-- hand out working invitations; the code itself is shown once, when it is created.
CREATE TABLE invitations (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash  bytea       NOT NULL UNIQUE,
    note       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    used_by    uuid        REFERENCES users (id) ON DELETE SET NULL,
    -- A used invitation names who used it; one that was never used names nobody.
    CHECK (used_by IS NULL OR used_at IS NOT NULL)
);

-- A browser session. The cookie holds a random token; only its SHA-256 is stored here.
-- expires_at slides forward while the session is in use; absolute_expires_at never moves.
CREATE TABLE sessions (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash          bytea       NOT NULL UNIQUE,
    created_at          timestamptz NOT NULL DEFAULT now(),
    last_seen_at        timestamptz NOT NULL DEFAULT now(),
    expires_at          timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- +goose Down
DROP TABLE sessions;
DROP TABLE invitations;
DROP TABLE users;
