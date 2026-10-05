#!/usr/bin/env bash
# Проверяет, что лимиты стенда действительно работают, а не только записаны в команде.
# Нужен собранный образ stt-parakeet (./build.sh parakeet) и файл data/smoke/ru00.wav (./smoke.sh).
set -uo pipefail
cd "$(dirname "$0")"
fail=0
ok()  { echo "  ок: $1"; }
bad() { echo "  СБОЙ: $1"; fail=1; }

echo "1. закрепление ядер: контейнер видит столько ядер, сколько ему дали"
for set in 0 0,1; do
  n=$(docker run --rm --network none --cpuset-cpus=$set --entrypoint python stt-parakeet -c "import os; print(len(os.sched_getaffinity(0)))")
  want=$(echo "$set" | tr ',' '\n' | wc -l | tr -d ' ')
  [ "$n" = "$want" ] && ok "--cpuset-cpus=$set -> $n" || bad "--cpuset-cpus=$set -> $n, ждали $want"
done

echo "2. память: лимит меньше потребности убивает контейнер, подкачки нет"
docker run --rm --network none --memory=300m --memory-swap=300m -v "$PWD/data/smoke:/data:ro" stt-parakeet \
  --repeat 1 --warmup 0 /data/ru00.wav > /dev/null 2>&1
code=$?
[ "$code" = 137 ] && ok "при лимите 300 МБ контейнер убит (код 137)" || bad "при лимите 300 МБ код $code, ждали 137"
limit=$(docker run --rm --memory=2g --memory-swap=2g --entrypoint sh stt-parakeet -c 'cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory.swap.max' 2>/dev/null | tr '\n' ' ')
[ "$limit" = "2147483648 0 " ] && ok "cgroup: memory.max=2 ГиБ, swap.max=0" || bad "cgroup показал «$limit», ждали «2147483648 0»"

echo "3. сети нет"
if docker run --rm --network none --entrypoint python stt-parakeet -c "import socket; socket.create_connection(('1.1.1.1', 53), 3)" > /dev/null 2>&1; then
  bad "сеть доступна"
else
  ok "соединение невозможно"
fi

[ $fail = 0 ] && echo "всё в порядке" || { echo "есть сбои"; exit 1; }
