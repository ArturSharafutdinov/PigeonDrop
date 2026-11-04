#!/usr/bin/env python3
"""Summary of the memory stress test (`stress-memory.sh`): for each run, whether memory grows and whether the limit is enough.

    python3 memory_report.py                  # results/memory/*.jsonl
    python3 memory_report.py --details        # plus the growth curve per pass
    python3 memory_report.py --results OTHER_FOLDER

Reads the JSON lines from `docker/common/stress.py` and the `.exit` files (whether the container was killed for memory,
the processes' exit codes). Words in the table:
  anon - the program's own memory: it cannot be evicted, and this is what gets the container killed;
  cg   - everything the cgroup counts against the container, that is anon plus the model's file cache (the system
         evicts the cache itself, so the headroom by cg is a conservative estimate and by anon an exact one).
The verdict "plateau" is given if the process's peak grew by no more than PLATEAU_MB over the second half of the passes.
For runs with simultaneous recordings (threads or several processes) a second table is printed: whether the calls
really overlapped (by the start and end time of each), whether two long recordings ran at once, and whether the
answers to the same recording agree when another call ran next to it."""
import argparse
import glob
import json
import os
import sys

PLATEAU_MB = 25  # peak growth over the second half of the series that is still counted as noise (about 1% of 2 GB)
LONG_S = 40  # a recording longer than this counts as long when counting simultaneous pairs


def read_events(path):
    """All JSON lines of the file in order; a truncated last line (the container was killed) is skipped."""
    rows = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line.startswith("{"):
                continue
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    return rows


def load_run(path):
    """Returns (the first load event, the list of calls, the done event or None)."""
    rows = read_events(path)
    loaded = next((r for r in rows if r.get("event") == "loaded"), None)
    done = next((r for r in rows if r.get("event") == "done"), None)
    return loaded, [r for r in rows if r.get("event") == "call"], done


def load_exit(path):
    """oom_killed, exit_code and the processes' exit codes (exit_codes=0,137) from the file next to the run (written by stress-memory.sh)."""
    out = {"oom_killed": None, "exit_code": None, "exit_codes": None}
    if os.path.exists(path):
        with open(path, encoding="utf-8") as f:
            parts = f.read().split()
        for part in parts:
            key, _, value = part.partition("=")
            if key == "oom_killed":
                out[key] = value == "true"
            elif key == "exit_code" and value.lstrip("-").isdigit():
                out[key] = int(value)
            elif key == "exit_codes":
                out[key] = [int(v) for v in value.split(",") if v.lstrip("-").isdigit()]
    return out


def per_pass(calls):
    """For each pass: the largest rss/anon/cg inside it (over all threads and processes)."""
    passes = {}
    for c in calls:
        p = passes.setdefault(c["pass"], {"rss": 0, "anon": 0, "cg": 0, "hwm": 0})
        p["rss"] = max(p["rss"], c.get("rss_mb") or 0)
        p["anon"] = max(p["anon"], c.get("anon_mb") or 0)
        p["cg"] = max(p["cg"], c.get("cg_mb") or 0)
        p["hwm"] = max(p["hwm"], c.get("hwm_mb") or 0)
    return dict(sorted(passes.items()))


def raises(loaded, calls, threshold):
    """Calls after which the process's largest size grew by at least threshold MB.

    [(call number in the file, pass, recording, +MB)]; the number counts lines in order, because the several
    processes have their own call counters, which coincide."""
    out = []
    peak = (loaded or {}).get("hwm_mb") or 0
    for seq, c in enumerate(calls, 1):
        hwm = c.get("hwm_mb") or 0
        if hwm - peak >= threshold:
            out.append((seq, c["pass"], c["file"], round(hwm - peak, 1)))
        peak = max(peak, hwm)
    return out


def verdict(passes, plateau_mb=PLATEAU_MB):
    """('plateau' | 'growing' | 'too little data', peak growth over the second half in MB)."""
    numbers = sorted(passes)
    if len(numbers) < 4:
        return "too little data", None
    middle = numbers[len(numbers) // 2 - 1]
    growth = round(passes[numbers[-1]]["hwm"] - passes[middle]["hwm"], 1)
    return ("plateau" if growth <= plateau_mb else "growing"), growth


def overlap(calls):
    """(share of the time with two or more simultaneous calls out of the time with at least one, the largest number of simultaneous calls).

    Computed from the start time t0 and end time t1 of each call; without them (old files) (None, 1)."""
    events = []
    for c in calls:
        if c.get("t0") is not None and c.get("t1") is not None:
            events += [(c["t0"], 1), (c["t1"], -1)]
    if not events:
        return None, 1
    events.sort()  # at equal moments the end (-1) comes first, then the start (+1)
    active = busy = both = most = 0
    previous = events[0][0]
    for moment, step in events:
        span = moment - previous
        if active >= 1:
            busy += span
        if active >= 2:
            both += span
        previous = moment
        active += step
        most = max(most, active)
    return (round(both / busy, 3) if busy else None), most


def long_pairs(calls, long_s=LONG_S):
    """How many pairs of calls longer than long_s seconds ran at the same time (the heaviest case for memory)."""
    longs = sorted((c for c in calls if (c.get("audio_s") or 0) >= long_s and c.get("t0") is not None),
                   key=lambda c: c["t0"])
    pairs = 0
    for i, a in enumerate(longs):
        for b in longs[i + 1:]:
            if b["t0"] >= a["t1"]:
                break
            pairs += 1
    return pairs


def inconsistent_files(calls):
    """Recordings that got different answers in different calls (checksum sha): in parallel operation this is corruption."""
    answers = {}
    for c in calls:
        if c.get("sha"):
            answers.setdefault(c["file"], set()).add(c["sha"])
    return sorted(name for name, shas in answers.items() if len(shas) > 1)


def summarize(path, exit_path=None):
    rows = read_events(path)
    loaded_all = [r for r in rows if r.get("event") == "loaded"]
    loaded = loaded_all[0] if loaded_all else None
    calls = [r for r in rows if r.get("event") == "call"]
    done = [r for r in rows if r.get("event") == "done"]
    errors = [r for r in rows if r.get("event") == "error"]
    status = load_exit(exit_path or os.path.splitext(path)[0] + ".exit")
    expected = sum(r["files"] * r["passes"] * (r.get("workers") or 1) for r in loaded_all) if loaded_all else None
    workers = sum((r.get("workers") or 1) for r in loaded_all) if loaded_all else 1
    passes = per_pass(calls)
    kind, growth = verdict(passes)
    limit = loaded.get("limit_mb") if loaded else None
    peak_cg = max((c.get("cg_peak_mb") or 0 for c in calls), default=0)
    peak_anon = max((c["anon_mb"] for c in calls if c.get("anon_mb") is not None), default=0)
    last_raise = raises(loaded, calls, 5)
    oom_events = max((c.get("oom_kill") or 0 for c in calls), default=0)
    bad_codes = [c for c in (status["exit_codes"] or []) if c != 0]
    died = bool(status["oom_killed"]) or oom_events > 0 or bool(bad_codes) or bool(errors)
    known = status["oom_killed"] is not None or status["exit_codes"] is not None
    share, most = overlap(calls)
    return {
        "name": os.path.splitext(os.path.basename(path))[0], "loaded": loaded, "calls": len(calls), "expected": expected,
        "finished": len(done) >= max(1, len(loaded_all)), "oom_killed": status["oom_killed"],
        "exit_code": status["exit_code"], "exit_codes": status["exit_codes"],
        "died": died if (died or known) else None, "oom_events": oom_events, "workers": workers,
        "limit_mb": limit, "passes": passes, "verdict": kind, "growth_mb": growth,
        "after_load_rss": loaded.get("rss_mb") if loaded else None,
        "after_load_cg": loaded.get("cg_mb") if loaded else None,
        "peak_rss": max((c.get("hwm_mb") or 0 for c in calls), default=0),
        "peak_anon": peak_anon, "peak_cg": peak_cg,
        "headroom_anon": None if limit is None else round(limit - peak_anon),
        "headroom_cg": None if limit is None else round(limit - peak_cg),
        "big_raises": raises(loaded, calls, 20),
        "last_raise_call": last_raise[-1][0] if last_raise else None,
        "overlap_share": share, "max_active": most, "long_pairs": long_pairs(calls),
        "inconsistent": inconsistent_files(calls),
    }


def print_table(rows):
    header = ["run", "calls", "killed for memory", "after load, MB (rss / cg)", "peak rss, MB", "peak anon, MB",
              "peak cg, MB", "headroom by anon, MB", "headroom by cg, MB", "last peak rise of 5+ MB (call)",
              "peak growth in the 2nd half, MB", "verdict"]
    print("| " + " | ".join(header) + " |")
    print("|" + "---|" * len(header))
    for r in rows:
        done = f"{r['calls']} of {r['expected']}" if r["expected"] else str(r["calls"])
        killed = "YES" if r["died"] else ("no" if r["died"] is False else "?")
        load = f"{r['after_load_rss']} / {r['after_load_cg']}"
        growth = "-" if r["growth_mb"] is None else f"{r['growth_mb']:+.1f}"
        last = "-" if r["last_raise_call"] is None else r["last_raise_call"]
        cells = [r["name"], done, killed, load, r["peak_rss"], r["peak_anon"], r["peak_cg"],
                 r["headroom_anon"], r["headroom_cg"], last, growth, r["verdict"]]
        print("| " + " | ".join(str(c) for c in cells) + " |")


def print_concurrency(rows):
    """The second table, only if some run had more than one call at the same time."""
    if not any(r["workers"] > 1 for r in rows):
        return
    print("\nSimultaneous recordings:\n")
    header = ["run", "threads and processes", "most calls at once", "share of time with 2+ calls",
              f"pairs of calls longer than {LONG_S} s at once", "recordings with differing answers"]
    print("| " + " | ".join(header) + " |")
    print("|" + "---|" * len(header))
    for r in rows:
        share = "-" if r["overlap_share"] is None else f"{r['overlap_share']:.0%}"
        diff = "none" if not r["inconsistent"] else f"{len(r['inconsistent'])}: " + ", ".join(r["inconsistent"][:5])
        print("| " + " | ".join(str(c) for c in [r["name"], r["workers"], r["max_active"], share, r["long_pairs"], diff]) + " |")


def print_curves(rows):
    for r in rows:
        print(f"\n### {r['name']}: peak rss inside a pass, MB")
        passes = r["passes"]
        shown = sorted({1, 2, 3, 5, 10, 20, 30, max(passes, default=1)} & set(passes))
        print("| pass | rss | anon | cg |")
        print("|---|---|---|---|")
        for n in shown:
            p = passes[n]
            print(f"| {n} | {p['rss']} | {p['anon']} | {p['cg']} |")
        print("\nCalls that raised the peak by 20+ MB: " + (", ".join(
            f"#{c} (pass {p}, {f}, +{d} MB)" for c, p, f, d in r["big_raises"]) or "none"))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--results", default="results/memory")
    ap.add_argument("--details", action="store_true")
    args = ap.parse_args()
    paths = sorted(glob.glob(os.path.join(args.results, "*.jsonl")))
    if not paths:
        sys.exit(f"no results in {args.results} (./stress-memory.sh)")
    rows = [summarize(p) for p in paths]
    print_table(rows)
    print_concurrency(rows)
    if args.details:
        print_curves(rows)


if __name__ == "__main__":
    main()
