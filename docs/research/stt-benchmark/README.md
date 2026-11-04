# Speech recognition benchmark (local models)

Compares local "audio → text" models on **the same recordings** made on the watch, each in its own Docker image with VPS-like limits. Why this was done and why these models were chosen is in [`../stt-comparison.md`](../stt-comparison.md). The project and the server are **not needed** to run it: an image receives WAV files and prints text, all from the terminal.

| Image | Model | How it runs |
|---|---|---|
| `stt-parakeet` | NVIDIA Parakeet TDT 0.6B v3, int8 | `onnx-asr` (ONNX Runtime) |
| `stt-gigaam` | GigaAM v3 e2e RNNT, int8, Russian | `onnx-asr` + Silero VAD (a call takes up to 25 s, so the recording is cut at pauses) |
| `stt-faster-whisper` | Whisper large-v3-turbo, int8 | `faster-whisper` (CTranslate2), VAD, without `condition_on_previous_text` |
| `stt-whispercpp` | Whisper large-v3-turbo, ggml q5_0 | `whisper.cpp` v1.9.4, VAD, `-mc 0` |

The models are downloaded **while the image is built** and live inside it, so the measurements run with `--network none`. All four images print the same JSON; the common runner is `docker/common/runner.py`.

The test phrases are in Russian and English because that is what the author speaks (the product's recordings are mostly Russian with English words mixed in); `phrases.md` has an English gloss for each Russian phrase. The harness is language-agnostic: to benchmark other languages, change `phrases.md` and `references.tsv`.

## What you need

- **Docker Desktop with at least 8 GB of memory** (Settings → Resources → Memory). The `relaxed` profile gives the container 4 GB and Docker's virtual machine needs some room on top; `run.sh` skips a profile by itself when there is not enough memory.
- `ffmpeg` and `ffprobe` (`brew install ffmpeg`), `python3` (standard library only).
- Disk space: the four images take about 8 GB (faster-whisper 3.8 GB, Parakeet 1.6 GB, whisper.cpp 1.3 GB, GigaAM 0.85 GB), plus the build cache.

## Workflow

1. **Build the images** (slow, about 5 GB are downloaded): `./build.sh` (or `./build.sh parakeet`).
2. **Check that the harness works:** `./smoke.sh` (three short files spoken by macOS voices), then `./check-limits.sh`. The latter checks that the container really sees as many cores as it was given, that it is killed (not swapped) when it runs out of memory, and that there is no network.
3. **Record the script.** Read the phrases from [`phrases.md`](phrases.md) on the watch in order, one recording per phrase (21 recordings, about 15 minutes).
4. **Download the originals** from the `/pair` page with the "Download original" button into one folder, for example `~/Downloads/stt-originals`. Download from the oldest recording to the newest: the names (server ids) do not tell which phrase is which, so the order comes from the download time.
5. **Import:** `python3 import_recordings.py ~/Downloads/stt-originals`. The script matches the files to the phrases, converts them to 16 kHz mono WAV in `data/wav/` and warns if a duration does not fit the text of the phrase (which means the order got mixed up). You can start with `--dry-run`.
6. **Run:** `./run.sh`. One image and profile takes from a few minutes (Parakeet, GigaAM) to half an hour or more (Whisper on one core). For a quick trial: `REPEAT=1 PROFILES=strict ./run.sh`. Automatic language detection is checked by `MODES="hint auto" ./run.sh`.
7. **Score:** `python3 score.py` (a Markdown table) or `python3 score.py --details` (the reference and the answer for every recording).
8. **Check memory over a long run** (for Parakeet, which the service keeps in one process): `./stress-memory.sh`, then `python3 memory_report.py`. The script runs all recordings and a 60 s recording dozens of times in a row in a container with a memory limit (by default 3 and 4 GB, 1 and 2 cores, 40 passes, about 40 minutes) and writes the memory after every call; the summary shows whether the container was killed, where the peak stopped growing and how much headroom is left. Variables: `MEMS="2 3"` (limits), `PASSES`, `CPUSETS`, `LENGTHS="8 14 20 26 32 38 44 50 56"` (also recordings of different lengths), `OUT` (where to write). The peak memory is set by the longest recordings, so without the 60 s recording the check underestimates the need. **Two recordings at the same time:** `WORKERS=2 SYNC=1` (two threads, one model) or `WORKERS=2 CONCURRENCY=processes` (two processes, two models under a shared limit); `SAME_ORDER=1 STRESS_PATHS="/data/stress/max60.wav /data/wav/long01.wav"` sets up the heaviest case (the same recording at the same time). **A different recording limit:** `MAXSEC=30` drops the longer ones. The summary for such runs (`python3 memory_report.py --results FOLDER`) prints a second table: whether the calls really overlapped, how many pairs of long ones there were, and whether the answers to one recording agreed.

Profiles: **strict** is 1 core and 2 GB, **relaxed** is 2 cores and 4 GB. Cores are set both by a quota and by pinning (`--cpuset-cpus`), so the program sees exactly as many cores as it would get on a VPS and picks the number of threads itself. Memory is given without swap.

## How to read the table

| Column | Meaning |
|---|---|
| WER ru / en / noisy / numbers / long / mixed / all | The share of wrong words, computed over the whole set at once. "All" leaves out the recordings with numbers and the pauses. **"Numbers" are separate:** the same number can be written in digits or in words, so for such recordings the best of the reference variants counts |
| CER | The same by characters |
| pause: words | How many words the model invented on the two recordings without speech (should be 0) |
| language detected | In `auto` mode: how many times the model chose Russian or English correctly by itself |
| CPU-s per audio min | CPU seconds per minute of audio: **the main speed figure**, it hardly depends on how many cores were given |
| RTF | Recognition time (without loading the model) divided by the audio duration; on a Mac it is optimistic, see below |
| peak, MB | The largest memory of the process |
| load, s | How long the model takes to load |
| stability | The share of recordings for which three runs gave the same text |

## Caveats

- **A Mac with an Apple M4 Pro (arm64) is faster than a typical VPS core.** `--cpus` limits the share of time but not the speed of the core itself, so wall-clock time and RTF are understated here. CPU seconds per audio minute transfer better. For the finalists the run has to be repeated on the VPS itself: build the same images there (`./build.sh` builds for the VPS's architecture) and run `run.sh` at a quiet time, since the VPS is shared.
- **About 21 recordings of one voice** show the order of magnitude and the weak spots, not an exact WER. Numbers and English names are written in several variants, but not in all possible ones.
- GigaAM understands only Russian, so its result on the English recordings is meaningless (by design: it is visible in the table).
- Whisper on one core is slow, so in the `strict` profile it may not keep up with the audio; that is the answer to "will such a VPS cope".

## Files

| File | Purpose |
|---|---|
| `phrases.md`, `references.tsv` | The script to read and the reference texts (with variants for writing numbers and names) |
| `docker/` | `common/runner.py` and one directory per image (`Dockerfile`, `engine.py`) |
| `build.sh`, `run.sh`, `smoke.sh`, `check-limits.sh` | Build, run, trial files, limit checks |
| `import_recordings.py`, `score.py` | Preparing the recordings and counting errors |
| `stress-memory.sh`, `docker/common/stress.py`, `memory_report.py` | Memory check over a long run: the launcher, the in-container measurement after every call, the summary |
| `test_score.py`, `test_import.py`, `test_memory_report.py` | Tests: `python3 -m unittest` (from this folder) |
| `data/`, `results/` | Your recordings (`data/stress/` and `data/varied/` are built from them) and the raw results. **Not in git** (see `.gitignore`): it is your voice |

## What comes next

The results of the local-model benchmark (2026-10-09) are in section 7 of [`../stt-comparison.md`](../stt-comparison.md). The same recordings and the same `score.py` would go to cloud services as well (a separate step; you put the keys into environment variables yourself), and their results would be added there.

**If your recordings live somewhere else:** `data/` and `results/` here are excluded from git by the `.gitignore` in this folder (the downloaded originals contain your voice). For any other location you have to do that yourself.
