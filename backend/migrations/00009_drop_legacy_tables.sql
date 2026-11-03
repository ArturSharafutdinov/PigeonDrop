-- +goose Up
-- The two tables of the Node prototype's contract (00001). The routes that used them were removed on 2026-10-04 and
-- nothing in the server reads or writes them any more.
--
-- Dropping a table cannot be undone, so the migration refuses to run while either of them holds a row: it fails
-- (the whole migration is one transaction, so nothing is dropped, and the server keeps the version it had). Look at
-- the rows first (`SELECT * FROM accounts`), delete them by hand if they are not needed, and migrate again.

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounts) THEN
        RAISE EXCEPTION 'the table accounts is not empty: look at its rows, delete them if they are not needed, and migrate again';
    END IF;
    IF EXISTS (SELECT 1 FROM processed_uploads) THEN
        RAISE EXCEPTION 'the table processed_uploads is not empty: look at its rows, delete them if they are not needed, and migrate again';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TABLE processed_uploads;
DROP TABLE accounts;

-- +goose Down
-- The empty tables as 00001 made them; what they held cannot come back.
CREATE TABLE accounts (
    code              text        PRIMARY KEY,
    custom_server_url text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE processed_uploads (
    file_name      text        PRIMARY KEY,
    bytes_received bigint      NOT NULL CHECK (bytes_received >= 0),
    saved_as       text        NOT NULL,
    wav_file       text,
    received_at    timestamptz NOT NULL DEFAULT now()
);
