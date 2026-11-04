# 0001. The server is Go with PostgreSQL

Status: accepted, 2026-10-03.

## Context

The first server was a Node prototype that stored recordings as files and metadata as JSON. The project was about to get accounts, device pairing, queues and a web front end, and the stack had to be chosen for that. The candidates for the backend language were TypeScript (Node), Go, Python, Rust, Kotlin/JVM and Elixir. The requirements: small resource use on a **shared VPS** (about 1.5 to 2 GB for everything), a working Opus decoder (recordings arrive as Opus and are played as WAV), background jobs on the database, safe outgoing HTTP requests (the server will call other services), and fast development.

What the comparison found:

- **Load does not choose the language.** At 1,000 users the peak is about 0.12 requests per second, and turning a 60 s Opus recording into WAV took 25 to 31 ms on one core of a laptop (*measured*).
- **An Opus decoder exists everywhere that matters.** One real 59.84 s recording was decoded in five libraries in three languages; all worked and two matched the reference bit for bit (*measured*).
- **Idle memory** is the main difference: about 10 to 30 MB for Go and 100 to 200 MB for Node with a framework (*estimate*; a Node prototype without requests measured 48.7 MB).
- TypeScript's strength is **one language from the watch to the server** and reuse (the Ogg packer runs unchanged on the server); Go's are the memory, a single static binary, and that a safe outgoing client (forbidding private addresses after name resolution) is simplest to write there.

## Decision

**Go** (1.26, `net/http`, `pgx`) with **PostgreSQL** and a TypeScript front end later. The comparison itself had leaned to TypeScript (one language, fastest to a first version); choosing Go was the author's decision, who knows Go and also writes code. Files stay on disk behind an interface (`internal/storage`), not in an object store: MinIO, the usual self-hosting choice, was reported archived, and a disk plus a quota is cheaper and more reliable for this size.

## Consequences

- A second language next to the watch app's JavaScript; the Ogg packer exists twice (a JavaScript one on the phone, a Go reader on the server) and each is checked against an independent reference.
- Pure-Go Opus decoding is young (`go-opus`); its version is pinned and its dependency on a newer Go release is why it is held back (see `backend/go.mod`).
- The server's memory limit is 512 MB; the speech model does not fit and lives in its own container ([0006](0006-one-recognition-at-a-time.md)).
- The load assumptions are estimates; nothing in the design needs them to be exact.
