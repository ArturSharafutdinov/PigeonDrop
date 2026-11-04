#!/usr/bin/env bash
#
# Dumps the PigeonDrop database to a plain .sql file that Backrest then snapshots into restic. Runs on the VPS, daily,
# via systemd (see the .timer beside this). The same pattern works for any other project that shares the server.
#
# Deliberately DECOUPLED from Backrest: this writes a file, Backrest backs up a directory, and neither knows about
# the other. Each half can be tested on its own, and Backrest never needs the Docker socket (which would hand anything
# that got into that container root on the host).
#
# Plain SQL, not -Fc and not gzipped, on purpose: restic compresses and deduplicates by itself, so a dump that
# changes a little from day to day costs almost nothing, while a gzip of it differs in every byte. Plain text is also
# what makes the completeness check below possible.
#
# Settings (from the environment, and what is not set there from /etc/pigeondrop-backup.env):
#   PIGEONDROP_DB_LABEL        Coolify label of the PostgreSQL instance (coolify.resourceName=postgresql-database-<uuid>)
#   PIGEONDROP_DB_CONTAINER    the container's name instead, for one without Coolify labels
#   PIGEONDROP_DB_NAME         the database inside it (default pigeondrop)
#   PIGEONDROP_BACKUP_DIR      where the dump goes (default /var/backups/pigeondrop)
#   PIGEONDROP_HEALTHCHECK_URL optional watchdog, pinged on success and at <url>/fail on any failure

set -euo pipefail

# The bug that made an earlier backup of another project useless: a step that ran `pg_dump ... | gzip > file` under
# `bash -e` WITHOUT pipefail took gzip's exit code, so a failed dump produced a 20-byte archive and a green check.
# `set -o pipefail` above is the fix, the trailer check below is the belt.

TAG=pigeondrop-db-dump
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=pigeondrop-backup-common.sh
. "$here/pigeondrop-backup-common.sh"
load_settings

OUT_DIR="${PIGEONDROP_BACKUP_DIR:-/var/backups/pigeondrop}"
DB_NAME="${PIGEONDROP_DB_NAME:-pigeondrop}"
HEALTHCHECK_URL="${PIGEONDROP_HEALTHCHECK_URL:-}"

# A dead-man's switch. A notification on failure cannot detect the failure that matters most: the job never running
# at all (a stopped timer, a powered-off server and a healthy system all produce silence). Pinging on success inverts
# that, so the ABSENCE of a ping is the alarm. It is separate from Backrest's own watchdog on purpose: Backrest would
# keep snapshotting a dump that stopped being updated weeks ago and report success every time; only a ping from THIS
# script proves the dump is fresh.
ping_healthcheck() {
  [ -n "$HEALTHCHECK_URL" ] || return 0
  command -v curl > /dev/null 2>&1 || { echo "[$TAG] WARN curl missing, cannot ping the watchdog" >&2; return 0; }
  # Never fails the run: a watchdog that can take the backup down with it is worse than none. A missed ping is itself
  # the alarm.
  curl -fsS -m 10 --retry 3 -o /dev/null "${HEALTHCHECK_URL}${1:-}" \
    || echo "[$TAG] WARN watchdog ping '${1:-/success}' failed" >&2
}

mkdir -p "$OUT_DIR"
tmp="$OUT_DIR/.${DB_NAME}.sql.tmp"
out="$OUT_DIR/${DB_NAME}.sql"

# On ANY way out other than success (a die, a failing command under set -e, a kill) the temporary file goes and the
# watchdog is told. Doing it here rather than in die() also covers the failures nobody wrote a message for.
finish() {
  local rc=$?
  rm -f "$tmp"
  [ "$rc" -eq 0 ] || ping_healthcheck /fail
  return "$rc"
}
trap finish EXIT

find_postgres

# The instance is shared: confirm the intended database is in THIS one before dumping. pg_dump would fail anyway, but
# several steps later and complaining about a connection rather than about having been pointed at the wrong server.
database_exists "$DB_NAME" || die "container ${pg:0:12} has no database named '$DB_NAME'"

log "dumping '$DB_NAME' from container ${pg:0:12}"

# --no-owner/--no-privileges: the dump restores cleanly whatever the role names are on the target, which is what you
# want at 3am during a restore.
if ! pg_run pg_dump -d "$DB_NAME" --no-owner --no-privileges > "$tmp"; then
  die "pg_dump failed"
fi

# pg_dump's plain output always ends with this line. Checking for it, rather than for a size over some threshold, tells
# a complete dump from one cut off halfway by a dropped connection or a full disk: a truncated dump is still large.
tail -n 5 "$tmp" | grep -q 'PostgreSQL database dump complete' \
  || die "dump is incomplete (the trailer is missing), refusing to replace $out"

tables=$(grep -c '^CREATE TABLE ' "$tmp" || true)
[ "$tables" -gt 0 ] || die "dump contains no CREATE TABLE statements, refusing to replace $out"

# Rows per table as the dump itself holds them (the lines of each COPY block). The restore check compares the restored
# database with THESE numbers, so it is exact; comparing with the live database would differ by whatever happened
# since the night.
rows="$(awk '
  /^COPY / { table = $2; sub(/^public\./, "", table); gsub(/"/, "", table); n = 0; inside = 1; next }
  inside && /^\\\.$/ { print "rows." table "=" n; inside = 0; next }
  inside { n++ }
' "$tmp")"

# mv within one directory is a rename(2), so it is atomic: Backrest can snapshot this directory at any moment and
# never catches a half-written dump.
mv -f "$tmp" "$out"

# Leaves evidence of freshness. Without this a job that quietly stopped would have restic faithfully snapshotting the
# same stale dump every night and nothing in the snapshot would say so; dumped_at makes staleness visible in any
# restore, without access to this machine's logs. Written after the dump so that a failure above keeps the last good
# pair together.
{
  echo "dumped_at=$(date -u +%FT%TZ)"
  echo "database=$DB_NAME"
  echo "bytes=$(file_size "$out")"
  echo "tables=$tables"
  echo "$rows"
} > "$OUT_DIR/BACKUP_INFO.tmp"
mv -f "$OUT_DIR/BACKUP_INFO.tmp" "$OUT_DIR/BACKUP_INFO"

log "wrote $out ($(file_size "$out") bytes, $tables tables)"
ping_healthcheck
