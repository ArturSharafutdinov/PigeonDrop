#!/usr/bin/env bash
# A throwaway PostgreSQL for development and tests, kept inside backend/.dev (ignored by git)
# and listening on localhost:55432 only. It never touches any other PostgreSQL on this machine.
#
#   scripts/dev-db.sh up      start it (creates the cluster on first use)
#   scripts/dev-db.sh down    stop it
#   scripts/dev-db.sh reset   stop it and delete its data
#   scripts/dev-db.sh url     print the connection URL for TEST_DATABASE_URL / DATABASE_URL
#
# Needs the PostgreSQL server binaries (initdb, pg_ctl). On macOS: brew install postgresql@16.
set -euo pipefail

# PostgreSQL refuses to start under a locale the system does not have; a plain C locale always works here.
export LC_ALL=C

root="$(cd "$(dirname "$0")/.." && pwd)"
data="$root/.dev/pg"
port="${DEV_DB_PORT:-55432}"

find_bin() {
  if command -v "$1" >/dev/null 2>&1; then command -v "$1"; return; fi
  for dir in /opt/homebrew/opt/postgresql@*/bin /usr/local/opt/postgresql@*/bin /usr/lib/postgresql/*/bin; do
    if [ -x "$dir/$1" ]; then echo "$dir/$1"; return; fi
  done
  echo "scripts/dev-db.sh: cannot find $1 - install the PostgreSQL server binaries" >&2
  exit 1
}

case "${1:-}" in
  up)
    initdb="$(find_bin initdb)"; pg_ctl="$(find_bin pg_ctl)"
    if [ ! -d "$data" ]; then
      mkdir -p "$data"
      "$initdb" -D "$data" --auth=trust --locale=C --encoding=UTF8 --username=postgres >/dev/null
    fi
    if "$pg_ctl" -D "$data" status >/dev/null 2>&1; then
      echo "already running on port $port"
    else
      "$pg_ctl" -D "$data" -o "-p $port -c listen_addresses=127.0.0.1 -c unix_socket_directories=$data -c fsync=off" -l "$data/server.log" -w start >/dev/null
      echo "started on port $port"
    fi
    ;;
  down)
    pg_ctl="$(find_bin pg_ctl)"
    if [ -d "$data" ] && "$pg_ctl" -D "$data" status >/dev/null 2>&1; then "$pg_ctl" -D "$data" -m fast -w stop >/dev/null; echo "stopped"; else echo "not running"; fi
    ;;
  reset)
    "$0" down
    rm -rf "$data"
    echo "data deleted"
    ;;
  url)
    echo "postgres://postgres@127.0.0.1:$port/postgres?sslmode=disable"
    ;;
  *)
    echo "usage: $0 up|down|reset|url" >&2
    exit 2
    ;;
esac
