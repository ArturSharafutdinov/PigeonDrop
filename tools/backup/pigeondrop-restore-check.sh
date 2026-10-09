#!/usr/bin/env bash
#
# Checks a RESTORED PigeonDrop snapshot: loads its dump into a scratch database (never the live one), compares what came
# back with what the dump held, and checks every recording's file. A backup that has never been restored is not a backup;
# run this after the first snapshot and from time to time after (it takes seconds).
#
# Usage: pigeondrop-restore-check.sh [--allow-stale] [--keep] <restored directory>
#
# The directory is where a snapshot was restored to (Backrest: Snapshot Browser -> restore -> /restore/<new name>, which
# the host sees under /opt/backrest/restore/<new name>). Under it are looked for the restored directories
# `pigeondrop-db` (pigeondrop.sql, BACKUP_INFO) and `pigeondrop-data` (messages/<id>.opus), wherever restic put them.
#
# What it checks, and what each failure means:
#   - dumped_at is recent                  the dump job stopped (and restic kept snapshotting the same old file)
#   - the dump loads without a single error (psql ON_ERROR_STOP=1) the dump is damaged or the schema cannot be rebuilt
#   - every table has exactly as many rows as the dump held (BACKUP_INFO)    the restore lost data
#   - every row of `messages` has its file <id>.opus, the same size as bytes_received, and an Ogg file starts with "OggS"
#                                          the volume was not backed up, or not completely
# Files with no row are only counted: a recording is written before its row, so an upload at the moment of the snapshot
# leaves one, and it is harmless.
#
# Exit status 0 only when nothing failed. --allow-stale downgrades the age check to a warning (for restoring an old
# snapshot on purpose), --keep leaves the scratch database for a look (drop it afterwards).
#
# Settings: PIGEONDROP_DB_LABEL or PIGEONDROP_DB_CONTAINER, PIGEONDROP_DB_NAME (see pigeondrop-db-dump.sh) and
# PIGEONDROP_SCRATCH_DB (default pigeondrop_restore_test).

set -uo pipefail

TAG=pigeondrop-restore-check
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=pigeondrop-backup-common.sh
. "$here/pigeondrop-backup-common.sh"
load_settings

DB_NAME="${PIGEONDROP_DB_NAME:-pigeondrop}"
SCRATCH="${PIGEONDROP_SCRATCH_DB:-pigeondrop_restore_test}"
MAX_AGE_HOURS=36 # a daily dump is at most a day old, plus margin for a late run

allow_stale=0
keep=0
restored=""
while [ $# -gt 0 ]; do
  case "$1" in
    --allow-stale) allow_stale=1 ;;
    --keep) keep=1 ;;
    -*) die "unknown option $1" ;;
    *) [ -z "$restored" ] || die "only one directory, got a second: $1"; restored="$1" ;;
  esac
  shift
done
[ -n "$restored" ] || die "usage: $0 [--allow-stale] [--keep] <restored directory>"
[ -d "$restored" ] || die "'$restored' is not a directory"

# Never anywhere near the live database.
[ "$SCRATCH" != "$DB_NAME" ] || die "the scratch database must not be the live one ('$DB_NAME')"
case "$SCRATCH" in *[!a-z0-9_]* | "") die "the scratch database name may only hold a-z, 0-9 and _" ;; esac

# Finds a restored directory by name, whatever the nesting restic gave it (restic restores under the original
# absolute path, /backup/pigeondrop-db, but this does not have to depend on that).
locate() { # name, override
  if [ -n "$2" ]; then echo "$2"; return; fi
  find "$restored" -type d -name "$1" 2> /dev/null | head -n 1
}
db_dir="$(locate pigeondrop-db "${PIGEONDROP_RESTORED_DB_DIR:-}")"
data_dir="$(locate pigeondrop-data "${PIGEONDROP_RESTORED_DATA_DIR:-}")"
if [ -z "$db_dir" ] || [ ! -f "$db_dir/$DB_NAME.sql" ]; then
  die "no pigeondrop-db/$DB_NAME.sql under $restored (was the dump directory part of the snapshot?)"
fi
[ -f "$db_dir/BACKUP_INFO" ] || die "no BACKUP_INFO next to the dump"
if [ -z "$data_dir" ] || [ ! -d "$data_dir/messages" ]; then
  die "no pigeondrop-data/messages under $restored (was the recordings volume part of the snapshot?)"
fi

info() { sed -n "s/^$1=//p" "$db_dir/BACKUP_INFO" | head -n 1; }
dumped_at="$(info dumped_at)"
case "$dumped_at" in
  [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z) ;;
  *) die "BACKUP_INFO has no usable dumped_at ('$dumped_at')" ;;
esac

problems=0
fail() { printf '  FAIL %s\n' "$*"; problems=$((problems + 1)); }
ok() { printf '  ok   %s\n' "$*"; }
warn() { printf '  warn %s\n' "$*"; }

find_postgres
database_exists "$SCRATCH" && die "database '$SCRATCH' already exists; drop it first (docker exec ... dropdb) or set PIGEONDROP_SCRATCH_DB"

created=0
cleanup() {
  if [ "$created" -eq 1 ] && [ "$keep" -eq 0 ]; then
    pg_run dropdb --if-exists "$SCRATCH" > /dev/null 2>&1 || echo "[$TAG] WARN could not drop '$SCRATCH', drop it by hand" >&2
  fi
}
trap cleanup EXIT

echo "snapshot of $dumped_at, dump $(info bytes) bytes, $(info tables) tables"
pg_run createdb "$SCRATCH" || die "cannot create the scratch database"
created=1

echo "loading the dump into '$SCRATCH'"
if pg_run_stdin psql -q -v ON_ERROR_STOP=1 -d "$SCRATCH" > /dev/null < "$db_dir/$DB_NAME.sql"; then
  ok "the dump loads without errors"
else
  fail "the dump did not load (psql stopped at the first error, see above)"
  echo; echo "RESTORE CHECK FAILED ($problems problem(s))"; exit 1
fi

echo "age of the dump"
old="$(pg_run psql -d "$SCRATCH" -Atc "SELECT (now() - timestamptz '$dumped_at') > interval '$MAX_AGE_HOURS hours'")"
if [ "$old" = f ]; then
  ok "dumped less than $MAX_AGE_HOURS hours ago ($dumped_at)"
elif [ "$allow_stale" -eq 1 ]; then
  warn "the dump is older than $MAX_AGE_HOURS hours ($dumped_at); allowed by --allow-stale"
else
  fail "the dump is older than $MAX_AGE_HOURS hours ($dumped_at): the dump job may have stopped"
fi

echo "rows per table (restored / held by the dump / live now)"
tables_seen=0
while IFS='=' read -r key want; do
  case "$key" in rows.*) ;; *) continue ;; esac
  table="${key#rows.}"
  case "$table" in [a-z_]*) ;; *) fail "BACKUP_INFO names a table '$table' that is not a plain name"; continue ;; esac
  case "$table" in *[!a-z0-9_]*) fail "BACKUP_INFO names a table '$table' that is not a plain name"; continue ;; esac
  tables_seen=$((tables_seen + 1))
  got="$(pg_run psql -d "$SCRATCH" -Atc "SELECT count(*) FROM public.\"$table\"" 2> /dev/null < /dev/null)"
  live="$(pg_run psql -d "$DB_NAME" -Atc "SELECT count(*) FROM public.\"$table\"" 2> /dev/null < /dev/null || true)"
  if [ "$got" = "$want" ]; then
    ok "$table: $got / $want / ${live:-n.a.}"
  else
    fail "$table: restored $got rows, the dump held $want"
  fi
done < "$db_dir/BACKUP_INFO"
[ "$tables_seen" -gt 0 ] || fail "BACKUP_INFO lists no tables, nothing to compare"

echo "recordings (files of the table messages)"
rows=0
missing=0
wrong_size=0
not_ogg=0
list="$(mktemp)"
trap 'rm -f "$list"; cleanup' EXIT
pg_run psql -d "$SCRATCH" -Atc "SELECT id, format, bytes_received FROM messages ORDER BY id" > "$list" \
  || fail "could not read the table messages"
while IFS='|' read -r id format bytes; do
  [ -n "$id" ] || continue
  rows=$((rows + 1))
  file="$data_dir/messages/$id.opus"
  if [ ! -f "$file" ]; then
    missing=$((missing + 1)); echo "       missing: $id.opus"; continue
  fi
  if [ "$(file_size "$file")" != "$bytes" ]; then
    wrong_size=$((wrong_size + 1)); echo "       wrong size: $id.opus is $(file_size "$file") bytes, the row says $bytes"
  fi
  if [ "$format" = ogg-opus ] && [ "$(head -c 4 "$file" | tr -d '\000')" != OggS ]; then
    not_ogg=$((not_ogg + 1)); echo "       not an Ogg file: $id.opus"
  fi
done < "$list"
files="$(find "$data_dir/messages" -type f -name '*.opus' | wc -l | tr -d ' ')"
if [ "$rows" -eq 0 ]; then
  warn "the table messages is empty, no file could be checked"
elif [ $((missing + wrong_size + not_ogg)) -eq 0 ]; then
  ok "all $rows recordings have their file, with the right size"
else
  fail "of $rows recordings: $missing without a file, $wrong_size with a wrong size, $not_ogg that are not Ogg"
fi
[ "$files" -le "$rows" ] || warn "$((files - rows)) file(s) have no row: uploads of the hours around the snapshot, harmless"

echo
if [ "$problems" -eq 0 ]; then
  echo "RESTORE CHECK PASSED"
else
  echo "RESTORE CHECK FAILED ($problems problem(s))"
fi
[ "$keep" -eq 0 ] || echo "the scratch database '$SCRATCH' was kept, drop it when done"
[ "$problems" -eq 0 ]
