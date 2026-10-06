#!/usr/bin/env python3
"""Раскладывает скачанные со страницы /pair оригиналы по сценарию и переводит их в WAV 16 кГц моно.

    python3 import_recordings.py ~/Downloads/stt-originals              # файлы по порядку скачивания (время изменения)
    python3 import_recordings.py ~/Downloads/stt-originals --sort name   # по имени, если порядок задан именами
    python3 import_recordings.py ПАПКА --ids ru01,ru02,en01              # файлов меньше, чем в сценарии: какие это записи
    python3 import_recordings.py ПАПКА --dry-run                         # только показать соответствие

Скачанные файлы называются по id записи на сервере, и по имени не понять, какая фраза в каком: порядок
берётся из времени скачивания, поэтому скачивайте от самой старой записи к самой новой. Скрипт печатает
таблицу «файл, фраза, длительность» и предупреждает, если длительность не похожа на текст фразы
(значит, порядок перепутан). Нужны `ffmpeg` и `ffprobe`.
"""
import argparse
import os
import shutil
import subprocess
import sys

import score

HERE = os.path.dirname(os.path.abspath(__file__))
AUDIO_EXTENSIONS = (".opus", ".ogg", ".oga", ".wav")
WORDS_PER_SECOND = 2.5  # обычный темп речи, для проверки правдоподобия


class MappingError(Exception):
    pass


def list_files(folder, sort):
    names = [n for n in os.listdir(folder) if n.lower().endswith(AUDIO_EXTENSIONS) and not n.startswith(".")]
    paths = [os.path.join(folder, n) for n in names]
    if sort == "mtime":
        return sorted(paths, key=lambda p: (os.path.getmtime(p), p))
    return sorted(paths)


def plan(files, ids):
    """Сопоставляет файлы и записи сценария по порядку."""
    if len(files) != len(ids):
        raise MappingError(
            f"файлов {len(files)}, а записей в сценарии {len(ids)}. Если часть записей не делали, перечислите, "
            f"какие делали, в --ids (через запятую, в порядке записи); если файлов лишние, уберите их из папки.")
    return list(zip(files, ids))


def check_duration(id_, words, seconds):
    """Предупреждение, если длительность не похожа на запись этой фразы; иначе None."""
    if words == 0:  # запись без речи: должна быть заметной длины, а не случайным касанием
        return "слишком короткая запись паузы" if seconds < 3 else None
    expected = words / WORDS_PER_SECOND
    if seconds < expected * 0.4 or seconds > expected * 2.5 + 2:
        return f"длительность {seconds:.1f} с не похожа на фразу из {words} слов (ждали ~{expected:.0f} с): порядок мог перепутаться"
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
    ap.add_argument("--ids", help="какие записи сценария соответствуют файлам (через запятую, по порядку)")
    ap.add_argument("--refs", default=os.path.join(HERE, "references.tsv"))
    ap.add_argument("--out", default=os.path.join(HERE, "data"))
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    refs = score.load_references(args.refs)
    ids = args.ids.split(",") if args.ids else list(refs)
    unknown = [i for i in ids if i not in refs]
    if unknown:
        sys.exit(f"нет таких записей в сценарии: {', '.join(unknown)}")
    if not args.dry_run and not (shutil.which("ffmpeg") and shutil.which("ffprobe")):
        sys.exit("нужны ffmpeg и ffprobe (brew install ffmpeg)")
    try:
        pairs = plan(list_files(args.folder, args.sort), ids)
    except MappingError as error:
        sys.exit(str(error))

    wav_dir, original_dir = os.path.join(args.out, "wav"), os.path.join(args.out, "original")
    if not args.dry_run:
        os.makedirs(wav_dir, exist_ok=True)
        os.makedirs(original_dir, exist_ok=True)

    warnings = 0
    print("| id | файл | длительность | ")
    print("|---|---|---|")
    for src, id_ in pairs:
        seconds = None
        if shutil.which("ffprobe"):
            seconds = probe_duration(src)
        words = len(score.tokens(refs[id_]["variants"][0]))
        note = check_duration(id_, words, seconds) if seconds is not None else None
        warnings += bool(note)
        shown = "?" if seconds is None else f"{seconds:.1f} с"
        print(f"| `{id_}` | {os.path.basename(src)} | {shown}{'  ⚠ ' + note if note else ''} |")
        if not args.dry_run:
            shutil.copyfile(src, os.path.join(original_dir, id_ + os.path.splitext(src)[1].lower()))
            convert(src, os.path.join(wav_dir, id_ + ".wav"))

    if args.dry_run:
        print("\nпробный запуск: ничего не записано")
    else:
        print(f"\nготово: {len(pairs)} записей в {wav_dir}")
    if warnings:
        sys.exit(f"предупреждений: {warnings}. Проверьте порядок, затем запустите ещё раз")


if __name__ == "__main__":
    main()
