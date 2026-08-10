# PigeonDrop: Go rewrite + multi-user roadmap

_Created: 2026-08-09. Living document — tick milestones off as they land, and record decisions that turn out differently in practice._

## Context

`server/` is a ~330-line dependency-light Node prototype that was always meant to be temporary (`CLAUDE.md`: "not production infrastructure"). [`API_SPEC.md`](API_SPEC.md) exists precisely so a rewrite can happen without touching the watch — the watch and Side Service depend on the HTTP contract, not on the Node implementation.

Two goals, deliberately sequenced:

1. **Rewrite the backend in Go with exact feature parity**, as a Go learning exercise.
2. **Only after parity is deployed and verified**, add: multi-user auth, a reworked signup/pairing flow, per-user encrypted storage, and a separate Vue 3 frontend.

The Go code is written by hand as a learning exercise — this document is the build guide, not an implementation spec. Each milestone states its goal, what it teaches, decisions already made (so they don't need re-deriving), gotchas specific to *this* project, and acceptance criteria.

## Decisions already locked in

- **Stdlib-first.** `net/http` + Go 1.22 routing, `database/sql`, `log/slog`, `encoding/json`, hand-rolled migrations. A dependency gets added only where stdlib genuinely hurts, and the reason is recorded.
- **Opaque server-side tokens, no JWT.** One `sessions` table backs both web and watch. Revocation is a `DELETE`. Rationale: a single backend with a single colocated DB makes JWT's statelessness worthless here, while instant revocation is a real requirement (lost watch, stolen session).
- **Bearer tokens in the `Authorization` header, not cookies.** Forced by the separate frontend deploy: a cookie from a different registrable domain is a third-party cookie, which Safari ITP blocks outright. Bearer tokens also mean **no CSRF surface at all**, and one auth mechanism shared with the watch. Tradeoff: a token in `localStorage` is XSS-reachable. Accepted; if both apps ever land on one domain (`app.` + `api.`), same-site cookies become the upgrade path.
- **Server-managed encryption keys** (envelope encryption), not password-derived. The operator can decrypt; password reset stays non-destructive. Same trust model as most SaaS.
- **Vue 3 + Vite + TypeScript**, deployed separately from the Go API.
- **Feature parity ships first.** No auth, no schema changes, no watch changes until M1 is live and verified on the real device.

## Target layout

```
amazfit/
├── PigeonDrop/     — watch app (untouched until M5)
├── server/         — Node prototype (deleted at end of M1)
├── backend/        — NEW Go API
│   ├── cmd/pigeondrop/main.go
│   ├── internal/{audio,store,httpapi,config}/
│   ├── migrations/          (from M2)
│   └── go.mod
└── web/            — NEW Vue 3 + Vite + TS app (from M4)
```

`cmd/` + `internal/` is the standard Go layout; `internal/` is compiler-enforced private, which is a real language feature worth using deliberately.

---

# Phase A — Feature parity (no new functionality)

## M0 — Spike: Opus → WAV in Go

- [x] **Complete (2026-08-09)** — `backend/internal/audio/`. `pion/opus` (pure Go, no cgo) decodes every real recording correctly; **the cgo fallback is not needed**. Verified against the Node implementation on all 9 recordings: identical sample counts, RMS agreeing to within 0.1%, WAV headers byte-identical. `go test -race ./...` green.

**Highest-risk item in the whole project, so it goes first.** If Opus decoding can't be made to work in Go, everything downstream needs rethinking. Don't start M1 until this passes.

Port `server/convert-to-wav.js` to `backend/internal/audio/`:

- Parse the Zepp packet framing, decode the Opus packets, write a canonical 44-byte RIFF/WAV header + PCM (16 kHz, mono, 16-bit).
- Write the WAV header **by hand** with `encoding/binary` — it's ~44 bytes, needs no library, and is a genuinely good `binary.LittleEndian` exercise.

> **✅ Framing resolved (2026-08-09).** The prefix is **8 bytes total**, of which the first 4 are the payload length, big-endian; the other 4 are unidentified and skipped. Verified against real recordings: under this reading files parse to an exact byte-for-byte fit, while the 12-byte reading Zepp's docs describe overruns on the second packet. `CLAUDE.md`, `ARCHITECTURE.md` §4, and `convert-to-wav.js`'s comment have been corrected.
>
> Payloads are **SILK wideband, mono, 20 ms per packet** (TOC byte `0x48` → config 9, 1 frame). Convenient: SILK is the most mature part of `pion/opus`, so this is the best case for the pure-Go decoder.

**Decoder choice:** start with [`pion/opus`](https://pkg.go.dev/github.com/pion/opus) — pure Go, no cgo, cross-compiles, trivial Railway builds. Caveats: v0.1.0, decoder-only, pre-1.0. Use `NewDecoderWithOutput(16000, 1)`.

**Fallback if it can't decode the watch's packets:** [`hraban/opus`](https://github.com/hraban/opus) (cgo bindings to libopus — the same library `opusscript` wraps, so it will definitely work). Cost: a `Dockerfile` installing `libopus-dev` on Railway, and no cross-compilation.

**Verification:** convert the existing real `.opus` files in `server/uploads/` with both the Node and Go paths. Expect **identical duration and sample count**, and a near-identical waveform — *not* byte-identical PCM, since pion/opus is an independent implementation of RFC 6716 rather than libopus. Then actually listen to the Go output. Keep one `.opus` + its expected `.wav` as a golden-file test fixture.

**Teaches:** `encoding/binary`, byte slices and `[]byte` aliasing, `io.Reader`/`io.Writer`, Go's error-returning idiom, table-driven tests, golden files.

## M1 — Go parity port (JSON stores, no auth)

- [ ] Complete

Reimplement every endpoint in `API_SPEC.md` exactly: `POST /messages` (idempotent by `fileName`), `POST /accounts`, `GET|PUT /accounts/{code}`, `GET /setup`, `GET /uploads`, `GET /uploads/{name}`. **The watch app is not modified and must keep working untouched** — that's the acceptance test.

Keep the JSON-file stores for now (`accounts.json`, `uploads.json`). Deliberately: it keeps this milestone purely about Go and HTTP, with SQL as its own milestone. Write them as **concrete structs, not interfaces** — Go's idiom is to define an interface at the consumer once a second implementation exists, and M2 is exactly where that extraction becomes justified.

Notes specific to this port:

- **Routing:** Go 1.22+ `http.ServeMux` handles this natively — `mux.HandleFunc("GET /accounts/{code}", ...)` plus `r.PathValue("code")`. No `chi` needed.
- **Config:** one `internal/config` struct populated from env (`PORT`, `STORAGE_DIR`, `DEFAULT_SERVER_URL`), validated once at startup. Replaces scattered `process.env` reads.
- **Logging:** `log/slog` with JSON output replaces `logger.js`. Drop the custom file-writing logger — Railway captures stdout.
- **Drop the `uncaughtException`/`unhandledRejection` net.** It has no Go equivalent and doesn't need one: `net/http` already recovers a panicking handler without killing the process. Add a small recover middleware purely to *log* panics and return `500`.
- **Add graceful shutdown** (`signal.NotifyContext` + `srv.Shutdown`) — no Node counterpart, but idiomatic and easy.
- **Keep** path-traversal rejection on `/uploads/{name}`, and the "save the recording even if conversion fails" rule (`API_SPEC.md` calls the latter out as required behavior).
- Serve `public/setup.html` unchanged, via `//go:embed`, so the binary is self-contained.

> **⚠ Concurrency bug you'll inherit.** `accounts-store.js`'s read-modify-write (`readAll` → mutate → `writeAll`) is racy today; Node's single thread mostly hides it. Go serves each request on its own goroutine, so this becomes a real data race and corrupted JSON. Guard the store with a `sync.Mutex` and run `go test -race`. This is the single most valuable lesson in this milestone.

**Acceptance:** every request in `API_SPEC.md` behaves identically against Go and Node; a real recording from the watch lands and converts; deployed to Railway; `server/` deleted (git history keeps it).

**Teaches:** `net/http` servers and middleware, `http.HandlerFunc`, `context`, structs and JSON tags, `sync.Mutex`, `go:embed`, `httptest`, graceful shutdown, Railway Go builds.

## M2 — Postgres + migrations

- [ ] Complete

Swap the JSON stores for Postgres, **changing zero API behavior**. That invariance is the whole point: the acceptance test is that M1's test suite still passes untouched.

- **Driver:** `database/sql` + `github.com/jackc/pgx/v5/stdlib`. pgx is the de-facto Postgres driver; going through `database/sql` keeps you on stdlib semantics.
- **Migrations:** hand-rolled — numbered `.sql` files in `migrations/`, an `embed.FS`, a `schema_migrations` table, applied in a transaction at startup. ~50 lines, no `golang-migrate` dependency, and you learn what migration tools actually do.
- Schema at this stage is an honest mirror of today: `accounts (code PK, custom_server_url, created_at, updated_at)` and `uploads (watch_file_name PK, saved_as, wav_file, bytes_received, received_at)`.
- **Now** extract store interfaces, defined in the package that consumes them.
- Add Postgres locally via Homebrew (there's no Docker on this machine); add the Postgres addon to the Railway project.

**Teaches:** `database/sql`, `context`-aware queries, `sql.ErrNoRows`, transactions, connection pooling, interface extraction, schema migration mechanics.

---

# Phase B — New functionality

Everything below starts only once Phase A is deployed and verified on the watch.

## M3 — Users, password auth, sessions

- [ ] Complete

Backend only; test with `curl` and `httptest` before any UI exists.

```sql
users    (id uuid pk, email citext unique, password_hash text, created_at, updated_at)
sessions (id uuid pk, user_id fk, token_hash bytea unique, kind text,   -- 'web' | 'device'
          scope text, label text, expires_at, created_at, last_seen_at)
```

- **Passwords:** `golang.org/x/crypto/bcrypt` — one call each way, hard to misuse. Argon2id via `x/crypto/argon2` is the modern upgrade but is low-level: you'd own salt generation, parameter tuning, and encoding. Start with bcrypt.
- **Tokens:** 32 bytes from `crypto/rand` → `base64.RawURLEncoding`. **Store only the `sha256` of the token**, so a DB leak yields no usable credentials. Look up by hash — indexed, so there's no timing concern on the lookup itself.
- **Middleware:** `requireAuth` parses `Authorization: Bearer`, loads the user, and puts it in the request `context`. Use an unexported context key type — the classic Go gotcha.
- `scope` on sessions lets a watch token later be restricted to uploads only. Add the column now, enforce it in M5.
- Endpoints: `POST /api/auth/signup`, `/api/auth/login`, `/api/auth/logout`, `GET /api/me`, `GET|DELETE /api/sessions`.
- **CORS**, needed from here on because the frontend is a separate origin: echo a specific allowed origin (never `*`), allow `Authorization` + `Content-Type`, handle `OPTIONS` preflight. No `Allow-Credentials` — there are no cookies.

**Teaches:** `crypto/rand`, hashing, `context` values, middleware composition, CORS, auth modeling.

## M4 — Vue 3 web app

- [ ] Complete

First real UI: signup, login, recordings list, in-browser playback. `web/` — Vite + Vue 3 `<script setup>` + TypeScript, Pinia for the auth store, and a typed `fetch` wrapper that injects the bearer token and handles `401`.

Vite dev proxy `/api` → `localhost:8080` in development; `VITE_API_BASE_URL` in production. Serve audio via `http.ServeContent` on the Go side — it gives HTTP range requests (seeking) for free.

**Playback format:** keep serving `.wav`. Ogg/Opus would be ~10× smaller, but Safari only gained Ogg/Opus support in 18.4, and WAV plays everywhere.

## M5 — Device pairing: bind a watch to a user

- [ ] Complete

First milestone that touches `PigeonDrop/`. Replaces today's flow, where the QR-visible device code is the *only* credential and anyone holding it can read or repoint an account.

Use the **device authorization grant** (RFC 8628 — how TVs and consoles log in), which maps almost 1:1 onto the existing QR screen:

1. Watch → `POST /api/devices/pair/start`, receives a secret `device_code` (never displayed) and a short `user_code`.
2. Watch renders a QR containing only the `user_code`.
3. User scans it, lands in the logged-in Vue app, approves.
4. Watch exchanges `device_code` for a long-lived `scope='upload'` session token — this is what «Синхронизация» becomes.

Watch-side changes: `utils/account.js` stores the token; `app-side/index.js` sends `Authorization: Bearer` on uploads; `page/qrcode.js` / `page/settings.js` follow the new flow. Rewrite `API_SPEC.md` alongside.

> **⚠ The idempotency key must become per-user.** `POST /messages` currently dedupes on a globally-unique `fileName`, but the watch generates names like `voice_<timestamp>.opus` — two users' watches can collide, and one user's upload would silently return another's result. The uniqueness constraint becomes `UNIQUE (user_id, watch_file_name)`. This is a real defect introduced by multi-tenancy, not a theoretical one.

## M6 — Per-user encrypted storage

- [ ] Complete

Envelope encryption, all from stdlib (`crypto/aes`, `crypto/cipher`, `crypto/rand`):

- **Master key**: 32 bytes, base64, from env (`MASTER_KEY`) — a Railway env var, never in git.
- **Per-user DEK**: 32 random bytes generated at signup, AES-256-GCM-wrapped under the master key, stored in `users.wrapped_dek`.
- **Per-file**: AES-256-GCM with a fresh 12-byte nonce, nonce prefixed to the ciphertext. Bind the file's DB id in as GCM additional authenticated data, so one file can't be swapped for another.
- **Layout**: `uploads/<user_id>/<random_uuid>` instead of a flat timestamp-named directory. Today's filenames leak *when* every recording happened to anyone who can list the directory; opaque ids move that metadata into the DB, where it's access-controlled.
- **Store only the original `.opus`, encrypted**; convert to WAV on demand at playback. Avoids encrypting and storing a ~10× larger derived artifact, and the conversion is already a pure function.
- GCM requires the whole message in memory — fine for ~100 KB voice messages. Note the ceiling; chunked encryption is the fix if recordings ever get long.
- Delete `GET /uploads` and `GET /uploads/{name}` — they're unauthenticated by construction (`API_SPEC.md` already flags this) and the Vue app replaces them.

## M7 — Settings, devices, and private-server UI

- [ ] Complete

Completes the frontend feature set: manage linked devices (rename, unlink), list and revoke active sessions, set a custom `serverUrl` for users running their own instance, delete recordings, change password.

---

## Verification

**Per milestone:** `gofmt` clean, `go vet ./...`, `go test -race ./...` green. Each milestone's acceptance criterion is stated inline above; M1 and M2 both reduce to "the watch keeps working and `API_SPEC.md` still describes reality".

**End-to-end on real hardware** (this project has always been verified this way, and should stay that way):

1. Record on the watch → confirm it arrives, converts, and is playable.
2. Airplane mode → record → reconnect → confirm the persistent upload queue still drains (M1 must not regress `uploadQueue.js` behavior).
3. Send the same `fileName` twice with `curl` → identical response, no duplicate file.
4. From M5: confirm an unpaired watch is rejected, and that a revoked device token stops working immediately.

**Testing infrastructure:** the project currently has zero automated tests. Go's tooling makes this the natural moment to fix that — `httptest` for handlers, golden files for audio conversion, a local Postgres for store tests.

## Open questions, to settle at the milestone that needs them

- ~~**M0:** does `pion/opus` decode the watch's packets, or is the cgo fallback needed?~~ Resolved 2026-08-09: `pion/opus` works on the whole corpus, within 0.1% RMS of libopus. Staying pure Go — no `Dockerfile`, no `libopus-dev`, cross-compilation intact.
- ~~**M0:** is the packet prefix 8 or 12 bytes?~~ Resolved 2026-08-09: 8 bytes. See M0.
- **M3:** bcrypt now with an argon2id upgrade later, or argon2id from the start?
- **M6:** confirm real `.opus` file sizes on device before committing to whole-file in-memory GCM (measure during M1 — the byte length is already logged).
