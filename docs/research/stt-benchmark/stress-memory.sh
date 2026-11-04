#!/usr/bin/env bash
# Does the recogniser's memory grow over a long run, and is the limit enough: many passes over all recordings in one
# process, memory after every call (docker/common/stress.py). Each limit and core count gets its own container.
#
#   ./stress-memory.sh                              # limits 3 and 4 GB, 1 and 2 cores, 40 passes, one recording at a time
#   MEMS="2 3" PASSES=10 ./stress-memory.sh         # other limits, faster
#   CPUSETS="0" IMAGE=stt-parakeet ./stress-memory.sh
#   LENGTHS="8 14 20 26 32 38 44 50 56" OUT=results/memory-varied ./stress-memory.sh   # also recordings of different lengths
#
# Two recordings at the same time:
#   WORKERS=2 SYNC=1 MEMS=6 CPUSETS=0,1 OUT=results/memory-x2 ./stress-memory.sh                 # two threads, one model
#   WORKERS=2 CONCURRENCY=processes MEMS=7 CPUSETS=0,1 OUT=results/memory-x2p ./stress-memory.sh # two processes, two models
#   WORKERS=2 SYNC=1 SAME_ORDER=1 STRESS_PATHS="/data/stress/max60.wav /data/wav/long01.wav" ...  # the heaviest case: the same
#                                                   # 60 s recording in both threads at the same moment
# A shorter recording limit (for example 30 s instead of 60):
#   MAXSEC=30 LENGTHS="8 14 20 26 30" OUT=results/memory-30s ./stress-memory.sh
#
# Files: all data/wav/*.wav and data/stress/max60.wav (a 60 s recording, the watch's limit; built from your recordings);
# STRESS_PATHS replaces this list (paths inside the container, where /data is ./data), MAXSEC drops longer
# recordings. LENGTHS adds recordings of the given lengths in seconds (data/varied/len-N.wav, the start of max60.wav):
# the engine's memory depends on the recording length, and one or two long recordings do not show what happens with
# all lengths mixed.
# Result: $OUT/<limit>g-<cores>c.jsonl and .exit (whether the container was killed for memory), then
#   python3 memory_report.py --results $OUT
# The container is limited like a VPS: memory without swap (--memory-swap equals --memory), no network.
set -euo pipefail
cd "$(dirname "$0")"

IMAGE=${IMAGE:-stt-parakeet}
MEMS=${MEMS:-"3 4"}
CPUSETS=${CPUSETS:-"0 0,1"}
PASSES=${PASSES:-40}
OUT=${OUT:-results/memory}
WORKERS=${WORKERS:-1}
CONCURRENCY=${CONCURRENCY:-threads}
SYNC=${SYNC:-0}
SAME_ORDER=${SAME_ORDER:-0}
MAXSEC=${MAXSEC:-}

[ -d data/wav ] || { echo "there is no data/wav: first python3 import_recordings.py FOLDER" >&2; exit 1; }
docker image inspect "$IMAGE" > /dev/null 2>&1 || { echo "there is no image $IMAGE (./build.sh)" >&2; exit 1; }
case "$CONCURRENCY" in threads | processes) ;; *) echo "CONCURRENCY: threads or processes" >&2; exit 1 ;; esac
if [ "$CONCURRENCY" = processes ] && [ "$SYNC" = 1 ]; then
  echo "SYNC only works between threads of one process (CONCURRENCY=threads): turned off for processes" >&2
  SYNC=0
fi

if [ ! -f data/stress/max60.wav ]; then
  # A long recording joined with two others and cut to 60 seconds: the longest input the watch can produce.
  command -v ffmpeg > /dev/null || { echo "ffmpeg is needed (brew install ffmpeg)" >&2; exit 1; }
  mkdir -p data/stress
  ffmpeg -loglevel error -y -i data/wav/long01.wav -i data/wav/ru03.wav -i data/wav/ru02.wav \
    -filter_complex "[0:a][1:a][2:a]concat=n=3:v=0:a=1" -t 60 -ar 16000 -ac 1 -c:a pcm_s16le data/stress/max60.wav
fi

read -r -a paths <<< "${STRESS_PATHS:-/data/wav /data/stress/max60.wav}"
if [ -n "${LENGTHS:-}" ]; then
  rm -rf data/varied && mkdir -p data/varied
  for sec in $LENGTHS; do
    ffmpeg -loglevel error -y -i data/stress/max60.wav -t "$sec" -ar 16000 -ac 1 -c:a pcm_s16le "data/varied/len-$sec.wav"
  done
  paths+=(/data/varied)
fi

args=(--passes "$PASSES")
[ -z "$MAXSEC" ] || args+=(--max-seconds "$MAXSEC")
if [ "$CONCURRENCY" = threads ]; then
  [ "$WORKERS" -le 1 ] || args+=(--workers "$WORKERS")
  [ "$SYNC" != 1 ] || args+=(--sync)
  [ "$SAME_ORDER" != 1 ] || args+=(--same-order)
fi

mkdir -p "$OUT"
for mem in $MEMS; do
  for cpuset in $CPUSETS; do
    cpus=$(echo "$cpuset" | tr ',' '\n' | wc -l | tr -d ' ')
    name="stress-${mem}g-${cpus}c"
    run="$OUT/${mem}g-${cpus}c"
    echo "== limit ${mem} GB, cores $cpus, passes $PASSES, at the same time $WORKERS ($CONCURRENCY)"
    docker rm -f "$name" > /dev/null 2>&1 || true
    docker_args=(--name "$name" --network none --cpus="$cpus" --cpuset-cpus="$cpuset"
      --memory="${mem}g" --memory-swap="${mem}g"
      -v "$PWD/data:/data:ro" -v "$PWD/docker/common/stress.py:/stress/stress.py:ro")
    if [ "$CONCURRENCY" = processes ] && [ "$WORKERS" -gt 1 ]; then
      # Several processes in one container: a shared memory limit, each with its own copy of the model.
      docker run "${docker_args[@]}" -e N="$WORKERS" --entrypoint sh "$IMAGE" -c '
        i=0; pids=""
        while [ "$i" -lt "$N" ]; do python /stress/stress.py "$@" --label "p$i" & pids="$pids $!"; i=$((i + 1)); done
        rc=0; codes=""
        for p in $pids; do wait "$p"; e=$?; codes="$codes$e,"; [ "$e" -eq 0 ] || rc=1; done
        echo "exit_codes=${codes%,}" >&2
        exit $rc' sh "${args[@]}" "${paths[@]}" > "$run.jsonl" 2> "$run.log" || true
    else
      docker run "${docker_args[@]}" --entrypoint python "$IMAGE" /stress/stress.py "${args[@]}" "${paths[@]}" \
        > "$run.jsonl" 2> "$run.log" || true
    fi
    docker inspect -f 'oom_killed={{.State.OOMKilled}} exit_code={{.State.ExitCode}}' "$name" > "$run.exit"
    grep -o 'exit_codes=[0-9,]*' "$run.log" | tail -n 1 >> "$run.exit" || true
    cat "$run.exit"
    docker rm "$name" > /dev/null
  done
done
echo "done. Summary: python3 memory_report.py --results $OUT"
