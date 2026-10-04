# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repo layout

Independent projects living side by side, connected only by the runtime data flow described below (no shared package.json, no monorepo tooling):

- **`PigeonDrop/`** — the Zepp OS 4.0 Mini Program itself (device app + Side Service). This is the core deliverable.
- **`backend/`** — the Go server that replaces the Node prototype (stack decision, set B in `STACK_COMPARISON.md`). Go 1.26, `net/http`, PostgreSQL via `pgx`, goose migrations, `sqlc` queries, OpenAPI-first with `oapi-codegen` (`backend/api/openapi.yaml` is the source of truth for the contract; run `make generate` after editing it or the SQL in `internal/db/queries`). It serves the same HTTP contract as `server/` today, verified by running `server/test` against it; on top of that it has people: registration by invitation code, email+password sign-in with cookie sessions, password change (`/v1/auth/*`, `/v1/me`; operator commands `pigeondrop invite|user`). Watches are paired with an account (RFC 8628 device flow, `/v1/device/*`), authenticate with a revocable device token and upload to `/v1/messages`, where every recording has an owner; a small page at `/pair` is the temporary UI for sign-in, pairing, devices and playback. The chain engine comes next. **The watch app now speaks only this API, not the old `/messages`, `/accounts`, `/setup`**, so it needs the Go server. Read `backend/README.md` before working on it. Work on it goes through branches and pull requests (the owner also writes Go), not straight to main.
- **`server/`** — a minimal standalone Node.js HTTP server that receives uploads from the phone. A prototype, not production infrastructure (no auth, JSON-file storage); runs locally behind an ngrok tunnel only; it is no longer deployed (the Go backend is, see the CI/CD section below).
- **`ARCHITECTURE.md`** (root) — up-to-date narrative overview of the whole system: data flow, per-file responsibilities, and the reasoning behind non-obvious design decisions (custom `.opus` framing, round-display safe zone, the account/QR config flow, etc). **Read this first** before making non-trivial changes — it captures "why" context that isn't in code comments.
- **`API_SPEC.md`** (root) — the HTTP contract the watch and Side Service depend on: the `/v1` pairing and upload endpoints (Go server) and, below that, the older contract of `server/` (`/messages`, `/accounts/*`, `/setup`, `/uploads`) that the Node prototype and its tests still use. The watch and Side Service depend only on this contract, not on any implementation — any server change must keep it intact.
- **`REQUIREMENTS_ANALYSIS.md`** (root) — Russian-language analysis of requirements for the planned SaaS platform (backend, flow engine, frontend, apps for other watch platforms): workstreams, risks, load scenarios, open questions. **Stack chosen (2026-10-03): set B - Go backend + TypeScript frontend**; the owner knows Go and will also write code, so expect their commits alongside yours. **`STACK_COMPARISON.md`** (root, Russian) compares tools for each of the ten topics with pros/cons, reliability tags ([проверено]/[источник]/[вторично]/[оценка]) and a clearly marked recommendation; the owner made the set choice above; every other recommendation in it stays a proposal until confirmed.
- **`todo.md`** (root) — the original draft proposal for a persistent *phone-side* upload queue. Historical: the queue was ultimately built on the **watch** instead (`PigeonDrop/utils/uploadQueue.js`), because the Side Service turned out to have no reliable persistent storage or lifecycle — see `ARCHITECTURE.md` §2.2 for the reasoning. Kept for context, not as a spec.

## Language

Talk to the user in Russian. User-facing docs (`ARCHITECTURE.md`, `API_SPEC.md`, `REQUIREMENTS_ANALYSIS.md`, `STACK_COMPARISON.md`, `backend/README.md`, `backend/DEPLOY.md`) are written in Russian; this file and other Claude-only files stay in English.

## Git

Commit your own work: after finishing a task or a meaningful part of one, without waiting to be asked. Messages are in English, short (imperative subject line, optionally one line of detail) and say what was done. Stage only the files that belong to the change - the index may hold unrelated pre-existing entries. Do not push unless asked, with one standing exception: **open pull requests yourself** (the owner asked for it on 2026-10-03), which includes pushing that PR's own branch (never `main`, never other branches). Use `gh pr create` (installed and signed in; never read its token out of the keyring), PR title in English, body in Russian (what was done, what to look at first, how it was verified, what is not verified), ending with the Claude Code attribution line. Base a new branch on `main` once its parent PR is merged; the owner merges stacked PRs into their own bases and never deletes merged branches, so check `git log origin/main..<branch>` after any merge before assuming `main` has the work.

## Commands

### PigeonDrop (watch app)

Run from `PigeonDrop/`. Uses the Zepp OS `zeus` CLI (installed globally: `@zeppos/zeus-cli`).

**First-time setup**: `PigeonDrop/config.js` is gitignored (not tracked) — it holds the real server URL, which shouldn't end up in the repo. Copy `config.example.js` to `config.js` and fill in your own `SERVER_URL` before building. This is a plain JS module read at build time by Rollup, not a `.env` file — the watch has no runtime `process.env` to read one from.

```bash
zeus dev
```
Runs the app in the Zepp OS simulator.

```bash
zeus preview
```
Generates a QR code to side-load the app onto a real watch via the Zepp App. **Do not run this yourself unless explicitly asked** — in this project the user runs `zeus preview` and scans the QR code themselves; just tell them when it's needed.

```bash
zeus build
```
Produces a `.zab` package under `PigeonDrop/dist/`.

```bash
zeus cache clean
```
Clears simulator/build cache — useful when UI or icon changes don't seem to take effect.

```bash
npm test
```
Runs the watch-app tests (`test/*.test.mjs`, plain node:test, no install needed). They load the app's real source files with the `@zos/*` modules stubbed (`test/helpers/load.mjs` rewrites each import; an unmapped import fails the test on purpose) - they cover the chunked upload protocol and its failure modes, the main page's screen hold, the settings page, and that `config.example.js` has every constant the code imports. They run on stubs, so they do not replace checking screen/BLE/memory behaviour on the watch. No lint script is configured.

### backend (Go server)

Run from `backend/`. Needs Go 1.26+ and the PostgreSQL server binaries (the dev database is a throwaway cluster that never touches other databases).

```bash
scripts/dev-db.sh up     # PostgreSQL on 127.0.0.1:55432, data in backend/.dev
make test                # go test -race ./...; tests create and drop their own databases (REQUIRE_DB=1 makes a missing database fail instead of skip)
make check               # gofmt + vet + tests, what CI runs
make generate            # after editing api/openapi.yaml or internal/db/queries; CI fails if generated code is stale
make run                 # server on :3000 against the dev database
```

To check the Go server against the contract tests: build it, create an empty database, then `cd server && SERVER_CMD="$PWD/../backend/bin/pigeondrop serve" DATABASE_URL=<empty database> MIGRATE_ON_START=true npm test`.

### server (test upload server)

Run from `server/`.

```bash
npm start          # node server.js, listens on PORT env var or 3000
```

```bash
node convert-to-wav.js <input.opus> [output.wav]
```
Standalone CLI for the opus→wav converter also used internally by `server.js` after each upload.

To expose the local server to the watch/phone over the internet, an ngrok tunnel is used; the resulting URL must be manually placed in **both** `PigeonDrop/config.js`'s `SERVER_URL` and the server's `DEFAULT_SERVER_URL` env var (`DEFAULT_SERVER_URL=https://... npm start`; the Go server takes the same variable) — the server needs to know its own public address: the Go server builds the pairing QR address from it. Never enter or manage the ngrok authtoken yourself — that's the user's action (`ngrok config add-authtoken <token>`).

`STORAGE_DIR` env var controls where `data/` and `uploads/` live (defaults to next to `server.js`; `/data` in the Docker image).

```bash
npm test    # from server/: starts the real server.js on a free port with a temp STORAGE_DIR
```

### CI/CD (Go backend on the VPS via Coolify)

`.github/workflows/ci.yml` runs the Node server tests, the Go backend tests (real PostgreSQL 17, race detector, generated code up to date), the contract tests against the Go binary, the watch-app tests, a `zeus build` and the deploy-script tests. A push to `main` that touches `backend/` or `.github/scripts/`, or a manual run, then builds `backend/Dockerfile` **in GitHub Actions**, pushes it to GHCR (`ghcr.io/artursharafutdinov/pigeondrop-backend:<commit>` and `:latest`) and calls the Coolify API (`.github/scripts/coolify-deploy.sh`), which waits for the deployment; the Coolify application is of type "Docker Image" and only pulls and runs `:latest` (the VPS is shared, so it never builds). Without the three GitHub secrets (`COOLIFY_URL`, `COOLIFY_TOKEN`, `COOLIFY_APP_UUID`) the image is still pushed and the deploy step only warns. Setup steps: `backend/DEPLOY.md`; never ask the user to paste a token into chat (the GHCR read token is entered by the user on the VPS with `docker login`). The Node prototype in `server/` is not deployed; its `Dockerfile` is only built in CI. Production runs at `https://pigeondrop.example.com` (Coolify application of type Docker Image, the owner's own Coolify and PostgreSQL; the owner runs Coolify and GitHub secrets themselves, never ask for tokens). Verified for real on 2026-10-04: build and push to GHCR, the Coolify API call that waits for `Deployed.`, pulling the private image, migrations, registration, pairing a real watch, upload and playback. Not verified: rollback by changing the image tag, backups. The first rollout's three problems (Coolify's pull needs `docker login ghcr.io` saved in the config of the server user Coolify connects as, a wrong role password, finding the current container) are in `backend/DEPLOY.md`, "Если что-то пошло не так".

## Architecture

### Three-node data flow

```
Watch (Zepp OS device app)  --BLE-->  Phone (Side Service, inside Zepp App)  --HTTP-->  server/
```

1. Hardware shortcut button on the watch starts/stops recording (`PigeonDrop/page/index.js`), saved to the Mini Program's private storage via `@zos/fs`.
2. On stop, the recording is sent to the phone over BLE via a vendored `MessageBuilder` channel (`@zos/ble/TransferFile` used to run alongside it — removed, see below) **in numbered 15 KB chunks, one `request()` at a time, waiting for each reply** (`PigeonDrop/utils/uploadQueue.js`; see the constraint below for why). Every chunk carries the watch's device token (`PigeonDrop/utils/device.js`, received when it was paired - see the pairing section below); a watch that is not paired sends nothing and its recordings wait in the queue.
3. The Side Service (`PigeonDrop/app-side/index.js`) collects the chunks in memory (`PigeonDrop/app-side/uploads.js`) and, once the last one arrives, packs the recording into Ogg/Opus and `fetch()`es it (base64 in JSON, `{ messageId, audio }`) to `SERVER_URL + '/v1/messages'` with `Authorization: Bearer <device token>`. The Side Service keeps no token itself.
4. `backend/` (the Go server) finds the device by the token's hash, stores the standard Ogg/Opus file under the message's id for the device's owner, converts it to `.wav`, and answers. Repeats of the same `messageId` from the same device are harmless. A refused token (revoked device) is answered 401, which the Side Service passes to the watch as `unauthorized`; the watch forgets the token and says so.
5. The Side Service returns the server's result as the reply to the last chunk (a success carries `storage`: how full the account is; a 413 comes back as `fileError`, a 429 as `rateLimited`); the watch marks the recording uploaded only on that reply and shows progress/errors on its screen — a reply channel is the only way to observe Side Service behavior at all, since **Side Service `console.log` output is never visible anywhere on the phone/Zepp App side** (the watch's own "JS App Log" only shows device-side logs).

`@zos/ble/TransferFile` was removed entirely (2026-08-01): its Side Service `FileObject` only ever exposed metadata (name/path/size), with **no way to read the actual file bytes** — it never did real work, just silently filled up phone storage. `MessageBuilder` was always the only channel that actually carried readable data.

### Pairing the watch (QR code + device token)

main page → "Настройки" (`page/settings.js`) → "Привязать часы" opens `page/pair.js`. It asks the server for a pair of codes through the Side Service (`app-side/pairing.js`; requests `pairStart`/`pairPoll` over `MessageBuilder`'s `request()`/`response()` channel), shows a QR code for `<server>/pair?code=...` plus the short code as text, and polls every few seconds. The person opens the page on the phone, signs in, compares the code with the watch's screen and approves; the next poll hands the watch its device token once, and `utils/device.js` stores it (`device.json` via `@zos/fs`). The screen is held on while waiting (bounded by the code's 10-minute life). Contract: `API_SPEC.md` (`/v1`); server side: `backend/README.md`. Nothing about pairing is optional: an unpaired watch uploads nothing. The old "Настройка сервера"/"Синхронизация" flow (device code in `account.json`, `serverUrl` per account, `/setup` page) was removed with the code that served it - the "own server" mode will come back as an account setting.

### `PigeonDrop/shared/` — vendored library, not an npm dependency

`message.js` (device side), `message-side.js` (Side Service side), `event.js`, `defer.js`, `data.js` implement `MessageBuilder`, copied from the official `zepp-health/zeppos-samples` repo. It auto-chunks/reassembles arbitrary payloads over BLE (raw BLE messages are size-limited; `MessageBuilder` hides that). Treat this directory as third-party code — prefer patching around it over editing it, except for two already-applied fixes: `message-side.js` falls back to a no-op logger when the global `Logger` is undefined, and `DEBUG` is `false` in both `message.js` and `message-side.js` (upstream ships `true`, which hex-dumps every BLE packet into the log and made large sends crawl).

### Zepp OS-specific constraints that shape the code

- **App structure**: `app.json` (`configVersion: v3`) declares a device app (`page/index.js`, `page/list.js`, `page/settings.js`, `page/pair.js`, `page/storage.js`) and a separate Side Service (`app-side/index.js`) that runs inside the phone's Zepp App with no UI and its own lifecycle (`onInit`/`onRun`/`onDestroy` — not continuously alive).
- **Round display**: target device is 480×480px, radius 240. Rectangular widgets placed too close to the top/bottom edge get clipped by the physical bezel; UI code keeps element bounds within a ~230px-radius safe zone (see `page/list.js` layout constants).
- **One live `Player` at a time**: the hardware only supports a single live `@zos/media` `Player` instance. Creating a new one per playback throws `cannot read property 'addEventListener' of undefined` on the second play — both `page/index.js` and `page/list.js` use a lazily-created singleton `Player`, reused via `setSource`/`prepare` across plays.
- **Custom `.opus` framing**: files recorded via `@zos/media`'s recorder are **not** standard Ogg/Opus containers. Each packet is `[8-byte header][opus payload]` repeated back to back, where the header's **first 4 bytes are the payload length, big-endian** (the other 4 are unidentified and ignored). Verified empirically against real recordings — Zepp's own faq doc describes a 4+8=12-byte prefix, which is wrong and does not parse. Payloads are SILK wideband, mono, 20 ms per packet, decoding to 16 kHz 16-bit PCM. That is what the watch stores and sends over BLE. The phone's Side Service (`app-side/uploads.js` -> `utils/oggOpus.js`) wraps the packets, unchanged and without re-encoding, into a standard Ogg/Opus file (RFC 3533/7845: OpusHead, OpusTags, 50 packets per page, pre-skip 0) before posting to the server, so every server - the SaaS and any user-supplied one - gets a normal file; a recording cut off mid-packet is trimmed to its complete packets instead of failing. A recording that cannot be wrapped is answered with `fileError` and skipped by the watch's queue rather than blocking it. `server/convert-to-wav.js` reads both formats with `opusscript`; standard tools (`ffmpeg`) open the wrapped file but not the raw Zepp one. The packer is verified against an independent reader and ffmpeg's strict decoder; its speed and memory on the real phone for a 60 s recording have not been measured (only a 2 s one, in the spike).
- **`device:os.mic` permission** must be declared in `app.json`'s `permissions` array or recording throws `Permission Denied device:os.mic` at runtime — this requirement isn't discoverable from docs, only from the runtime error.
- **`QRCODE` widget background sizing**: `@zos/ui`'s QR widget takes `x/y/w/h` for the code itself and *separate* `bg_x/bg_y/bg_w/bg_h` for its background quad. Omitting the `bg_*` fields renders a background far larger than the code, bleeding past the round bezel — not visible from the type definitions, only on a real device. Always set both together, centered (see `page/pair.js`).
- **Never send a recording as one `MessageBuilder` message.** `call()` pushes every ~3.6 KB BLE packet in one synchronous loop with no backpressure; a 60 s recording (~212 KB as base64, ~60 packets) overran the BLE send queue, `ble.send` returned `false`, and the resulting exception was swallowed by an unhandled promise - uploads failed silently, and the work/memory involved made app open slow and the list page crash. Uploads therefore go in chunks of `CHUNK_BYTES` (must stay a multiple of 3 so the chunks' base64 concatenates) with a `request()` per chunk; keep it that way and re-test with a 60 s+ recording on the device.
- **Close file descriptors as `closeSync({ fd })`.** The bare-number form leaves the descriptor open and the device only has a few, so after several uploads `openSync` fails ("cannot open file"). `utils/uploadQueue.js` also treats an unreadable file as that file's own problem (sweep skips it and continues, the file stays queued), never treats a failed `statSync` as an empty file (that would delete the recording), and falls back to `readFileSync` when `openSync` fails. Settings has a "Очистить память" button (`clearAllRecordings`) that wipes recordings and the queue.
- **Screen sleep kills the recording.** Zepp OS sleeps the screen and ~10 s later closes the Mini Program, so an unattended long recording is lost and never queued for upload. `page/index.js` therefore holds the screen on while recording (`setPageBrightTime`, `pauseDropWristScreenOff`, `pausePalmScreenOff` from `@zos/display`, all bounded by `MAX_RECORDING_SECONDS`), auto-stops at that limit, and closes the file in `onDestroy` if the page is torn down mid-recording. Any upload also holds it, for a `UPLOAD_SCREEN_HOLD_MS` (60 s) window: armed on stop and by the first chunk of any sweep (so old queued recordings sent at app open are covered too), renewed by upload progress once under half is left, ended early when the queue empties, left to run out after an error (so the reason can be read), and replaced by the next recording's hold. Don't remove any of these without re-testing a long recording on the device.
- **Storage warning.** The server keeps at most 100 MiB of recordings per account (`STORAGE_ACCOUNT_BYTES`), deletes the oldest ones when a new one does not fit, and says so in the upload answer (`storage.state`: `ok`, `nearly_full` from 90%, `full` when it just deleted something). It cannot push to the watch, so the watch learns from that answer: `utils/uploadQueue.js` keeps the state (`utils/storageState.js`, `storage.json`) and, when it is news (it just became `nearly_full`, once; or `full`, every time, since each is a deletion), reports an `alert` with its last event. `page/index.js` then opens `page/storage.js` (state passed as the page parameter): a page of its own, because a label on the main page printed over the status text and the buttons (seen on the real watch). The page buzzes once (`VIBRATOR_SCENE_DURATION_LONG`, 1 s), holds the screen for 10 s and goes `back()` to the main page by itself; it is not opened at app open, and an alert that arrives during a recording waits until the recording stops. A refused token clears the remembered state. Other limits the watch has to handle: 413 (recording over 512 KiB: skipped like an unreadable file) and 429 (more than 60 uploads an hour from this device: sweep stops, recordings wait). The page's look and the buzz on the real round display are unverified.
- **Configurable values live in `PigeonDrop/config.js`**, not hardcoded: `MAX_RECORDINGS` (oldest recordings pruned via `utils/recordings.js` once exceeded), `MAX_RECORDING_SECONDS` (longest single recording; also bounds how long the screen is held on), and `SERVER_URL` (the server for pairing and uploads).
