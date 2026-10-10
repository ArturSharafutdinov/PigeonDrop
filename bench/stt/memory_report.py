#!/usr/bin/env python3
"""Сводка нагрузочной проверки памяти (`stress-memory.sh`): по каждому прогону растёт ли память и хватает ли лимита.

    python3 memory_report.py                  # results/memory/*.jsonl
    python3 memory_report.py --details        # плюс кривая роста по проходам
    python3 memory_report.py --results ДРУГАЯ_ПАПКА

Читает строки JSON из `docker/common/stress.py` и файлы `.exit` (убит ли контейнер по памяти). Слова в таблице:
  anon - память самой программы: её вытеснить нельзя, по ней и убивают;
  cg   - всё, что считает контейнеру cgroup, то есть anon и кэш файлов модели (кэш система вытесняет сама, поэтому
         запас по cg - консервативная оценка, а запас по anon - точная).
Вердикт «потолок» ставится, если пик процесса за вторую половину проходов вырос не больше чем на PLATEAU_MB."""
import argparse
import glob
import json
import os
import sys

PLATEAU_MB = 25  # рост пика за вторую половину серии, который ещё считаем шумом (около 1% от 2 ГБ)


def load_run(path):
    """Возвращает (событие загрузки, список вызовов, событие done или None)."""
    loaded, calls, done = None, [], None
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line.startswith("{"):
                continue
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue  # последняя строка могла оборваться вместе с убитым контейнером
            event = row.get("event")
            if event == "loaded":
                loaded = row
            elif event == "call":
                calls.append(row)
            elif event == "done":
                done = row
    return loaded, calls, done


def load_exit(path):
    """oom_killed и exit_code из файла рядом с прогоном (его пишет stress-memory.sh)."""
    out = {"oom_killed": None, "exit_code": None}
    if os.path.exists(path):
        for part in open(path, encoding="utf-8").read().split():
            key, _, value = part.partition("=")
            if key == "oom_killed":
                out[key] = value == "true"
            elif key == "exit_code" and value.lstrip("-").isdigit():
                out[key] = int(value)
    return out


def per_pass(calls):
    """По каждому проходу: наибольшие rss/anon/cg внутри него и значения после его последнего вызова."""
    passes = {}
    for c in calls:
        p = passes.setdefault(c["pass"], {"rss": 0, "anon": 0, "cg": 0, "hwm": 0})
        p["rss"] = max(p["rss"], c.get("rss_mb") or 0)
        p["anon"] = max(p["anon"], c.get("anon_mb") or 0)
        p["cg"] = max(p["cg"], c.get("cg_mb") or 0)
        p["hwm"] = max(p["hwm"], c.get("hwm_mb") or 0)
    return dict(sorted(passes.items()))


def raises(loaded, calls, threshold):
    """Вызовы, после которых наибольший объём процесса вырос не меньше чем на threshold МБ: [(вызов, проход, запись, +МБ)]."""
    out = []
    peak = (loaded or {}).get("hwm_mb") or 0
    for c in calls:
        hwm = c.get("hwm_mb") or 0
        if hwm - peak >= threshold:
            out.append((c["call"], c["pass"], c["file"], round(hwm - peak, 1)))
        peak = max(peak, hwm)
    return out


def verdict(passes, plateau_mb=PLATEAU_MB):
    """('потолок' | 'растёт' | 'мало данных', рост пика за вторую половину в МБ)."""
    numbers = sorted(passes)
    if len(numbers) < 4:
        return "мало данных", None
    middle = numbers[len(numbers) // 2 - 1]
    growth = round(passes[numbers[-1]]["hwm"] - passes[middle]["hwm"], 1)
    return ("потолок" if growth <= plateau_mb else "растёт"), growth


def summarize(path, exit_path=None):
    loaded, calls, done = load_run(path)
    status = load_exit(exit_path or os.path.splitext(path)[0] + ".exit")
    expected = (loaded["files"] * loaded["passes"]) if loaded else None
    passes = per_pass(calls)
    kind, growth = verdict(passes)
    last = calls[-1] if calls else {}
    limit = loaded.get("limit_mb") if loaded else None
    peak_cg = max((c.get("cg_peak_mb") or 0 for c in calls), default=0)
    peak_anon = max((c["anon_mb"] for c in calls if c.get("anon_mb") is not None), default=0)
    last_raise = raises(loaded, calls, 5)
    return {
        "name": os.path.splitext(os.path.basename(path))[0], "loaded": loaded, "calls": len(calls), "expected": expected,
        "finished": done is not None, "oom_killed": status["oom_killed"], "exit_code": status["exit_code"],
        "limit_mb": limit, "passes": passes, "verdict": kind, "growth_mb": growth,
        "after_load_rss": loaded.get("rss_mb") if loaded else None,
        "after_load_cg": loaded.get("cg_mb") if loaded else None,
        "peak_rss": max((c.get("hwm_mb") or 0 for c in calls), default=0),
        "peak_anon": peak_anon, "peak_cg": peak_cg,
        "headroom_anon": None if limit is None else round(limit - peak_anon),
        "headroom_cg": None if limit is None else round(limit - peak_cg),
        "oom_events": last.get("oom_kill"), "big_raises": raises(loaded, calls, 20),
        "last_raise_call": last_raise[-1][0] if last_raise else None,
    }


def print_table(rows):
    header = ["прогон", "вызовов", "убит по памяти", "после загрузки, МБ (rss / cg)", "пик rss, МБ", "пик anon, МБ",
              "пик cg, МБ", "запас по anon, МБ", "запас по cg, МБ", "последний подъём пика на 5+ МБ (вызов)", "рост пика во 2-й половине, МБ", "вердикт"]
    print("| " + " | ".join(header) + " |")
    print("|" + "---|" * len(header))
    for r in rows:
        done = f"{r['calls']} из {r['expected']}" if r["expected"] else str(r["calls"])
        killed = "ДА" if r["oom_killed"] else ("нет" if r["oom_killed"] is False else "?")
        load = f"{r['after_load_rss']} / {r['after_load_cg']}"
        growth = "-" if r["growth_mb"] is None else f"{r['growth_mb']:+.1f}"
        last = "-" if r["last_raise_call"] is None else r["last_raise_call"]
        cells = [r["name"], done, killed, load, r["peak_rss"], r["peak_anon"], r["peak_cg"],
                 r["headroom_anon"], r["headroom_cg"], last, growth, r["verdict"]]
        print("| " + " | ".join(str(c) for c in cells) + " |")


def print_curves(rows):
    for r in rows:
        print(f"\n### {r['name']}: пик rss внутри прохода, МБ")
        passes = r["passes"]
        shown = sorted({1, 2, 3, 5, 10, 20, 30, max(passes, default=1)} & set(passes))
        print("| проход | rss | anon | cg |")
        print("|---|---|---|---|")
        for n in shown:
            p = passes[n]
            print(f"| {n} | {p['rss']} | {p['anon']} | {p['cg']} |")
        print("\nВызовы, поднявшие пик на 20+ МБ: " + (", ".join(
            f"№{c} (проход {p}, {f}, +{d} МБ)" for c, p, f, d in r["big_raises"]) or "нет"))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--results", default="results/memory")
    ap.add_argument("--details", action="store_true")
    args = ap.parse_args()
    paths = sorted(glob.glob(os.path.join(args.results, "*.jsonl")))
    if not paths:
        sys.exit(f"нет результатов в {args.results} (./stress-memory.sh)")
    rows = [summarize(p) for p in paths]
    print_table(rows)
    if args.details:
        print_curves(rows)


if __name__ == "__main__":
    main()
