#!/usr/bin/env python3
"""Matches the originals downloaded from the /pair page to the script of phrases and converts them to 16 kHz mono WAV.

    python3 import_recordings.py ~/Downloads/stt-originals              # files in download order (modification time)
    python3 import_recordings.py ~/Downloads/stt-originals --sort name   # by name, if the order is encoded in the names
    python3 import_recordings.py FOLDER --ids ru01,ru02,en01             # fewer files than phrases: which recordings they are
    python3 import_recordings.py FOLDER --dry-run                        # only show the matching

The downloaded files are named after the recording's id on the server, so the name does not tell which phrase is
which: the order comes from the download time, so download from the oldest recording to the newest. The script
prints a table "file, phrase, duration" and warns when a duration does not fit the phrase's text (which means
the order got mixed up). `ffmpeg` and `ffprobe` are needed.
"""
import argparse
import os
import shutil
import subprocess
import sys

import score

HERE = os.path.dirname(os.path.abspath(__file__))
AUDIO_EXTENSIONS = (".opus", ".ogg", ".oga", ".wav")
WORDS_PER_SECOND = 2.5  # normal speaking rate, for the plausibility check


class MappingError(Exception):
    pass


def list_files(folder, sort):
    names = [n for n in os.listdir(folder) if n.lower().endswith(AUDIO_EXTENSIONS) and not n.startswith(".")]
    paths = [os.path.join(folder, n) for n in names]
    if sort == "mtime":
        return sorted(paths, key=lambda p: (os.path.getmtime(p), p))
    return sorted(paths)


def plan(files, ids):
    """Matches the files to the script's recordings in order."""
    if len(files) != len(ids):
        raise MappingError(
            f"there are {len(files)} files but {len(ids)} recordings in the script. If some recordings were not made, list "
            f"the ones that were in --ids (comma-separated, in recording order); if there are extra files, remove them from the folder.")
    return list(zip(files, ids))


def check_duration(id_, words, seconds):
    """A warning if the duration does not look like a recording of this phrase; otherwise None."""
    if words == 0:  # a recording without speech: it should have a noticeable length, not be an accidental touch
        return "the recording of the pause is too short" if seconds < 3 else None
    expected = words / WORDS_PER_SECOND
    if seconds < expected * 0.4 or seconds > expected * 2.5 + 2:
        return f"a duration of {seconds:.1f} s does not fit a phrase of {words} words (expected ~{expected:.0f} s): the order may be mixed up"
    return None


def probe_duration(path):
    done = subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path],
                          capture_output=True, text=True, check=True)
    return float(done.stdout.strip())


def convert(src, dst):
    subprocess.run(["ffmpeg", "-loglevel", "error", "-y", "-i", src, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", dst],
                   check=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("folder")
    ap.add_argument("--sort", choices=["mtime", "name"], default="mtime")
    ap.add_argument("--ids", help="which recordings of the script the files are (comma-separated, in order)")
    ap.add_argument("--refs", default=os.path.join(HERE, "references.tsv"))
    ap.add_argument("--out", default=os.path.join(HERE, "data"))
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    refs = score.load_references(args.refs)
    ids = args.ids.split(",") if args.ids else list(refs)
    unknown = [i for i in ids if i not in refs]
    if unknown:
        sys.exit(f"no such recordings in the script: {', '.join(unknown)}")
    if not args.dry_run and not (shutil.which("ffmpeg") and shutil.which("ffprobe")):
        sys.exit("ffmpeg and ffprobe are needed (brew install ffmpeg)")
    try:
        pairs = plan(list_files(args.folder, args.sort), ids)
    except MappingError as error:
        sys.exit(str(error))

    wav_dir, original_dir = os.path.join(args.out, "wav"), os.path.join(args.out, "original")
    if not args.dry_run:
        os.makedirs(wav_dir, exist_ok=True)
        os.makedirs(original_dir, exist_ok=True)

    warnings = 0
    print("| id | file | duration | ")
    print("|---|---|---|")
    for src, id_ in pairs:
        seconds = None
        if shutil.which("ffprobe"):
            seconds = probe_duration(src)
        words = len(score.tokens(refs[id_]["variants"][0]))
        note = check_duration(id_, words, seconds) if seconds is not None else None
        warnings += bool(note)
        shown = "?" if seconds is None else f"{seconds:.1f} s"
        print(f"| `{id_}` | {os.path.basename(src)} | {shown}{'  ⚠ ' + note if note else ''} |")
        if not args.dry_run:
            shutil.copyfile(src, os.path.join(original_dir, id_ + os.path.splitext(src)[1].lower()))
            convert(src, os.path.join(wav_dir, id_ + ".wav"))

    if args.dry_run:
        print("\ndry run: nothing was written")
    else:
        print(f"\ndone: {len(pairs)} recordings in {wav_dir}")
    if warnings:
        sys.exit(f"warnings: {warnings}. Check the order, then run again")


if __name__ == "__main__":
    main()
