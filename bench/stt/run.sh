#!/usr/bin/env bash
# Прогоняет образы по профилям ресурсов на одних и тех же записях и складывает результаты в results/.
#
#   ./run.sh                                  # все образы, оба профиля, 3 замера на файл
#   IMAGES="parakeet gigaam" ./run.sh         # только эти образы
#   PROFILES=strict REPEAT=1 ./run.sh         # быстрый прогон
#   MODES="hint auto" ./run.sh                # ещё и проверка автоопределения языка
#   DATA=data/smoke ./run.sh                  # пробный прогон на файлах из ./smoke.sh
#
# Профили имитируют VPS:  strict - 1 ядро и 2 ГБ;  relaxed - 2 ядра и 4 ГБ.
# Ядра задаются и квотой (--cpus), и закреплением (--cpuset-cpus): тогда программа видит ровно столько ядер,
# сколько получит на VPS, и сама выбирает число потоков (иначе она увидит ядра всего Mac и растратит квоту на потоки).
# Память без подкачки (--memory-swap равен --memory): при нехватке контейнер убивается, а не замедляется.
# Сети нет (--network none): модели лежат в образах.
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
    *) echo "неизвестный профиль $1 (есть strict и relaxed)" >&2; return 1 ;;
  esac
}
profile_gib() { case "$1" in strict) echo 2 ;; relaxed) echo 4 ;; esac; }

shopt -s nullglob
wavs=("$DATA"/*.wav)
[ ${#wavs[@]} -gt 0 ] || { echo "в $DATA нет .wav: сначала python3 import_recordings.py ПАПКА (или ./smoke.sh)" >&2; exit 1; }
files=()
for f in "${wavs[@]}"; do files+=("/data/$(basename "$f")"); done

docker_mem=$(docker info --format '{{.MemTotal}}')
docker_cpus=$(docker info --format '{{.NCPU}}')
mkdir -p "$OUT"
{
  echo "дата: $(date '+%Y-%m-%d %H:%M:%S')"
  echo "Docker: $(docker info --format '{{.OperatingSystem}} {{.ServerVersion}}, {{.Architecture}}, процессоров {{.NCPU}}, память {{.MemTotal}} байт')"
  echo "записей: ${#files[@]} из $DATA; повторов: $REPEAT, прогрев: $WARMUP"
  for name in $IMAGES; do docker image ls --format "образ {{.Repository}}:{{.Tag}} {{.ID}} {{.Size}}" "stt-$name"; done
} > "$OUT/environment.txt"

for profile in $PROFILES; do
  flags=$(profile_flags "$profile")
  need=$(( $(profile_gib "$profile") + 1 ))   # плюс гигабайт на систему внутри виртуальной машины Docker
  if [ "$docker_mem" -lt $(( need * 1073741824 )) ] && [ "${FORCE:-0}" != 1 ]; then
    echo "пропуск профиля $profile: Docker Desktop даёт $(( docker_mem / 1048576 )) МБ, нужно не меньше ${need} ГБ." >&2
    echo "  Поднимите память в Docker Desktop: Settings > Resources > Memory (или FORCE=1, но тогда результаты ненадёжны)." >&2
    continue
  fi
  for name in $IMAGES; do
    for mode in $MODES; do
      echo "== $name / $profile / $mode"
      docker run --rm --network none $flags -v "$PWD/$DATA:/data:ro" "stt-$name" \
        --repeat "$REPEAT" --warmup "$WARMUP" --lang-mode "$mode" "${files[@]}" \
        > "$OUT/${name}__${profile}__${mode}.jsonl" 2> "$OUT/${name}__${profile}__${mode}.log" \
        || { echo "  ОШИБКА (код $?), см. $OUT/${name}__${profile}__${mode}.log" >&2; tail -3 "$OUT/${name}__${profile}__${mode}.log" >&2; }
    done
  done
done
echo "готово. Сводка: python3 score.py"
