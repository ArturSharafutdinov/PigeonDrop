#!/usr/bin/env python3
"""Сводка нагрузочной проверки памяти (`stress-memory.sh`): по каждому прогону растёт ли память и хватает ли лимита.

    python3 memory_report.py                  # results/memory/*.jsonl
    python3 memory_report.py --details        # плюс кривая роста по проходам
    python3 memory_report.py --results ДРУГАЯ_ПАПКА

Читает строки JSON из `docker/common/stress.py` и файлы `.exit` (убит ли контейнер по памяти, коды выхода процессов).
Слова в таблице:
  anon - память самой программы: её вытеснить нельзя, по ней и убивают;
  cg   - всё, что считает контейнеру cgroup, то есть anon и кэш файлов модели (кэш система вытесняет сама, поэтому
         запас по cg - консервативная оценка, а запас по anon - точная).
Вердикт «потолок» ставится, если пик процесса за вторую половину проходов вырос не больше чем на PLATEAU_MB.
Для прогонов с одновременными записями (потоки или несколько процессов) печатается вторая таблица: правда ли вызовы
шли одновременно (по времени начала и конца каждого), были ли одновременно две длинные записи и совпадают ли ответы
на одну и ту же запись, когда рядом шёл другой вызов."""
import argparse
import glob
import json
import os
import sys

PLATEAU_MB = 25  # рост пика за вторую половину серии, который ещё считаем шумом (около 1% от 2 ГБ)
LONG_S = 40  # запись дольше этого считается длинной для подсчёта одновременных пар


def read_events(path):
    """Все строки JSON файла по порядку; оборванная последняя строка (контейнер убит) пропускается."""
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
    """Возвращает (первое событие загрузки, список вызовов, событие done или None)."""
    rows = read_events(path)
    loaded = next((r for r in rows if r.get("event") == "loaded"), None)
    done = next((r for r in rows if r.get("event") == "done"), None)
    return loaded, [r for r in rows if r.get("event") == "call"], done


def load_exit(path):
    """oom_killed, exit_code и коды выхода процессов (exit_codes=0,137) из файла рядом с прогоном (его пишет stress-memory.sh)."""
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
    """По каждому проходу: наибольшие rss/anon/cg внутри него (по всем потокам и процессам)."""
    passes = {}
    for c in calls:
        p = passes.setdefault(c["pass"], {"rss": 0, "anon": 0, "cg": 0, "hwm": 0})
        p["rss"] = max(p["rss"], c.get("rss_mb") or 0)
        p["anon"] = max(p["anon"], c.get("anon_mb") or 0)
        p["cg"] = max(p["cg"], c.get("cg_mb") or 0)
        p["hwm"] = max(p["hwm"], c.get("hwm_mb") or 0)
    return dict(sorted(passes.items()))


def raises(loaded, calls, threshold):
    """Вызовы, после которых наибольший объём процесса вырос не меньше чем на threshold МБ.

    [(номер вызова в файле, проход, запись, +МБ)]; номер считается по порядку строк, потому что у нескольких
    процессов собственные счётчики вызовов совпадают."""
    out = []
    peak = (loaded or {}).get("hwm_mb") or 0
    for seq, c in enumerate(calls, 1):
        hwm = c.get("hwm_mb") or 0
        if hwm - peak >= threshold:
            out.append((seq, c["pass"], c["file"], round(hwm - peak, 1)))
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


def overlap(calls):
    """(доля времени с двумя и более одновременными вызовами от времени с хотя бы одним, наибольшее число одновременных).

    Считается по времени начала t0 и конца t1 каждого вызова; без них (старые файлы) (None, 1)."""
    events = []
    for c in calls:
        if c.get("t0") is not None and c.get("t1") is not None:
            events += [(c["t0"], 1), (c["t1"], -1)]
    if not events:
        return None, 1
    events.sort()  # при равных моментах сначала конец (-1), потом начало (+1)
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
    """Сколько пар вызовов длиннее long_s секунд шли в одно и то же время (самый тяжёлый случай для памяти)."""
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
    """Записи, на которые дали разные ответы в разных вызовах (контрольная сумма sha): при параллельной работе это порча."""
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
    header = ["прогон", "вызовов", "убит по памяти", "после загрузки, МБ (rss / cg)", "пик rss, МБ", "пик anon, МБ",
              "пик cg, МБ", "запас по anon, МБ", "запас по cg, МБ", "последний подъём пика на 5+ МБ (вызов)",
              "рост пика во 2-й половине, МБ", "вердикт"]
    print("| " + " | ".join(header) + " |")
    print("|" + "---|" * len(header))
    for r in rows:
        done = f"{r['calls']} из {r['expected']}" if r["expected"] else str(r["calls"])
        killed = "ДА" if r["died"] else ("нет" if r["died"] is False else "?")
        load = f"{r['after_load_rss']} / {r['after_load_cg']}"
        growth = "-" if r["growth_mb"] is None else f"{r['growth_mb']:+.1f}"
        last = "-" if r["last_raise_call"] is None else r["last_raise_call"]
        cells = [r["name"], done, killed, load, r["peak_rss"], r["peak_anon"], r["peak_cg"],
                 r["headroom_anon"], r["headroom_cg"], last, growth, r["verdict"]]
        print("| " + " | ".join(str(c) for c in cells) + " |")


def print_concurrency(rows):
    """Вторая таблица, только если в каком-то прогоне работало больше одного вызова одновременно."""
    if not any(r["workers"] > 1 for r in rows):
        return
    print("\nОдновременные записи:\n")
    header = ["прогон", "потоков и процессов", "наибольшее число вызовов разом", "доля времени с 2+ вызовами",
              f"пар вызовов длиннее {LONG_S} с разом", "записей с разными ответами"]
    print("| " + " | ".join(header) + " |")
    print("|" + "---|" * len(header))
    for r in rows:
        share = "-" if r["overlap_share"] is None else f"{r['overlap_share']:.0%}"
        diff = "нет" if not r["inconsistent"] else f"{len(r['inconsistent'])}: " + ", ".join(r["inconsistent"][:5])
        print("| " + " | ".join(str(c) for c in [r["name"], r["workers"], r["max_active"], share, r["long_pairs"], diff]) + " |")


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
    print_concurrency(rows)
    if args.details:
        print_curves(rows)


if __name__ == "__main__":
    main()
