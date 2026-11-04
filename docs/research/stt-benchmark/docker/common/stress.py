"""Memory stress test: many recognitions in a row in one process, memory after every call.

Runs in the image instead of the usual runner (`--entrypoint python`), with the engine from the same image:
    python /stress/stress.py --passes 40 /data/wav /data/stress/max60.wav
    python /stress/stress.py --passes 20 --workers 2 --sync /data/wav /data/stress/max60.wav    # two recordings at the same time

"""What it prints (one JSON line per call, straight to stdout, so that when the container is killed for memory
everything written so far survives): the process's current size (rss_mb, i.e. now, not the maximum), the maximum so
far (hwm_mb) and the cgroup counters Docker uses to decide to kill the container: memory.current (anon_mb is the
program's memory, which cannot be evicted; file_mb is the file cache, which the system evicts itself), memory.peak
and the number of memory kills. The usual runner (`runner.py`) does not do this: it gives one final peak for the whole run.

Simultaneous recordings (`--workers N`): N threads of one process share one loaded model, as a worker with a thread
pool would. Every call records its start and end time (t0, t1, seconds since the epoch), from which the summary checks
that the calls really overlapped, and a checksum of the answer (sha): the answer to the same recording must not depend
on another call running next to it. Two copies of the model (two processes) are started by `stress-memory.sh` with
CONCURRENCY=processes; each process gets its own `--label`.
    --sync          before every call the threads wait for each other and start together
    --same-order    all threads go through the recordings in the same order: with --sync that is the same recording at once"""
import argparse
import hashlib
import json
import os
import random
import sys
import threading
import time

sys.path.insert(0, "/app")

from engine import Engine  # noqa: E402  (the engine of the image the script runs in)
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
    """VmRSS (now) or VmHWM (the maximum) from /proc/self/status, in MiB."""
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
    """Directories expand into their .wav files (by name), files are taken as they are; max_seconds drops longer recordings."""
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
    """The order of recordings in a pass: the first pass follows the list, the rest are shuffled (each thread has its own seed)."""
    order = list(files)
    if number > 1:
        random.Random(seed + number + (0 if same_order else 1000 * worker)).shuffle(order)
    return order


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("paths", nargs="+", help="`.wav` files and directories with them")
    ap.add_argument("--passes", type=int, default=40, help="how many times to go through the whole set (by each thread)")
    ap.add_argument("--seed", type=int, default=1, help="shuffle seed: the first pass is in order, the rest are shuffled")
    ap.add_argument("--workers", type=int, default=1, help="how many threads recognise at the same time")
    ap.add_argument("--sync", action="store_true", help="threads start every call together")
    ap.add_argument("--same-order", action="store_true", help="all threads go through the recordings in the same order")
    ap.add_argument("--max-seconds", type=float, help="skip recordings longer than this (to test a shorter limit)")
    ap.add_argument("--label", default="", help="process label, if several are started")
    args = ap.parse_args()

    files = collect(args.paths, args.max_seconds)
    if not files:
        sys.exit("no .wav files")
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
        except BaseException as error:  # noqa: BLE001  (any thread error must be visible, not lost)
            failed.append(f"thread {worker}: {error!r}")
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
