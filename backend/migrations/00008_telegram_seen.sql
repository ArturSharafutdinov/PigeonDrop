-- +goose Up
-- The "Seen" button under a recording in the chat deletes the recording's messages from the chat. A recording
-- is one message (a voice message with its text as the caption) or two (a voice message and, as a reply to it, the
-- text that was too long for a caption). The voice message was already remembered (voice_message_id); this is the
-- other one. Both are looked up by the recording's id, which the button carries.
ALTER TABLE telegram_deliveries ADD COLUMN text_message_id bigint;

-- +goose Down
ALTER TABLE telegram_deliveries DROP COLUMN text_message_id;
