# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repo layout

Three independent Node/JS projects living side by side, connected only by the runtime data flow described below (no shared package.json, no monorepo tooling):

- **`PigeonDrop/`** — the Zepp OS 4.0 Mini Program itself (device app + Side Service). This is the core deliverable.
- **`server/`** — a minimal standalone Node.js HTTP server used as a local test target for uploads from the phone. Not production infrastructure. Deployable to Railway (see `server/DEPLOY.md`) as a stopgap before the planned Go rewrite.
- **`ARCHITECTURE.md`** (root) — up-to-date narrative overview of the whole system: data flow, per-file responsibilities, and the reasoning behind non-obvious design decisions (custom `.opus` framing, round-display safe zone, the account/QR config flow, etc). **Read this first** before making non-trivial changes — it captures "why" context that isn't in code comments.
- **`API_SPEC.md`** (root) — the HTTP contract for `server/`'s endpoints (`/messages`, `/accounts/*`, `/setup`). This is the source of truth for the planned server rewrite (Node prototype → Go) — the watch and Side Service only depend on this contract, not on the Node implementation, so a Go backend just needs to satisfy this doc.
- **`todo.md`** (root) — draft technical proposal for the not-yet-implemented persistent phone-side upload queue. Reference this before implementing that feature rather than redesigning from scratch.

## Commands

### PigeonDrop (watch app)

Run from `PigeonDrop/`. Uses the Zepp OS `zeus` CLI (installed globally: `@zeppos/zeus-cli`).

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

There is no test suite (`package.json`'s `test` script is a placeholder) and no lint script configured.

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

For a persistent public deployment instead of ngrok, see `server/DEPLOY.md` (Railway). `STORAGE_DIR` env var controls where `data/` and `uploads/` live (defaults to next to `server.js` for local dev) — must point at a mounted persistent volume when deployed, or state is lost on every redeploy.

## Architecture

### Three-node data flow

```
Watch (Zepp OS device app)  --BLE-->  Phone (Side Service, inside Zepp App)  --HTTP-->  server/
```

1. Hardware shortcut button on the watch starts/stops recording (`PigeonDrop/page/index.js`), saved to the Mini Program's private storage via `@zos/fs`.
2. On stop, the recording is sent to the phone over BLE via a vendored `MessageBuilder` channel (`@zos/ble/TransferFile` used to run alongside it — removed, see below). The payload includes the watch's currently-configured `serverUrl` (see the account/QR flow below), or `null` if the user hasn't synced one yet.
3. The Side Service (`PigeonDrop/app-side/index.js`) receives the `MessageBuilder` payload, base64-decodes it, and `fetch()`s it to `(serverUrl || SERVER_URL) + '/messages'` as JSON — `SERVER_URL` (`config.js`) is only the bootstrap fallback used before the first sync.
4. `server/server.js` decodes, saves the `.opus` file (named by **server receipt time**, not watch recording time — for readable sorting), and immediately converts it to `.wav` via `server/convert-to-wav.js` so it's playable without a manual step.
5. The Side Service relays the upload result back to the watch over `MessageBuilder`, where it's logged — this is the only way to observe Side Service behavior at all, since **Side Service `console.log` output is never visible anywhere on the phone/Zepp App side** (the watch's own "JS App Log" only shows device-side logs).

`@zos/ble/TransferFile` was removed entirely (2026-08-01): its Side Service `FileObject` only ever exposed metadata (name/path/size), with **no way to read the actual file bytes** — it never did real work, just silently filled up phone storage. `MessageBuilder` was always the only channel that actually carried readable data.

### Server URL configuration (QR code + account)

The user can point uploads at their own server without rebuilding the watch app: main page → "Настройки" (`page/settings.js`) → "Настройка сервера" opens `page/qrcode.js`, which generates/persists a device code (`PigeonDrop/utils/account.js`, `account.json` via `@zos/fs`) and shows a QR encoding `WEB_APP_BASE_URL + '/setup?code=...'`. Scanning it opens `server/public/setup.html`, where the user picks the default server or enters their own, saved via the server's `/accounts/:code` endpoint. "Синхронизация" then pulls the resolved `serverUrl` down to the watch via `MessageBuilder`'s `request()`/`response()` channel (distinct from the fire-and-forget `call()` used for recordings) and persists it locally. Full endpoint contract: `API_SPEC.md`. Deliberately no auth yet — the device code is just an identifier, not a secret.

### `PigeonDrop/shared/` — vendored library, not an npm dependency

`message.js` (device side), `message-side.js` (Side Service side), `event.js`, `defer.js`, `data.js` implement `MessageBuilder`, copied from the official `zepp-health/zeppos-samples` repo. It auto-chunks/reassembles arbitrary payloads over BLE (raw BLE messages are size-limited; `MessageBuilder` hides that). Treat this directory as third-party code — prefer patching around it over editing it, except for the already-applied fix in `message-side.js` (falls back to a no-op logger when the global `Logger` is undefined).

### Zepp OS-specific constraints that shape the code

- **App structure**: `app.json` (`configVersion: v3`) declares a device app (`page/index.js`, `page/list.js`, `page/settings.js`, `page/qrcode.js`) and a separate Side Service (`app-side/index.js`) that runs inside the phone's Zepp App with no UI and its own lifecycle (`onInit`/`onRun`/`onDestroy` — not continuously alive).
- **Round display**: target device is 480×480px, radius 240. Rectangular widgets placed too close to the top/bottom edge get clipped by the physical bezel; UI code keeps element bounds within a ~230px-radius safe zone (see `page/list.js` layout constants).
- **One live `Player` at a time**: the hardware only supports a single live `@zos/media` `Player` instance. Creating a new one per playback throws `cannot read property 'addEventListener' of undefined` on the second play — both `page/index.js` and `page/list.js` use a lazily-created singleton `Player`, reused via `setSource`/`prepare` across plays.
- **Custom `.opus` framing**: files recorded via `@zos/media`'s recorder are **not** standard Ogg/Opus containers. Each packet is `[4-byte big-endian length][8-byte header][opus payload]`, repeated, at 16kHz mono 16-bit PCM (per Zepp's own faq doc). `server/convert-to-wav.js` implements this framing manually with `opusscript`; standard tools (`ffmpeg -f opus`, etc.) cannot open these files directly.
- **`device:os.mic` permission** must be declared in `app.json`'s `permissions` array or recording throws `Permission Denied device:os.mic` at runtime — this requirement isn't discoverable from docs, only from the runtime error.
- **`QRCODE` widget background sizing**: `@zos/ui`'s QR widget takes `x/y/w/h` for the code itself and *separate* `bg_x/bg_y/bg_w/bg_h` for its background quad. Omitting the `bg_*` fields renders a background far larger than the code, bleeding past the round bezel — not visible from the type definitions, only on a real device. Always set both together, centered (see `page/qrcode.js`).
- **Configurable values live in `PigeonDrop/config.js`**, not hardcoded: `MAX_RECORDINGS` (oldest recordings pruned via `utils/recordings.js` once exceeded), `SERVER_URL` (bootstrap/account server), and `WEB_APP_BASE_URL` (the setup-page host for the QR code).
