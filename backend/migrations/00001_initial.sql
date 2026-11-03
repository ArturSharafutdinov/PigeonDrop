-- +goose Up
-- The tables behind today's HTTP contract (see docs/api.md).
-- They deliberately mirror what the Node prototype kept in JSON files; the account
-- and message model of the real service replaces them in later migrations.

CREATE TABLE accounts (
    code              text        PRIMARY KEY,
    custom_server_url text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- One row per recording that was accepted. file_name is the name the watch gave the
-- recording and is the idempotency key: a repeated upload with the same name returns
-- the stored result instead of saving a second copy.
CREATE TABLE processed_uploads (
    file_name      text        PRIMARY KEY,
    bytes_received bigint      NOT NULL CHECK (bytes_received >= 0),
    saved_as       text        NOT NULL,
    wav_file       text,
    received_at    timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE processed_uploads;
DROP TABLE accounts;
