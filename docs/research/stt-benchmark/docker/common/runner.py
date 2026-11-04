#!/usr/bin/env python3
"""Common runner for all benchmark images: loads the model the same way, runs the files and measures.

Each image provides its own `engine.py` with an `Engine` class:

    class Engine:
        name = "what this model is"
        def __init__(self): ...                        # loads the model
        def transcribe(self, path, lang) -> dict: ...  # {"text": str, "language": str | None, "load_s": float | None}

`lang` is a language hint ("ru", "en"), or None if the model has to detect the language itself.
The engine returns `load_s` only if it loads the model on every call (whisper.cpp runs as a separate
program); then `infer_s` is computed without that load.

Usage: `docker run ... image [--repeat 3] [--warmup 1] [--lang-mode hint|auto] file.wav ...`
Stdout gets one JSON line per file and a last line with the summary. Everything else goes to stderr.
"""
import argparse
import json
import os
import resource
import statistics
import sys
import time
import wave


def cpu_seconds():
    """CPU time of the process and its children (user + system) in seconds."""
    t = os.times()
    return t.user + t.system + t.children_user + t.children_system


def peak_mb():
    """Peak resident memory (RSS) of the process and its children, MB (on Linux ru_maxrss is in kilobytes)."""
    own = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    children = resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss
    return round(max(own, children) / 1024, 1)


def audio_seconds(path):
    with wave.open(path, "rb") as w:
        return w.getnframes() / w.getframerate()


def language_hint(path, mode):
    """The language hint from the file name (ru01.wav, en03.wav, mix01.wav, long01.wav, sil01.wav).

    In the product the user sets the language, so by default (`hint`) the model knows it. The pauses were
    recorded by a Russian speaker, so they get "ru" too. The `auto` mode checks how the model copes alone."""
    if mode == "auto":
        return None
    stem = os.path.basename(path).lower()
    if stem.startswith("en"):
        return "en"
    if stem.startswith(("ru", "mix", "long", "sil")):
        return "ru"
    return None


def run(engine_class):
    ap = argparse.ArgumentParser()
    ap.add_argument("files", nargs="+")
    ap.add_argument("--repeat", type=int, default=3, help="how many times to measure each file (the median is taken)")
    ap.add_argument("--warmup", type=int, default=1, help="warm-up runs on the first file, not counted")
    ap.add_argument("--lang-mode", choices=["hint", "auto"], default="hint")
    args = ap.parse_args()

    wall0, cpu0 = time.perf_counter(), cpu_seconds()
    engine = engine_class()
    load_s = round(time.perf_counter() - wall0, 2)
    load_cpu_s = round(cpu_seconds() - cpu0, 2)
    peak_after_load = peak_mb()
    print(f"model loaded in {load_s} s, memory {peak_after_load} MB", file=sys.stderr)

    for _ in range(args.warmup):
        engine.transcribe(args.files[0], language_hint(args.files[0], args.lang_mode))

    for path in args.files:
        lang = language_hint(path, args.lang_mode)
        walls, cpus, infers, texts, langs, per_call_loads = [], [], [], [], [], []
        for _ in range(args.repeat):
            w, c = time.perf_counter(), cpu_seconds()
            out = engine.transcribe(path, lang)
            walls.append(time.perf_counter() - w)
            cpus.append(cpu_seconds() - c)
            texts.append(out["text"].strip())
            langs.append(out.get("language"))
            call_load = out.get("load_s") or 0.0
            per_call_loads.append(call_load)
            infers.append(walls[-1] - call_load)
        print(json.dumps({
            "file": os.path.basename(path),
            "text": texts[0],
            "same_text_every_run": len(set(texts)) == 1,
            "language": langs[0],
            "lang_hint": lang,
            "audio_s": round(audio_seconds(path), 2),
            "wall_s": round(statistics.median(walls), 3),
            "infer_s": round(statistics.median(infers), 3),
            "cpu_s": round(statistics.median(cpus), 3),
            "call_load_s": round(statistics.median(per_call_loads), 3),
        }, ensure_ascii=False), flush=True)

    print(json.dumps({
        "summary": True,
        "engine": engine.name,
        "load_s": load_s,
        "load_cpu_s": load_cpu_s,
        "peak_mb_after_load": peak_after_load,
        "peak_mb_final": peak_mb(),
        "cpus_visible": len(os.sched_getaffinity(0)),
        "repeat": args.repeat,
        "lang_mode": args.lang_mode,
    }, ensure_ascii=False), flush=True)


def visible_cpus():
    """How many cores the container can really use (with --cpuset-cpus this is the limit, not the whole host)."""
    return len(os.sched_getaffinity(0))
