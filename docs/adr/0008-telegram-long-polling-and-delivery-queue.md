# 0008. The Telegram bot uses long polling and a delivery queue that waits for the transcript

Status: accepted, 2026-10-11.

## Context

A new recording should arrive in the person's Telegram as a voice message with its transcript. Facts from the Bot API documentation (*source*): `sendVoice` accepts Ogg/Opus; a caption is at most 1024 characters and a message at most 4096; a bot can write only to a chat that has written to it first; at most about one message a second to one chat; updates are kept on Telegram's side for 24 hours; a second `getUpdates` with the same token supersedes the first (409); the token is part of every request URL, so a standard Go network error would print it into the log.

## Decision

- **One bot for the whole service**, its token in `TELEGRAM_BOT_TOKEN`; no token means no bot. Each account links **one private chat**: the web page gives a one-time link `t.me/<bot>?start=<code>` (128-bit code, 10 minutes, stored as a hash), the person presses Start, the bot links the chat.
- The bot lives **inside the Go server** and receives updates by **long polling**: no public address and no new container.
- **Delivery is a queue** like [the transcription one](0007-job-queues-in-postgresql.md), filled in the same transaction as the message and only for an account that is linked at that moment. The worker holds a recording back until its transcript is ready, **at most `TELEGRAM_WAIT` (3 minutes)**, and then sends the voice message with the text as its caption, or without text if none came.
- A long transcript goes as a reply message; silence and failed transcription go as a plain voice message; a recording in the old raw format goes as text only.
- The **Seen** button under a recording deletes it from the chat (never on the server); Telegram allows a bot to delete only within 48 hours.
- The token never reaches a log or an error text; the chat number is never given out by the API.

## Consequences

- A request that reached Telegram while the answer was lost is retried and may produce a **duplicate message**; Telegram has no idempotency key. This is documented rather than hidden.
- The bot keeps its position in the update stream in memory; after a restart Telegram may hand over updates already handled, and every handler is written so that repeating it is harmless.
- A chat that blocks the bot switches the link off and the page says so.
- Recordings made before a chat is linked are not sent.
- The path was tried against the real Telegram (the format, linking and delivery); blocking the bot, `/stop`, and running on the VPS were not.
