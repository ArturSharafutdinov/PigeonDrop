# 0009. The API is OpenAPI-first and the SQL is generated with sqlc

Status: accepted, 2026-10-03.

## Context

The watch and its Side Service depend only on the HTTP contract, not on any implementation, and they cannot be updated as fast as a server can: a changed status code or field can break a watch with no change on its side. A second client (the web page, later a real front end) will use the same API. The server's queries also need to be correct against a schema that changes with migrations.

## Decision

- **`backend/api/openapi.yaml` is the source of truth.** Types and the server's handler interface are generated from it with `oapi-codegen`; the contract is changed first, then `make generate`, then the code. [`docs/api.md`](../api.md) is the prose version of what the watch relies on, with the invariants that must survive any change.
- **SQL is written as SQL** in `internal/db/queries` and `sqlc` generates the typed Go code, checked against the migrations at generation time. No ORM; goose migrations are forward only and embedded in the binary.
- Ownership checks live inside the SQL queries, not in the handlers, so they cannot be forgotten.
- **CI fails when generated code is stale**, and `make check` does the same locally.
- Input is untrusted: every length is checked and what cannot be parsed is an error, not a panic; the parsers have fuzz tests.

## Consequences

- A change of contract is visible in one file and in the diff of the generated code.
- The generators are a separate Go module (`backend/tools`) so that their dependencies stay out of the server's.
- Generated files must never be edited by hand, which CI enforces.
- Some behaviour is not expressible in the OpenAPI file (rate limits, the answer's meaning for the watch); it is in the prose document and the tests.
