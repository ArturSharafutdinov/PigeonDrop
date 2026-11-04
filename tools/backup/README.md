# PigeonDrop backups

A daily copy of what cannot be rebuilt from code: **the `pigeondrop` database** (in the shared PostgreSQL on the VPS) and **the recordings** (the volume `pigeondrop-data`, files `messages/<id>.opus`). Both have to be copied together: a row of the `messages` table and its recording file belong together, and one without the other is useless.

The scheme has two independent halves that know nothing about each other. A systemd timer on the host runs `pg_dump` into a file. Backrest (already running in Docker on the VPS) takes a restic snapshot of that directory and of the recordings volume on a schedule, into a repository on Backblaze B2. Backrest gets no access to the Docker socket: it only reads directories. The same scheme can back up any other project that shares the VPS.

```
systemd timer (03:15 UTC)
  └─ /usr/local/bin/pigeondrop-db-dump.sh
       └─ docker exec <postgres> pg_dump  →  /var/backups/pigeondrop/pigeondrop.sql
                                              /var/backups/pigeondrop/BACKUP_INFO

Backrest, plan `b2-pigeondrop` (03:20 UTC), repository `b2`
  ├─ /backup/pigeondrop-db    ←  /var/backups/pigeondrop                      (read only)
  └─ /backup/pigeondrop-data  ←  /var/lib/docker/volumes/<volume>/_data       (read only)
```

A suggested timetable on a shared VPS (UTC):

| Time | What |
|---|---|
| 02:00 | Coolify copies its own database |
| 03:00 | another project's dump |
| 03:15 | another project's short plan (seconds) and **the PigeonDrop dump** |
| 03:20 | **the plan `b2-pigeondrop`** |
| 03:30 | the plan `vps` (the whole server) |

The plans write to one repository, and `forget`/`prune` take an exclusive lock on it, so plans must not start in the same minute. This plan runs after the other project's short one and before the big `vps` one: ten minutes separate it from `vps`, which grows with the server.

## What each script does

- **`pigeondrop-db-dump.sh`** (run by the timer) dumps the database as plain SQL without compression: restic compresses and deduplicates by itself, while gzip would change every byte every day. Before it replaces a good dump it checks that the dump is complete (the line `PostgreSQL database dump complete`; a truncated dump is still large, so the size says nothing) and that it has a `CREATE TABLE`. It writes to a temporary file and renames it (an atomic operation: Backrest never sees half a file). Next to it it puts `BACKUP_INFO`: the time of the dump and the row count of every table *as counted from the dump itself*, which the restore is checked against. It pings a watchdog on success and `<url>/fail` on any error.
- **`pigeondrop-restore-check.sh`** (by hand, after a restore) loads the dump from a snapshot into a *separate* database `pigeondrop_restore_test` (never the live one), compares the row counts with `BACKUP_INFO`, and checks the file of every recording: it exists, its size equals `bytes_received` from the row, and an Ogg file starts with `OggS`. Files without a row it only counts: the server writes the file before the row, so an upload at the moment of the snapshot leaves such a file, which is harmless.
- **`pigeondrop-backup-common.sh`** is the shared code: finding the PostgreSQL container by its Coolify label (not by name: the name has a UUID and a deployment timestamp; more than one matching container is an error, not "I will take any") and working with it through `docker exec` (the password is taken from the container's own environment and never appears on the host).

The order "the dump first, the files after" is deliberate. The server writes a recording's file before its row. If the dump is taken before the files, every row of the dump has a file, except recordings deleted in between; the other way round, rows without a file appear with any upload in between. Uploads are more frequent than deletions, so the dump comes first and five minutes before the plan.

## Installation (once, on the VPS)

The repository is the source of truth, but the files do not reach the VPS by themselves: copy them again after any edit of the scripts.

The commands below can be pasted into a terminal as they are, except in two places that have `<angle brackets>` (the server address in `scp` and the values in the settings file): replace them with your own *before* pasting. The shell takes angle brackets for redirection and answers `No such file or directory`.

### 1. Scripts and units

From your computer, from the repository root:

```bash
scp tools/backup/pigeondrop-backup-common.sh tools/backup/pigeondrop-db-dump.sh tools/backup/pigeondrop-restore-check.sh \
    tools/backup/pigeondrop-db-dump.service tools/backup/pigeondrop-db-dump.timer <user>@<vps>:/tmp/
```

On the VPS:

```bash
sudo install -m 755 /tmp/pigeondrop-backup-common.sh /tmp/pigeondrop-db-dump.sh /tmp/pigeondrop-restore-check.sh /usr/local/bin/
sudo install -m 644 /tmp/pigeondrop-db-dump.service /tmp/pigeondrop-db-dump.timer /etc/systemd/system/
sudo mkdir -p /var/backups/pigeondrop
```

### 2. Settings: `/etc/pigeondrop-backup.env`

You need the label of the shared PostgreSQL container:

```bash
docker ps --format '{{.Names}}\t{{.Image}}\t{{.Label "coolify.resourceName"}}' | grep postgres
```

You need a line like `coolify.resourceName=postgresql-database-<uuid>`. Whether this PostgreSQL has a `pigeondrop` database (created by step 3 of [`docs/deployment.md`](../../docs/deployment.md)) is checked by the first run of the dump in step 3: if there is no database it stops with the message `has no database named 'pigeondrop'`.

Create the file (mode 600; the watchdog address is a "key": whoever has it can send a successful ping and silence the alarm, so do not paste it anywhere):

```bash
sudo tee /etc/pigeondrop-backup.env > /dev/null <<'EOF'
PIGEONDROP_DB_LABEL=coolify.resourceName=postgresql-database-<uuid>
PIGEONDROP_HEALTHCHECK_URL=https://hc-ping.com/<check-uuid>
EOF
sudo chmod 600 /etc/pigeondrop-backup.env
```

Create a separate check in healthchecks.io for the dump: a period of 1 day and a grace of a couple of hours. The address may be left out, then there is simply no ping, but without it nobody notices a stopped timer.

### 3. Start and the first dump

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now pigeondrop-db-dump.timer
sudo systemctl start pigeondrop-db-dump.service && journalctl -u pigeondrop-db-dump -n 20 --no-pager
cat /var/backups/pigeondrop/BACKUP_INFO
```

`BACKUP_INFO` must have `dumped_at` and lines `rows.users=…`, `rows.messages=…` and so on. If the command says `no running postgres container carries the label`, the label in `/etc/pigeondrop-backup.env` is wrong.

### 4. Connect the directories to Backrest

You need the path of the volume with the recordings. Coolify adds the application's UUID to the volume's name (it comes out like `lysivnglgqwx3xbuhvccw2ny-pigeondrop-data`), so the path is looked up by name and nothing has to be filled in:

```bash
docker volume inspect "$(docker volume ls -q | grep 'pigeondrop-data$')" --format '{{.Name}}  {{.Mountpoint}}'
```

The answer is one line: the volume's name and a path like `/var/lib/docker/volumes/<volume-name>/_data`. If there are two lines or more (an old volume from an earlier installation) `docker volume inspect` answers with an error: pick the one attached to the running application (`docker inspect <the application's container> --format '{{range .Mounts}}{{.Name}} {{end}}'`).

Backrest is described in `/opt/backrest/docker-compose.yaml`. Add two lines to the `volumes` list of the `backrest` service, indented (with spaces, not tabs) like the neighbouring lines, and do not delete the existing ones: `/opt/backrest/restore:/restore` must stay among them, it is needed for restoring.

```bash
cd /opt/backrest && cp docker-compose.yaml docker-compose.yaml.bak
```

```yaml
      - /var/backups/pigeondrop:/backup/pigeondrop-db:ro
      - /var/lib/docker/volumes/<volume-name>/_data:/backup/pigeondrop-data:ro
```

(These are lines for the file, not commands: replace `<volume-name>` with the name from the answer above. If you do have a separate `docker-compose.override.yaml`, the same two lines can go there: Compose merges `volumes` by the path inside the container.)

Check the result (this command starts nothing): all the old mounts must stay in the list, plus the two new ones. Recreating the container interrupts a running snapshot, so do not apply it around 03:00 to 03:40 UTC:

```bash
docker compose config
docker compose up -d
docker exec backrest ls /backup/pigeondrop-db /backup/pigeondrop-data/messages
```

If `ls` answers `Permission denied` on the volume, Backrest does not run as root: it has to be given read access to the volume's directory (`/var/lib/docker/volumes` belongs to root); how depends on how Backrest was started.

### 5. The plan in Backrest

The repository `b2` exists already (its address must start with `s3:`, otherwise restic takes it for a local path). A new plan:

| Field | Value |
|---|---|
| Plan ID | `b2-pigeondrop` |
| Repo | `b2` |
| Paths | `/backup/pigeondrop-db` and `/backup/pigeondrop-data` (two paths in one plan: one snapshot holds a consistent pair) |
| Excludes | `.tmp-*` (temporary files the server writes a recording into while it is being received) |
| Schedule | cron `20 3 * * *` (UTC, like the other plans) |
| Retention | by time: daily 7, weekly 4, monthly 12 |
| Hooks | Healthchecks for this plan (a separate check: a period of 1 day and a grace of a couple of hours). A Telegram hook on the repository `b2` already covers the errors of all the plans |

Both pings are needed and neither replaces the other: the plan's ping is silent when a snapshot did not reach B2; the script's ping is silent when the dump is stale. If the timer stops, Backrest goes on night after night snapshotting the same old file and reporting success.

Run the plan by hand (**Backup now**). The **Snapshot Browser** must show `/backup/pigeondrop-db/pigeondrop.sql`, `BACKUP_INFO` and `/backup/pigeondrop-data/messages/…`.

## Checking a restore (mandatory)

A copy that was never restored is not a copy. Do it once now, again after a new migration that changes the schema, and now and then for no reason.

1. In Backrest: the plan → a snapshot → **Snapshot Browser** → the snapshot's root (both paths) → the restore icon → **Restore to path**: `/restore/pigeondrop-test-1`.
   - The directory **must not exist**: restic creates it itself and refuses otherwise (`target directory already exists`). Take a new name when repeating.
   - The path must lie inside `/restore`: it is mounted from the host (`/opt/backrest/restore`). Any other path would be inside the container and vanish when it is recreated.
2. On the VPS:

   ```bash
   sudo pigeondrop-restore-check.sh /opt/backrest/restore/pigeondrop-test-1
   ```

   The script finds the restored directories (`pigeondrop-db`, `pigeondrop-data`) by itself and reads its settings from `/etc/pigeondrop-backup.env`. It takes seconds and prints `RESTORE CHECK PASSED` or `FAILED` with the reasons at the end. It does not touch the live database: the load goes into `pigeondrop_restore_test`, which is dropped afterwards (`--keep` leaves it for a look; drop it by hand).
3. What the messages mean:

   | Message | What happened |
   |---|---|
   | `the dump is older than 36 hours` | the dump timer stopped and restic went on snapshotting an old file. `--allow-stale` is for knowingly restoring an old snapshot |
   | `the dump did not load` | the dump is damaged; `psql` stopped at the first error |
   | `restored N rows, the dump held M` | rows were lost in the restore |
   | `missing` / `wrong size` / `not an Ogg file` | the recordings volume was not copied, or not all of it |
   | `N file(s) have no row` | a warning, not an error: uploads around the moment of the snapshot |

4. Clean up what was restored: `sudo rm -rf /opt/backrest/restore/pigeondrop-test-1`.

## Restoring from scratch (after losing the server)

The order, and what to have at hand:

1. **The restic repository password** and the B2 keys. The password cannot be recovered: without it the snapshots are unreadable. It must be in a password manager together with Coolify's `APP_KEY` (`/data/coolify/source/.env`).
2. Bring up Coolify and PostgreSQL (a copy of Coolify's database is in the snapshot of the plan `vps`, `/backup/coolify`), restore the PigeonDrop application from it or create it again by [`docs/deployment.md`](../../docs/deployment.md).
3. Create the role and the database (step 3 of `docs/deployment.md`). Roles are not part of a single database's dump.
4. Restore the snapshot of the plan `b2-pigeondrop` (as above) and load the dump **as the role** `pigeondrop`, so that it owns the tables (`SET ROLE`, because the dump was made with `--no-owner`):

   ```bash
   ( echo 'SET ROLE pigeondrop;'; cat /opt/backrest/restore/<name>/backup/pigeondrop-db/pigeondrop.sql ) \
     | docker exec -i <postgres> sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -h 127.0.0.1 -d pigeondrop'
   ```

   `psql` prints one `set_config` line: that is normal. `ON_ERROR_STOP=1` is mandatory: otherwise `psql` reports success after skipping every failed command, and a half restore looks like a whole one.
5. Put the recording files into the volume `pigeondrop-data` (the `messages/` directory) and make `65532:65532`, the user the container runs as, their owner (otherwise it cannot write to them).
6. Start the application. The migrations apply by themselves (`MIGRATE_ON_START=true`) and the schema version (`goose_db_version`) comes from the dump.

## What this protects against

| Failure | How it is covered |
|---|---|
| The dump broke off (disk, connection) | the closing line and the presence of tables are checked; the good file is not replaced, the watchdog gets `/fail` |
| `pg_dump` failed | the same; any unsuccessful exit (including one with no message of its own) pings `/fail` |
| Backrest snapshots a half-written dump | the dump is written to a temporary file and renamed atomically |
| The timer stopped and nobody noticed | a watchdog ping on success: the alarm is in its absence; also `dumped_at` in `BACKUP_INFO` is visible in any restore |
| The container was recreated or renamed | it is found by Coolify's label, not by name |
| The wrong server was dumped (a shared PostgreSQL) | exactly one matching container is required, and the database `pigeondrop` must be in it, before `pg_dump` |
| A restore looks successful and there is no data | the row counts are compared with the dump and every recording's file is checked |
| A restore overwrote the live database | it goes only into `pigeondrop_restore_test`; a name equal to the live database is refused, and an existing database with that name is not overwritten |

## Good to know before a public service

- **Voice recordings are personal data.** The bucket is in `eu-central-003` (the EU) and restic encrypts on the client side, a good option for a European audience.
- **A deleted recording stays in the copies** for up to 12 months (`monthly 12`): a person pressed *Delete* and the file is still in old snapshots. That has to be described in the privacy policy or the retention for this plan shortened. The retention is the operator's decision.
- While there are few recordings the copy costs next to nothing (restic stores every file once, and recordings do not change). The server's limit is 100 MiB of recordings per account.

## What is verified and what is not

**On the VPS** (as reported by the maintainer, 2026-10-09): the scripts are installed, the dump timer and the plan `b2-pigeondrop` in Backrest are set up and a restore of a snapshot was checked. Backrest's mounts are in `/opt/backrest/docker-compose.yaml` (there is no separate override file). The output of the check and the layout of the restored folder were not seen and are not recorded in the repository.

**By a test**, `tools/backup/test/test-backup.sh`, on a real PostgreSQL 17 in Docker (the real migrations, `pg_dump`, `psql`, `docker exec`): the dump and its `BACKUP_INFO`, every kind of dump failure, the choice of the server, reading the settings from the file, a restore, every way a restore can be broken (see the table above) and loading the dump into an empty server as the application's role (step 4 of "from scratch"). The test runs in CI together with `shellcheck` (the "Backup scripts" job). The test was checked by breaking things: 21 protections in the scripts were broken one at a time (the completeness check of the dump, finding the container, comparing rows and files, protecting the live database, reading the settings and others), and each time the test failed.

**Not verified:**
- Restoring from scratch after losing the server (the section above) as a whole: loading a dump as the role (in the test) and restoring a snapshot on a live VPS were checked, but not setting up a new server.
- The retention of deleted recordings: the operator's decision (see "Good to know before a public service").

Repeat the restore check after a migration that changes the schema, and now and then for no reason.
