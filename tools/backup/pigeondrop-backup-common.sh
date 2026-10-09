#!/usr/bin/env bash
# Shared by pigeondrop-db-dump.sh and pigeondrop-restore-check.sh (sourced, not run): finding the PostgreSQL container
# and talking to it. The caller sets TAG (the prefix of its log lines) first.
#
# Everything goes through `docker exec` INTO the database container, never through a client on the host: the client
# version always matches the server, and the password is read from that container's own environment, so it never
# reaches this host's filesystem, a script or the process list.

# Settings come from the environment and, for whatever is not set there, from /etc/pigeondrop-backup.env
# (PIGEONDROP_ENV_FILE names another file): lines KEY=value for PIGEONDROP_* names only, with optional double quotes
# around the value. The scripts read it themselves, rather than systemd's EnvironmentFile doing it, so that a run by
# hand sees exactly what the timer sees. The file is parsed, not sourced: systemd's format does not need the quoting a
# shell does, and a URL with & or ? in it would break a `source`. It is root's file (the watchdog URL in it is a
# capability), so it is created with mode 600.
load_settings() {
  local file="${PIGEONDROP_ENV_FILE:-/etc/pigeondrop-backup.env}" key value
  [ -r "$file" ] || return 0
  while IFS='=' read -r key value; do
    case "$key" in PIGEONDROP_*) ;; *) continue ;; esac
    case "$key" in *[!A-Z0-9_]*) continue ;; esac
    value="${value%\"}"; value="${value#\"}"
    [ -n "${!key+x}" ] || export "$key=$value" # what the environment already says wins
  done < "$file"
}

log() { printf '[%s] %s %s\n' "$TAG" "$(date -u +%FT%TZ)" "$*"; }
die() { printf '[%s] ERROR %s\n' "$TAG" "$*" >&2; exit 1; }

# Identifies the Postgres INSTANCE (PIGEONDROP_DB_LABEL, or PIGEONDROP_DB_CONTAINER for a container without Coolify
# labels); PIGEONDROP_DB_NAME picks the database inside it. The instance is shared with other projects, so "which
# server" and "which database" are separate questions, and both are checked.
#
# By label, not by name: Coolify names a container after the resource UUID plus a per-deploy suffix. The label
# survives both that and a rename. Sets $pg to the container id.
find_postgres() {
  local c image matches=""
  if [ -n "${PIGEONDROP_DB_CONTAINER:-}" ]; then
    pg="$(docker ps -q --filter "name=^/${PIGEONDROP_DB_CONTAINER}$")"
    [ -n "$pg" ] || die "no running container named '${PIGEONDROP_DB_CONTAINER}'"
    return 0
  fi
  [ -n "${PIGEONDROP_DB_LABEL:-}" ] || die "neither PIGEONDROP_DB_LABEL nor PIGEONDROP_DB_CONTAINER is set (see tools/backup/README.md)"

  for c in $(docker ps -q --filter "label=$PIGEONDROP_DB_LABEL"); do
    image="$(docker inspect -f '{{.Config.Image}}' "$c")"
    case "$image" in
      postgres:* | */postgres:*) matches="$matches $c" ;;
    esac
  done

  # Refuses to guess rather than taking the first match: dumping the wrong server would still produce a large,
  # plausible-looking file.
  # shellcheck disable=SC2086 # the ids contain no spaces, splitting is the point
  set -- $matches
  [ $# -gt 0 ] || die "no running postgres container carries the label $PIGEONDROP_DB_LABEL (see tools/backup/README.md)"
  [ $# -eq 1 ] || die "$# postgres containers carry the label $PIGEONDROP_DB_LABEL, refusing to guess which holds '$DB_NAME'"
  pg="$1"
}

# pg_run <tool> [arguments]: runs a PostgreSQL client tool (psql, pg_dump, createdb...) inside the container as the
# container's own superuser. pg_run_stdin is the same with the caller's stdin passed through (for loading a dump);
# keep it out of loops that read stdin themselves, it would swallow their input.
pg_run() {
  docker exec "$pg" sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" exec "$0" -U "${POSTGRES_USER:-postgres}" -h 127.0.0.1 "$@"' "$@"
}
pg_run_stdin() {
  docker exec -i "$pg" sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" exec "$0" -U "${POSTGRES_USER:-postgres}" -h 127.0.0.1 "$@"' "$@"
}

# database_exists <name>: -A is what makes this reliable: psql's default aligned output pads every name with spaces,
# and stripping that afterwards (tr -d '[:space:]') also eats the newlines and collapses the list into one line.
database_exists() {
  pg_run psql -lqtA | cut -d'|' -f1 | grep -qx "$1"
}

# file_size <path>: bytes, the same on Linux and macOS (stat's flags differ).
file_size() { wc -c < "$1" | tr -d ' '; }
