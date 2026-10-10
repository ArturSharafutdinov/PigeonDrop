"""Нагрузочная проверка памяти: много распознаваний подряд в одном процессе, память после каждого вызова.

Запускается в образе вместо обычного запускателя (`--entrypoint python`), с движком из этого же образа:
    python /stress/stress.py --passes 40 /data/wav /data/stress/max60.wav
    python /stress/stress.py --passes 20 --workers 2 --sync /data/wav /data/stress/max60.wav    # две записи одновременно

Что печатает (по строке JSON на вызов, сразу на stdout, поэтому при убийстве контейнера по памяти остаётся всё,
что успело записаться): текущий объём процесса (rss_mb, то есть сейчас, а не наибольший), наибольший за всё время
(hwm_mb) и счётчики cgroup, по которым Docker решает убить контейнер: memory.current (anon_mb - память программы,
которую вытеснить нельзя; file_mb - кэш файлов, его система вытесняет сама), memory.peak и число убийств по памяти.
Обычный запускатель (`runner.py`) такого не делает: он даёт один итоговый пик за весь прогон.

Одновременные записи (`--workers N`): N потоков одного процесса делят одну загруженную модель, как делал бы воркер с
пулом потоков. Каждый вызов записывает время начала и конца (t0, t1, секунды с эпохи), по ним сводка проверяет, что
вызовы действительно перекрывались, и контрольную сумму ответа (sha): ответ на одну и ту же запись не должен зависеть
от того, что рядом шёл другой вызов. Две копии модели (два процесса) запускает `stress-memory.sh` с CONCURRENCY=processes,
каждому процессу даётся своя метка `--label`.
    --sync          перед каждым вызовом потоки ждут друг друга и стартуют вместе
    --same-order    все потоки идут по записям в одном порядке: с --sync это одна и та же запись одновременно"""
import argparse
import hashlib
import json
import os
import random
import sys
import threading
import time

sys.path.insert(0, "/app")

from engine import Engine  # noqa: E402  (движок того образа, в котором запущен скрипт)
from runner import audio_seconds, visible_cpus  # noqa: E402

CGROUP = "/sys/fs/cgroup"
MIB = 1024 * 1024
BARRIER_TIMEOUT_S = 600


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


def collect(paths, max_seconds=None):
    """Каталоги раскрываются в их .wav (по имени), файлы берутся как есть; max_seconds отбрасывает более длинные записи."""
    files = []
    for path in paths:
        if os.path.isdir(path):
            files += sorted(os.path.join(path, n) for n in os.listdir(path) if n.lower().endswith(".wav"))
        else:
            files.append(path)
    if max_seconds is not None:
        files = [f for f in files if audio_seconds(f) <= max_seconds]
    return files


def order_for(files, number, worker, seed, same_order):
    """Порядок записей в проходе: первый проход по списку, остальные вперемешку (своё зерно у каждого потока)."""
    order = list(files)
    if number > 1:
        random.Random(seed + number + (0 if same_order else 1000 * worker)).shuffle(order)
    return order


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("paths", nargs="+", help="файлы .wav и каталоги с ними")
    ap.add_argument("--passes", type=int, default=40, help="сколько раз пройти весь набор (каждым потоком)")
    ap.add_argument("--seed", type=int, default=1, help="зерно перемешивания: первый проход по порядку, остальные вперемешку")
    ap.add_argument("--workers", type=int, default=1, help="сколько потоков распознают одновременно")
    ap.add_argument("--sync", action="store_true", help="потоки стартуют каждый вызов вместе")
    ap.add_argument("--same-order", action="store_true", help="все потоки идут по записям в одном порядке")
    ap.add_argument("--max-seconds", type=float, help="не брать записи длиннее этого (проверка более короткого предела)")
    ap.add_argument("--label", default="", help="метка процесса, если их запущено несколько")
    args = ap.parse_args()

    files = collect(args.paths, args.max_seconds)
    if not files:
        sys.exit("нет .wav")
    memory_max = cgroup_number("memory.max")
    print_lock = threading.Lock()

    def emit(record):
        record = {"proc": args.label, **record} if args.label else record
        with print_lock:
            print(json.dumps(record, ensure_ascii=False), flush=True)

    t0 = time.perf_counter()
    engine = Engine()
    emit({"event": "loaded", "load_s": round(time.perf_counter() - t0, 2), "files": len(files),
          "passes": args.passes, "workers": args.workers, "sync": args.sync, "same_order": args.same_order,
          "max_seconds": args.max_seconds, "cpus": visible_cpus(),
          "limit_mb": None if memory_max is None else round(memory_max / MIB), **snapshot()})

    barrier = threading.Barrier(args.workers) if args.sync and args.workers > 1 else None
    counter = {"n": 0}
    counter_lock = threading.Lock()
    failed = []

    def work(worker):
        try:
            for number in range(1, args.passes + 1):
                for path in order_for(files, number, worker, args.seed, args.same_order):
                    if barrier:
                        barrier.wait(BARRIER_TIMEOUT_S)
                    started, clock = time.time(), time.perf_counter()
                    text = engine.transcribe(path, None)["text"]
                    wall = time.perf_counter() - clock
                    with counter_lock:
                        counter["n"] += 1
                        call = counter["n"]
                    emit({"event": "call", "call": call, "worker": worker, "pass": number,
                          "file": os.path.basename(path), "audio_s": round(audio_seconds(path), 2),
                          "wall_s": round(wall, 3), "t0": round(started, 3), "t1": round(started + wall, 3),
                          "words": len(text.split()), "sha": hashlib.sha1(text.encode()).hexdigest()[:8],
                          **snapshot()})
        except BaseException as error:  # noqa: BLE001  (любая ошибка потока должна быть видна, а не потеряна)
            failed.append(f"поток {worker}: {error!r}")
            if barrier:
                barrier.abort()

    threads = [threading.Thread(target=work, args=(w,), daemon=True) for w in range(args.workers)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    if failed:
        emit({"event": "error", "errors": failed, **snapshot()})
        sys.exit("; ".join(failed))
    emit({"event": "done", "calls": counter["n"], **snapshot()})


if __name__ == "__main__":
    main()
