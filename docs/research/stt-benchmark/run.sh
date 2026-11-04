#!/usr/bin/env bash
# Runs the images through the resource profiles on the same recordings and puts the results in results/.
#
#   ./run.sh                                  # all images, both profiles, 3 measurements per file
#   IMAGES="parakeet gigaam" ./run.sh         # only these images
#   PROFILES=strict REPEAT=1 ./run.sh         # a quick run
#   MODES="hint auto" ./run.sh                # also tries automatic language detection
#   DATA=data/smoke ./run.sh                  # a trial run on the files from ./smoke.sh
#
# The profiles imitate a VPS:  strict - 1 core and 2 GB;  relaxed - 2 cores and 4 GB.
# Cores are set both by a quota (--cpus) and by pinning (--cpuset-cpus): then the program sees exactly as many cores
# as it would get on a VPS and picks the number of threads itself (otherwise it would see the whole Mac's cores and waste the quota on threads).
# Memory without swap (--memory-swap equals --memory): when it runs out the container is killed, not slowed down.
# No network (--network none): the models are inside the images.
set -euo pipefail
cd "$(dirname "$0")"

IMAGES=${IMAGES:-"parakeet gigaam faster-whisper whispercpp"}
PROFILES=${PROFILES:-"strict relaxed"}
MODES=${MODES:-"hint"}
REPEAT=${REPEAT:-3}
WARMUP=${WARMUP:-1}
DATA=${DATA:-data/wav}
OUT=${OUT:-results}

profile_flags() {
  case "$1" in
    strict)  echo "--cpus=1 --cpuset-cpus=0 --memory=2g --memory-swap=2g" ;;
    relaxed) echo "--cpus=2 --cpuset-cpus=0,1 --memory=4g --memory-swap=4g" ;;
    *) echo "unknown profile $1 (there are strict and relaxed)" >&2; return 1 ;;
  esac
}
profile_gib() { case "$1" in strict) echo 2 ;; relaxed) echo 4 ;; esac; }

shopt -s nullglob
wavs=("$DATA"/*.wav)
[ ${#wavs[@]} -gt 0 ] || { echo "there is no .wav in $DATA: first python3 import_recordings.py FOLDER (or ./smoke.sh)" >&2; exit 1; }
files=()
for f in "${wavs[@]}"; do files+=("/data/$(basename "$f")"); done

docker_mem=$(docker info --format '{{.MemTotal}}')
mkdir -p "$OUT"
{
  echo "date: $(date '+%Y-%m-%d %H:%M:%S')"
  echo "Docker: $(docker info --format '{{.OperatingSystem}} {{.ServerVersion}}, {{.Architecture}}, processors {{.NCPU}}, memory {{.MemTotal}} bytes')"
  echo "recordings: ${#files[@]} from $DATA; repeats: $REPEAT, warm-up: $WARMUP"
  for name in $IMAGES; do docker image ls --format "image {{.Repository}}:{{.Tag}} {{.ID}} {{.Size}}" "stt-$name"; done
} > "$OUT/environment.txt"

for profile in $PROFILES; do
  flags=$(profile_flags "$profile")
  need=$(( $(profile_gib "$profile") + 1 ))   # plus a gigabyte for the system inside Docker's virtual machine
  if [ "$docker_mem" -lt $(( need * 1073741824 )) ] && [ "${FORCE:-0}" != 1 ]; then
    echo "skipping profile $profile: Docker Desktop gives $(( docker_mem / 1048576 )) MB, at least ${need} GB is needed." >&2
    echo "  Raise the memory in Docker Desktop: Settings > Resources > Memory (or FORCE=1, but then the results are unreliable)." >&2
    continue
  fi
  for name in $IMAGES; do
    for mode in $MODES; do
      echo "== $name / $profile / $mode"
      # shellcheck disable=SC2086  # $flags is several options on purpose
      docker run --rm --network none $flags -v "$PWD/$DATA:/data:ro" "stt-$name" \
        --repeat "$REPEAT" --warmup "$WARMUP" --lang-mode "$mode" "${files[@]}" \
        > "$OUT/${name}__${profile}__${mode}.jsonl" 2> "$OUT/${name}__${profile}__${mode}.log" \
        || { echo "  ERROR (code $?), see $OUT/${name}__${profile}__${mode}.log" >&2; tail -3 "$OUT/${name}__${profile}__${mode}.log" >&2; }
    done
  done
done
echo "done. Summary: python3 score.py"
