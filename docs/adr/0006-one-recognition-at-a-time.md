# 0006. Speech recognition: Parakeet TDT v3, one recording at a time, in a container of its own

Status: accepted, 2026-10-10.

## Context

Recordings should come with text. Requirements: **no cloud** (audio must not leave the author's server; recordings are personal), good accuracy on Russian and English, modest CPU use on a shared 4-core VPS, and a hard memory budget.

Four local models were measured on 21 of the author's real recordings (Russian, English, noise, numbers, a long one, a mixed-language one, silence) on a laptop, and then on the VPS itself:

| Model | Word error rate | Note |
|---|---|---|
| Parakeet TDT v3 | 7.2% | multilingual, fast, no invented words on silence |
| faster-whisper (turbo) | 6.2% | the most accurate, but slower and heavier |
| whisper.cpp (turbo q5) | 11.3% | |
| GigaAM v3 | 16.4% | very good on Russian, no English at all |

Parakeet used about 0.4 CPU-seconds for a 5 to 8 s recording and a **peak of 2.23 GB of memory with recordings up to 60 s**; the container was killed with a 2 GB limit. On the VPS a core was about four times slower than the laptop's: a one-minute recording in 21 s on one core and 12 s on two, an ordinary recording in a second or two. Two recordings at once did **not** add speed (20.2 audio seconds per second for one recording, 21.5 for two threads, 17.1 for two processes) but memory grew to 2.9 and 4.5 GB.

## Decision

- **Parakeet TDT 0.6B v3, int8, through `onnx-asr`**, in a **separate Python container** with **3 GB of memory and no swap** and 2 cores. The Go server sends it a WAV over HTTP; the model does not fit in the server's 512 MB.
- **One recording at a time**: the server's worker is sequential and the service also answers `429` when it is already busy (this matters while an old and a new container overlap during a redeploy).
- The model reads audio, not Opus, so Opus must be decoded somewhere; the Go server already does it (`audio.ToWAV`, about 30 ms a minute, both recording formats), and the model image then needs no audio decoder at all.
- The service is reachable only on the internal network and requires a shared token; it knows nothing about users or the database. With `STT_URL` empty the whole feature is off.
- The longest recording sent to the model is 65 s: memory was measured up to 60 s. Longer recordings are stored and not transcribed.

## Consequences

- Accuracy is traded for memory and speed: faster-whisper was slightly more accurate. The text can be wrong, especially with whispering, noise and names, and the page says so.
- Numbers come from a laptop and the author's VPS, not from every VPS; memory under the backup window and peaks of other applications was not measured.
- The model is CC BY 4.0: its author must be credited (the page, the README, [THIRD_PARTY_NOTICES.md](../../THIRD_PARTY_NOTICES.md)). The image pins the model's revision and fails to build if upstream changed it.
- Details and numbers: [the comparison](../research/stt-comparison.md).
