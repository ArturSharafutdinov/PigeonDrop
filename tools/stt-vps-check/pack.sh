#!/usr/bin/env bash
# Собирает один архив для проверки на VPS: скрипт проверки, исходники образа (stt/), ваши записи и тексты стенда.
# Запускается на Mac, в клоне репозитория, где лежит bench/stt/data (записи в git не попадают):
#
#   tools/stt-vps-check/pack.sh                      # архив bench/stt/data/stt-vps-check.tar.gz
#   tools/stt-vps-check/pack.sh /куда/угодно.tar.gz
#   DATA=путь/к/data REFERENCE=путь/к/результатам.jsonl tools/stt-vps-check/pack.sh
#
# В архиве ваш голос: отправляйте его только на свой VPS (scp) и удалите после проверки (README.md).
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
DATA=${DATA:-$ROOT/bench/stt/data}
REFERENCE=${REFERENCE:-$ROOT/bench/stt/results/parakeet__relaxed__hint.jsonl}
OUT=${1:-$DATA/stt-vps-check.tar.gz}

ls "$DATA"/wav/*.wav > /dev/null 2>&1 || { echo "нет $DATA/wav/*.wav: сначала bench/stt/import_recordings.py (bench/stt/README.md)" >&2; exit 1; }
[ -f "$DATA/stress/max60.wav" ] || { echo "нет $DATA/stress/max60.wav: его создаёт bench/stt/stress-memory.sh" >&2; exit 1; }
[ -f "$ROOT/stt/Dockerfile" ] && [ -d "$ROOT/stt/app" ] || { echo "нет $ROOT/stt (Dockerfile и app)" >&2; exit 1; }

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
kit="$stage/stt-vps-check"
mkdir -p "$kit/data/wav" "$kit/data/stress" "$kit/image"

cp check.py README.md "$kit/"
cp "$DATA"/wav/*.wav "$kit/data/wav/"
cp "$DATA/stress/max60.wav" "$kit/data/stress/"
if [ -f "$REFERENCE" ]; then
  cp "$REFERENCE" "$kit/data/reference.jsonl"
else
  echo "нет $REFERENCE: проверка пройдёт без сверки текстов с Mac" >&2
fi
# The image is built from exactly the sources of this checkout.
cp "$ROOT/stt/Dockerfile" "$ROOT/stt/.dockerignore" "$kit/image/"
cp -R "$ROOT/stt/app" "$kit/image/app"
find "$kit/image" -name __pycache__ -type d -prune -exec rm -rf {} +

mkdir -p "$(dirname "$OUT")"
# COPYFILE_DISABLE: the macOS tar would otherwise add "._" files that mean nothing on Linux.
COPYFILE_DISABLE=1 tar -C "$stage" -czf "$OUT" stt-vps-check
echo "готово: $OUT ($(du -h "$OUT" | cut -f1))"
echo "дальше (README.md): scp '$OUT' ПОЛЬЗОВАТЕЛЬ@VPS:~/ , на VPS: tar xzf $(basename "$OUT") && cd stt-vps-check"
