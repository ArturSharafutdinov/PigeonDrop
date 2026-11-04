# Architecture decision records

One short record per decision that shaped the system: what was decided, why, and what it costs. They were condensed from the longer requirements analysis, stack comparison and design plans written while the project was designed (those documents stay in the git history of the original repository). A record states how sure it is: *measured* (I ran it), *source* (read in the documentation), *estimate* (reasoned, not tried).

| # | Decision |
|---|---|
| [0001](0001-go-and-postgresql.md) | The server is Go with PostgreSQL |
| [0002](0002-upload-queue-on-the-watch.md) | The upload queue lives on the watch, not on the phone |
| [0003](0003-chunked-acknowledged-upload.md) | Recordings go over Bluetooth in acknowledged chunks |
| [0004](0004-standard-ogg-opus-on-the-phone.md) | The phone repackages Zepp's Opus into a standard Ogg/Opus file |
| [0005](0005-device-pairing-rfc-8628.md) | Watches are paired with RFC 8628 and carry a revocable device token |
| [0006](0006-one-recognition-at-a-time.md) | Speech recognition: Parakeet TDT v3, one recording at a time, in a container of its own |
| [0007](0007-job-queues-in-postgresql.md) | Background work is queued in PostgreSQL, not in a broker |
| [0008](0008-telegram-long-polling-and-delivery-queue.md) | The Telegram bot uses long polling and a delivery queue that waits for the transcript |
| [0009](0009-openapi-first-and-sqlc.md) | The API is OpenAPI-first and the SQL is generated with sqlc |
