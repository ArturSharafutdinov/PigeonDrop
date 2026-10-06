#!/usr/bin/env bash
# Собирает образы стенда (по одному, чтобы не упереться в память Docker Desktop).
# Использование: ./build.sh [parakeet gigaam faster-whisper whispercpp]
set -euo pipefail
cd "$(dirname "$0")/docker"

images=("$@")
[ ${#images[@]} -gt 0 ] || images=(parakeet gigaam faster-whisper whispercpp)

for name in "${images[@]}"; do
  [ -f "$name/Dockerfile" ] || { echo "нет образа $name (есть: parakeet gigaam faster-whisper whispercpp)" >&2; exit 1; }
  echo "== сборка stt-$name"
  docker build -f "$name/Dockerfile" -t "stt-$name" .
done
echo "готово:"
docker images --format '  {{.Repository}}:{{.Tag}}  {{.Size}}' | grep '^  stt-' || true
