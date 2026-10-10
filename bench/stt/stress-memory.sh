#!/usr/bin/env bash
# Растёт ли память распознавателя при долгой работе и хватает ли лимита: много проходов по всем записям в одном
# процессе, память после каждого вызова (docker/common/stress.py). Для каждого лимита и числа ядер отдельный контейнер.
#
#   ./stress-memory.sh                              # лимиты 3 и 4 ГБ, 1 и 2 ядра, 40 проходов, одна запись за раз
#   MEMS="2 3" PASSES=10 ./stress-memory.sh         # другие лимиты, быстрее
#   CPUSETS="0" IMAGE=stt-parakeet ./stress-memory.sh
#   LENGTHS="8 14 20 26 32 38 44 50 56" OUT=results/memory-varied ./stress-memory.sh   # ещё и записи разной длины
#
# Две записи одновременно:
#   WORKERS=2 SYNC=1 MEMS=6 CPUSETS=0,1 OUT=results/memory-x2 ./stress-memory.sh                 # два потока, одна модель
#   WORKERS=2 CONCURRENCY=processes MEMS=7 CPUSETS=0,1 OUT=results/memory-x2p ./stress-memory.sh # два процесса, две модели
#   WORKERS=2 SYNC=1 SAME_ORDER=1 STRESS_PATHS="/data/stress/max60.wav /data/wav/long01.wav" ...  # самое тяжёлое: одна и та же
#                                                   # запись на 60 с в обоих потоках в один и тот же момент
# Более короткий предел записи (например 30 с вместо 60):
#   MAXSEC=30 LENGTHS="8 14 20 26 30" OUT=results/memory-30s ./stress-memory.sh
#
# Файлы: все data/wav/*.wav и data/stress/max60.wav (запись на 60 с, предел часов; создаётся из ваших записей);
# STRESS_PATHS заменяет этот список (пути внутри контейнера, каталог /data - это ./data), MAXSEC отбрасывает более
# длинные записи. С LENGTHS добавляются записи указанной длины в секундах (data/varied/len-N.wav, начало max60.wav):
# память движка зависит от длины записи, и одна-две длинные записи не показывают, что будет при любых длинах вперемешку.
# Результат: $OUT/<лимит>g-<ядер>c.jsonl и .exit (убит ли контейнер по памяти), затем
#   python3 memory_report.py --results $OUT
# Контейнер ограничен как на VPS: память без подкачки (--memory-swap равен --memory), сети нет.
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

[ -d data/wav ] || { echo "нет data/wav: сначала python3 import_recordings.py ПАПКА" >&2; exit 1; }
docker image inspect "$IMAGE" > /dev/null 2>&1 || { echo "нет образа $IMAGE (./build.sh)" >&2; exit 1; }
case "$CONCURRENCY" in threads | processes) ;; *) echo "CONCURRENCY: threads или processes" >&2; exit 1 ;; esac
if [ "$CONCURRENCY" = processes ] && [ "$SYNC" = 1 ]; then
  echo "SYNC работает только между потоками одного процесса (CONCURRENCY=threads): для процессов отключён" >&2
  SYNC=0
fi

if [ ! -f data/stress/max60.wav ]; then
  # Склейка длинной записи с двумя другими, обрезанная до 60 секунд: самый длинный вход, который дают часы.
  command -v ffmpeg > /dev/null || { echo "нужен ffmpeg (brew install ffmpeg)" >&2; exit 1; }
  mkdir -p data/stress
  ffmpeg -loglevel error -y -i data/wav/long01.wav -i data/wav/ru03.wav -i data/wav/ru02.wav \
    -filter_complex "[0:a][1:a][2:a]concat=n=3:v=0:a=1" -t 60 -ar 16000 -ac 1 -c:a pcm_s16le data/stress/max60.wav
fi

paths=(${STRESS_PATHS:-/data/wav /data/stress/max60.wav})
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
    echo "== лимит ${mem} ГБ, ядер $cpus, проходов $PASSES, одновременно $WORKERS ($CONCURRENCY)"
    docker rm -f "$name" > /dev/null 2>&1 || true
    docker_args=(--name "$name" --network none --cpus="$cpus" --cpuset-cpus="$cpuset"
      --memory="${mem}g" --memory-swap="${mem}g"
      -v "$PWD/data:/data:ro" -v "$PWD/docker/common/stress.py:/stress/stress.py:ro")
    if [ "$CONCURRENCY" = processes ] && [ "$WORKERS" -gt 1 ]; then
      # Несколько процессов в одном контейнере: общий лимит памяти, у каждого своя копия модели.
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
echo "готово. Сводка: python3 memory_report.py --results $OUT"
