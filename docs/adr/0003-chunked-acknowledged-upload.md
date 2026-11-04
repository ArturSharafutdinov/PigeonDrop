# 0003. Recordings go over Bluetooth in acknowledged chunks

Status: accepted, 2026-08.

## Context

A 60 s recording is about 160 KB, about 212 KB as base64. The first version sent a recording as **one** `MessageBuilder.call()`. `call()` cuts the payload into about 60 BLE packets of about 3.6 KB and pushes them all in one synchronous loop with no backpressure. The send queue overran, `ble.send` returned `false`, and the resulting exception was thrown inside a promise nobody handled: the upload failed **silently**. The library's debug flag made it worse by building a hex dump of every packet for the log (megabytes of strings), and unsent recordings were retried at every app start, several at once. What the person saw: recordings that did not arrive, an app that took five seconds to open and a list page that crashed (the last is a suspicion). Recordings of 4 to 25 KB had hidden all of it.

## Decision

A recording is read from disk in **15 KB chunks** (a multiple of 3 bytes, so the base64 of the chunks concatenates into the base64 of the file) and each chunk is sent with its own `request()`; the **reply to each chunk is the flow control**. The reply to the last chunk carries the server's result, and only it marks the recording uploaded. A chunk is retried twice; at the first final failure the sweep stops and the reason is shown. A file that cannot be read is that file's problem and is skipped, not the queue's. The vendored library's `DEBUG` is switched off.

## Consequences

- Memory stays flat however long the recording is; the server's contract is unchanged (it still gets one request with the whole file from the phone).
- Slower than one burst but reliable; progress ("part 4 of 11") is shown on the watch.
- The timeouts (30 s per chunk, 60 s for the last, 10 s ping) are estimates, not measured on a real Bluetooth link; the phone's own deadlines must stay shorter than the watch's waits so that "no answer" can be told from "the server is down" ([architecture.md](../architecture.md), section 3).
- Found by reading the code and reproduced on stubs (drops, a restarted Side Service, server errors, short reads); confirmed on the real watch by the author.
