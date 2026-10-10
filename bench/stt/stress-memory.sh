#!/usr/bin/env bash
# Растёт ли память распознавателя при долгой работе и хватает ли лимита: много проходов по всем записям в одном
# процессе, память после каждого вызова (docker/common/stress.py). Для каждого лимита и числа ядер отдельный контейнер.
#
#   ./stress-memory.sh                              # лимиты 3 и 4 ГБ, 1 и 2 ядра, 40 проходов
#   MEMS="2 3" PASSES=10 ./stress-memory.sh         # другие лимиты, быстрее
#   CPUSETS="0" IMAGE=stt-parakeet ./stress-memory.sh
#   LENGTHS="8 14 20 26 32 38 44 50 56" OUT=results/memory-varied ./stress-memory.sh   # ещё и записи разной длины
#
# Файлы: все data/wav/*.wav и data/stress/max60.wav (запись на 60 с, предел часов; создаётся из ваших записей).
# С LENGTHS добавляются записи указанной длины в секундах (data/varied/len-N.wav, начало max60.wav): память
# движка зависит от длины записи, и одна-две длинные записи не показывают, что будет при любых длинах вперемешку.
# Результат: results/memory/<лимит>g-<ядер>c.jsonl и .exit (убит ли контейнер по памяти), затем
#   python3 memory_report.py
# Контейнер ограничен как на VPS: память без подкачки (--memory-swap равен --memory), сети нет.
set -euo pipefail
cd "$(dirname "$0")"

IMAGE=${IMAGE:-stt-parakeet}
MEMS=${MEMS:-"3 4"}
CPUSETS=${CPUSETS:-"0 0,1"}
PASSES=${PASSES:-40}
OUT=${OUT:-results/memory}

[ -d data/wav ] || { echo "нет data/wav: сначала python3 import_recordings.py ПАПКА" >&2; exit 1; }
docker image inspect "$IMAGE" > /dev/null 2>&1 || { echo "нет образа $IMAGE (./build.sh)" >&2; exit 1; }

if [ ! -f data/stress/max60.wav ]; then
  # Склейка длинной записи с двумя другими, обрезанная до 60 секунд: самый длинный вход, который дают часы.
  command -v ffmpeg > /dev/null || { echo "нужен ffmpeg (brew install ffmpeg)" >&2; exit 1; }
  mkdir -p data/stress
  ffmpeg -loglevel error -y -i data/wav/long01.wav -i data/wav/ru03.wav -i data/wav/ru02.wav \
    -filter_complex "[0:a][1:a][2:a]concat=n=3:v=0:a=1" -t 60 -ar 16000 -ac 1 -c:a pcm_s16le data/stress/max60.wav
fi

extra=()
if [ -n "${LENGTHS:-}" ]; then
  rm -rf data/varied && mkdir -p data/varied
  for sec in $LENGTHS; do
    ffmpeg -loglevel error -y -i data/stress/max60.wav -t "$sec" -ar 16000 -ac 1 -c:a pcm_s16le "data/varied/len-$sec.wav"
  done
  extra=(/data/varied)
fi

mkdir -p "$OUT"
for mem in $MEMS; do
  for cpuset in $CPUSETS; do
    cpus=$(echo "$cpuset" | tr ',' '\n' | wc -l | tr -d ' ')
    name="stress-${mem}g-${cpus}c"
    echo "== лимит ${mem} ГБ, ядер $cpus, проходов $PASSES"
    docker rm -f "$name" > /dev/null 2>&1 || true
    docker run --name "$name" --network none --cpus="$cpus" --cpuset-cpus="$cpuset" \
      --memory="${mem}g" --memory-swap="${mem}g" \
      -v "$PWD/data:/data:ro" -v "$PWD/docker/common/stress.py:/stress/stress.py:ro" \
      --entrypoint python "$IMAGE" /stress/stress.py --passes "$PASSES" /data/wav /data/stress/max60.wav ${extra[@]+"${extra[@]}"} \
      > "$OUT/${mem}g-${cpus}c.jsonl" 2> "$OUT/${mem}g-${cpus}c.log" || true
    docker inspect -f 'oom_killed={{.State.OOMKilled}} exit_code={{.State.ExitCode}}' "$name" > "$OUT/${mem}g-${cpus}c.exit"
    cat "$OUT/${mem}g-${cpus}c.exit"
    docker rm "$name" > /dev/null
  done
done
echo "готово. Сводка: python3 memory_report.py"
