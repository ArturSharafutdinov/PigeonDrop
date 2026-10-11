-- +goose Up
-- Linking a person's account to a Telegram chat. The bot (internal/telegram) writes the link when the person opens the
-- link the page gave them and presses Start; the delivery queue that uses it comes with the next migration.
-- Design: TELEGRAM_BOT_PLAN.md.

-- The chat an account's recordings go to: one chat for an account, one account for a chat, so a new link replaces
-- the old (a person who links again, or a chat that moves to another account, does not end up with two).
CREATE TABLE telegram_links (
    user_id    uuid        PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    chat_id    bigint      NOT NULL UNIQUE,
    -- What the page shows to say whom the account is linked to. Either may be missing.
    username   text        CHECK (char_length(username) <= 64),
    first_name text        CHECK (char_length(first_name) <= 128),
    linked_at  timestamptz NOT NULL,
    -- Set when Telegram says the chat cannot be written to any more (the person blocked the bot). Nothing is sent
    -- to a blocked link; a new link (the person presses Start again) clears it.
    blocked_at timestamptz
);

-- One-time codes that turn a press of Start into a link. Only a hash of the code is kept, so a copy of the database
-- does not hand out working links; the code itself is in the t.me link, shown once.
CREATE TABLE telegram_link_codes (
    code_hash    bytea       PRIMARY KEY,
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    -- A used code is kept until it expires, so that the same Start arriving twice (the bot was restarted while it
    -- handled it) from the same chat gets "done" again instead of "no such link".
    used_at      timestamptz,
    used_chat_id bigint,
    CHECK ((used_at IS NULL) = (used_chat_id IS NULL))
);

CREATE INDEX telegram_link_codes_user_idx ON telegram_link_codes (user_id);
CREATE INDEX telegram_link_codes_expires_idx ON telegram_link_codes (expires_at);

-- +goose Down
DROP TABLE telegram_link_codes;
DROP TABLE telegram_links;
