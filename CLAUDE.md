# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repo layout

Independent projects living side by side, connected only by the runtime data flow described below (no shared package.json, no monorepo tooling):

- **`PigeonDrop/`** — the Zepp OS 4.0 Mini Program itself (device app + Side Service). This is the core deliverable.
- **`backend/`** — the Go server that replaces the Node prototype (stack decision, set B in `STACK_COMPARISON.md`). Go 1.26, `net/http`, PostgreSQL via `pgx`, goose migrations, `sqlc` queries, OpenAPI-first with `oapi-codegen` (`backend/api/openapi.yaml` is the source of truth for the contract; run `make generate` after editing it or the SQL in `internal/db/queries`). It serves the same HTTP contract as `server/` today, verified by running `server/test` against it; on top of that it has people: registration by invitation code, email+password sign-in with cookie sessions, password change (`/v1/auth/*`, `/v1/me`; operator commands `pigeondrop invite|user`). Device tokens and the chain engine come next. Read `backend/README.md` before working on it. Work on it goes through branches and pull requests (the owner also writes Go), not straight to main.
- **`server/`** — a minimal standalone Node.js HTTP server that receives uploads from the phone. A prototype, not production infrastructure (no auth, JSON-file storage); runs locally behind an ngrok tunnel, or in Docker on the owner's VPS via Coolify (see `server/DEPLOY.md` and the CI/CD section below).
- **`ARCHITECTURE.md`** (root) — up-to-date narrative overview of the whole system: data flow, per-file responsibilities, and the reasoning behind non-obvious design decisions (custom `.opus` framing, round-display safe zone, the account/QR config flow, etc). **Read this first** before making non-trivial changes — it captures "why" context that isn't in code comments.
- **`API_SPEC.md`** (root) — the HTTP contract for `server/`'s endpoints (`/messages`, `/accounts/*`, `/setup`, `/uploads`). The watch and Side Service depend only on this contract, not on the Node implementation — any server change must keep it intact.
- **`REQUIREMENTS_ANALYSIS.md`** (root) — Russian-language analysis of requirements for the planned SaaS platform (backend, flow engine, frontend, apps for other watch platforms): workstreams, risks, load scenarios, open questions. **Stack chosen (2026-10-03): set B - Go backend + TypeScript frontend**; the owner knows Go and will also write code, so expect their commits alongside yours. **`STACK_COMPARISON.md`** (root, Russian) compares tools for each of the ten topics with pros/cons, reliability tags ([проверено]/[источник]/[вторично]/[оценка]) and a clearly marked recommendation; the owner made the set choice above; every other recommendation in it stays a proposal until confirmed.
- **`todo.md`** (root) — the original draft proposal for a persistent *phone-side* upload queue. Historical: the queue was ultimately built on the **watch** instead (`PigeonDrop/utils/uploadQueue.js`), because the Side Service turned out to have no reliable persistent storage or lifecycle — see `ARCHITECTURE.md` §2.2 for the reasoning. Kept for context, not as a spec.

## Language

Talk to the user in Russian. User-facing docs (`ARCHITECTURE.md`, `API_SPEC.md`, `REQUIREMENTS_ANALYSIS.md`, `STACK_COMPARISON.md`, `server/DEPLOY.md`) are written in Russian; this file and other Claude-only files stay in English.

## Git

Commit your own work: after finishing a task or a meaningful part of one, without waiting to be asked. Messages are in English, short (imperative subject line, optionally one line of detail) and say what was done. Stage only the files that belong to the change - the index may hold unrelated pre-existing entries. Do not push unless asked.

## Commands

### PigeonDrop (watch app)

Run from `PigeonDrop/`. Uses the Zepp OS `zeus` CLI (installed globally: `@zeppos/zeus-cli`).

**First-time setup**: `PigeonDrop/config.js` is gitignored (not tracked) — it holds the real server URL, which shouldn't end up in the repo. Copy `config.example.js` to `config.js` and fill in your own `SERVER_URL`/`WEB_APP_BASE_URL` before building. This is a plain JS module read at build time by Rollup, not a `.env` file — the watch has no runtime `process.env` to read one from.

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

To expose the local server to the watch/phone over the internet, an ngrok tunnel is used; the resulting URL must be manually placed in **both** `PigeonDrop/config.js`'s `SERVER_URL`/`WEB_APP_BASE_URL` and `server/server.js`'s `DEFAULT_SERVER_URL` (pass it as an env var: `DEFAULT_SERVER_URL=https://... npm start`) — the server needs to know its own public address to resolve accounts' default `serverUrl` correctly. Never enter or manage the ngrok authtoken yourself — that's the user's action (`ngrok config add-authtoken <token>`).

`STORAGE_DIR` env var controls where `data/` and `uploads/` live (defaults to next to `server.js`; `/data` in the Docker image).

```bash
npm test    # from server/: starts the real server.js on a free port with a temp STORAGE_DIR
```

### CI/CD (server on the VPS via Coolify)

`server/Dockerfile` builds the image Coolify runs; `.github/workflows/ci.yml` runs the server tests + image build, the watch-app tests, a `zeus build`, and then (push to `main` touching `server/`, or a manual run) calls the Coolify API and waits for the deployment. Coolify's own auto-deploy must stay OFF, otherwise it deploys before the checks. Setup steps and the three GitHub secrets (`COOLIFY_URL`, `COOLIFY_TOKEN`, `COOLIFY_APP_UUID`) are in `server/DEPLOY.md`; never ask the user to paste the token into chat. The pipeline has only been exercised against a mock of the Coolify API, not a real Coolify or GitHub Actions run.

## Architecture

### Three-node data flow

```
Watch (Zepp OS device app)  --BLE-->  Phone (Side Service, inside Zepp App)  --HTTP-->  server/
```

1. Hardware shortcut button on the watch starts/stops recording (`PigeonDrop/page/index.js`), saved to the Mini Program's private storage via `@zos/fs`.
2. On stop, the recording is sent to the phone over BLE via a vendored `MessageBuilder` channel (`@zos/ble/TransferFile` used to run alongside it — removed, see below) **in numbered 15 KB chunks, one `request()` at a time, waiting for each reply** (`PigeonDrop/utils/uploadQueue.js`; see the constraint below for why). The payload includes the watch's currently-configured `serverUrl` (see the account/QR flow below), or `null` if the user hasn't synced one yet.
3. The Side Service (`PigeonDrop/app-side/index.js`) collects the chunks in memory (`PigeonDrop/app-side/uploads.js`) and, once the last one arrives, `fetch()`es the whole recording (the concatenated chunk strings are the base64 of the file) to `(serverUrl || SERVER_URL) + '/messages'` as JSON — `SERVER_URL` (`config.js`) is only the bootstrap fallback used before the first sync.
4. `server/server.js` decodes, saves the `.opus` file (a standard Ogg/Opus file; named by **server receipt time**, not watch recording time — for readable sorting), and immediately converts it to `.wav` via `server/convert-to-wav.js` so it's playable without a manual step. The converter reads Ogg/Opus (detected by the `OggS` signature) and also still accepts Zepp's own framing.
5. The Side Service returns the server's result as the reply to the last chunk; the watch marks the recording uploaded only on that reply and shows progress/errors on its screen — a reply channel is the only way to observe Side Service behavior at all, since **Side Service `console.log` output is never visible anywhere on the phone/Zepp App side** (the watch's own "JS App Log" only shows device-side logs).

`@zos/ble/TransferFile` was removed entirely (2026-08-01): its Side Service `FileObject` only ever exposed metadata (name/path/size), with **no way to read the actual file bytes** — it never did real work, just silently filled up phone storage. `MessageBuilder` was always the only channel that actually carried readable data.

### Server URL configuration (QR code + account)

The user can point uploads at their own server without rebuilding the watch app: main page → "Настройки" (`page/settings.js`) → "Настройка сервера" opens `page/qrcode.js`, which generates/persists a device code (`PigeonDrop/utils/account.js`, `account.json` via `@zos/fs`) and shows a QR encoding `WEB_APP_BASE_URL + '/setup?code=...'`. Scanning it opens `server/public/setup.html`, where the user picks the default server or enters their own, saved via the server's `/accounts/:code` endpoint. "Синхронизация" then pulls the resolved `serverUrl` down to the watch via `MessageBuilder`'s `request()`/`response()` channel (distinct from the fire-and-forget `call()` used for recordings) and persists it locally. Full endpoint contract: `API_SPEC.md`. Deliberately no auth yet — the device code is just an identifier, not a secret.

### `PigeonDrop/shared/` — vendored library, not an npm dependency

`message.js` (device side), `message-side.js` (Side Service side), `event.js`, `defer.js`, `data.js` implement `MessageBuilder`, copied from the official `zepp-health/zeppos-samples` repo. It auto-chunks/reassembles arbitrary payloads over BLE (raw BLE messages are size-limited; `MessageBuilder` hides that). Treat this directory as third-party code — prefer patching around it over editing it, except for two already-applied fixes: `message-side.js` falls back to a no-op logger when the global `Logger` is undefined, and `DEBUG` is `false` in both `message.js` and `message-side.js` (upstream ships `true`, which hex-dumps every BLE packet into the log and made large sends crawl).

### Zepp OS-specific constraints that shape the code

- **App structure**: `app.json` (`configVersion: v3`) declares a device app (`page/index.js`, `page/list.js`, `page/settings.js`, `page/qrcode.js`) and a separate Side Service (`app-side/index.js`) that runs inside the phone's Zepp App with no UI and its own lifecycle (`onInit`/`onRun`/`onDestroy` — not continuously alive).
- **Round display**: target device is 480×480px, radius 240. Rectangular widgets placed too close to the top/bottom edge get clipped by the physical bezel; UI code keeps element bounds within a ~230px-radius safe zone (see `page/list.js` layout constants).
- **One live `Player` at a time**: the hardware only supports a single live `@zos/media` `Player` instance. Creating a new one per playback throws `cannot read property 'addEventListener' of undefined` on the second play — both `page/index.js` and `page/list.js` use a lazily-created singleton `Player`, reused via `setSource`/`prepare` across plays.
- **Custom `.opus` framing**: files recorded via `@zos/media`'s recorder are **not** standard Ogg/Opus containers. Each packet is `[8-byte header][opus payload]` repeated back to back, where the header's **first 4 bytes are the payload length, big-endian** (the other 4 are unidentified and ignored). Verified empirically against real recordings — Zepp's own faq doc describes a 4+8=12-byte prefix, which is wrong and does not parse. Payloads are SILK wideband, mono, 20 ms per packet, decoding to 16 kHz 16-bit PCM. That is what the watch stores and sends over BLE. The phone's Side Service (`app-side/uploads.js` -> `utils/oggOpus.js`) wraps the packets, unchanged and without re-encoding, into a standard Ogg/Opus file (RFC 3533/7845: OpusHead, OpusTags, 50 packets per page, pre-skip 0) before posting to the server, so every server - the SaaS and any user-supplied one - gets a normal file; a recording cut off mid-packet is trimmed to its complete packets instead of failing. A recording that cannot be wrapped is answered with `fileError` and skipped by the watch's queue rather than blocking it. `server/convert-to-wav.js` reads both formats with `opusscript`; standard tools (`ffmpeg`) open the wrapped file but not the raw Zepp one. The packer is verified against an independent reader and ffmpeg's strict decoder; its speed and memory on the real phone for a 60 s recording have not been measured (only a 2 s one, in the spike).
- **`device:os.mic` permission** must be declared in `app.json`'s `permissions` array or recording throws `Permission Denied device:os.mic` at runtime — this requirement isn't discoverable from docs, only from the runtime error.
- **`QRCODE` widget background sizing**: `@zos/ui`'s QR widget takes `x/y/w/h` for the code itself and *separate* `bg_x/bg_y/bg_w/bg_h` for its background quad. Omitting the `bg_*` fields renders a background far larger than the code, bleeding past the round bezel — not visible from the type definitions, only on a real device. Always set both together, centered (see `page/qrcode.js`).
- **Never send a recording as one `MessageBuilder` message.** `call()` pushes every ~3.6 KB BLE packet in one synchronous loop with no backpressure; a 60 s recording (~212 KB as base64, ~60 packets) overran the BLE send queue, `ble.send` returned `false`, and the resulting exception was swallowed by an unhandled promise - uploads failed silently, and the work/memory involved made app open slow and the list page crash. Uploads therefore go in chunks of `CHUNK_BYTES` (must stay a multiple of 3 so the chunks' base64 concatenates) with a `request()` per chunk; keep it that way and re-test with a 60 s+ recording on the device.
- **Close file descriptors as `closeSync({ fd })`.** The bare-number form leaves the descriptor open and the device only has a few, so after several uploads `openSync` fails ("cannot open file"). `utils/uploadQueue.js` also treats an unreadable file as that file's own problem (sweep skips it and continues, the file stays queued), never treats a failed `statSync` as an empty file (that would delete the recording), and falls back to `readFileSync` when `openSync` fails. Settings has a "Очистить память" button (`clearAllRecordings`) that wipes recordings and the queue.
- **Screen sleep kills the recording.** Zepp OS sleeps the screen and ~10 s later closes the Mini Program, so an unattended long recording is lost and never queued for upload. `page/index.js` therefore holds the screen on while recording (`setPageBrightTime`, `pauseDropWristScreenOff`, `pausePalmScreenOff` from `@zos/display`, all bounded by `MAX_RECORDING_SECONDS`), auto-stops at that limit, and closes the file in `onDestroy` if the page is torn down mid-recording. Any upload also holds it, for a `UPLOAD_SCREEN_HOLD_MS` (60 s) window: armed on stop and by the first chunk of any sweep (so old queued recordings sent at app open are covered too), renewed by upload progress once under half is left, ended early when the queue empties, left to run out after an error (so the reason can be read), and replaced by the next recording's hold. Don't remove any of these without re-testing a long recording on the device.
- **Configurable values live in `PigeonDrop/config.js`**, not hardcoded: `MAX_RECORDINGS` (oldest recordings pruned via `utils/recordings.js` once exceeded), `MAX_RECORDING_SECONDS` (longest single recording; also bounds how long the screen is held on), `SERVER_URL` (bootstrap/account server), and `WEB_APP_BASE_URL` (the setup-page host for the QR code).
