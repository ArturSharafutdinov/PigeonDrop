# Speech recognition for PigeonDrop: comparing the options

Version 1.1, 2026-10-09 (the VPS check of section 7.7 added 2026-10-10). Status: **the local models were measured on the author's own recordings (section 7); the cloud services were surveyed but not measured**. The decision is recorded in [ADR 0006](../adr/0006-one-recognition-at-a-time.md); this is the evidence behind it.

How reliable a statement is, is marked in square brackets: **[measured]** I ran it; **[source]** read on the project's page or in its documentation, not run; **[secondary]** taken from retellings, check it before relying on it; **[estimate]** reasoned or common knowledge, not tried. Where there is no mark, the statement follows from the lines next to it.

## 1. Conditions and criteria

Decided by the author on 2026-10-05:

- **Audience:** the EU and the world (so GDPR and where data is processed matter).
- **Languages:** Russian now, English and others when published. The model must be **replaceable without pain**.
- **Volume at the start:** up to 1,000 minutes a month for the whole service.
- **A cloud service is acceptable, a local model is better.** The VPS is limited; 4 GB of RAM could be added. (The author later decided that recordings must not leave the server, which settled the choice for a local model.)

The input: a recording of up to 60 s; the server keeps the original Ogg/Opus (SILK, 16 kHz, mono) and makes a 16 kHz WAV from it when needed (`decodeToWAV` in `backend/internal/httpapi/recordings.go`). A minute of audio is about 1.9 MB as WAV.

Criteria: quality on Russian and on audio from a wrist microphone (noise, wind); languages; price; where the data is processed; load on the VPS; how easy the choice is to replace.

## 2. Cloud services (surveyed, not measured)

Hosted speech-to-text costs from roughly $0.67 to $4.50 per 1,000 minutes for the services named here **[secondary]**: for example Mistral Voxtral Mini Transcribe V2 about $3.00 (13 languages including Russian, data in the EU), OpenAI's transcription models about $4.50, Groq's Whisper large-v3-turbo about $0.67, Speechmatics about $2.15 to $4.00. For the first stage's volume that is a few dollars a month. None of them was measured on the author's recordings, because recordings are personal data and were not to be sent to a third party, not even for a benchmark. The surveyed vendors' data-retention policies were mostly not read, and prices change; the figures are from 2026-10-05.

## 3. Local models

All four run without a GPU, on the CPU. The speed figures in this table are other people's, on other hardware **[secondary]**; they are corrected by the measurement in section 7.

| Model | Languages | Russian | Memory and speed | License | Pros | Cons |
|---|---|---|---|---|---|---|
| **Parakeet TDT 0.6B v3** (onnx-asr or sherpa-onnx, int8) | 25 European languages (ru, en, uk, de, fr, es, it, pl and others), detects the language itself [source] | WER 5.51% on FLEURS, 3.00% on CoVoST; with 10 dB noise the overall WER grows from 6.34 to 7.12% [source] | The model is ~640 MB on disk; 1.2 to 2 GB in memory [secondary]; about 20× real time on an i7-12700KF [secondary] | CC-BY-4.0: commercial use allowed, credit required [source] | Punctuation, case, word timestamps [source]; a Go API exists (sherpa-onnx, Apache-2.0) [source] | European languages only; made for NVIDIA GPUs, not measured on 1 to 2 VPS cores at first; needs 16 kHz mono WAV/FLAC input [source] |
| **GigaAM v3** (onnx-asr; the official library is PyTorch) | Russian (the family also has multilingual models, not evaluated) [source] | 1.5% WER against 12.8% for Whisper large-v3 on 200 short Golos Crowd clips; the author warns it is a small sample; 9.5% against 23.9% on call-centre audio (the developers' claim) [secondary] | 220 to 600 million parameters [source]; about 59× real time (CTC) on a fast CPU [secondary] | MIT [source] | The best Russian among open models; light on the CPU | Russian only; one call takes up to 25 s, so 60 s recordings need cutting at pauses (VAD) [source] |
| **Whisper large-v3-turbo** (faster-whisper int8 or whisper.cpp) | ~99 languages | Whisper large-v3: 12.8% on the same clips; versions fine-tuned for Russian: 5.7 to 6.4% on Common Voice [secondary] | about 1.5 GB peak in int8 [secondary]; 8 to 12× real time on a multi-core CPU, slower on 1 to 2 cores [secondary] | MIT | The only local option for **all** languages; whisper.cpp gives an OpenAI-compatible HTTP server and Go bindings [source] | Weaker on Russian; **invents text on silence and noise**: needs a VAD and `condition_on_previous_text` off [source] |
| Whisper small and medium | ~99 | clearly worse than large | 0.85 and 2.1 GB of RAM [source] | MIT | Light | Low quality on Russian |

Not evaluated: Voxtral with open weights (billions of parameters, not tried on a CPU), Vosk and Silero (an older generation).

**What this means for the VPS.** At 1,000 minutes a month about 33 minutes a day must be recognized. Even at half of real time that is about 8 CPU hours a month **[estimate]**, so the CPU load is tolerable if recordings are processed one at a time in the background. The bottleneck is memory: the models take 1.2 to 2 GB, while the server's container is limited to 512 MB, so the recognizer must be a container of its own.

**A correction from the measurement (section 7).** "Half of real time" holds for Parakeet and GigaAM (14 to 25 times faster than real time) but **not for Whisper on one core**: faster-whisper spends about 10 CPU seconds on a 5 to 8 s recording and whisper.cpp about 40, and the cost hardly depends on the length of a short recording (Whisper always processes a 30-second window). For short recordings that is 29 and 112 CPU hours a month instead of 8 **[estimate from the laptop measurement; a VPS will not be faster]**.

## 4. Keeping the model replaceable

The design idea: transcription is an interface, not a particular service. A sketch for Go (not implemented as such; the production code talks to one HTTP service):

```go
type Transcriber interface {
    Transcribe(ctx context.Context, wav io.Reader, opts Options) (Result, error)
}
```

- Two kinds of adapters cover almost everything: an "OpenAI-compatible" `POST /v1/audio/transcriptions` (OpenAI, Groq, DeepInfra and local servers answer it) and native adapters for the rest.
- **Always send 16 kHz WAV:** everyone understands it and the server can make it from the original. Not everyone accepts Ogg/Opus.
- Keep in every transcript the engine, the model, the language and the duration (the table has `engine`, `language`, `audio_ms`, `stt_ms`), so that old texts stay explicable after a model change.
- Recognition is a **background job**, not part of the upload: the watch's upload must not wait for the model.

## 5. The choice (an opinion; the decision is the author's)

1. **Locally: Parakeet TDT v3 (int8)** in a container of its own. The measurement confirmed it: 7.2% word errors on the author's recordings, about 0.4 CPU seconds for a typical recording, 14× faster than real time on one core, Russian, English and 23 more European languages at no per-minute price. The only one of the four that recognized a whisper correctly (in this setup, see 7.3). Conditions: **a memory limit of 3 GB with one recording at a time** (7.4), and its English is weaker than faster-whisper's.
2. **Do not add GigaAM for now.** It is the most accurate on ordinary Russian speech (1.5% against 5.3% for Parakeet on the long recording) and takes 0.66 GB, but knows only Russian and loses the whisper in this setup. A second engine for a few words a recording at 1,000 minutes a month does not pay; come back to it if Russian accuracy becomes the bottleneck.
3. **Whisper large-v3-turbo.** `faster-whisper` is the best on English (3.6%) and writes names in Latin letters (GitHub, Slack), but uses 16 to 25 times more CPU than Parakeet (about 10 seconds a recording against 0.4): reasonable only for languages outside the 25 European ones, and for those a cloud service is closer. **whisper.cpp is not recommended:** it is four times slower than `faster-whisper` and half as good on Russian (12.7% against 6.8%); the measurement of it was stopped as too slow.
4. **Keep the numbers in proportion.** The WERs of section 3 come from other people's recordings. The figures of section 7 come from 21 recordings of one voice on a laptop: they show the order of magnitude and the weak spots, not an exact WER, and they were repeated on the VPS itself (7.7).

## 6. Where the data stays

With a local model the audio and the text stay on the server; nothing is sent to a cloud service. The only exception is a Telegram chat that a person links themselves ([ADR 0008](../adr/0008-telegram-long-polling-and-delivery-queue.md)).

## 7. The measurement on the author's recordings

**Status: the local models were measured on 2026-10-09 [measured]; cloud services were not measured.** The harness is in [`stt-benchmark/`](stt-benchmark/); the raw results stay on the author's disk (they are not in git: it is the author's voice).

### 7.1. How it was measured

- **Recordings:** 21 recordings of one voice (the author's) made on the watch, 2026-10-05 to 08, following the script `stt-benchmark/phrases.md`: 12 in Russian (numbers, names, English words, fast speech, a whisper, three in noise), 5 in English (one in noise), 1 mixed, 1 long one of 49.6 s, 2 without speech (10 seconds of silence and 10 seconds of street noise). The order of the files was checked against the text: all 21 matched their phrases.
- **Images:** four, the model baked in, run with `--network none`. Profiles: **strict** (1 core, pinned, 2 GB without swap) and **free** (2 cores, 4 GB). The limits were verified: the container sees as many cores as it was given, is killed (code 137) when memory runs out, and has no network.
- **Repeats:** 1 warm-up run and 3 measured runs per recording, the median is taken. The text was identical in all repeats for all models.
- **The language was given** (in the product the user sets it). Automatic language detection was not tested.
- **Hardware:** a Mac with an Apple M4 Pro (arm64), Docker Desktop with 8 GB. An M4 core is faster than a typical VPS core, so the absolute times are optimistic.
- **What was and was not measured:** Parakeet's memory under long operation separately (7.4); Parakeet and GigaAM in both profiles; faster-whisper and whisper.cpp only in the strict one. The free profile for them and the language detection were not finished: whisper.cpp was stopped as too slow, the rest was cut short to get to the conclusions.

### 7.2. Results

**Accuracy** (the share of wrong words, lower is better; the profile does not affect the text; numbers count as the better of two spellings, digits or words):

| Model | all | ru | en | noise | numbers | long (49.6 s) | mixed | by characters (CER) | invented on silence |
|---|---|---|---|---|---|---|---|---|---|
| Parakeet TDT v3 | 7.2% | 6.8% | 8.9% | 4.5% | 0.0% | 5.3% | 0.0% | 2.0% | 0 words |
| GigaAM v3 | 16.4% | 6.8% | 57.1% (it does not know English) | 11.4% | 13.2% | **1.5%** | 7.1% | 7.6% | 0 words |
| faster-whisper (turbo) | **6.2%** | 6.8% | **3.6%** | 4.5% | 0.0% | 4.6% | 0.0% | 3.5% | 0 words |
| whisper.cpp (turbo q5_0) | 11.3% | 12.7% | 5.4% | 4.5% | 2.6% | 13.7% | 0.0% | 8.7% | 0 words |

**Resources** (strict profile, 1 core; on one core wall time equals CPU time):

| Model | CPU-s per minute of audio (1 / 2 cores) | a 5 to 8 s recording | a 49.6 s recording | memory after loading / peak of the run, MB | model load | CPU hours a month* |
|---|---|---|---|---|---|---|
| Parakeet TDT v3 | 4.3 / 5.9 | 0.4 s | 4.0 s | 1225 / 2038 in this run; **2232** with recordings up to 60 s (7.4) | 1.4 s | 1.2 |
| GigaAM v3 | 2.1 / 3.3 | 0.2 s | 2.7 s | 515 / 661 | 0.7 s | 0.5 |
| faster-whisper | 70.9 / not measured | 10.3 s | 24.1 s | 1658 / 1658 | 1.5 s | 29 |
| whisper.cpp | 277 / not measured | 40.4 s | 88.7 s | a process per call / 816 | at every call | 112 |

\* **[estimate]** 1,000 minutes a month as 10,000 recordings of about 6 s; a VPS core is slower than in the measurement. "CPU-s per minute of audio" includes pauses, where there is little work, so it is lower than the per-recording figure.

### 7.3. What the errors show

- **On Russian accuracy three models do not differ:** Parakeet, GigaAM and faster-whisper each have 16 errors in 236 words (6.8%); a difference of one or two words on 21 recordings is not a difference. They differ in the **kind of error**.
- **The whisper** ("Quiet, the child has just fallen asleep, let's talk later"): Parakeet recognized all of it; faster-whisper gave only the beginning, GigaAM "Quiet the child.", whisper.cpp "Quiet.". Without that recording, on Russian: GigaAM 3.9%, faster-whisper 4.8%, Parakeet 7.0%, whisper.cpp 10.1%. The three models that lost the end have a voice-activity detector (VAD) and Parakeet has none; **it is probably the detector, not the model, that cuts it off, but this was not tried without a VAD**.
- **Ordinary and long Russian speech:** GigaAM is the best (2 errors in 131 words in the long recording against 7 for Parakeet). Parakeet's errors are word endings and similar words.
- **English:** faster-whisper is the best (3.6%). Parakeet 8.9%: a wrong name spelling and an inserted "uh". GigaAM is not meant for English.
- **English words in Russian speech:** faster-whisper writes them in Latin letters (GitHub, pull request, Slack), Parakeet in Cyrillic, GigaAM in a mixture with distortions. The reference accepts both spellings, so only distortions count as errors; which spelling the user wants is a product decision.
- **A pause:** no model invented a word on silence or on street noise. For three of them that is the VAD's work; Parakeet returned an empty text without one (two recordings).
- **Parakeet's memory:** 1.22 GB right after loading, up to 2.04 GB in this run. That is neither a leak nor the limit: there was no 60 s recording in this run and the peak is set by long recordings (see 7.4).
- **Whisper on short recordings:** the cost hardly depends on the length (a 30-second window): faster-whisper spends 10 s of CPU on a 5 to 8 s recording and 24 s on 50 s.

### 7.4. Parakeet's memory under long operation (2, 3 and 4 GB)

Why: in the run of 7.2 Parakeet's peak was 2.04 GB with a 2 GB limit, and it was unclear whether that was a leak or a ceiling. The check: `stress-memory.sh` starts Parakeet in a container without swap and without network and calls it dozens of times in a row **in one process** (as the service will run), recording memory after every call. The set has a 60 second recording (the watch's limit, spliced from the author's). The first pass goes through the list, the others in random order. The container counter `memory.peak` was measured.

| Limit | Cores | Set | Calls | Killed by memory | Container peak, MB | Headroom to the limit, MB | Last rise of the peak by 5+ MB |
|---|---|---|---|---|---|---|---|
| 2 GB | 1 | 22 recordings, up to 60 s | 34 of 880 | **yes** (code 137) | at least 2065 | none | call 26 |
| 3 GB | 1 | the same | 880 of 880 | no | 2156 | 916 | call 45 |
| 3 GB | 2 | the same | 880 of 880 | no | 2167 | 905 | call 45 |
| 4 GB | 1 | the same | 880 of 880 | no | 2157 | 1939 | call 45 |
| 4 GB | 2 | the same | 880 of 880 | no | 2168 | 1928 | call 45 |
| 3 GB | 2 | 31 recordings, 27 different lengths from 4 to 60 s | 1,240 of 1,240 | no | **2232** | **840** | call 54 |

What this shows:

- **Memory reaches a ceiling and does not grow further.** The growth of the peak in the second half of a series is +0.0 to +0.1 MB. After the last rise (call 45 in the series of 22 recordings, call 54 in the series with different lengths) the peak never rose by 5 MB or more again: 835 and 1,186 calls in a row. The series with different lengths holds about 6 hours of audio and 200 calls longer than 40 seconds.
- **The peak is set by long recordings, not by the number of calls.** The first call of the 49.6 s recording raised the peak by 340 MB and the second by another 310 MB; the 60 s recording by 50 to 80 MB at its first three calls; lengths new to the process raised the peak early in a series. Short recordings do not change it. Memory after a long recording is not given back to the process, so the ceiling is reached after the first 45 to 55 calls with long input.
- **A 2 GB limit does not do:** the container was killed at call 35 (34 recorded): the 60 s recording raised the peak above the limit. In 7.2 it survived only because that run had no 60 s recording.
- **3 GB is enough for one recording at a time:** headroom of about 0.8 GB (27%). 4 GB would give about 1.8 GB (by calculation: the different-lengths series was not run at 4 GB). **Two recordings at once are not needed:** they add no speed (7.5) and memory grows to 2.9 GB in one process and 4.5 GB in two. With a 30-second limit on a recording the ceiling for one at a time is about 1.7 GB.

What it does not show (see section 8): recordings longer than 60 s, other languages, weeks of continuous operation, an x86 CPU instead of an Apple M4 (a different build of the maths kernels, memory use may differ).

### 7.5. Two recordings at once, and a 30-second limit

The same harness, the container's limit deliberately large (6 GB) to see the real peak, 2 cores. "Two threads" is one loaded model and two simultaneous calls in one process (like a worker with a thread pool); "two processes" is two copies of the model in one container (like two workers under a shared limit). The calls start together (threads) and run in random lengths; by the timestamps the calls did overlap.

| Scenario | Recording limit | Calls | Container peak, MB | Growth of the peak in the 2nd half, MB | Audio per second* |
|---|---|---|---|---|---|
| one at a time (for comparison, 7.4) | 60 s | 1,240 | 2,232 | +0.0 | not recorded |
| two threads, **the same 60 s recording at the same moment** (40 pairs) | 60 s | 80 | 2,768 | +0.7 | |
| two threads, random pairs of different lengths (18 pairs of 40+ s) | 60 s | 930 | **2,943** | +14.5 (the last rise at call 911 of 930) | 20.9 |
| two processes, two models | 60 s | 930 | **4,508** | +0.2 | 16.8 |
| one at a time | 30 s | 500 | 1,708 | +0.1 | 20.2 |
| two threads | 30 s | 1,000 | 1,977 | **+72.0: did not reach a ceiling in 20 passes** (a rise at call 551 of 1,000) | 21.5 |
| two processes | 30 s | 1,000 | 3,413 | +0.1 | 17.1 |

\* Seconds of audio recognized per second, on two cores, by the calls' timestamps (not recorded in the earlier series without timestamps).

What this shows:

- **Parallelism adds no speed.** On one recording the model apparently already uses both cores: one at a time gives 20.2 seconds of audio per second, two threads 21.5 (6% more), two processes 17.1 (15% less). The time of one call on a 4 to 8 s recording grows from 0.32 to 0.55 seconds (threads) and 0.74 (processes). So the design with one recording at a time loses no speed on two cores.
- **Memory grows noticeably.** A second simultaneous call in the same process adds about 0.7 GB at a 60 s limit (2.94 GB against 2.23 GB) and about 0.3 GB at 30 s: the model (1.2 GB) is shared, only the working buffers grow. Two copies of the model in two processes double it: 4.5 GB and 3.4 GB. So with two recordings at once a 3 GB limit does not do even for threads (headroom 129 MB, 4%); 4 GB is needed for threads and 5 GB for processes.
- **The heaviest case is not the highest.** The same 60 s recording in two threads at once (2.77 GB) gave less than random pairs of different lengths (2.94 GB): probably different sizes take different memory blocks, so the peak grows from variety of lengths and not only from the longest recording (the cause was not checked).
- **The answers are not corrupted.** The same recording across all calls (930 and 1,000 overlapping calls) gave one and the same text: recognition in `onnx-asr` is safe for simultaneous calls in this setup.
- **A 30-second limit saves about 0.5 GB** for one at a time (1.7 GB against 2.2 GB). With it a 2 GB container limit leaves 340 MB (17%), measured on 500 calls; 2.5 GB would be a better margin.
- **Did not reach a ceiling:** two threads at the 30 s limit still raised the peak by 72 MB in the second half of the series. So 2.0 GB is a lower estimate, not a ceiling; such a design would need a longer measurement before it is relied on.

### 7.6. Conclusion

For a local recognizer on the author's recordings **Parakeet TDT v3** fits: accuracy at the level of the best, 16 to 25 times cheaper than faster-whisper on the CPU, one engine for Russian, English and 23 more languages. The reservations: it needs a 3 GB memory limit with one recording at a time (7.4) and its English is weaker than faster-whisper's. GigaAM is more accurate on ordinary Russian but knows only Russian; Whisper is convenient only for languages outside the 25 European ones and is expensive on the CPU; whisper.cpp falls behind real time on one core and does not fit.

**Reservations about the measurement:** 21 recordings of one voice show the order of magnitude and the weak spots, not an exact WER (a difference of 1 or 2 words is not significant); a laptop is faster than a VPS, so absolute times on a VPS will be worse, and the ratio between models will probably stay (not tested); the finalist had to be run on the VPS itself with the same images (7.7).

### 7.7. The check on the author's VPS (2026-10-10)

A one-off kit (removed from the tree afterwards; it is in the git history) ran the real image on the VPS: x86_64, 4 cores (AVX2, no AVX-512), 7.9 GB of memory (5.5 GB available at the start), 14 other containers, kernel 6.8, Docker 29.8, cgroup v2, steal 0.00%. The image was built on the VPS itself from `stt/Dockerfile` and run as in Coolify: `--memory 3g --memory-swap 3g`, a `--cpus` quota of 1 and 2. Two runs: a quick one (limits, speed, 46 calls) and a memory step (186 calls, 6 passes over 31 recordings of different lengths, 2 cores, about 11 minutes). The raw results stayed with the author.

| | Mac (M4 Pro), 1 / 2 cores | VPS, 1 core | VPS, 2 cores |
|---|---|---|---|
| Seconds of audio per second | 12.5 / 19.3 | 3.1 | 5.3 |
| CPU seconds per minute of audio | 5.0 / 6.7 | 19.8 | 23.2 |
| An ordinary 4 to 8 s recording, median | 0.5 / 0.3 s | 1.8 s | 1.1 s |
| A 60 s recording, the worst call | 4.7 / 2.8 s | 21.3 s | 11.8 s |
| Model load | 2.7 / 2.1 s | 11 s | 12 s |

The Mac column: the same kit run on the Mac with the `stt/` image (not the harness of 7.2).

What this shows:

- **A VPS core is about four times slower than an M4 core** (the estimate before the check was two to three). A one-minute recording takes 21 s on one core and 12 s on two, which leaves 4.2 and 7.6 times of headroom to the server's 90 s timeout. 1,000 minutes of audio a month is about 5.5 to 6.5 CPU hours: the queue will not pile up. A second core speeds things up 1.7 times.
- **Memory: 3 GB is enough.** The container is charged up to 2,787 MiB of 3,072, but 711 MiB of that is the model's file cache (Docker Desktop counts it differently, so there was hardly any on the Mac); the memory that cannot be dropped reached 2,066 MiB. When little is left before the limit the kernel drops the file cache before it kills a process, so the headroom before a kill is about 1 GB (840 MB on the Mac). The ceiling was reached at call 57 of 186 (on the Mac at call 54 of 1,240), with 0 MiB of growth in the second half of the series, and the container was not killed. Dropping the cache under pressure was not tried in this run (the peak did not reach the limit).
- **The machine:** at the worst moment the host had 2,984 MiB of memory available out of 5,610 at the start.
- **Texts:** 19 of 21 matched the Mac word for word; two recordings differ by one word (in one the VPS is closer to the reference, in the other further). 6.5% wrong words against 7.2% on the Mac, 1.9% against 2.0% by characters, and still 0 invented words on silence. A one-word difference is noise: integer kernels on x86 and ARM compute slightly differently.
- **Limits and protection:** `memory.max` 3 GiB, no swap, the CPU quota as set, as many model threads as cores by the quota (the 4 visible cores of the VPS do not interfere); 700 MiB in a 256 MiB container is killed with code 137. Two recordings at once: one was accepted, the second got `429 busy` with `Retry-After: 5`, and the next ordinary one went through.

Reservations: the quick run was right after the image build (load average about 3 of 4), so speed is more likely understated than overstated; memory was measured over 186 calls, on the Mac over 1,240; not tried: the network between containers in Coolify, the backup window (03:15 to 03:30 UTC) and a week of operation.

## 8. What stayed unverified

- The accuracy of cloud services on watch audio. All WER figures in sections 2 and 3 were obtained by other people on other recordings. The local models were measured (section 7), but only on 21 recordings of one voice.
- The speed and memory of the other local models on the author's VPS: not measured (7.7 covers only the chosen Parakeet). The rest was measured on a Mac with an Apple M4 Pro, whose core is about four times faster than this VPS's.
- Not finished in the measurement: faster-whisper and whisper.cpp in the free profile (2 cores, 4 GB), and language detection for every model. For Parakeet and GigaAM only the language-detection check was skipped.
- The cause of the whisper lost by GigaAM, faster-whisper and whisper.cpp: the voice-activity detector is suspected, but a run without a VAD was not made. If the winner were not Parakeet this would be worth checking.
- Parakeet's memory was checked with one process and one recording at a time, on an arm64 Mac (7.4). Two recordings at once were measured (7.5), but the threads at a 30 s limit did not reach a ceiling in 20 passes. Not checked: recordings longer than 60 s, other languages (more or less output text), weeks of continuous operation (about 6 hours of audio and 1,240 calls were checked; on the x86 VPS 186 calls, 7.7). Ways of lowering the peak (for example switching off ONNX Runtime's memory arena) were not tried.
- Parakeet gets the recording whole, without cutting: a 60 s recording (the watch's limit) was checked, a longer one not.
- The official price pages of Google Cloud and Azure did not open at the time (their prices are [secondary]); for Amazon Transcribe the sources disagree ($0.006 and $0.024 a minute).
- The data-retention policies of the surveyed cloud vendors were not read, and neither was the support for Russian of every cloud model. Prices may change; the date of the survey is 2026-10-05.
- Whether ready-made OpenAI-compatible servers for Parakeet (community projects) are fit for use was not checked.
