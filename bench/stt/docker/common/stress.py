"""Нагрузочная проверка памяти: много распознаваний подряд в одном процессе, память после каждого вызова.

Запускается в образе вместо обычного запускателя (`--entrypoint python`), с движком из этого же образа:
    python /stress/stress.py --passes 40 /data/wav /data/stress/max60.wav

Что печатает (по строке JSON на вызов, сразу на stdout, поэтому при убийстве контейнера по памяти остаётся всё,
что успело записаться): текущий объём процесса (rss_mb, то есть сейчас, а не наибольший), наибольший за всё время
(hwm_mb) и счётчики cgroup, по которым Docker решает убить контейнер: memory.current (anon_mb - память программы,
которую вытеснить нельзя; file_mb - кэш файлов, его система вытесняет сама), memory.peak и число убийств по памяти.
Обычный запускатель (`runner.py`) такого не делает: он даёт один итоговый пик за весь прогон."""
import argparse
import json
import os
import random
import sys
import time

sys.path.insert(0, "/app")

from engine import Engine  # noqa: E402  (движок того образа, в котором запущен скрипт)
from runner import audio_seconds, visible_cpus  # noqa: E402

CGROUP = "/sys/fs/cgroup"
MIB = 1024 * 1024


def read_text(path):
    try:
        with open(path) as f:
            return f.read()
    except OSError:
        return None


def process_mb(key):
    """VmRSS (сейчас) или VmHWM (наибольший) из /proc/self/status, в МиБ."""
    for line in (read_text("/proc/self/status") or "").splitlines():
        if line.startswith(key + ":"):
            return round(int(line.split()[1]) / 1024, 1)
    return None


def cgroup_number(name):
    text = read_text(f"{CGROUP}/{name}")
    return int(text) if text and text.strip().isdigit() else None


def cgroup_mb(name):
    value = cgroup_number(name)
    return None if value is None else round(value / MIB, 1)


def cgroup_stat(name):
    out = {}
    for line in (read_text(f"{CGROUP}/{name}") or "").splitlines():
        parts = line.split()
        if len(parts) == 2 and parts[1].isdigit():
            out[parts[0]] = int(parts[1])
    return out


def snapshot():
    stat = cgroup_stat("memory.stat")
    return {
        "rss_mb": process_mb("VmRSS"),
        "hwm_mb": process_mb("VmHWM"),
        "cg_mb": cgroup_mb("memory.current"),
        "cg_peak_mb": cgroup_mb("memory.peak"),
        "anon_mb": round(stat["anon"] / MIB, 1) if "anon" in stat else None,
        "file_mb": round(stat["file"] / MIB, 1) if "file" in stat else None,
        "oom_kill": cgroup_stat("memory.events").get("oom_kill"),
    }


def collect(paths):
    """Каталоги раскрываются в их .wav (по имени), файлы берутся как есть."""
    files = []
    for path in paths:
        if os.path.isdir(path):
            files += sorted(os.path.join(path, n) for n in os.listdir(path) if n.lower().endswith(".wav"))
        else:
            files.append(path)
    return files


def emit(record):
    print(json.dumps(record, ensure_ascii=False), flush=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("paths", nargs="+", help="файлы .wav и каталоги с ними")
    ap.add_argument("--passes", type=int, default=40, help="сколько раз пройти весь набор")
    ap.add_argument("--seed", type=int, default=1, help="зерно перемешивания: первый проход по порядку, остальные вперемешку")
    args = ap.parse_args()

    files = collect(args.paths)
    if not files:
        sys.exit("нет .wav")
    memory_max = cgroup_number("memory.max")

    t0 = time.perf_counter()
    engine = Engine()
    emit({"event": "loaded", "load_s": round(time.perf_counter() - t0, 2), "files": len(files),
          "passes": args.passes, "cpus": visible_cpus(),
          "limit_mb": None if memory_max is None else round(memory_max / MIB), **snapshot()})

    call = 0
    for number in range(1, args.passes + 1):
        order = list(files)
        if number > 1:
            random.Random(args.seed + number).shuffle(order)
        for path in order:
            call += 1
            started = time.perf_counter()
            text = engine.transcribe(path, None)["text"]
            emit({"event": "call", "call": call, "pass": number, "file": os.path.basename(path),
                  "audio_s": round(audio_seconds(path), 2), "wall_s": round(time.perf_counter() - started, 3),
                  "words": len(text.split()), **snapshot()})
    emit({"event": "done", "calls": call, **snapshot()})


if __name__ == "__main__":
    main()
