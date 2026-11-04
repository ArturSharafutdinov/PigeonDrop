# 0002. The upload queue lives on the watch, not on the phone

Status: accepted, 2026-08.

## Context

A recording made while the phone is out of Bluetooth range, or while the phone or the server is unreachable, used to be lost: there was no retry, and the cap on stored recordings could delete it before it was ever sent. A queue that survives that has to live somewhere persistent. The obvious place is the phone's Side Service, which already does the uploading.

## Findings

- `MessageBuilder`, the only channel that carries readable data from the watch, gives **no delivery guarantee**: a `call()` can fail without a trace when Bluetooth drops, and there is no retry or resend on reconnect.
- The Side Service lifecycle (`onInit`, `onRun`, `onDestroy`) is not documented in a way that can be relied on, and it has **no timer wake-up**: it is purely reactive. The only confirmed persistent store on its side is `settingsStorage`, a small key-value store.
- The watch has `@zos/fs`, which was used and trusted.

## Decision

The watch is the **single source of truth**. It keeps every recording until the server confirms it (`utils/uploadQueue.js`, `uploads.json` lists what is confirmed), sweeps the queue on three triggers (a new recording, Bluetooth reconnecting, the app opening), and sends one recording and one chunk at a time. The Side Service stays a thin stateless relay: receive, try `fetch()`, report back, keep nothing.

Because the confirmation travels back over the same unreliable link, a recording can be sent twice; the server therefore makes `POST /v1/messages` idempotent by `messageId` within a device, which is part of the API contract.

## Consequences

- No recording is lost to a lost link; the cost is storage on the watch, capped separately for uploaded and pending recordings.
- Duplicates are normal and handled by idempotence, not avoided.
- There is no periodic retry timer: a watch that stays connected and idle retries only on the three triggers. This was chosen deliberately and is listed as a known limitation.
- **Learned later:** every page and `app.js` is its own bundle with its own copy of the queue module, so the queue's live state has to be kept in the app's global data, with an expiring lease for "a sweep is running". See [architecture.md](../architecture.md), section 3.
