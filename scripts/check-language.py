#!/usr/bin/env python3
"""Checks the language policy: code, comments, docs and test names are English.

Cyrillic and CJK letters are allowed only where they are data: the Russian UI texts, test fixtures that check
Unicode handling or Russian rendering, and the benchmark's Russian test phrases. Anything else fails the check.

    python3 scripts/check-language.py          # checks every file tracked by git
    python3 scripts/check-language.py FILE...  # checks the given files
"""
import fnmatch
import re
import subprocess
import sys

# (glob, why it may contain such letters). fnmatch's "*" also matches "/", so "dir/*" covers the whole tree below it.
ALLOWED = [
    ("PigeonDrop/i18n/ru.js", "the Russian texts of the watch app"),
    ("backend/internal/httpapi/pair.ru.json", "the Russian texts of the pairing page"),
    ("backend/internal/telegram/texts.go", "the Russian texts of the Telegram bot"),
    ("*_test.go", "test fixtures: Unicode handling and Russian rendering"),
    ("PigeonDrop/test/*", "tests of the Russian screens"),
    ("stt/tests/*", "test fixtures: Unicode handling"),
    ("docs/research/stt-benchmark/*", "the benchmark's Russian test phrases and their checks"),
]

LETTERS = re.compile(
    "[Ѐ-ӿ"          # Cyrillic
    "぀-ヿ"           # Hiragana, Katakana
    "㐀-䶿一-鿿"  # CJK ideographs
    "가-힯]"          # Hangul
)


def allowed(path):
    return any(fnmatch.fnmatch(path, pattern) for pattern, _ in ALLOWED)


def tracked_files():
    out = subprocess.run(["git", "ls-files", "-z"], capture_output=True, check=True).stdout
    return [p for p in out.decode().split("\0") if p]


def check(path):
    try:
        with open(path, encoding="utf-8") as f:
            lines = f.read().split("\n")
    except (UnicodeDecodeError, FileNotFoundError, IsADirectoryError):
        return []  # binary files and files removed from the working tree
    return [(n, line.strip()) for n, line in enumerate(lines, 1) if LETTERS.search(line)]


def main(argv):
    paths = argv or tracked_files()
    failed = 0
    for path in paths:
        if allowed(path):
            continue
        for number, line in check(path):
            print(f"{path}:{number}: {line[:120]}")
            failed += 1
    if failed:
        print(f"\n{failed} line(s) with Cyrillic or CJK letters outside the allowed files.")
        print("Translate them to English, or, if they are data, add the file to ALLOWED in scripts/check-language.py.")
        return 1
    print("language check: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
