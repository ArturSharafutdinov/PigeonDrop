#!/usr/bin/env python3
"""Общий запуск для всех образов стенда: одинаково грузит модель, гоняет файлы и меряет.

Образ даёт свой `engine.py` с классом `Engine`:

    class Engine:
        name = "что это за модель"
        def __init__(self): ...                        # грузит модель
        def transcribe(self, path, lang) -> dict: ...  # {"text": str, "language": str | None, "load_s": float | None}

`lang` - подсказка языка ("ru", "en") или None, если модель должна определить язык сама.
`load_s` движок возвращает только если загружает модель на каждый вызов (whisper.cpp запускается как
отдельная программа); тогда `infer_s` считается без этой загрузки.

Запуск: `docker run ... образ [--repeat 3] [--warmup 1] [--lang-mode hint|auto] файл.wav ...`
В stdout - по одной строке JSON на файл и последняя строка со сводкой. Всё остальное идёт в stderr.
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
    """Процессорное время процесса и его потомков (user + system) в секундах."""
    t = os.times()
    return t.user + t.system + t.children_user + t.children_system


def peak_mb():
    """Пиковый объём занятой памяти (RSS) процесса и потомков, МБ (в Linux ru_maxrss в килобайтах)."""
    own = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    children = resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss
    return round(max(own, children) / 1024, 1)


def audio_seconds(path):
    with wave.open(path, "rb") as w:
        return w.getnframes() / w.getframerate()


def language_hint(path, mode):
    """Язык-подсказка по имени файла (ru01.wav, en03.wav, mix01.wav, long01.wav, sil01.wav).

    В продукте язык задаёт пользователь, поэтому по умолчанию (`hint`) модель его знает. Паузы записаны
    русскоязычным пользователем, для них тоже "ru". Режим `auto` проверяет, как модель справляется сама."""
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
    ap.add_argument("--repeat", type=int, default=3, help="сколько раз замерить каждый файл (берётся медиана)")
    ap.add_argument("--warmup", type=int, default=1, help="прогревочных запусков на первом файле, не учитываются")
    ap.add_argument("--lang-mode", choices=["hint", "auto"], default="hint")
    args = ap.parse_args()

    wall0, cpu0 = time.perf_counter(), cpu_seconds()
    engine = engine_class()
    load_s = round(time.perf_counter() - wall0, 2)
    load_cpu_s = round(cpu_seconds() - cpu0, 2)
    peak_after_load = peak_mb()
    print(f"модель загружена за {load_s} с, память {peak_after_load} МБ", file=sys.stderr)

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
    """Сколько ядер реально доступно контейнеру (с --cpuset-cpus это число лимита, а не всего хоста)."""
    return len(os.sched_getaffinity(0))
