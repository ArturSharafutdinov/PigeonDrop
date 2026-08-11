# M1 backlog — port the Node service to Go

_Created 2026-08-09. Companion to [ROADMAP.md](../ROADMAP.md) M1. Tick tasks off as they land._

**Goal:** reimplement every endpoint in [API_SPEC.md](../API_SPEC.md) in Go with the JSON-file stores intact, change nothing on the watch, then delete `server/`.

Tasks are ordered so nothing is ever blocked by later work. Each is ~1–2 hours, gated by an automated test, with a manual command to see it work. Package layout: `config`, `store`, `recordings`, `httpapi`, `audio` (done in M0), plus `cmd/pigeondrop`.

**Paths in this document are relative to `backend/`** — that is the working directory for every command below. References to the old Node service therefore appear as `../server/…`.

## Three rules that apply to every task

1. **The wire format must not change.** `API_SPEC.md` is the contract and the watch is not being rebuilt. Field names, status codes, and the `{ok:…}` envelope stay exactly as they are.
2. **Go must read JSON that Node wrote.** The Railway volume already holds `data/accounts.json` in Node's format, and there is no migration step. T2 and T3 verify this against the real files.
3. **Keep Node running on `:3000` while porting.** Run Go on `:8080` and diff the two. Cheapest possible parity check, and it disappears the moment `server/` is deleted.

---

## T1 — Config + logger + runnable skeleton

- [ ] Done

**Files:** `internal/config/config.go`, `cmd/pigeondrop/main.go`

`Config{Port, StorageDir, DefaultServerURL}` plus `Load()`. Defaults must match Node exactly (`storage.js`, `server.js:44-55`): port `3000`, storage dir the working directory, default server URL `http://localhost:<port>`. Build a `slog` JSON logger in `main` and log the resolved config at startup. This replaces `logger.js` entirely — no file logging, Railway captures stdout.

**Test:** table-driven with `t.Setenv` — defaults, each override, and that `DefaultServerURL` follows a custom port.
**Manual:** `go run ./cmd/pigeondrop` prints one startup line; re-run with `PORT=9999` and watch it change.

## T2 — Accounts store

- [ ] Done

**Files:** `internal/store/jsonfile.go`, `internal/store/accounts.go`

Port `accounts-store.js`. `Account{Code, CustomServerURL *string, CreatedAt, UpdatedAt}`; an `Accounts` type owning a path and a `sync.Mutex`; methods `Get`, `Upsert`, `UpdateServerURL`. Preserve the exact semantics: missing file → empty, corrupt file → log and treat as empty (never fatal), `Upsert` on an existing code returns it **unchanged**, `UpdateServerURL` on a missing code reports not-found rather than creating one.

**Test:** upsert idempotency, update-missing, corrupt-file tolerance, and **a concurrency test firing 100 goroutines at `Upsert`**. Run with `-race` and confirm it fails *without* the mutex — that is the lesson.
**Manual:** point a throwaway test at the real `../server/data/accounts.json` and confirm it parses (rule 2).

## T3 — Uploads dedup store

- [ ] Done

**Files:** `internal/store/uploads.go`

Port `uploads-store.js`. Map `watchFileName → {BytesReceived, SavedAs, WavFile, ReceivedAt}` with `Get` and `MarkProcessed` (which stamps `ReceivedAt`). Same mutex and corrupt-file rules as T2; reuses `jsonfile.go`.

**Test:** round-trip, overwrite, missing key, plus the `-race` concurrency test.
**Manual:** same as T2, against the real `../server/data/uploads.json`.

## T4 — Recordings directory

- [ ] Done

**Files:** `internal/recordings/recordings.go`

Owns everything about audio files on disk: the storage directory, `Save(name, data)`, `List()` (newest first), `Open(name)`, and the `receivedAtFileName()` naming from `server.js:97-115` (`2026-07-30_16-05-42-123` — sortable, filesystem-safe, millisecond suffix so two uploads in the same second cannot collide). **Path-traversal rejection lives here, not in the handler** — a name containing `/` or `..` is refused at the storage boundary.

**Test:** `t.TempDir()` throughout — save/read round-trip, listing order, traversal attempts (`../../etc/passwd`, `a/b`) rejected, name-format check.
**Manual:** point `List()` at `../server/uploads/` and confirm it returns the 9 real recordings, newest first.

## T5 — HTTP skeleton: server, routing, envelope, middleware

- [ ] Done

**Files:** `internal/httpapi/{server.go,respond.go,middleware.go}`, `cmd/pigeondrop/main.go`

A `Server` struct holding config, stores, recordings, logger, and a `*http.ServeMux`, with handlers as methods on it. One `routes()` method holds the entire URL surface — readable in a single screen, unlike `server.js`'s `else if` chain. `respond.go` centralises the `{ok:true,…}` / `{ok:false,error:…}` envelope. Middleware: request logging (replacing `log(req.method, req.url)`) and panic recovery that logs and returns `500 {ok:false,error:"internal error"}`.

**No `uncaughtException` equivalent is needed** — `net/http` already contains a panicking handler rather than letting it kill the process.

**Test:** unknown route → `404 {"ok":false,"error":"not found"}`; a deliberately panicking handler → 500, process survives. `httptest.NewRequest`/`NewRecorder`, no port binding.
**Manual:** `curl -i localhost:8080/nope`

## T6 — GET /setup

- [ ] Done

**Files:** `internal/httpapi/static/setup.html`, `internal/httpapi/setup.go`

Copy `../server/public/setup.html` into the package and serve it with `//go:embed`. **`//go:embed` cannot reference `..`**, so the file has to physically live inside the `httpapi` package directory — this surprises people who expect it to behave like a path. Content unchanged; the page parses `?code=` itself in the browser.

**Test:** 200, `Content-Type: text/html`, body contains a known marker string.
**Manual:** open `http://localhost:8080/setup?code=test-123` and confirm the page renders and shows the code.

## T7 — POST /accounts and GET /accounts/{code}

- [ ] Done

**Files:** `internal/httpapi/accounts.go`

Both handlers plus the response DTO. **Define the API shape as its own type in `httpapi`**, separate from `store.Account`, carrying the `json` tags and the resolved `serverUrl` (`customServerUrl || DefaultServerURL`) — the `toPublicShape` logic from `accounts-store.js:67-75`. With one implementation this feels redundant, but M2's entire acceptance criterion is "the storage changed and the API didn't", and separate types make that structurally impossible to break rather than merely intended.

**Test:** create → 200 with resolved `serverUrl`; repeat create → same record, `createdAt` unchanged; missing `code` → 400 `expected { code }`; malformed JSON → 400 `invalid JSON body`; unknown code → 404 `account not found`.
**Manual:**
```bash
curl -s -X POST localhost:8080/accounts -H 'Content-Type: application/json' -d '{"code":"test-123"}'
```
then `curl -s localhost:8080/accounts/test-123`, and diff both against `:3000`.

## T8 — PUT /accounts/{code}

- [ ] Done

**Files:** `internal/httpapi/accounts.go`

Sets `customServerUrl`; `null` resets to the default. Keep the deliberately-light validation from `server.js:223` — `null`, or a string starting with `http://`/`https://`, and **not** full URL parsing. `API_SPEC.md` lists that looseness as intentional, so "improving" it would be a contract change. Must 404 rather than create.

**Test:** set custom → `serverUrl` reflects it and `updatedAt` moves; set `null` → falls back to default; `"ftp://x"` and `"nonsense"` → 400 with the exact message; unknown code → 404.
**Manual:**
```bash
curl -s -X PUT localhost:8080/accounts/test-123 -H 'Content-Type: application/json' -d '{"customServerUrl":"https://example.com"}'
```

## T9 — POST /messages, storage path only

- [ ] Done

**Files:** `internal/httpapi/messages.go`

Everything except conversion: parse the body, require `fileName` and `audio`, check the dedup store and short-circuit with the original result, base64-decode, save through `recordings`, record via `MarkProcessed`, respond. Leave `wavFile` as `null` for now.

**Test:** happy path returns `bytesReceived`/`savedAs`; **replaying the same `fileName` returns an identical response and creates no second file** — the `API_SPEC.md` idempotency invariant; missing fields → 400; invalid base64 → 400.
**Manual:**
```bash
curl -s -X POST localhost:8080/messages -H 'Content-Type: application/json' -d "{\"fileName\":\"voice_test.opus\",\"audio\":\"$(base64 -i ../pigeondrop/uploads/2026-08-05_15-52-28-383.opus | tr -d '\n')\"}"
```
Run it twice — the second response must be identical to the first.

## T10 — POST /messages, wire in the converter

- [ ] Done

**Files:** `internal/httpapi/messages.go`

Call `audio.OpusToWAV` (built in M0) after saving, store the `.wav` alongside, populate `wavFile`. **A conversion failure must not fail the upload** — log it, leave `wavFile: null`, still return 200. `API_SPEC.md` states this as required behavior: saving and converting are two separate chances to fail, and the second must not void the first.

**Test:** a real fixture yields a non-nil `wavFile` and a `.wav` on disk; **garbage audio still returns 200 with the recording saved and `wavFile: null`** — that is the test that actually matters here.
**Manual:** re-run T9's curl with a fresh `fileName`; confirm both `.opus` and `.wav` appear and the `.wav` plays.

## T11 — GET /uploads listing

- [ ] Done

**Files:** `internal/httpapi/uploads.go`

HTML index of `.opus`/`.wav` files, newest first, with a count. Use **`html/template`**, which escapes automatically and replaces the hand-rolled `escapeHtml` in `server.js:244`.

**Test:** listing contains the saved names in the right order; a filename containing `<script>` comes back escaped.
**Manual:** open `http://localhost:8080/uploads`

## T12 — GET /uploads/{name}

- [ ] Done

**Files:** `internal/httpapi/uploads.go`

Serve a single file. Traversal rejection comes from `recordings` (T4) → 400 `invalid filename`; missing → 404 `file not found`; content type `audio/wav` for `.wav`, otherwise `application/octet-stream`. Prefer `http.ServeContent` — it handles range requests, so the browser can seek within a recording.

**Test:** download round-trip byte-compare; `../secret` → 400; unknown name → 404; correct content type per extension.
**Manual:** click a link on `/uploads` and confirm it plays in the browser.

## T13 — Graceful shutdown and startup hardening

- [ ] Done

**Files:** `cmd/pigeondrop/main.go`

`signal.NotifyContext` for SIGINT/SIGTERM, `srv.Shutdown` with a timeout, ensure storage directories exist at startup, and fail fast with a clear message on bad config. Railway sends SIGTERM on every redeploy, so this is what decides whether an in-flight upload finishes or is dropped.

**Test:** start on `:0` inside a test, issue a request, cancel the context, assert `Shutdown` returns cleanly.
**Manual:** `Ctrl-C` a running server and see a clean shutdown line instead of an abrupt exit.

## T14 — Parity sweep against Node

- [ ] Done

**Files:** `scripts/parity.sh`

With both servers up, compare every endpoint in `API_SPEC.md` — status code and JSON body, for each success case *and* each error case. This is M1's real acceptance gate: everything before it tested Go against expectations, this tests Go against the actual Node service.

**Test:** the script exits non-zero on any mismatch.
**Manual:** run it with both servers up and investigate every diff before moving on.

## T15 — Deploy to Railway

- [ ] Done

**Files:** Railway service settings

Root directory `backend`, with an explicit build command (`go build -o app ./cmd/pigeondrop`) since Railpack expects `main` at the module root and this is a `cmd/` layout. Reuse the existing volume and env vars (`STORAGE_DIR`, `DEFAULT_SERVER_URL`, `PORT`). The existing `data/*.json` must be picked up unchanged — this is where rule 2 pays off.

**Test:** `curl https://<railway-url>/accounts/<a real existing code>` returns the account Node created.
**Manual:** `/setup` and `/uploads` load in a phone browser.

## T16 — End-to-end on the watch, then retire Node

- [ ] Done

**Files:** delete `server/`

Record on the watch and confirm it lands, converts, and plays. Then airplane-mode → record → reconnect, confirming the persistent upload queue still drains (M1 must not regress `uploadQueue.js`). Finally resend a duplicate `fileName` by hand and confirm dedup holds. Only then delete `server/` — git history keeps it.

**Test:** `go test -race ./...` green, and the three device scenarios pass.
**Manual:** the watch itself. No `config.js` change is needed as long as the Railway URL is unchanged.

---

## Verification

- **Per task:** `gofmt -l .` clean, `go vet ./...`, `go test -race ./...` green, plus that task's manual command.
- **Milestone:** T14's parity sweep passes, then T16's three device scenarios.
- **Done means:** the watch works against Go exactly as it did against Node, with zero watch-side changes, and `server/` is gone.

## Notes

- **Do `/accounts` before `/messages`** (T7–T8 before T9–T10): three simple handlers establish the pattern before the complicated one that touches dedup, disk, and the converter all at once.
- **T5 can be pulled earlier** if seeing a live server sooner helps — its tests don't depend on the stores. The cost is stubbing handlers you'll rewrite.
- **`main.go` should end up under ~60 lines.** Much past that means logic has leaked out of the packages and back into wiring.
