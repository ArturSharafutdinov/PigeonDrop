#!/usr/bin/env python3
"""Counts recognition errors from the results of `run.sh` and prints a summary table (Markdown).

    python3 score.py                 # results/*.jsonl against references.tsv
    python3 score.py --details       # plus a breakdown per recording: reference, answer, errors

Standard library only. The counting rules:

* The text is normalised before comparing: case, the letter "ё", punctuation, hyphens and apostrophes are ignored.
* WER is the share of wrong words (substitutions, insertions, deletions) out of the number of reference words, computed
  over the whole set at once (sum of errors / sum of words) rather than as a mean over recordings, so that a long
  recording weighs more than a short one.
* CER is the same by characters.
* A recording can have several acceptable reference variants (numbers as digits and as words, English names in Latin
  and in Cyrillic letters): the best one counts.
* Recordings without speech (tag `silence`) are left out of WER; instead the script counts how many words the model invented.
"""
import argparse
import json
import os
import re
import sys
import unicodedata

HERE = os.path.dirname(os.path.abspath(__file__))


def normalize(text):
    text = unicodedata.normalize("NFKC", text).lower().replace("ё", "е")
    text = text.replace("'", "").replace("’", "")  # i'm -> im: the same for the reference and the answer
    text = re.sub(r"[^\w]+|_", " ", text)
    return " ".join(text.split())


def tokens(text):
    return normalize(text).split()


def edit_distance(a, b):
    """Levenshtein distance between two sequences."""
    if not a:
        return len(b)
    if not b:
        return len(a)
    previous = list(range(len(b) + 1))
    for i, x in enumerate(a, 1):
        current = [i]
        for j, y in enumerate(b, 1):
            current.append(min(previous[j] + 1, current[j - 1] + 1, previous[j - 1] + (x != y)))
        previous = current
    return previous[-1]


def best_variant(variants, hypothesis):
    """Picks the reference variant that fits the answer best. Returns (word errors, words in the reference, the variant)."""
    hyp = tokens(hypothesis)
    best = None
    for variant in variants:
        ref = tokens(variant)
        errors = edit_distance(ref, hyp)
        if best is None or errors / max(len(ref), 1) < best[0] / max(best[1], 1):
            best = (errors, len(ref), variant)
    return best


def char_errors(variants, hypothesis):
    hyp = normalize(hypothesis)
    best = None
    for variant in variants:
        ref = normalize(variant)
        errors = edit_distance(ref, hyp)
        if best is None or errors / max(len(ref), 1) < best[0] / max(best[1], 1):
            best = (errors, len(ref))
    return best


def load_references(path):
    refs = {}
    with open(path, encoding="utf-8") as f:
        header = f.readline().rstrip("\n").split("\t")
        for line in f:
            row = dict(zip(header, line.rstrip("\n").split("\t")))
            if not row.get("id"):
                continue
            variants = [row.get("reference", "")] + [v for v in row.get("alt", "").split("|") if v]
            refs[row["id"]] = {"lang": row["lang"], "tags": set(row["tags"].split(",")), "variants": variants}
    return refs


def load_results(path):
    files, summary = {}, {}
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line.startswith("{"):
                continue
            row = json.loads(line)
            if row.get("summary"):
                summary = row
            else:
                files[os.path.splitext(row["file"])[0]] = row
    return files, summary


# Groups for the table: the name and the condition on the recording's tags.
def _scored(r):
    return "silence" not in r["tags"]


def _main(r):
    return _scored(r) and "numbers" not in r["tags"]


GROUPS = [
    ("ru", lambda i, r: _main(r) and i.startswith(("ru", "mix", "long"))),
    ("en", lambda i, r: _main(r) and i.startswith("en")),
    ("noisy", lambda i, r: _scored(r) and "noisy" in r["tags"]),
    ("numbers", lambda i, r: _scored(r) and "numbers" in r["tags"]),
    ("long", lambda i, r: "long" in r["tags"]),
    ("mixed", lambda i, r: "mixed" in r["tags"]),
    ("all", lambda i, r: _main(r)),
]


def score_run(refs, files):
    """Errors per group, words invented on silence, and the accuracy of language detection."""
    out = {"groups": {}, "cer": None, "silence_words": 0, "silence_files": 0, "language": None, "missing": []}
    totals = {name: [0, 0] for name, _ in GROUPS}
    cer_total = [0, 0]
    detected_ok = detected_n = 0
    for id_, ref in refs.items():
        row = files.get(id_)
        if row is None:
            out["missing"].append(id_)
            continue
        text = row.get("text", "")
        if "silence" in ref["tags"]:
            out["silence_files"] += 1
            out["silence_words"] += len(tokens(text))
            continue
        errors, words, _ = best_variant(ref["variants"], text)
        for name, belongs in GROUPS:
            if belongs(id_, ref):
                totals[name][0] += errors
                totals[name][1] += words
        if _main(ref):
            c_err, c_len = char_errors(ref["variants"], text)
            cer_total[0] += c_err
            cer_total[1] += c_len
        if row.get("language") and row.get("lang_hint") is None:
            detected_n += 1
            expected = "en" if id_.startswith("en") else "ru"
            detected_ok += row["language"] == expected
    out["groups"] = {name: (e / w if w else None) for name, (e, w) in totals.items()}
    out["cer"] = cer_total[0] / cer_total[1] if cer_total[1] else None
    out["language"] = (detected_ok, detected_n) if detected_n else None
    return out


def performance(files, summary):
    audio = sum(r["audio_s"] for r in files.values())
    if not audio:
        return {}
    # whisper.cpp loads the model on every call (the engine reports that time separately): add it to the load time.
    per_call = sorted(r["call_load_s"] for r in files.values() if r.get("call_load_s"))
    call_load = per_call[len(per_call) // 2] if per_call else 0.0
    return {
        "cpu_per_min": sum(r["cpu_s"] for r in files.values()) / audio * 60,
        "rtf": sum(r["infer_s"] for r in files.values()) / audio,
        "peak_mb": summary.get("peak_mb_final"),
        "load_s": (summary.get("load_s") or 0.0) + call_load if summary.get("load_s") is not None else None,
        "stable": sum(1 for r in files.values() if r.get("same_text_every_run")) / len(files),
        "cpus": summary.get("cpus_visible"),
    }


def percent(value):
    return "-" if value is None else f"{value * 100:.1f}%"


def print_table(rows):
    header = ["image", "profile", "language"] + [f"WER {n}" for n, _ in GROUPS] + [
        "CER", "pause: words", "language detected", "CPU-s per audio min", "RTF", "peak, MB", "load, s", "stability"]
    print("| " + " | ".join(header) + " |")
    print("|" + "---|" * len(header))
    for label, scored, perf in rows:
        image, profile, mode = label
        cells = [image, f"{profile} ({perf.get('cpus', '?')} CPU)", mode]
        cells += [percent(scored["groups"][n]) for n, _ in GROUPS]
        cells.append(percent(scored["cer"]))
        cells.append(f"{scored['silence_words']}" if scored["silence_files"] else "-")
        lang = scored["language"]
        cells.append(f"{lang[0]}/{lang[1]}" if lang else "-")
        cells.append(f"{perf['cpu_per_min']:.1f}" if perf else "-")
        cells.append(f"{perf['rtf']:.2f}" if perf else "-")
        cells.append(f"{perf['peak_mb']:.0f}" if perf and perf.get("peak_mb") else "-")
        cells.append(f"{perf['load_s']:.1f}" if perf and perf.get("load_s") is not None else "-")
        cells.append(percent(perf.get("stable")) if perf else "-")
        print("| " + " | ".join(cells) + " |")


def print_details(label, refs, files):
    print(f"\n### {label[0]}, {label[1]}, {label[2]}\n")
    for id_, ref in refs.items():
        row = files.get(id_)
        if row is None:
            print(f"- `{id_}`: no result")
            continue
        if "silence" in ref["tags"]:
            print(f"- `{id_}` (pause): answer \"{row['text']}\"")
            continue
        errors, words, variant = best_variant(ref["variants"], row["text"])
        print(f"- `{id_}`: {errors}/{words} words, {row['cpu_s']:.1f} CPU-s for {row['audio_s']:.1f} s of audio\n"
              f"  - reference: {variant}\n  - answer:    {row['text']}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--results", default=os.path.join(HERE, "results"))
    ap.add_argument("--refs", default=os.path.join(HERE, "references.tsv"))
    ap.add_argument("--details", action="store_true", help="show the reference and the answer for each recording")
    args = ap.parse_args()

    refs = load_references(args.refs)
    names = sorted(n for n in os.listdir(args.results) if n.endswith(".jsonl")) if os.path.isdir(args.results) else []
    if not names:
        sys.exit(f"there are no results in {args.results}: run ./run.sh first")

    rows, detail = [], []
    for name in names:
        image, profile, mode = (name[:-len(".jsonl")].split("__") + ["", ""])[:3]
        files, summary = load_results(os.path.join(args.results, name))
        scored = score_run(refs, files)
        if scored["missing"]:
            print(f"warning: {name} has no recordings {', '.join(scored['missing'])}", file=sys.stderr)
        rows.append(((image, profile, mode), scored, performance(files, summary)))
        detail.append(((image, profile, mode), files))
    print_table(rows)
    if args.details:
        for label, files in detail:
            print_details(label, refs, files)


if __name__ == "__main__":
    main()
