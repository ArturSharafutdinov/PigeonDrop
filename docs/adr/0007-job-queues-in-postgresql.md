# 0007. Background work is queued in PostgreSQL, not in a broker

Status: accepted, 2026-10-10.

## Context

Two kinds of background work follow an upload: transcription, and delivery to Telegram. The load is tiny (about 330 recordings a day at 1,000 minutes a month; a recording takes one or two seconds), the server is one process, and PostgreSQL is already there. A message broker or Redis would be a new service to run and to lose data in.

The stack comparison had suggested a PostgreSQL-backed job library for Go (River). For one kind of job and one consumer it was not worth its schema and dependency yet.

## Decision

Each queue is **a table with one row per recording**, created **in the same transaction that stores the message**. A recording cannot be accepted without its place in the queue, a rolled-back upload leaves nothing behind, and a repeated upload (the same `messageId`) creates no second row. The row is deleted with its message (the person deleted it, the quota evicted it, the account went).

A worker is a goroutine inside `pigeondrop serve`, under `internal/supervise`:

- takes the oldest ready row with `FOR UPDATE SKIP LOCKED` under a **lease** (`locked_until`), so two servers during a redeploy never take the same row and a dead worker's row is taken again;
- **fences** the finishing write by the attempt number, so a late result cannot overwrite a newer one;
- counts attempts only for failures that are the recording's fault (a service that cannot be reached gives the attempt back and pauses the worker with growing delays);
- is woken by a new recording and otherwise polls every 2 seconds.

States are `pending`, `running`, and a final one (`done`/`sent`, `failed`, `skipped`), with the reason in `error_code`; constraints in the table keep the state, the text and the timestamps consistent.

## Consequences

- No new infrastructure; state is readable with plain SQL and covered by the database backups.
- The tests run against a real PostgreSQL and break each protection on purpose to see them fail.
- When several kinds of jobs and chains arrive, the tables can be replaced by a library such as River; the transaction rule stays.
- A single instance is assumed for the in-memory rate limiters; the queues themselves are safe with two.
