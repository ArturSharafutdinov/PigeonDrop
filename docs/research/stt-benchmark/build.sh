#!/usr/bin/env bash
# Builds the benchmark's images (one at a time, so as not to run into Docker Desktop's memory).
# Usage: ./build.sh [parakeet gigaam faster-whisper whispercpp]
set -euo pipefail
cd "$(dirname "$0")/docker"

images=("$@")
[ ${#images[@]} -gt 0 ] || images=(parakeet gigaam faster-whisper whispercpp)

for name in "${images[@]}"; do
  [ -f "$name/Dockerfile" ] || { echo "no image $name (there are: parakeet gigaam faster-whisper whispercpp)" >&2; exit 1; }
  echo "== building stt-$name"
  docker build -f "$name/Dockerfile" -t "stt-$name" .
done
echo "done:"
docker images --format '  {{.Repository}}:{{.Tag}}  {{.Size}}' | grep '^  stt-' || true
