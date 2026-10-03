-- +goose Up
-- Watches belonging to a user, and the short-lived requests by which a watch gets paired with one.

-- A paired watch. It proves itself with a token; only the token's SHA-256 is kept here. A revoked device is
-- kept (not deleted) so that what it uploaded still says where it came from.
CREATE TABLE devices (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    token_hash   bytea       NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz,
    revoked_at   timestamptz
);

CREATE INDEX devices_user_id_idx ON devices (user_id);

-- A pairing in progress (RFC 8628, the device authorization grant). The watch asks for it, shows user_code,
-- and polls with the device code (only its hash is stored); a signed-in person approves the user code; the
-- next poll turns the approval into a device and its token, once (redeemed_at).
CREATE TABLE device_pairings (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    device_code_hash bytea       NOT NULL UNIQUE,
    user_code        text        NOT NULL UNIQUE CHECK (user_code ~ '^[BCDFGHJKLMNPQRSTVWXZ]{8}$'),
    -- What the watch calls itself; a suggestion for the device's name, shown to the person approving.
    client_name      text        CHECK (char_length(client_name) <= 64),
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    last_polled_at   timestamptz,
    user_id          uuid        REFERENCES users (id) ON DELETE CASCADE,
    device_name      text        CHECK (char_length(device_name) BETWEEN 1 AND 64),
    approved_at      timestamptz,
    redeemed_at      timestamptz,
    -- Approval sets all three together; redemption only follows approval.
    CHECK ((approved_at IS NULL) = (user_id IS NULL) AND (approved_at IS NULL) = (device_name IS NULL)),
    CHECK (redeemed_at IS NULL OR approved_at IS NOT NULL)
);

-- +goose Down
DROP TABLE device_pairings;
DROP TABLE devices;
