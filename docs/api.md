# HTTP API: what the watch and the phone rely on

The server (`backend/`) implements the `/v1` API. The machine-readable description of the whole API is [`backend/api/openapi.yaml`](../backend/api/openapi.yaml); this document covers the part the watch app and its Side Service depend on, and the rules that must survive any change of the implementation. The watch and the Side Service (`PigeonDrop/`) know only this HTTP contract, not what is behind it: if the server starts to behave differently from what is written here, they will probably break without a single changed line on their side, because they rely on exactly these formats and status codes.

The routes without `/v1` that the first Node prototype had (`/messages`, `/accounts`, `/setup`, `/uploads`) are gone; they exist only in the git history.

## Conventions

- Responses are `application/json` (except audio and the `/pair` page); request bodies are JSON.
- **Errors** are `{"ok": false, "error": "<an English phrase for the log>", "code": "<machine code>"}`; a client reads `code`. Successful `/v1` responses are not wrapped in `ok`, with one exception: the answer to an upload carries `ok: true`, which the Side Service checks.
- An unknown route or method is `404 { ok: false, error: "not found" }`; an unexpected error is `500 { ok: false, error: "internal error" }`.
- Every `/v1` answer carries `Cache-Control: no-store`.
- Health check: `GET /healthz` → `200 { ok: true }`, or `503` if the database cannot be reached.

## Pairing a watch (RFC 8628)

The watch knows nothing about users. To become "somebody's" it is paired; the Side Service makes these calls when the watch asks (`pairStart`, `pairPoll`).

1. **`POST /v1/device/code`** `{ "clientName": "Amazfit Balance" }` (the name is optional, up to 64 characters) → `200 { deviceCode, userCode, verificationUri, verificationUriComplete, expiresIn, interval }`.
   - `deviceCode` is the watch's secret (43 characters); `userCode` is a short code like `BCDF-GHJK`; `verificationUriComplete` is what the watch shows as a QR code (`<server>/pair?code=BCDF-GHJK`); `expiresIn` is the seconds until the pairing ends (600); `interval` is how often to poll (5 s).
   - Codes are rate-limited by address: `429 { code: "too_many_attempts" }` with `Retry-After`.
2. The person opens the link on their phone, signs in, compares the code with the watch's screen and approves (`POST /v1/device/approve`, with the session cookie; the watch takes no part in this).
3. **`POST /v1/device/token`** `{ "deviceCode": "..." }`, polled every `interval` seconds:
   - `200 { "status": "pending", "interval": 5 }` while nobody has approved;
   - `200 { "status": "approved", "deviceToken": "pdd_...", "deviceId": "..." }` once approved. The token is handed out **once**; repeating the same poll gets `400 invalid_device_code`;
   - `400 { code: "expired_token" }` (time ran out) or `400 { code: "invalid_device_code" }` (the code is unknown, or the token was already taken): in both cases pairing starts over;
   - `200 { "status": "relayed", "envelope": { "v": 1, "ct": "...", "tag": "..." } }` when the person handed settings for a server of their own to the pairing page (see below). Handed out **once**, like the token;
   - `400 { code: "expired_token" }` (time ran out) or `400 { code: "invalid_device_code" }` (the code is unknown, or the token or the envelope was already taken): in both cases pairing starts over;
   - `429 { code: "too_many_attempts" }` for polling faster than every 3 seconds (not counted against the code) or for too many wrong codes: wait for `Retry-After`.

The watch keeps `deviceToken` (`utils/device.js`) and sends it with every upload. The server stores only its hash.

### Handing over the settings of a server of the person's own

**`POST /v1/device/relay`** `{ "userCode": "BCDF-GHJK", "envelope": { "v": 1, "ct": "...", "tag": "..." } }`, called by the pairing page's browser, with no session. The envelope is the address and the access token of the person's own server, encrypted in the browser with a key only the watch has; the server cannot read it and keeps it **in memory only** until the watch's next poll takes it (or the pairing ends, at most 10 minutes). Sending again replaces the envelope that waits.

- `204` the envelope is waiting; `404 invalid_user_code` the code is unknown, expired, approved or closed (one answer for all of them); `400 invalid_request` the body or the envelope has the wrong shape (`ct` and `tag` base64url without padding, `ct` 32 to 1024 bytes in whole 16-byte blocks, `tag` 32 bytes); `400 unsupported_version` `v` is not 1; `413` the body is over 4 KiB; `429 too_many_attempts` for ten wrong codes from one address in 15 minutes, or when the server's memory for envelopes is full (1000).
- Delivering the envelope closes the pairing: the code can no longer be approved, and a second poll is `invalid_device_code`. An approval that arrives for a code that has an envelope waiting is dropped with the pairing.
- Nothing about the envelope is written to the database or a log (the pairing row is the same as before it arrived), and it does not survive a restart: the pairing runs out and the person starts again.

## Uploading a recording

**`POST /v1/messages`**, header `Authorization: Bearer <deviceToken>`, body `{ "messageId": "<the file name on the watch>", "audio": "<base64 of a standard Ogg/Opus file>" }`.

The server also accepts the old raw Zepp format (Opus packets with Zepp's own 8-byte header; it tells the two apart by the first four bytes, `OggS`), but the app does not send it.

**The format of `audio`.** An ordinary Ogg/Opus file (RFC 3533, RFC 7845) that `ffmpeg`, VLC and browsers open and Telegram plays as a voice message:

- one logical stream, **mono**; pages: `OpusHead`, `OpusTags` (vendor `pigeondrop`, no user tags), then audio at 50 packets per page; the last page is marked as the end of the stream;
- `OpusHead`: version 1, 1 channel, **pre-skip 0**, input rate 16000 Hz, gain 0, channel mapping family 0;
- the packets are the ones the watch recorded, SILK wideband at 20 ms, **not re-encoded**; the granule position counts 960 samples (20 ms at 48 kHz) per packet;
- a recording that was cut off in the middle of a packet is trimmed to whole packets; a recording without a single whole packet is not sent at all.

The server treats the file as untrusted input: it checks page checksums, bounds and the number of channels. If the file cannot be parsed it is still stored (`hasWav: false`).

### Answers

- **`200 { ok: true, id, messageId, bytesReceived, duplicate, receivedAt, storage, pruned }`**: the recording is stored for the device's owner. **Idempotent within a device by `messageId`:** a repeat returns the stored result (`duplicate: true`, `pruned: 0`) and stores nothing again; another device may use the same `messageId`. The recording is stored even when no audio could be decoded from it (the page then has no player, only *Download original*).
  - **`storage`** is `{ state, usedBytes, limitBytes }`: how full the account's storage is (only the recording files count). `state` is `ok` (under 90%), `nearly_full` (from 90%) or `full` (this very upload did not fit and deleted old recordings). The watch shows a warning from it. A client ignores a `state` it does not know.
  - **`pruned`** is how many of the account's oldest recordings this upload deleted to make room.
- **`401 { ok: false, code: "unauthorized" }`**: no token, an unknown one, or the device was revoked. The Side Service answers the watch `{ unauthorized: true }`; the watch forgets the token and asks to be paired again.
- **`413 { ok: false, code: "payload_too_large" }`**: the recording is over 512 KiB (a minute of audio is about 140 KiB), or the request body is over what an upload may be (about 770 KB). Repeating will not help. The Side Service answers `{ fileError: true }`: the watch skips this recording and goes on with the rest.
- **`429 { ok: false, code: "too_many_attempts" }`** with `Retry-After`: more than 60 uploads an hour from this device (every request counts, repeats too). The Side Service answers `{ rateLimited: true }`: the watch stops sending, keeps the recordings queued and tries again at the next start, connection or recording.
- `400 invalid_request`: the body is not JSON, fields are missing, `audio` is not base64, or `messageId` is longer than 255 characters or has control characters.

**Account storage.** An account holds 100 MiB of recordings. A new recording is always accepted; if it does not fit, the server deletes the account's oldest recordings (by time received) just enough for it to fit, and says so in `storage.state = "full"` and `pruned`. Rows and files are both deleted. The server cannot message the watch by itself, so the warning only arrives in the answer to an upload. The WAV for the browser is not stored: it is made from the original when played, and only the original counts against the limit.

Viewing, playing and deleting recordings by a person (`GET/DELETE /v1/messages`, `GET /v1/storage`, `/v1/devices`, the `/pair` page) is not the watch's business.

## Invariants to keep whatever the implementation

1. **`POST /v1/messages` is idempotent by `messageId` within a device.** A repeat does not create a second recording and returns the stored result. The watch relies on this when it resends from its persistent queue (`utils/uploadQueue.js`): the confirmation can be lost on the way back over Bluetooth, and without idempotence every loss would leave a duplicate.
2. **A recording is stored whether or not it could be decoded.** These are two separate chances for an error, and the second must not void the first.
3. **The `401`, `413` and `429` of an upload are exactly as described.** The Side Service decides from them what to tell the watch (`unauthorized`, `fileError`, `rateLimited`), and the watch decides to forget the token, skip the recording or stop sending.
4. **The device token is shown once** (at pairing) and only its hash is kept on the server.

## Out of scope

Processing chains beyond speech recognition and Telegram are further work and not part of this contract. A server that only receives recordings (a "Personal Server") needs a smaller part of it: see [Personal Server](personal-server.md).

## Who calls what

| Endpoint | Called by | File |
|---|---|---|
| `POST /v1/device/code`, `POST /v1/device/token` | the Side Service, when the watch asks (`pairStart`, `pairPoll`) | `PigeonDrop/app-side/pairing.js` |
| `POST /v1/messages` | the Side Service, after it has all the chunks of a recording from the watch, with the device token | `PigeonDrop/app-side/uploads.js` |
| `GET /healthz` and an empty `POST /v1/messages` | the Side Service, for the watch's *Check connection* button | `PigeonDrop/app-side/serverCheck.js` |
| `POST /v1/device/approve`, `GET /v1/device/pairing/{code}`, `GET/DELETE /v1/devices`, `GET/DELETE /v1/messages` (`DELETE` without an id deletes every recording), `GET /v1/storage`, `GET /v1/messages/{id}/audio`, `GET /v1/messages/archive` (a zip of all recordings, uncompressed, the original `.opus` files), `POST /v1/messages/{id}/transcribe`, `/v1/telegram`, `/v1/auth/*`, `/v1/me` | a person, on the `/pair` page (not the watch or the Side Service) | `backend/internal/httpapi/pair.js` |
