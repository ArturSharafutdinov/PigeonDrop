# Backend (Go)

The PigeonDrop server: it accepts recordings from the watches, stores and converts them, queues them for speech recognition and for Telegram, and serves the small web page where a person signs in, pairs a watch and listens. It replaced a Node prototype (removed on 2026-10-04, it remains in the git history). The choice of stack is recorded in [`docs/adr/0001-go-and-postgresql.md`](../docs/adr/0001-go-and-postgresql.md).

For the system as a whole see [`docs/architecture.md`](../docs/architecture.md); the HTTP contract the watch relies on is [`docs/api.md`](../docs/api.md), the full contract is [`api/openapi.yaml`](api/openapi.yaml); deployment is [`docs/deployment.md`](../docs/deployment.md).

## Quick start

You need Go 1.26 and the PostgreSQL server programs (`brew install postgresql@16`). The server needs PostgreSQL 13 or newer (`gen_random_uuid()`); production runs 17 and CI tests against 17, 16 is enough locally.

```bash
cd backend
scripts/dev-db.sh up        # a throwaway PostgreSQL on 127.0.0.1:55432, data in .dev/ (it touches no other database)
make test                   # the tests; the database comes from TEST_DATABASE_URL, by default from dev-db.sh
make run                    # the server on :3000, migrations are applied at start
curl localhost:3000/healthz
scripts/dev-db.sh down      # stop it
```

The tests create a database of their own with a random name and drop it afterwards. If there is no database the tests are skipped; with `REQUIRE_DB=1` (as in CI) they fail.

## Layout

| Path | What is there |
|---|---|
| `api/openapi.yaml` | **The HTTP API contract, the source of truth.** The code is generated from it |
| `internal/api` | Generated types and routes (`make generate`); never edit by hand |
| `internal/httpapi` | Handlers (`auth.go` sign-in and sessions; `devices.go` pairing and device tokens; `recordings.go` recordings with an owner; `telegram.go`, `transcripts.go`; `pair.go` with the `pair.*` files and audio playback are hand-written routes for the browser) and the common middleware (logs, panic recovery, JSON errors, request body limit, protection against requests from other sites) |
| `internal/auth` | What needs neither the database nor HTTP: Argon2id password hashing, random tokens, invitation codes, e-mail and password checks, the rate limiter |
| `internal/audio` | Parsing Ogg/Opus and the Zepp format, decoding Opus (pure Go), WAV. The recordings in `testdata/` are synthetic; `generate.mjs` says how to recreate them |
| `internal/storage` | File storage behind an interface: a directory on disk now, possibly S3 later |
| `internal/transcribe` | Transcription: the client of the speech service (`stt/`), the queue in PostgreSQL and the worker that takes recordings one at a time |
| `internal/telegram` | The Telegram bot: the Bot API client (`client.go`: errors by kind, the token never reaches an error text), linking a chat to an account and the bot's commands (`bot.go`), one-time codes (`link.go`), the delivery queue and its worker (`deliver.go`), the "Seen" button (`seen.go`), the replies in two languages (`texts.go`) |
| `internal/supervise` | Runs a background loop and restarts it, after a pause, when it panics |
| `internal/config` | Settings from environment variables |
| `internal/db` | The connection pool and migrations; `queries/` holds the SQL queries sqlc generates `dbgen/` from |
| `internal/testdb` | A separate database for every test |
| `migrations/` | SQL migrations (goose), embedded in the binary |
| `cmd/pigeondrop` | The entry point: `serve`, `migrate`, `healthcheck`, `version` and the operator commands `invite`, `user`, `stt`, `telegram` |
| `tools/` | A separate module with the generators (oapi-codegen, sqlc), so that the server's dependencies stay small |

## Commands

```bash
make generate   # after editing api/openapi.yaml or the SQL in internal/db/queries
make check      # gofmt + go vet + tests + "generated code is up to date" (what CI runs)
make lint       # golangci-lint, the version CI uses
make vuln       # govulncheck
make build      # bin/pigeondrop
```

The program's own commands:

```bash
pigeondrop serve            # the HTTP server
pigeondrop migrate up       # apply migrations
pigeondrop migrate status   # the state of the migrations
pigeondrop healthcheck      # exit code 0 if the local server answers (for HEALTHCHECK in Docker)
pigeondrop invite create    # a new invitation code (printed once); flags -expires 168h -note "who for"
pigeondrop invite list      # invitations: free, used by whom, expired
pigeondrop invite revoke ID # revoke an unused one
pigeondrop user list        # users
pigeondrop user set-password EMAIL   # a new password (asks twice or reads stdin); all the user's sessions end
pigeondrop stt check                 # transcription: send the service (STT_URL, STT_TOKEN) a second of silence and say whether it answered; does not touch the database
pigeondrop stt status                # transcription: the queue by state, how long the oldest waits, failures, the last error
pigeondrop stt enqueue -missing      # queue recordings that never got into the queue (from before transcription was switched on); -max-audio 65s
pigeondrop stt retry -failed         # return every recording that was given up on to the queue (from scratch, at the end)
pigeondrop telegram check            # Telegram bot: ask Telegram (TELEGRAM_BOT_TOKEN) who the bot is and whether a webhook is set; does not touch the database
pigeondrop telegram status           # Telegram bot: how many accounts are linked (and how many blocked the bot), the delivery queue by state, how long the oldest waits, failures
```

## Settings (environment variables)

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | none (required) | The PostgreSQL connection string |
| `PORT` | `3000` | The HTTP port |
| `STORAGE_DIR` | `./data` (`/data` in the image) | Where recordings are kept; in Docker it is a volume |
| `DEFAULT_SERVER_URL` | `http://localhost:$PORT` | The server's public address; the pairing QR code's link is built from it |
| `MIGRATE_ON_START` | `false` | Apply migrations at start (for a single instance); otherwise run `migrate up` as a separate step |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `MAX_DB_CONNS` | `10` | Pool size: the PostgreSQL may be shared with other projects |
| `COOKIE_SECURE` | `true` | The session cookie over HTTPS only. `false` is for development over plain http (`make run` sets it) |
| `ALLOWED_ORIGINS` | none | Other sites (`https://app.example.com`, comma separated) whose requests the cross-origin check lets through to `/v1` with a session, for example a front end's dev server. It feeds only that check: no CORS headers are ever sent |
| `STORAGE_ACCOUNT_BYTES` | `104857600` (100 MiB) | How much space one account's recordings take; beyond it the oldest are deleted. At least 100000 (a guard against a typo). A small limit is handy for trying the warning on the watch; a recording that alone is larger than the limit is still accepted and the others are deleted |
| `TRUST_PROXY_HEADERS` | `false` | Read the client's address from `X-Forwarded-For` (for the sign-in rate limit). Turn on only behind a proxy that sets the header itself (Traefik in Coolify) |
| `STT_URL` | none | The speech service's address (`http://name:8000`, no path or password). **Empty: transcription is off**, recordings are not queued, no worker starts |
| `STT_TOKEN` | none | The secret shared with the service (`Authorization: Bearer`). Cannot be set without `STT_URL` (a typo: transcription would silently stay off) |
| `STT_TIMEOUT` | `90s` | How long to wait for one answer, `1s` to `10m`. A recording's lease while the worker has it is this value plus 30 seconds |
| `STT_MAX_ATTEMPTS` | `5` | How many counted attempts before a recording is given up on (1 to 20) |
| `STT_MAX_AUDIO_SECONDS` | `65` | The longest recording sent to the model. The model's memory was measured up to 60 seconds; longer ones are accepted and stored but not transcribed (`too_long`) |
| `TELEGRAM_BOT_TOKEN` | none | The bot's token from @BotFather. **Empty: the Telegram bot is off** (no section on the page, nothing is sent). It is printed nowhere, not even in the message that it is wrong |
| `TELEGRAM_API_URL` | `https://api.telegram.org` | The Bot API address (`scheme://host[:port]`) for tests or your own Bot API server. Cannot be set without a token (a typo) |
| `TELEGRAM_WAIT` | `3m` | How long a recording waits for its transcript before being sent to Telegram (`1s` to `30m`); if it is not ready it goes without text |
| `TELEGRAM_TIMEOUT` | `30s` | How long to wait for Telegram's answer to one call (`1s` to `5m`) |

If the database has unapplied migrations, `serve` does not start and says so.

Recordings are in `STORAGE_DIR/messages`. The log goes to stdout as JSON.

## Accounts and sign-in (`/v1`)

The requests are described in `api/openapi.yaml`. This is what it does not say: how it works and how to use it.

**Registration is by invitation only.** The operator creates a code with `pigeondrop invite create` and gives it to the person, who registers with `POST /v1/auth/register` (code, e-mail, password). A code is single-use and lives a week by default. Only its hash is in the database, so a copy of the database yields no working invitation. The code is spent in the same transaction that creates the user: an error (e-mail taken, weak password) does not burn it, and if two people come with one code at once, only one gets an account.

**The first user.** Needs an invitation too, so after the first start run `pigeondrop invite create` on the server. The image has no shell and needs none: `docker exec <container> /pigeondrop invite create` (for a password: `docker exec -it <container> /pigeondrop user set-password email`). Coolify's terminal does not do for this: the image has no `sh`.

**Passwords.** Argon2id (19 MiB, 2 passes, 1 thread: the lowest of OWASP's recommended settings, chosen for a shared VPS), at most four hashes at a time. The parameters are stored inside the hash, so when they change a password is rehashed at the next sign-in. The length is 12 characters (characters, not bytes) to 256 bytes; a request body with a password over 1024 bytes as sent is refused before any work is done, so that nobody can make the server hash megabytes. Before hashing the text is normalized to NFKC so that one password typed on different keyboards matches. **What is missing:** a check against a list of leaked passwords.

**Sessions.** A random token (256 bits) is in an `HttpOnly; SameSite=Lax` cookie (over HTTPS also `Secure` and a `__Host-` name prefix); only its SHA-256 is in the database. A session lives 30 days from last use, renewed at most once an hour and for at most 90 days from sign-in. Changing the password ends all the user's other sessions. Expired sessions are deleted hourly.

**Brute-force protection.** In the server's memory (counters reset on restart and each instance has its own; enough for one small server): 5 failed sign-ins per e-mail in 15 minutes, 20 per client address in 15 minutes, 10 wrong invitation codes per address per hour; after that, 429 with `Retry-After`. Non-existent e-mails count too, so the answers do not reveal who is registered; an unknown e-mail costs the server as much time as a wrong password. An IPv6 address counts by its /64 network.

**Requests from other sites.** For `/v1` paths unsafe requests (POST and the like) with a foreign `Origin` or `Sec-Fetch-Site: cross-site` get 403 `forbidden_origin` (the standard `http.CrossOriginProtection`); apps and `curl` do not send these headers and pass.

**Errors** in `/v1` carry a machine code (`code`): clients translate them themselves; the text in `error` is English and for the log. Every `/v1` answer carries `Cache-Control: no-store`.

**What is missing:** e-mails (e-mail confirmation, password reset: the operator resets with `user set-password`), changing the e-mail or language, deleting an account, listing and revoking individual sessions, sign-in through third-party services.

## Watches: pairing and device tokens (`/v1/device/*`, `/v1/devices`)

A watch proves whose it is with a **device token**. Pairing follows RFC 8628 (the Device Authorization Grant): a device without a convenient input shows a code and a person confirms it on the phone.

```
watch ──(through the phone)──► POST /v1/device/code      { clientName }
                               ◄── deviceCode (the watch's secret), userCode (BCDF-GHJK), verificationUriComplete, expiresIn 600, interval 5
the watch shows a QR code (verificationUriComplete) and the userCode, and polls every 5 s:
watch ───────────────────────► POST /v1/device/token      { deviceCode }  →  { status: "pending" }
the person: opens /pair?code=..., signs in, compares the code with the watch, presses "Pair"
                               POST /v1/device/approve    { userCode, name }      (with the session cookie)
watch ───────────────────────► POST /v1/device/token      →  { status: "approved", deviceToken, deviceId }   (once)
```

- **The token** is `pdd_` and 43 random characters (256 bits). Only its SHA-256 is in the database; it cannot be recovered. If the answer carrying the token is lost on the way, pairing starts over and the extra device on the page can be revoked.
- **The code for a person** is 8 consonants out of 20 (as RFC 8628 advises: no vowels, no 0/O or 1/I confusion), 34 bits. That is safe only because it lives 10 minutes and wrong guesses are limited: 10 per user in 15 minutes. Approval gives another 2 minutes to collect the token.
- **Protection against a swapped link.** The link to the approval can be made to point at someone else's watch; the page shows the code large and asks to compare it with the watch's screen. The watch's name comes from the watch itself and is shown only as text.
- **Limits:** issuing codes is 10 per address in 10 minutes (each leaves a row in the database), polling with a non-existent code is 30 per address in 15 minutes, polling more often than every 3 seconds is refused (429 and `Retry-After`) and not counted.
- **Revoking:** `DELETE /v1/devices/{id}` (from the page). The token stops working at once; the device's recordings stay. Expired pairings are deleted hourly.
- The token goes in the header `Authorization: Bearer ...` (the `deviceAuth` scheme in `api/openapi.yaml`). A device does not need a person's session, and a device token does not replace a session for a person.

## Recordings (`/v1/messages`)

Only a device with a token uploads: `POST /v1/messages { messageId, audio }`. Every recording has an owner, and idempotence is counted **within a device** (`messageId` is the file name on the watch): two devices can have identical names and they do not meet.

- The file is in `STORAGE_DIR/messages`, named after the message id (`<id>.opus`, as received). A name sent by a client never becomes a path. A recording is stored even if it could not be decoded (`hasWav: false`: no player on the page, the original can be downloaded). **The WAV for the browser is not stored:** it is made from the original at every playback (about 30 ms for a minute, at most four at a time), so the disk holds only what counts against the limit. A `<id>.wav` left by an earlier version is deleted with the recording.
- A person sees only their own: `GET /v1/messages` (newest first, paged by `before`), `GET /v1/messages/{id}/audio` (WAV by default or `?format=original`; with ranges so that a player can seek), `GET /v1/messages/archive` (all a person's recordings as one zip, below), `DELETE /v1/messages/{id}` (the row and the files). The ownership check is in the SQL query itself, not in the handler, so it cannot be forgotten; someone else's recording is "not found" for every request.
- **The archive of all recordings.** `GET /v1/messages/archive` returns a zip of the originals of all the person's recordings (`.opus` exactly as received), uncompressed (the audio is already compressed), with the standard library's `archive/zip`; **next to a recording whose transcript is ready and not empty there is a `.txt` with the same name** (UTF-8, the text and a newline; compressed, unlike the audio; silence, waiting and failure get no file). It is written as a stream, file by file, so it is never in memory as a whole. Files are named by the time received (`PigeonDrop-2026-10-08_12-30-15.opus`, UTC, as when downloading one recording; recordings of one second get `_2`, `_3`), the archive itself by the time of the request (`PigeonDrop-<date>_<time>.zip`). Old recordings come first. No recordings: `404 not_found`. A file that disappeared between reading the list and packing is skipped with a warning in the log; a read error in the middle of the stream ends the archive without its central directory, so unpackers see that it is incomplete. The archive is at most the account's limit (100 MiB), so the write deadline of the answer is extended by 2 minutes per file instead of once for the whole answer. Large archives over a slow link have not been tried on a real network.
- **Upload limits.** A recording is at most 512 KiB (a minute of audio is about 140 KiB): larger gets `413` with the code `payload_too_large`; the request body is limited the same way (about 770 KB instead of the general 8 MiB). A device may upload at most 60 recordings an hour (every request counts, repeats too): beyond that `429` and `Retry-After`. It protects against a looping watch and a stolen token; without it the automatic deletion below would let them push real recordings out with junk. The counter is in the process's memory, like the other limiters.
- **Account storage: 100 MiB**, changed with `STORAGE_ACCOUNT_BYTES`. Only the originals' files count (the sum of `bytes_received`, computed in the database, not kept as a counter). A new recording is always accepted: if it does not fit, the account's oldest recordings are deleted (the row and the file) just enough for it to fit. All this (the insert, deleting old ones, counting) happens in one transaction under a lock on the user's row, so parallel uploads from several watches cannot exceed the limit; the files of deleted rows are erased after the commit (a failure in between leaves extra files, never rows without files). The answer to an upload carries `storage` (`ok`, `nearly_full` from 90%, `full` if something had to be deleted) and `pruned`; the watch warns the person from it. The server cannot write to the watch by itself. A person frees space by deleting recordings one by one or with the *Delete all recordings* button (`DELETE /v1/messages`); usage is shown on the page and in `GET /v1/storage`.
- The Node prototype's routes without `/v1` (`/messages`, `/accounts`, `/setup`, `/uploads`) were removed on 2026-10-04, and the tables `accounts` and `processed_uploads` are dropped by migration `00009`. Dropping is irreversible, so it **refuses to run while either table has a row** (the message names the table; the migration is one transaction, nothing is erased, and the server stays on the previous version). Look at them on the server before it is deployed (through the PostgreSQL container, as in `tools/backup/README.md`): `docker exec -i <postgres> sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -U "$POSTGRES_USER" -h 127.0.0.1 -d pigeondrop -c "SELECT (SELECT count(*) FROM accounts) AS accounts, (SELECT count(*) FROM processed_uploads) AS processed_uploads"'` (both zero: safe). If there are rows and they are not needed, delete them by hand and the migration will pass.

## Transcription (the queue)

Switched on with `STT_URL`. The service itself: [`stt/README.md`](../stt/README.md); the reasoning: [`docs/adr/0006-one-recognition-at-a-time.md`](../docs/adr/0006-one-recognition-at-a-time.md).

- **Queueing.** A new recording gets a row in `transcriptions` in the same transaction as its `messages` row: a recording cannot be accepted without a place in the queue, and if the upload rolls back there is no recording, no file and no place (the watch will repeat). Repeating the same upload creates no second row. A recording that cannot be decoded or is longer than `STT_MAX_AUDIO_SECONDS` gets the state `skipped` with a reason and is not sent to the model. The row goes with its recording (deletion by the person, eviction by the quota, deleting the account): a text never outlives its recording.
- **The worker.** One goroutine next to the HTTP server takes the oldest ready recording (`FOR UPDATE SKIP LOCKED`, so two servers during a redeploy do not take the same one), decodes the original to WAV (`audio.ToWAV`) and sends it to the service. A new recording wakes the worker at once, otherwise it checks the queue every 2 seconds. The upload from the watch does not wait for transcription. It runs under `internal/supervise`: a panic is logged with its stack and the loop restarts after a pause.
- **States** (`transcriptions.state`): `pending` (waiting, not before `run_after`), `running` (taken by a worker while the lease `locked_until` has not passed), `done` (there is text, possibly empty: silence), `failed` and `skipped` (the reason is in `error_code`: `not_decodable`, `too_long`, `rejected`, `too_many_failures`, `file_missing`).
- **Failures are told apart by whose fault they are** (`internal/transcribe/client.go`): the service being unavailable (the request did not complete, 502/503/504, a wrong token or address) and "busy" (429) do **not** count as an attempt, and unavailability also stops the worker for 15 seconds, 30, 1 minute … up to 5 minutes (the service's downtime does not burn the attempts of the whole queue); a timeout, a drop after sending and a 500 **do** count (a recording on which the model hangs or crashes must not be repeated forever), with pauses of 30 seconds, 2 minutes, 10 minutes, 1 hour, 6 hours, then `failed`; the service refusing to take the recording (400, 413) is `failed` at once.
- **Reliability.** If the worker dies the lease expires and the recording is taken again (the attempt counts; if that happened more often than allowed the recording is declared `failed` without reaching the model). A result is written only if the recording still belongs to this attempt: a late result after a change of owner or deletion of the recording is discarded. When the server stops (SIGTERM) the worker returns the recording to the queue at once, together with the attempt.
- **The log.** Per recording: the identifier, the attempt number, the call's identifier (the service writes the same one), durations and the number of characters. The text of a recording is not in the log.
- **Testing on the real parts.** The queue's tests run on a real PostgreSQL with a fake model (failures, races, leases and stopping are tested; each protection was checked by breaking it). The end-to-end test `internal/transcribe/e2e_test.go` is skipped without variables and works with a running `stt/` service (the real model): `STT_E2E_URL=http://127.0.0.1:18000 STT_E2E_TOKEN=... STT_E2E_FILE=recording.opus STT_E2E_WANT='the expected text' go test -run EndToEnd ./internal/transcribe/`.
- **The text in the API.** `GET /v1/messages` gives every recording an optional field `transcript` (`state` and, when there is one, `text`, `engine`, `language`, `finishedAt`, `errorCode`; the contract is in `api/openapi.yaml`). A recording that was never in the queue (accepted before transcription was switched on and not added by the operator command) has no such field. The field is shown even when transcription is off while rows exist in the table: finished texts belong to the person and do not disappear because the operator removed `STT_URL`. The service's error message (`last_error`) is never given out.
- **Retry.** `POST /v1/messages/{id}/transcribe` returns a person's `failed` recording to the queue (`202` and the new state `pending`; attempts from zero, at the end of the queue, the worker is woken). Retrying would do nothing for the other states, so `409 conflict`: waiting or being transcribed, done, `skipped` (not decodable or too long), the recording has no queue row, or transcription is off on this server (otherwise the recording would wait forever). Someone else's or a non-existent recording: `404 not_found`. There is no separate rate limit: only a `failed` recording can be retried and after a retry it is already `pending`, so a second request in a row gets `409`.
- **Operator commands** (`pigeondrop stt ...`, inside the container or wherever the database is reachable; the server's worker picks the work up by itself within 2 seconds): `status` (the number of recordings by state, the age of the oldest waiting one, how many gave up in the last day, the latest failure with the service's message), `enqueue -missing` (old recordings without a queue row; by age, the oldest first, at the end of the queue; those that cannot be decoded or are longer than `-max-audio` are `skipped` at once; without the flag the command does nothing), `retry -failed` (every `failed` one; `skipped` is not touched). If `STT_URL` is not set in the command's environment it warns: perhaps the server runs without it and nobody takes the queue.
- **The `/pair` page** shows under a recording *Waiting to be transcribed…*, *Transcribing…*, the text, *No speech detected.*, *Could not transcribe. Retry* or the reason it was skipped; while the page has waiting recordings it rereads the list every 3 seconds (it does not ask on a hidden tab and asks at once when the tab is shown). Above the list there is a credit for the model and a note that the text may contain mistakes.
- **Credit for the model.** The text is made by NVIDIA Parakeet TDT 0.6B v3 (`nvidia/parakeet-tdt-0.6b-v3`, license [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)), in the form istupakov published it for the `onnx-asr` library (the ONNX conversion `istupakov/parakeet-tdt-0.6b-v3-onnx`, the int8 variant). The model was not fine-tuned and nothing was changed by us. The note with the name, the license and a link is on the `/pair` page; it must not be removed while the model is used, and any future front end and privacy policy must keep the credit (see also [`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md)).
- **Texts are personal data.** They are in the same database, go into its backups (`tools/backup`) and stay there up to a year after the recording is deleted; they are deleted by cascade with the recording and with the account; they are in the *Download all* archive. This has to be in the privacy policy before a public service is offered.
- **Testing.** Tests on a real PostgreSQL (`internal/httpapi/transcripts_test.go`, `cmd/pigeondrop/stt_test.go`): every state in the list, nobody sees someone else's text, retry (every allowed and disallowed state, other people's recordings, no session and with a watch token, a foreign site, transcription off), the queue's order after a retry, the archive with texts; the page was checked in a browser on a real server and a model container (retry, waiting and the text appearing by themselves).

## Telegram: linking a chat and sending recordings (`/v1/telegram`)

- **Switching on.** The bot runs only if `TELEGRAM_BOT_TOKEN` is set. Without it `GET /v1/telegram` answers `available: false`, there is no section on the page and `POST /v1/telegram/link` answers `503 unavailable`. To check the connection to Telegram: `pigeondrop telegram check` (who the bot is, whether a webhook is set; does not touch the database).
- **Linking.** On `/pair` a person presses *Link Telegram*: `POST /v1/telegram/link` creates a one-time code (128 bits, lives 10 minutes, only its hash is in the database) and answers with a link `https://t.me/<bot>?start=<code>`. The person opens it in Telegram and presses Start; the bot the server runs next to the HTTP server (long polling, Telegram needs no public address) receives `/start <code>`, links that chat to the account and answers "Done". The page notices the link by itself (every 3 seconds while the link is open and the tab is shown). A new link voids the previous unused code; a repeated Start from the same chat (the bot was restarted in the middle of handling) answers "Done" again, from another chat it is refused. An expired, used and unknown code are answered the same way ("the link is not valid").
- **One chat per account and one account per chat.** A new link replaces the previous one: the chat leaves the account it was linked to, and the account gets the new chat and loses the blocked mark. The bot works only in private chats (in a group it answers that it works in private messages, and a group cannot be linked).
- **The bot's commands.** `/start <code>`, `/start` without a code (explains what to do), `/stop` (unlink this chat), `/help` and any other text. The replies are in Russian or English: after linking by the account's language, before it by the language of the Telegram app. The bot sets its own command menu at start.
- **The API.** `GET /v1/telegram` (`available`, `botUsername` and, if linked, `link`: `username`, `firstName`, `linkedAt`, `blocked`; the chat number is never given out), `POST /v1/telegram/link` (at most 10 links an hour per user, 429 with `Retry-After`), `DELETE /v1/telegram` (removes the link and the unused codes; repeating it is 204 too). The contract is in `api/openapi.yaml`.
- **The bot under failure.** Telegram unavailable at start: the bot repeats `getMe` with pauses from 5 seconds to 5 minutes while the server runs (the section on the page is hidden meanwhile). Another process with the same token or a set webhook (answer 409): a pause and a retry, one warning in the log per failure. A message that could not be handled does not lose the following ones by answering "try again". The log has no token, no chat number and no codes; it has the link's `user_id`. The bot keeps its position in the update stream in memory, so after a restart Telegram may hand over updates the bot already handled; the handlers are written so that this is harmless (a repeated `/start <code>` is answered the same, "Seen" treats an already deleted message as done, a repeated `/stop` says "not linked").
- **Sending recordings (the `telegram_deliveries` queue).** A new recording of an account that has a working link (not blocked) gets a queue row in the same transaction as its `messages` row; an account without a link has no row, and linking after the upload does not send old recordings. The worker (one goroutine next to the HTTP server, built like the transcription one: `FOR UPDATE SKIP LOCKED`, a lease, fencing by the attempt number, run under `internal/supervise`) takes the oldest ready recording. It is **ready** when transcription is no longer running (done, failed, skipped, or recognition is off and the recording is not in that queue) or `TELEGRAM_WAIT` (3 minutes by default) has passed: then the voice message goes out without text.
- **What arrives in the chat.** One `sendVoice` with the original (Ogg/Opus as it is), the duration and the transcript as the caption if it fits in 1024 UTF-16 units (that is how Telegram counts; an emoji is two). Longer: the voice message without a caption and the text after it (up to 4096, longer is cut with "…") as a reply. Silence and a failed transcription: a voice message without a caption. A recording in the old Zepp format (not Ogg) cannot be played by Telegram: only the text goes, and without text the recording is skipped (`no_audio`). If the person forbade voice messages in privacy settings (`VOICE_MESSAGES_FORBIDDEN`), the same goes as a file (`sendDocument`). At least a second between messages to one chat.
- **The *Seen* button.** Under every message of a recording (the voice message, the text as a separate message, the file, the text of an old recording) there is a *Seen* button (*Просмотрено* for an account in Russian). Pressing it deletes both messages of the recording from the chat (the voice message and the text) without confirmation; the recording and the transcript **stay on the server** (the `/pair` page, the archive). Telegram lets a bot delete messages only within 48 hours: on an older message the button is removed and the person gets a pop-up asking them to delete the message themselves. The button carries the recording's id (`seen:<uuid>`); which of the chat's messages belong to it the bot takes from `telegram_deliveries` (`voice_message_id`, `text_message_id`), only for the chat the press came from. Messages sent before the button existed do not get it.
- **When Telegram answers otherwise than hoped.** Unavailable (no network, 5xx, the token rejected): the recording returns to the queue with a pause of 15 seconds, then 30, up to 5 minutes, and the attempt is not spent; a rejected token is logged as an error (`TELEGRAM_BOT_TOKEN`). 429: a pause of at least `retry_after` for the whole worker, the attempt is not spent. The chat blocked the bot or disappeared (403, "chat not found"): the link is marked blocked, the recording is skipped (`blocked`), new recordings of that account are not queued until the person links a chat again. Telegram refused the message (other 400s): `failed` (`rejected`), a retry will not help. The request went out and there is no answer (a timeout, a drop): the outcome is unknown, the attempt is spent, a retry after 30 seconds, 2 minutes, 10 minutes, an hour, 6 hours, after the fifth failure `failed` (`too_many_failures`); **a duplicate message is possible here**, Telegram has no idempotency key. If the voice message went out and the text after it did not, a retry does not send the voice message a second time. Stopping the server returns the recording to the queue and the attempt is not lost. The log has no transcripts, no chat numbers and no token.
- **Deletion.** A recording deleted by the person or evicted by the quota (and a whole account) takes its queue row with it: nothing of someone else's or deleted goes to a chat.
- **Testing.** Tests on a real PostgreSQL with a fake Telegram (`internal/telegram/bot_test.go`, `internal/httpapi/telegram_test.go`, including the whole path: the link from the page, Start, the bot, the link showing on the page). The page's section was checked in a browser against a real server and a fake Bot API. The whole chain was checked on real parts (the model container, a real server, real recordings, a fake Bot API accepting `sendVoice`): the transcripts arrived as captions, silence without a caption, duration and order kept. The format was checked on the real Telegram (2026-10-11): five recordings by a real client, all accepted as voice messages (the duration measured correctly, a caption of 797 units and the text as a reply accepted). Linking through a real bot and the queue were checked on the real Telegram locally the same day: the link from the page, Start, five recordings uploaded as from a watch went to the chat as voice messages with transcripts in 9 seconds. **Not tried live:** the bot being blocked, `/stop`, `VOICE_MESSAGES_FORBIDDEN`, running on a VPS, the *Seen* button (tests on a fake Telegram cover every branch of the press; the texts of Telegram's answers to `deleteMessage` are taken from secondary sources).

## The `/pair` page

A few small static files (`pair.html`, `pair.js`, `pair.css`, and `pair.texts.js`, which the server builds from `pair.en.json` and `pair.ru.json`), no build step: sign-in or registration by invitation, confirming a watch's code, a list of devices with *Revoke*, a list of recordings with a player, the recording's text (see the section on transcription), *Download original* and *Delete*, a *Download all* button next to the *Recordings* heading (an ordinary link to the archive, the browser downloads it; hidden while there are no recordings), how much space is used (with a warning from 90%) and *Delete all recordings*. It is a temporary replacement for a real front end. It speaks English and Russian: the page starts in the language the person chose with the EN / RU buttons (kept in the browser), else the account's language once signed in, else the browser's; registration saves the page's language as the account's. The page's security policy is strict (`default-src 'none'`, its own script and style, nothing inline), so the markup can have neither inline handlers nor `style=`: a test checks it. Everything that comes from the server or a watch is put on the page as text, not as markup.

**What is missing:** different quotas for different plans (one limit for everyone: 100 MiB), protecting not yet delivered recordings from the automatic deletion (when chains appear it will have to be decided whether it is needed), deleting files when a user is deleted (deleting users does not exist yet either), renaming a device, silent token renewal.

## Working rules

- Code and its comments are in English, and so are the documents (see [`CONTRIBUTING.md`](../CONTRIBUTING.md)). Russian is only a language of the interface.
- The contract is changed **first in `api/openapi.yaml`**, then `make generate`, then the code. The same with SQL: write the query in `internal/db/queries` and get the code from sqlc. CI checks that what is generated matches what is committed.
- Input is untrusted: every length is checked, what cannot be parsed is an error and not a panic (the parsers have fuzz tests: `go test -run '^$' -fuzz FuzzPackets ./internal/audio`).
- Migrations only forward and only as new files; already applied ones are not edited (comments aside).
- Changes go through branches and pull requests.

## Docker

```bash
docker build -t pigeondrop-backend backend
```

The image is one static binary, does not run as root; the state (`STORAGE_DIR=/data`) must be kept on a volume. Deploying to a VPS through Coolify and GitHub Actions (the image is built in Actions and pushed to GHCR): [`docs/deployment.md`](../docs/deployment.md).
