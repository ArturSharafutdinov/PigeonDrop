# Speech recognition service (`stt/`)

A small HTTP service: a WAV goes in, text comes out. Inside is Parakeet TDT 0.6B v3 (int8) on the CPU, through `onnx-asr`. It runs in a container of its own next to the PigeonDrop server, and the queue, the retries and the storage of the text are the server's business (the reasoning: [`docs/adr/0006-one-recognition-at-a-time.md`](../docs/adr/0006-one-recognition-at-a-time.md), the choice of the model and the measurements: [`docs/research/stt-comparison.md`](../docs/research/stt-comparison.md)). The service knows nothing about users, recordings or the database, so the model is changed by replacing the image.

It uses only the Python standard library for HTTP; the heavy libraries (`onnx-asr`, `numpy`) are loaded only together with the model, so the server and its tests start without them.

## Protocol

| Request | Answer |
|---|---|
| `POST /v1/transcribe`<br>`Content-Type: audio/wav` (WAV, 16 kHz, 16 bit, mono)<br>`Authorization: Bearer $STT_TOKEN`<br>`X-Request-Id: ...` (optional) | `200` `{"text", "language", "engine", "audio_ms", "stt_ms"}` |
| (the audio is of another kind, or not a WAV) | `400` `{"code": "bad_audio", "message"}` |
| (longer than `MAX_AUDIO_SECONDS`, or the body is larger than allowed) | `413` `{"code": "too_long"}` |
| (no token or a wrong one) | `401` `{"code": "unauthorized"}` |
| (another `Content-Type`; no `Content-Length`) | `415` / `411` |
| (another recording is being recognized at this moment) | `429` `{"code": "busy"}` and `Retry-After: 5` |
| (more connections than the service serves at once) | `503` `{"code": "busy"}` |
| (the model is still loading) | `503` `{"code": "loading"}` and `Retry-After: 10` |
| (the model failed on this recording) | `500` `{"code": "internal"}`; the details only in the log |
| `GET /healthz` | `200` `{"ok": true, "engine"}`, `503` until the model is loaded |

- Every answer is JSON in UTF-8 and carries `X-Request-Id` (the one sent, if it consists of letters, digits and `._-` up to 64 characters, otherwise one that was made).
- `text` is empty when there is no speech: that is a success, not an error. `language` is `null`: Parakeet does not report the language.
- **One recording at a time.** A second one that arrives during recognition gets 429 and does not wait; the caller keeps the queue. The health check answers during recognition too. The lock that lets one recording through the model is released *before* an answer is written, also after a failure, so a client that asks again at once is never told "busy" by the request it has just heard back from.
- **The token is checked before the body is read.** A refused client is still given a short time (2 seconds, and up to the size limit) to finish sending, so that it sees the 401 and not a connection reset. At most 8 connections are served at once; the next one gets `503 busy` without a thread.
- The body is read in full, before any refusal, if its length is allowed: a server that answers and closes the connection while the client is still sending shows the client a reset instead of the answer. The length is limited (`MAX_AUDIO_SECONDS` × 32,000 bytes + 4 KiB), the connection's timeout is 30 seconds.
- The log (one JSON line per request, to stderr) never holds the audio, the text or the token: only the request id, the code, durations and the number of characters.

## Settings (environment variables)

| Variable | Default | What |
|---|---|---|
| `STT_TOKEN` | **required** | the secret shared with the server, **at least 16 characters** (`openssl rand -hex 32`); the service does not start without it, or with a short one |
| `STT_INSECURE` | no | `true` allows an empty token. For manual experiments only |
| `PORT`, `HOST` | `8000`, `0.0.0.0` | where to listen |
| `MAX_AUDIO_SECONDS` | `65` | the longest recording. Memory was checked up to 60 seconds: do not raise it without measuring again |
| `STT_THREADS` | automatic | the number of the model's threads; by default as many as the container can really use (the smaller of the cores and the `--cpus` quota), because with a quota extra threads do not speed it up but slow it down |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warning`, `error` |

All wrong values are reported at once, and the process exits with code 2. If the model could not be loaded, the process exits with code 1 (Docker restarts the container).

## Running it and its resources

```bash
docker build -f stt/Dockerfile -t pigeondrop-stt stt      # from the repository root; downloads the model, about 1.6 GB
docker run --rm -p 127.0.0.1:8000:8000 -e STT_TOKEN="$(openssl rand -hex 32)" \
  --memory=3g --memory-swap=3g --cpus=2 pigeondrop-stt
```

- **3 GB of memory without swap.** The peak with recordings of up to 60 seconds was 2.23 GB, and with 2 GB the container is killed (see the memory measurements in [`docs/research/stt-comparison.md`](../docs/research/stt-comparison.md)).
- **Do not publish the port.** The service is for the server's internal network; the token is only a second line of defence.
- The container writes nothing to disk (the audio is parsed in memory, there are no temporary files), so a read-only root file system will do. The model cache in the image belongs to the unprivileged user the service runs as, and the image is built at a pinned model revision (the build fails if upstream changed it).

## Deployment

The image is built and published by the `deploy-stt` job in CI (`ghcr.io/<owner>/pigeondrop-stt`, its own `:deployed` tag, the secret `COOLIFY_STT_APP_UUID`); before it is published it is started with the production limits and recognizes a made-up recording (`smoke-synthetic.sh`, the same checks as `smoke.sh` but on recordings made on the spot: there are no real ones in the repository). The application in Coolify: 3 GB of memory without swap, 2 cores, no public address, the token `STT_TOKEN` shared with the server; step by step: [`docs/deployment.md`](../docs/deployment.md), step 9. `pigeondrop stt check` in the server's container checks the connection to the server.

## Credit and license of the model

The service runs NVIDIA Parakeet TDT 0.6B v3 (`nvidia/parakeet-tdt-0.6b-v3`), license [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/): giving credit is mandatory. The weights are taken in the form istupakov published them for the [`onnx-asr`](https://github.com/istupakov/onnx-asr) library (the ONNX conversion `istupakov/parakeet-tdt-0.6b-v3-onnx`, the int8 variant, the same license); the model was not changed by us. The image has the weights inside, so anyone it is handed to receives this license too. Where the credit is shown to people: the `/pair` page (`backend/internal/httpapi/pair.html`), `backend/README.md`, [`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md).

## Testing

```bash
cd stt && python3 -m unittest discover -s tests -t .      # no Docker and no model, seconds
./smoke.sh /path/to/your/wav/recordings                   # the real image with the limits, your recordings; needs a built image
```

The tests start a real server on a free port with a fake model: the token and its absence, every kind of bad audio, the length and body limits, "busy" for a second request, a model failure without stopping the service, the connection limit, a log without the text and the token. Each protection was checked by breaking it: the tests fail if it is removed.

`smoke.sh` starts the image with the limits from the section above, sends every WAV in the directory several times, compares the text with the results of the benchmark harness (`docs/research/stt-benchmark`, if there are any) and then prints the container's memory. It is the only place where the real model gets audio from memory: a test with a fake model does not prove that.

Result at the first release (2026-10-10, a Mac with an Apple M4 Pro, the image 1.58 GB, a build of about 3 minutes, limits `--memory=3g --cpus=2`): 21 recordings in 2 rounds, 42 requests, **no errors; all 21 texts matched the benchmark harness's results** (so audio from memory through the service gives the same text as reading a file), 353 seconds of audio recognized in 18 seconds, the container's memory at the end 1.98 GiB of 3, no kills by memory, and the model is read from the image by an unprivileged user without writing to disk.

## What is not verified

- Speed and memory on a VPS: all the numbers are from a Mac with an Apple M4 Pro, whose core is faster than a typical VPS core.
- The image was verified to build and run only on arm64 (a Mac); the x86 build (for a VPS) is made in CI at deployment, and `smoke.sh` has to be repeated there.
- Long operation of one process: memory was checked with the benchmark harness, on the `onnx-asr` engine directly and not through this service.
- Two recordings at once are deliberately not supported (429); whether concurrency is needed will be shown by a measurement on a VPS.
