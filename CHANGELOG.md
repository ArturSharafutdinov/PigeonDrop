# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project does not have tagged releases yet, so everything is under *Unreleased*.

## [Unreleased]

### Added
- Watch app for Zepp OS 4.0: records with the hardware shortcut button, queues recordings on the watch and sends them to the phone in acknowledged chunks over Bluetooth; plays recordings back; shows what is wrong in words (phone not answering, server not reachable, storage full); English and Russian interface.
- Side Service on the phone: wraps the watch's recordings into standard Ogg/Opus files without re-encoding and uploads them.
- Go server with PostgreSQL: invitation-only accounts, cookie sessions, watch pairing (RFC 8628) with revocable device tokens, recordings with per-account storage limits, an OpenAPI-first `/v1` API, and a small web page to sign in, pair watches and listen.
- Speech recognition service (NVIDIA Parakeet TDT 0.6B v3, int8) fed through a queue in PostgreSQL, one recording at a time.
- Telegram bot that sends each recording and its transcript to a linked chat, with a "Seen" button that deletes it from the chat.
- English and Russian interface on the watch and on the pairing page.
- CI: tests, linting, vulnerability, secret and shell checks, image builds; deployment through Coolify.
- Backups of the database and the recordings volume with a restore check (`tools/backup`).

### Security
- Hardening found by an audit before the first public release: write deadline of the archive download, supervision of background workers, bounds on password and audio size, token check before reading a request in the speech service, `Cache-Control: no-store` on the API, up-to-date toolchain and dependencies.
