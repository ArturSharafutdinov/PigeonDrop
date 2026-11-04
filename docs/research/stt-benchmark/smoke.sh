#!/usr/bin/env bash
# Makes three short files to try the images without your recordings (macOS only: the `say` voices).
# The files are named like the script's recordings so that the language hint works: ru00, en00, sil00.
set -euo pipefail
cd "$(dirname "$0")"
out=data/smoke
mkdir -p "$out"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# The Russian sentence is input for a Russian voice: the benchmark checks Russian recognition too.
say -v Milena -o "$tmp/ru.aiff" "Привет. Сегодня двадцать третье октября, встреча в пятнадцать тридцать, не забудь взять документы."
say -v Samantha -o "$tmp/en.aiff" "The meeting is at three thirty in the afternoon, please bring the documents."
ffmpeg -loglevel error -y -i "$tmp/ru.aiff" -ar 16000 -ac 1 -c:a pcm_s16le "$out/ru00.wav"
ffmpeg -loglevel error -y -i "$tmp/en.aiff" -ar 16000 -ac 1 -c:a pcm_s16le "$out/en00.wav"
ffmpeg -loglevel error -y -f lavfi -i anullsrc=r=16000:cl=mono -t 8 -c:a pcm_s16le "$out/sil00.wav"
ls -l "$out"
