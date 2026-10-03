-- +goose Up
-- Recordings received from paired watches (the new /v1 API). The files themselves are in the message store
-- (STORAGE_DIR/messages), named after the message id: <id>.opus is the recording as it was received, <id>.wav
-- the converted copy when conversion worked. The older upload table stays for the prototype's contract.

CREATE TABLE messages (
    -- Chosen by the server before the files are written, so that it can name them.
    id                uuid        PRIMARY KEY,
    user_id           uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    device_id         uuid        NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    -- The watch's own name for the recording (its file name). It is the idempotency key, but only within one
    -- device: two devices can use the same name without meeting.
    client_message_id text        NOT NULL CHECK (char_length(client_message_id) BETWEEN 1 AND 255),
    format            text        NOT NULL CHECK (format IN ('ogg-opus', 'zepp')),
    bytes_received    bigint      NOT NULL CHECK (bytes_received >= 0),
    -- Length of the audio, known when conversion worked.
    duration_ms       integer     CHECK (duration_ms >= 0),
    has_wav           boolean     NOT NULL,
    received_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (device_id, client_message_id)
);

-- A user's messages, newest first, which is how they are listed.
CREATE INDEX messages_user_received_idx ON messages (user_id, received_at DESC, id DESC);

-- +goose Down
DROP TABLE messages;
