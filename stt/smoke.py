#!/usr/bin/env python3
"""Sends WAV files to a running service and compares what comes back with the benchmark's texts.

    python3 smoke.py http://127.0.0.1:8000 TOKEN DIR_WITH_WAV [--repeat 3] [--compare ../docs/research/stt-benchmark/results/parakeet__strict__hint.jsonl]

The benchmark (docs/research/stt-benchmark) fed the same model the same recordings, from files; this feeds them from memory through the
service. The texts must be identical, otherwise something between the WAV and the model (the conversion to samples)
differs from the benchmark. Exits 0 only when every answer was 200 and nothing differed."""
import argparse
import glob
import json
import os
import sys
import time
import urllib.error
import urllib.request


def post(base, token, path):
    with open(path, "rb") as f:
        data = f.read()
    request = urllib.request.Request(base + "/v1/transcribe", data=data, method="POST",
                                     headers={"Content-Type": "audio/wav", "Authorization": "Bearer " + token})
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            return response.status, json.loads(response.read()), time.perf_counter() - started
    except urllib.error.HTTPError as error:
        return error.code, json.loads(error.read() or b"{}"), time.perf_counter() - started


def reference_texts(path):
    texts = {}
    with open(path, encoding="utf-8") as f:
        for line in f:
            row = json.loads(line)
            if "file" in row:
                texts[os.path.splitext(row["file"])[0]] = row.get("text", "").strip()
    return texts


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("token")
    ap.add_argument("directory")
    ap.add_argument("--repeat", type=int, default=1)
    ap.add_argument("--compare", help="a benchmark result file (docs/research/stt-benchmark/results/*.jsonl) to compare the texts with")
    args = ap.parse_args()

    files = sorted(glob.glob(os.path.join(args.directory, "*.wav")))
    if not files:
        sys.exit(f"no .wav in {args.directory}")
    expected = reference_texts(args.compare) if args.compare else {}

    problems, calls, seconds_total, audio_total = 0, 0, 0.0, 0.0
    first_text = {}
    for round_ in range(args.repeat):
        for path in files:
            name = os.path.splitext(os.path.basename(path))[0]
            status, body, seconds = post(args.base, args.token, path)
            calls += 1
            if status != 200:
                problems += 1
                print(f"{name}: HTTP {status} {body}")
                continue
            seconds_total += seconds
            audio_total += body["audio_ms"] / 1000
            text = body["text"].strip()
            first_text.setdefault(name, text)
            if text != first_text[name]:
                problems += 1
                print(f"{name}: the answer changed between rounds:\n  {first_text[name]!r}\n  {text!r}")
            if name in expected and text != expected[name]:
                problems += 1
                print(f"{name}: differs from the benchmark:\n  benchmark {expected[name]!r}\n  service   {text!r}")
    print(f"{calls} requests, {len(files)} files, {args.repeat} round(s): {problems} problem(s); "
          f"{audio_total:.0f} s of audio in {seconds_total:.0f} s; compared with the benchmark: {len(expected)} text(s)")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
