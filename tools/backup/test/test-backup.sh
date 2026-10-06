#!/usr/bin/env bash
# Runs pigeondrop-db-dump.sh and pigeondrop-restore-check.sh against a REAL PostgreSQL 17 in a throwaway Docker container
# (the real migrations, real pg_dump and psql, a real `docker exec`), with a fake watchdog on a local port, and checks
# every outcome that matters: the dump and what it records, each way the dump can go wrong (the last good file must
# survive and the watchdog must hear about it), and each way a restore can be broken (the check must say so).
# Needs docker, python3 (for the fake watchdog) and a few seconds to pull postgres:17 the first time.
here="$(cd "$(dirname "$0")" && pwd)"
BACKUP="$here/.."
MIGRATIONS="$here/../../../backend/migrations"
DUMP="$BACKUP/pigeondrop-db-dump.sh"
RESTORE="$BACKUP/pigeondrop-restore-check.sh"

work="$(mktemp -d)"
pgname="pd-backup-test-$$"
pg2name="pd-backup-test-$$-second"
hc_pid=""
fails=0
check() { if [ "$2" = "$3" ]; then echo "  ok   $1"; else echo "  FAIL $1 (wanted '$3', got '$2')"; fails=$((fails+1)); fi; }
listing() { find "$work/backups" -mindepth 1 -maxdepth 1 | sed 's|.*/||' | sort | tr '\n' ' '; }
contains() { case "$2" in *"$3"*) check "$1" ok ok ;; *) check "$1" "no: $(echo "$2" | tr '\n' '|' | cut -c1-300)" ok ;; esac; }

# shellcheck disable=SC2329 # runs from the EXIT trap
cleanup() {
  docker rm -f "$pgname" "$pg2name" "$pg2name-notpg" > /dev/null 2>&1; docker rmi pd-backup-test-notpg:1 > /dev/null 2>&1
  [ -z "$hc_pid" ] || kill "$hc_pid" 2> /dev/null
  rm -rf "$work"
}
trap cleanup EXIT

LABEL=coolify.resourceName=postgresql-database-test
echo "starting PostgreSQL 17"
docker run -d --name "$pgname" --label "$LABEL" -e POSTGRES_USER=pg -e POSTGRES_PASSWORD=secret \
  --tmpfs /var/lib/postgresql/data postgres:17 > /dev/null || { echo "cannot start postgres:17"; exit 1; }
sql() { docker exec -i -e PGPASSWORD=secret "$pgname" psql -U pg -h 127.0.0.1 -v ON_ERROR_STOP=1 -q -d "$1"; }
ready=0
for _ in $(seq 1 60); do
  if docker exec -e PGPASSWORD=secret "$pgname" psql -U pg -h 127.0.0.1 -d postgres -Atc 'select 1' > /dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
[ "$ready" = 1 ] || { echo "postgres did not become ready"; docker logs "$pgname" 2>&1 | tail -5; exit 1; }
echo "CREATE DATABASE pigeondrop" | sql postgres
echo "CREATE DATABASE emptydb" | sql postgres

# The real schema: the Up part of every migration.
for f in "$MIGRATIONS"/*.sql; do sed -n '/^-- +goose Up/,/^-- +goose Down/p' "$f" | sql pigeondrop || { echo "migration $f failed"; exit 1; }; done

# Some data: two users, a watch, three recordings with files, and one file nobody owns.
U1=11111111-1111-4111-8111-111111111111; U2=22222222-2222-4222-8222-222222222222
D1=33333333-3333-4333-8333-333333333333
M1=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa; M2=bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb; M3=cccccccc-cccc-4ccc-8ccc-cccccccccccc
ORPHAN=dddddddd-dddd-4ddd-8ddd-dddddddddddd
mkdir -p "$work/data/messages"
ogg() { { printf OggS; head -c "$(($2 - 4))" /dev/zero; } > "$work/data/messages/$1.opus"; }
ogg $M1 100; ogg $M2 250; ogg $ORPHAN 80
head -c 64 /dev/urandom > "$work/data/messages/$M3.opus"
sql pigeondrop <<EOF
INSERT INTO users (id, email, password_hash) VALUES ('$U1', 'a@example.com', 'x'), ('$U2', 'b@example.com', 'x');
INSERT INTO devices (id, user_id, name, token_hash) VALUES ('$D1', '$U1', 'Watch', '\x01');
INSERT INTO messages (id, user_id, device_id, client_message_id, format, bytes_received, has_wav) VALUES
  ('$M1', '$U1', '$D1', 'one', 'ogg-opus', 100, true),
  ('$M2', '$U1', '$D1', 'two', 'ogg-opus', 250, true),
  ('$M3', '$U1', '$D1', 'three', 'zepp', 64, false);
EOF

# A fake watchdog: remembers the path of every request.
port=$((39000 + RANDOM % 900))
python3 - "$port" "$work/pings" > /dev/null 2>&1 <<'EOF' &
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        open(sys.argv[2], 'a').write(self.path + '\n')
        self.send_response(200); self.end_headers(); self.wfile.write(b'OK')
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
EOF
hc_pid=$!
disown "$hc_pid" 2> /dev/null
for _ in $(seq 1 30); do curl -s -o /dev/null "http://127.0.0.1:$port/up" && break; sleep 0.2; done
: > "$work/pings"
pings() { tr '\n' ' ' < "$work/pings"; }

# A docker that behaves like the real one, except where a test asks for pg_dump to misbehave.
mkdir "$work/shim"
REAL_DOCKER="$(command -v docker)"
cat > "$work/shim/docker" <<EOF
#!/usr/bin/env bash
case "\$*" in
  *pg_dump*)
    if [ "\${SHIM_PGDUMP:-}" = fail ]; then echo "pg_dump: connection lost" >&2; exit 1; fi
    if [ "\${SHIM_PGDUMP:-}" = truncate ]; then "$REAL_DOCKER" "\$@" | head -c 6000; exit 0; fi ;;
esac
exec "$REAL_DOCKER" "\$@"
EOF
chmod +x "$work/shim/docker"

export PIGEONDROP_DB_LABEL="$LABEL" PIGEONDROP_DB_NAME=pigeondrop PIGEONDROP_BACKUP_DIR="$work/backups"
export PIGEONDROP_HEALTHCHECK_URL="http://127.0.0.1:$port/hc"
dump() { env PATH="$work/shim:$PATH" "$@" "$DUMP" 2>&1; }

echo "the dump"
out=$(dump); code=$?
check "exit code" "$code" 0
[ -s "$work/backups/pigeondrop.sql" ] && r=yes || r=no; check "pigeondrop.sql exists and is not empty" "$r" yes
contains "ends with pg_dump's trailer" "$(tail -n 5 "$work/backups/pigeondrop.sql")" "PostgreSQL database dump complete"
info="$(cat "$work/backups/BACKUP_INFO")"
contains "BACKUP_INFO says when" "$info" "dumped_at=20"
contains "BACKUP_INFO counts users as in the data" "$info" "rows.users=2"
contains "BACKUP_INFO counts devices" "$info" "rows.devices=1"
contains "BACKUP_INFO counts messages" "$info" "rows.messages=3"
contains "BACKUP_INFO counts an empty table too" "$info" "rows.sessions=0"
check "nothing but the two files is left behind" "$(listing)" "BACKUP_INFO pigeondrop.sql "
check "the watchdog got the success ping" "$(pings)" "/hc "
case "$(cat "$work/backups/pigeondrop.sql")" in *secret*) check "the password is not in the dump" leaked ok ;; *) check "the password is not in the dump" ok ok ;; esac
cksum < "$work/backups/pigeondrop.sql" > "$work/good.sum"; cp "$work/backups/BACKUP_INFO" "$work/good.info"

echo "a dump that goes wrong keeps the last good one and tells the watchdog"
: > "$work/pings"
out=$(dump SHIM_PGDUMP=truncate); code=$?
contains "truncated dump: it is cut after the tables, so only the trailer gives it away" "$(head -c 6000 "$work/backups/pigeondrop.sql")" "CREATE TABLE"
check "truncated dump: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "truncated dump: says it is incomplete" "$out" "incomplete"
check "truncated dump: the good dump is untouched" "$(cksum < "$work/backups/pigeondrop.sql")" "$(cat "$work/good.sum")"
check "truncated dump: no temporary file is left" "$(listing)" "BACKUP_INFO pigeondrop.sql "
check "truncated dump: the watchdog got /fail and no success" "$(pings)" "/hc/fail "
: > "$work/pings"
out=$(dump SHIM_PGDUMP=fail); code=$?
check "pg_dump failing: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "pg_dump failing: says so" "$out" "pg_dump failed"
check "pg_dump failing: the good dump is untouched" "$(cksum < "$work/backups/pigeondrop.sql")" "$(cat "$work/good.sum")"
check "pg_dump failing: BACKUP_INFO is untouched" "$(cat "$work/backups/BACKUP_INFO")" "$(cat "$work/good.info")"
check "pg_dump failing: the watchdog got /fail" "$(pings)" "/hc/fail "
: > "$work/pings"
out=$(dump PIGEONDROP_DB_NAME=nope); code=$?
check "wrong database: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "wrong database: names it" "$out" "no database named 'nope'"
check "wrong database: the watchdog got /fail" "$(pings)" "/hc/fail "

out=$(dump PIGEONDROP_DB_NAME=emptydb); code=$?
check "a database without tables: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "a database without tables: refuses to replace anything" "$out" "no CREATE TABLE"
ls "$work/backups/emptydb.sql" > /dev/null 2>&1 && r=written || r=none
check "a database without tables: nothing is written" "$r" none

echo "which server"
: > "$work/pings"
out=$(dump PIGEONDROP_DB_LABEL=); code=$?
check "no label and no container: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "no label and no container: points to the README" "$out" "tools/backup/README.md"
out=$(dump PIGEONDROP_DB_LABEL=coolify.resourceName=postgresql-database-other); code=$?
check "a label nothing carries: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "a label nothing carries: says so" "$out" "no running postgres container carries the label"
docker tag postgres:17 pd-backup-test-notpg:1
docker run -d --name "$pg2name-notpg" --label coolify.resourceName=postgresql-database-notpg --entrypoint sleep pd-backup-test-notpg:1 600 > /dev/null
out=$(dump PIGEONDROP_DB_LABEL=coolify.resourceName=postgresql-database-notpg); code=$?
check "a labelled container that is not PostgreSQL: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "a labelled container that is not PostgreSQL: is not taken for one" "$out" "no running postgres container carries the label"
docker rm -f "$pg2name-notpg" > /dev/null; docker rmi pd-backup-test-notpg:1 > /dev/null
docker run -d --name "$pg2name" --label "$LABEL" --entrypoint sleep postgres:17 600 > /dev/null
out=$(dump); code=$?
check "two containers with the label: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "two containers with the label: refuses to guess" "$out" "refusing to guess"
check "two containers with the label: the good dump is untouched" "$(cksum < "$work/backups/pigeondrop.sql")" "$(cat "$work/good.sum")"
out=$(dump PIGEONDROP_DB_LABEL= PIGEONDROP_DB_CONTAINER="$pgname"); code=$?
check "a container named outright works even with two labelled ones" "$code" 0
out=$(dump PIGEONDROP_DB_LABEL= PIGEONDROP_DB_CONTAINER=no-such-container); code=$?
check "a container that does not exist: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
docker rm -f "$pg2name" > /dev/null
dump > /dev/null

echo "settings from the file"
# DOCKER_HOST is in the file only to be ignored: were it taken, docker would stop working. (PATH would prove nothing, the
# environment already has it and wins.)
printf '%s\n' '# a comment' "DOCKER_HOST=tcp://127.0.0.1:1" "OTHER=ignored" "PIGEONDROP_DB_LABEL=$LABEL" \
  "PIGEONDROP_HEALTHCHECK_URL=\"http://127.0.0.1:$port/file?a=1&b=2\"" > "$work/settings.env"
: > "$work/pings"
out=$(env -u DOCKER_HOST -u PIGEONDROP_DB_LABEL -u PIGEONDROP_HEALTHCHECK_URL PIGEONDROP_ENV_FILE="$work/settings.env" "$DUMP" 2>&1); code=$?
check "the dump runs on the file alone: exit code" "$code" 0
check "the watchdog URL from the file keeps its quotes off and its query intact" "$(pings)" "/file?a=1&b=2 "
printf '%s\n' "PIGEONDROP_DB_LABEL=coolify.resourceName=postgresql-database-wrong" > "$work/wrong.env"
out=$(PIGEONDROP_ENV_FILE="$work/wrong.env" "$DUMP" 2>&1); check "what the environment says wins over the file" "$?" 0
out=$(env -u PIGEONDROP_DB_LABEL PIGEONDROP_ENV_FILE="$work/wrong.env" "$DUMP" 2>&1); code=$?
check "the file is really used when the environment is silent" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "the file is really used when the environment is silent: the label is the file's" "$out" "postgresql-database-wrong"

echo "no watchdog configured is fine"
out=$(env -u PIGEONDROP_HEALTHCHECK_URL "$DUMP" 2>&1); check "exit code" "$?" 0

# --- restoring -----------------------------------------------------------------------------------------------------
restore() { env PATH="$work/shim:$PATH" "$@" 2>&1; } # restore <env...> script args
fresh() { # a restored snapshot, laid out the way restic restores it
  rm -rf "$work/restored"; mkdir -p "$work/restored/backup/pigeondrop-db"
  cp "$work/backups/pigeondrop.sql" "$work/backups/BACKUP_INFO" "$work/restored/backup/pigeondrop-db/"
  cp -R "$work/data" "$work/restored/backup/pigeondrop-data"
}
scratch_exists() { docker exec -e PGPASSWORD=secret "$pgname" psql -U pg -h 127.0.0.1 -d postgres -Atc "select count(*) from pg_database where datname='${1:-pigeondrop_restore_test}'"; }
R="$work/restored/backup/pigeondrop-db"; F="$work/restored/backup/pigeondrop-data/messages"

echo "restoring a good snapshot"
fresh; out=$(restore "$RESTORE" "$work/restored"); code=$?
check "exit code" "$code" 0
contains "says it passed" "$out" "RESTORE CHECK PASSED"
contains "compares rows with what the dump held" "$out" "messages: 3 / 3 / 3"
contains "checks the recordings" "$out" "all 3 recordings have their file"
contains "counts the file nobody owns without failing" "$out" "1 file(s) have no row"
check "the scratch database is gone afterwards" "$(scratch_exists)" 0
fresh; mv "$work/restored/backup" "$work/flat"; mkdir "$work/flat2"; mv "$work/flat/pigeondrop-db" "$work/flat/pigeondrop-data" "$work/flat2/"
out=$(restore "$RESTORE" "$work/flat2"); check "a flat layout (no backup/ level) works too" "$?" 0
rm -rf "$work/flat" "$work/flat2"
fresh; out=$(restore "$RESTORE" --keep "$work/restored"); check "--keep: exit code" "$?" 0
check "--keep: the scratch database is still there" "$(scratch_exists)" 1
docker exec -e PGPASSWORD=secret "$pgname" dropdb -U pg -h 127.0.0.1 pigeondrop_restore_test

fresh; out=$(env -u DOCKER_HOST -u PIGEONDROP_DB_LABEL PIGEONDROP_ENV_FILE="$work/settings.env" "$RESTORE" "$work/restored"); check "the restore check reads the same file" "$?" 0

echo "a restore that is broken is caught"
fresh; rm "$F/$M2.opus"
out=$(restore "$RESTORE" "$work/restored"); code=$?
check "a missing recording: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "a missing recording: names the file" "$out" "missing: $M2.opus"
check "a missing recording: the scratch database is still gone" "$(scratch_exists)" 0
fresh; head -c 40 "$F/$M1.opus" > "$work/cut"; mv "$work/cut" "$F/$M1.opus"
out=$(restore "$RESTORE" "$work/restored"); code=$?
check "a cut-off recording: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "a cut-off recording: says the size is wrong" "$out" "wrong size: $M1.opus"
fresh; { printf 'XXXX'; head -c 96 /dev/zero; } > "$F/$M1.opus"
out=$(restore "$RESTORE" "$work/restored"); code=$?
check "an Ogg recording that is not Ogg: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "an Ogg recording that is not Ogg: says so" "$out" "not an Ogg file: $M1.opus"
fresh; sed -i.bak 's/^rows.messages=3$/rows.messages=4/' "$R/BACKUP_INFO"
out=$(restore "$RESTORE" "$work/restored"); code=$?
check "fewer rows than the dump held: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "fewer rows than the dump held: says so" "$out" "messages: restored 3 rows, the dump held 4"
fresh; sed -i.bak 's/^dumped_at=.*/dumped_at=2020-01-01T00:00:00Z/' "$R/BACKUP_INFO"
out=$(restore "$RESTORE" "$work/restored"); code=$?
check "a stale dump: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "a stale dump: says the job may have stopped" "$out" "the dump job may have stopped"
out=$(restore "$RESTORE" --allow-stale "$work/restored"); check "--allow-stale: exit code" "$?" 0
contains "--allow-stale: still says so, as a warning" "$out" "allowed by --allow-stale"
fresh
python3 - "$R/pigeondrop.sql" <<'EOF'
import sys
p = sys.argv[1]
s = open(p).read()
i = s.index('CREATE TABLE public.messages')
open(p, 'w').write(s[:i] + 'THIS IS NOT SQL;\n' + s[i:])
EOF
out=$(restore "$RESTORE" "$work/restored"); code=$?
check "a damaged dump: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "a damaged dump: says it did not load" "$out" "the dump did not load"
check "a damaged dump: the scratch database is still gone" "$(scratch_exists)" 0
fresh; rm -rf "$work/restored/backup/pigeondrop-data"
out=$(restore "$RESTORE" "$work/restored"); code=$?
check "a snapshot without the recordings volume: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "a snapshot without the recordings volume: says so" "$out" "was the recordings volume part of the snapshot"
fresh; rm -rf "$work/restored/backup/pigeondrop-db"
out=$(restore "$RESTORE" "$work/restored"); code=$?
check "a snapshot without the dump: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "a snapshot without the dump: says so" "$out" "was the dump directory part of the snapshot"

echo "it never touches the live database"
fresh
out=$(restore PIGEONDROP_SCRATCH_DB=pigeondrop "$RESTORE" "$work/restored"); code=$?
check "scratch = live: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "scratch = live: says so" "$out" "must not be the live one"
docker exec -e PGPASSWORD=secret "$pgname" createdb -U pg -h 127.0.0.1 pigeondrop_restore_test
out=$(restore "$RESTORE" "$work/restored"); code=$?
check "a scratch database that already exists: exit code is not 0" "$([ "$code" -ne 0 ] && echo yes)" yes
contains "a scratch database that already exists: refuses before creating anything" "$out" "drop it first"
check "a scratch database that already exists: it is left alone" "$(scratch_exists)" 1
docker exec -e PGPASSWORD=secret "$pgname" dropdb -U pg -h 127.0.0.1 pigeondrop_restore_test
check "the live database still has its rows" "$(docker exec -e PGPASSWORD=secret "$pgname" psql -U pg -h 127.0.0.1 -d pigeondrop -Atc 'select count(*) from messages')" 3

echo "loading the dump onto an empty server, the way README.md describes (as the application's role, via SET ROLE)"
echo "CREATE ROLE pd_app LOGIN PASSWORD 'apppw'; CREATE DATABASE pd_fresh OWNER pd_app" | sql postgres
( echo 'SET ROLE pd_app;'; cat "$work/backups/pigeondrop.sql" ) | sql pd_fresh
check "the application's role owns the restored tables" "$(echo "SELECT tableowner FROM pg_tables WHERE tablename = 'messages'" | docker exec -i -e PGPASSWORD=secret "$pgname" psql -U pg -h 127.0.0.1 -d pd_fresh -Atq)" pd_app
check "and can use them with its own password" "$(docker exec -e PGPASSWORD=apppw "$pgname" psql -U pd_app -h 127.0.0.1 -d pd_fresh -Atc 'select count(*) from messages')" 3

echo; [ "$fails" = 0 ] && echo "ALL CHECKS PASS" || echo "$fails FAILURE(S)"
exit "$fails"
