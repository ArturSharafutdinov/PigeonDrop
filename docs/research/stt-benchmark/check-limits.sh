#!/usr/bin/env bash
# Checks that the benchmark's limits really work, and are not only written in the command.
# Needs the built image stt-parakeet (./build.sh parakeet) and the file data/smoke/ru00.wav (./smoke.sh).
set -uo pipefail
cd "$(dirname "$0")" || exit 1
fail=0
ok()  { echo "  ok: $1"; }
bad() { echo "  FAIL: $1"; fail=1; }

echo "1. core pinning: the container sees as many cores as it was given"
for set in 0 0,1; do
  n=$(docker run --rm --network none --cpuset-cpus=$set --entrypoint python stt-parakeet -c "import os; print(len(os.sched_getaffinity(0)))")
  want=$(echo "$set" | tr ',' '\n' | wc -l | tr -d ' ')
  [ "$n" = "$want" ] && ok "--cpuset-cpus=$set -> $n" || bad "--cpuset-cpus=$set -> $n, expected $want"
done

echo "2. memory: a limit below the need kills the container, there is no swap"
docker run --rm --network none --memory=300m --memory-swap=300m -v "$PWD/data/smoke:/data:ro" stt-parakeet \
  --repeat 1 --warmup 0 /data/ru00.wav > /dev/null 2>&1
code=$?
[ "$code" = 137 ] && ok "with a 300 MB limit the container was killed (code 137)" || bad "with a 300 MB limit the code is $code, expected 137"
limit=$(docker run --rm --memory=2g --memory-swap=2g --entrypoint sh stt-parakeet -c 'cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory.swap.max' 2>/dev/null | tr '\n' ' ')
[ "$limit" = "2147483648 0 " ] && ok "cgroup: memory.max=2 GiB, swap.max=0" || bad "cgroup showed '$limit', expected '2147483648 0'"

echo "3. there is no network"
if docker run --rm --network none --entrypoint python stt-parakeet -c "import socket; socket.create_connection(('1.1.1.1', 53), 3)" > /dev/null 2>&1; then
  bad "the network is reachable"
else
  ok "a connection is impossible"
fi

[ $fail = 0 ] && echo "all is well" || { echo "there are failures"; exit 1; }
