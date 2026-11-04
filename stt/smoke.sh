#!/usr/bin/env bash
# Runs the real image with the production limits and sends it recordings through the real protocol.
#
#   ./smoke.sh ../docs/research/stt-benchmark/data/wav        # one round
#   REPEAT=5 COMPARE=../docs/research/stt-benchmark/results/parakeet__relaxed__hint.jsonl \
#     ./smoke.sh ../docs/research/stt-benchmark/data/wav
#
# Needs the image (docker build -f Dockerfile -t pigeondrop-stt .) and Docker. The container has no network access
# beyond the published local port; the token is made up for this run.
set -euo pipefail
cd "$(dirname "$0")"

DIR=${1:?usage: smoke.sh DIRECTORY_WITH_WAV_FILES}
IMAGE=${IMAGE:-pigeondrop-stt}
REPEAT=${REPEAT:-1}
PORT=${PORT:-18000}
MEMORY=${MEMORY:-3g}
CPUS=${CPUS:-2}
NAME=pigeondrop-stt-smoke
# At least as long as the service insists on (app/config.py, MIN_TOKEN_LENGTH); $RANDOM alone can be a single digit.
TOKEN=smoke-$(od -An -N16 -tx1 /dev/urandom | tr -d " \n")

docker image inspect "$IMAGE" > /dev/null 2>&1 || { echo "no image $IMAGE: docker build -f Dockerfile -t $IMAGE ." >&2; exit 1; }
[ -d "$DIR" ] || { echo "$DIR is not a directory" >&2; exit 1; }
docker rm -f "$NAME" > /dev/null 2>&1 || true

# shellcheck disable=SC2329 # run by the trap below
cleanup() {
  docker logs "$NAME" 2>&1 | tail -n 5 || true
  docker rm -f "$NAME" > /dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --name "$NAME" -p "127.0.0.1:$PORT:8000" -e STT_TOKEN="$TOKEN" \
  --memory="$MEMORY" --memory-swap="$MEMORY" --cpus="$CPUS" "$IMAGE" > /dev/null

echo "waiting for the model to load..."
for _ in $(seq 1 120); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" > /dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS "http://127.0.0.1:$PORT/healthz" || { echo "the service did not become ready" >&2; exit 1; }
echo

compare=()
[ -z "${COMPARE:-}" ] || compare=(--compare "$COMPARE")
status=0
python3 smoke.py "http://127.0.0.1:$PORT" "$TOKEN" "$DIR" --repeat "$REPEAT" ${compare[@]+"${compare[@]}"} || status=$?

echo "container memory now, and the limit:"
docker stats --no-stream --format '  {{.MemUsage}} ({{.MemPerc}})' "$NAME"
echo "killed by the memory limit: $(docker inspect -f '{{.State.OOMKilled}}' "$NAME")"
exit $status
