# Deploying the server on a VPS with Coolify

The Go server in `backend/` is built into a Docker image **in GitHub Actions**; the image is pushed to GitHub's container registry (GHCR), and Coolify on your VPS only pulls the ready image and runs it. That way the Go build never competes for a shared VPS. The watch app is not deployed like this: it is published through Zepp.

This is the setup the author runs in production. Anything with a different shape (Kubernetes, plain Docker Compose) can run the same two images; the interesting parts of the configuration are the environment variables and the limits below. The addresses and names in angle brackets are yours: `<owner>` is the GitHub account that owns the repository, `<your-subdomain>` the public address of the server.

## How it works

```mermaid
flowchart TD
    push["git push to main<br/>(backend/ or .github/scripts/ differs from what is deployed)"] --> ci["GitHub Actions: .github/workflows/ci.yml"]
    ci --> checks["checks: backend, deploy-script, ..."]
    checks --> deploy["deploy (only if the checks passed)"]
    deploy --> s1["1. compare the commit with what is deployed (the :deployed tag)"]
    s1 --> s2["2. build backend/Dockerfile, push to GHCR:<br/>ghcr.io/&lt;owner&gt;/pigeondrop-backend:&lt;commit&gt; and :latest"]
    s2 --> s3["3. ask Coolify to deploy and wait for the result<br/>(Coolify pulls :latest and restarts the container)"]
    s3 --> s4["4. once Coolify confirms, tag the image :deployed"]
```

- Whether a deployment is needed is decided by comparing with the **deployed commit**, not with the previous push (`.github/scripts/image-changed.sh`). An image's commit is in its label `org.opencontainers.image.revision`, and the `:deployed` tag is set only after Coolify confirmed a deployment. So nothing is lost: if a run was cancelled (two merges seconds apart: GitHub keeps only the newest of the queued runs) or a deployment failed, the next push, even a documentation-only one, deploys that too. A push after which `backend/` and `.github/scripts/` match what is deployed neither rebuilds the image nor restarts the server. If it cannot be found out what is deployed (no tag yet, no label) the workflow deploys. A manual run ("Run workflow") always deploys.
- The image is pushed to GHCR **even before the Coolify secrets exist**: the job warns and finishes successfully. The first image appears by itself, before the application is created in Coolify.
- A deployment counts as **failed** if Coolify says so: the job turns red.
- The commit tag is for rolling back and for knowing what runs: `pigeondrop version` prints that commit.
- Without the Coolify secrets the `:deployed` tag is not set (nothing was deployed), so until Coolify is configured every push to `main` rebuilds the image.
- A second, identical chain (`deploy-stt`) deploys the speech recognition service from `stt/`: its own image `pigeondrop-stt`, its own Coolify application, its own `:deployed` tag. Step 9 sets it up.
- Both deploy jobs run only in the repository they were written for (`github.repository`), so a fork that pushes to its own `main` neither builds images nor looks for Coolify.

## One-time setup

### 1. The image reaches GHCR

Merge a pull request that changes `.github/scripts/` (or run the workflow by hand: **Actions → CI/CD → Run workflow**). The `deploy` job builds the image and pushes it to GHCR; the missing Coolify secrets are fine at this point. **Packages** on the repository page will list `pigeondrop-backend`. Keep the package **private**.

### 2. The VPS can read the image

Coolify pulls the private image with the Docker credentials of the server itself. Once, over SSH:

1. On GitHub: **Settings → Developer settings → Personal access tokens → Tokens (classic) → Generate new token**, with the single scope **`read:packages`**. The token is shown once. **Give it a long expiry or none, and write the expiry date down: when it runs out, every deployment fails within seconds** (see *If something goes wrong*).
2. On the VPS: `docker login ghcr.io -u <your-github-user>` and paste the token at the password prompt.

### 3. PostgreSQL: its own database and role

Create a separate role and database for PigeonDrop so that the server cannot touch other projects' databases.

1. In Coolify open the PostgreSQL resource, its **Terminal** tab, and run `psql -U <user>` (the user and password are on the database's page, usually `postgres`).
2. Make up a password, for example with `openssl rand -hex 24`. Letters and digits only: the password goes inside a URL and special characters would have to be escaped.
3. Run:
   ```sql
   CREATE ROLE pigeondrop LOGIN PASSWORD '<password>';
   CREATE DATABASE pigeondrop OWNER pigeondrop;
   ```
   Nothing else is needed: the server needs no superuser rights and no extensions; the migrations create only tables and indexes in their own database.
4. The internal address: the PostgreSQL page in Coolify shows a **Postgres URL (internal)** like `postgres://postgres:…@<container-name>:5432/postgres`. You need `<container-name>`; the application's address is
   ```
   postgres://pigeondrop:<password>@<container-name>:5432/pigeondrop?sslmode=disable
   ```
   `sslmode=disable` because the traffic stays inside Docker's internal network. The application and the database must be on the same Docker network (in Coolify that is so by default when both are on one server and in the same *destination*).

### 4. Coolify: create the application

1. **Project → New → Resource → Docker Image**.
2. **Image** `ghcr.io/<owner>/pigeondrop-backend`, **Tag** `latest`.
3. **Ports Exposes** `3000`. Leave **Ports Mappings empty**: the server must be reachable only through Coolify's proxy (Traefik), because trust in the `X-Forwarded-For` header is switched on below.
4. **Domains** `https://<your-subdomain>`. The DNS provider needs an A record for the subdomain pointing at the VPS. Traefik gets the certificate by itself.
5. **Environment Variables** (all ordinary ones, not "build variables"):

   | Name | Value |
   |---|---|
   | `DATABASE_URL` | the address from step 3 |
   | `DEFAULT_SERVER_URL` | `https://<your-subdomain>` with no trailing slash. This is the public address: the pairing QR code's link is built from it |
   | `TRUST_PROXY_HEADERS` | `true` (the client's address for the sign-in rate limit is read from `X-Forwarded-For`; correct as long as no port is reachable directly from outside) |
   | `MIGRATE_ON_START` | `true` (a single copy of the application; migrations are applied at start) |

   Everything else has a default: cookies over HTTPS only (`COOKIE_SECURE=true`), 100 MiB of storage per account (`STORAGE_ACCOUNT_BYTES`), a pool of 10 database connections (`MAX_DB_CONNS`; the PostgreSQL may be shared with other projects, lower it if needed). The full list is in [`backend/README.md`](../backend/README.md).
6. **Persistent Storage → Add → Volume Mount**: name `pigeondrop-data`, destination path `/data`. A **Volume**, not a Directory: the container does not run as root and can write only to what Docker created from the image. The recordings themselves are in `/data`; without the volume they vanish with the first deployment.
7. **Resource Limits** (the Configuration tab; values with units `b`, `k`, `m` or `g`, `0` meaning unlimited):

   | Field | Value |
   |---|---|
   | CPU limit | `1` |
   | CPU set | empty |
   | CPU weight | `1024` (the default) |
   | Memory reservation | `128m` |
   | Memory limit | `512m` |
   | Memory and swap limit | `512m` (the same as the memory limit: the container does not swap, and if it runs out of memory it is restarted, which does not slow the whole VPS down) |
   | Swappiness | `60` (the default; it has no effect without swap) |

   Where the numbers come from, measured on a real container: at rest the server takes about 30 MiB. Under a burst of 32 simultaneous sign-ins (each Argon2 hash takes 19 MiB) and 32 simultaneous playbacks a container limited to 128 MiB was killed for lack of memory; one limited to 256 MiB survived five such bursts in a row and afterwards held about 50 MiB. Real use is nowhere near this, so 512 MiB is a doubled margin. The limit is a ceiling only; the server takes what it needs. One CPU is plenty: a sign-in (Argon2) takes a fraction of a second and converting a one-minute recording about 30 ms.
8. **Do not enable the health check in Coolify.** The image has no `curl`, so such a check would always find the container unhealthy. The image defines its own check (`/pigeondrop healthcheck`).
9. Press **Deploy** and check:
   ```bash
   curl https://<your-subdomain>/healthz
   ```
   The answer is `{"ok":true}`. The application's log (the Logs tab) must have a line `migrations applied` with `count` equal to the number of migrations in `backend/migrations/`.

### 5. Coolify: an API token

1. **Settings → Advanced → API Access**: enable it if it is off.
2. **Keys & Tokens → API tokens**: create a token with the `read` and `deploy` permissions (nothing more). The token is shown once.
3. The application's UUID is in the address bar on its page (`.../application/<UUID>`).

### 6. GitHub: secrets

Repository → **Settings → Secrets and variables → Actions → New repository secret**, three secrets:

| Name | Value |
|---|---|
| `COOLIFY_URL` | the address of your Coolify, for example `https://coolify.example.com` |
| `COOLIFY_TOKEN` | the token from step 5 |
| `COOLIFY_APP_UUID` | the application's UUID |

Keep the token only there.

### 7. The first user

Registration is by invitation only, so the first invitation is created by the operator on the server. Over SSH on the VPS:

```bash
docker ps --filter ancestor=ghcr.io/<owner>/pigeondrop-backend:latest --format '{{.ID}}  {{.Names}}  {{.Status}}'
docker exec <ID> /pigeondrop invite create -note owner
```

The first command finds the current container by its image (the name changes with every deployment, so an ID from an old note will not do); its status must read `Up … (healthy)`. The image has no shell: run `/pigeondrop` directly, not `sh`.

The command prints the code on the first line and, on the second (stderr), when it expires: in 7 days. Open `https://<your-subdomain>/pair`, the *Register* tab, and enter the code, an e-mail address and a password of at least 12 characters. More invitations are created the same way; `invite list` and the other operator commands (`user set-password`) are in `backend/README.md`.

### 8. Point the watch at the new server

1. In `PigeonDrop/config.js` set `SERVER_URL = 'https://<your-subdomain>'`.
2. `cd PigeonDrop && zeus preview`.
3. On the watch: **Settings → Connection → Pair via QR code**, open the QR code on the phone (HTTPS now), sign in and approve. A pairing made with a test server on your Mac is not valid on the new server: it is a different database, so the watch is paired again.

### 9. Speech recognition: the second application

Do this after steps 1 to 8, when the server already runs. The model lives in a container of its own (`stt/`, image `ghcr.io/<owner>/pigeondrop-stt`): it does not fit in the server's 512 MiB. The server reaches it over Docker's internal network; the service must not be exposed. While the server's `STT_URL` is empty nothing changes: recordings are not queued and no worker starts.

The order matters: first the image and the application, then connecting the server.

1. **The image.** Merging a pull request with this pipeline triggers the `deploy-stt` job: it builds the image (about 5 to 6 minutes, the 640 MB model is downloaded during the build), runs it with the production limits and pushes it to GHCR as `pigeondrop-stt` (a private package). There is no Coolify secret for it yet, which is fine: the deployment step only warns. Check over SSH as the user Coolify connects as: `docker pull ghcr.io/<owner>/pigeondrop-stt:latest`. The `read:packages` token from step 2 works for the new package too.
2. **The service token.** Make up a secret shared by the two applications, for example `openssl rand -hex 32`. It is needed in two places (steps 3 and 5). The service refuses to start with a token shorter than 16 characters.
3. **Coolify: create the application.** The same project and server, and the same network (**destination**) as the server's application. **New → Docker Image**:

   | Field | Value |
   |---|---|
   | Image / Tag | `ghcr.io/<owner>/pigeondrop-stt` / `latest` |
   | Ports Exposes | `8000`; Ports Mappings empty |
   | **Domains** | **empty.** Coolify may fill in a generated address (sslip.io): delete it. The service must be reachable only over the internal network |
   | Environment Variables | `STT_TOKEN` = the token from step 2 (an ordinary variable, not a "build variable"). Nothing else: the model's thread count comes from the CPU limit |
   | Network → Network Aliases | `pigeondrop-stt`. Coolify renames the container with every deployment, but the alias stays: it is how the server finds the service |
   | Persistent Storage | not needed: the model is inside the image and the service writes nothing to disk |
   | CPU limit | `2` (a ceiling, not a reservation: idle, the container takes nothing) |
   | CPU set | empty |
   | CPU weight | `1024` |
   | Memory reservation | `1g` |
   | Memory limit | `3g` |
   | Memory and swap limit | `3g` (the same: no swap) |

   Where the numbers come from (measured on a production-sized VPS, see [`docs/research/stt-comparison.md`](research/stt-comparison.md)): the model holds up to 2.07 GiB of its own memory and about 0.7 GiB of file cache; with 2 GB the container is killed. With two cores a one-minute recording is processed in 12 seconds (21 on one core), an ordinary recording in a second or two. **Do not enable the health check in Coolify.** The image defines its own check; and as far as is known an enabled check makes Coolify start the new container before stopping the old one, so for the switchover memory for two containers is needed (up to 6 GB). How your Coolify behaves was not tried.

   Press **Deploy**. After 10 to 15 seconds the log shows the line `the model is loaded`.
4. **GitHub: the secret `COOLIFY_STT_APP_UUID`.** The UUID is in the address bar of the application's page in Coolify; add the secret where the others are (step 6). Then run **Actions → CI/CD → Run workflow** once: the image gets its `:deployed` tag, and from then on the service is deployed by itself whenever `stt/` changes. Until then every push to `main` rebuilds the service's image (minutes) and deploys nothing.
5. **Connect the server.** In the server's application (not the new one) add `STT_URL` = `http://pigeondrop-stt:8000` and `STT_TOKEN` = the same token. Leave the other settings (`STT_TIMEOUT` 90 s, `STT_MAX_ATTEMPTS` 5, `STT_MAX_AUDIO_SECONDS` 65) at their defaults; they are in `backend/README.md`. Redeploy. The server's log shows `speech recognition is on`.
6. **Check the connection.** Find the server's current container as in step 7 and run in it:
   ```bash
   docker exec <ID> /pigeondrop stt check
   ```
   The command sends the service a second of silence with the same address and token the server uses and prints `ok: …` or the reason. It does not touch the database. Typical answers:

   | Answer | Meaning |
   |---|---|
   | `ok: the service at … answered in …` | all is well |
   | `HTTP 401` | the two applications' tokens differ (or there is a stray space) |
   | `no such host` | the alias did not apply or the applications are on different Docker networks: check step 3 and that both have the same destination |
   | `connection refused`, `i/o timeout` | the service is not running or is still loading the model (the first 15 seconds) |
   | `HTTP 503`, `loading` | the model is still loading, try again in a few seconds |

7. **Try it.** Record a phrase on the watch. A few seconds after the upload the text appears under the recording on the `/pair` page. The state of the queue: `docker exec <ID> /pigeondrop stt status`.
8. **Old recordings (optional).** Recordings accepted before recognition was switched on are not transcribed by themselves. To queue them: `docker exec <ID> /pigeondrop stt enqueue -missing` (then `stt status` shows how many are left). An ordinary recording takes one or two seconds, so a hundred take a few minutes.

**Switching recognition off:** remove `STT_URL` from the server and restart it. New recordings are not queued and no worker starts; finished texts stay visible, waiting ones stay in the table and resume when it is switched on again. The service's application can be stopped.

**Good to know.**
- Restarting the service is safe: while it is unavailable recordings wait and attempts are not used up (the server pauses 15 seconds, 30, … up to 5 minutes). Recordings are not lost when the server itself restarts either.
- The service always holds the model's memory: up to 2.8 GiB of the 3 GB limit. Watch the other applications after switching it on.
- A backup window may slow recognition down, but does not break it.
- Rolling the service's image back: set **Tag** to `<commit>` in its application and press Deploy, as for the server.

### 10. The Telegram bot (optional)

The recordings of a linked account arrive in a private Telegram chat as a voice message with the transcript as its caption. The bot runs inside the server (no separate application in Coolify) and fetches updates from Telegram itself (long polling), so it needs no public address. The server only needs to be able to open `https://api.telegram.org` (outgoing HTTPS); some networks block it, which step 3 checks.

1. **Create the bot.** In Telegram open @BotFather, `/newbot`, choose a name and a username (ending in `bot`). BotFather gives a token, needed only in step 2. It is a good idea to run `/setjoingroups` and choose Disable now: the bot works only in private messages. The bot sets its own command menu.
2. **The variable.** In the server's application add `TELEGRAM_BOT_TOKEN` and redeploy. The log shows `telegram bot is on` (with the bot's name) and `telegram is on`. Leave `TELEGRAM_WAIT` (how long a recording waits for its transcript, 3 minutes) and `TELEGRAM_TIMEOUT` (30 seconds) at their defaults; they are in `backend/README.md`.
3. **Check the connection.** Find the current container as in step 7 and run in it:
   ```bash
   docker exec <ID> /pigeondrop telegram check
   ```
   The command asks Telegram who the bot is and whether a webhook is set; it does not touch the database and sends nothing to anyone. Typical answers:

   | Answer | Meaning |
   |---|---|
   | `ok: the bot @… answers` | all is well |
   | `unavailable (401)`, `Unauthorized` | the wrong token (a stray space, or the bot was deleted in BotFather) |
   | `cannot be used`, `i/o timeout`, `no such host` | the server cannot reach `api.telegram.org`: DNS or outgoing HTTPS is closed |
   | `has a webhook set` | this bot has a webhook, so Telegram does not hand updates to the server; remove it (the Bot API method `deleteWebhook`) or use another bot |

4. **Link a chat.** Open `/pair`, sign in, press *Link Telegram* in the Telegram section and open the link in Telegram (preferably on the phone), then press Start. The bot answers "Done", and a few seconds later the page shows "Recordings arrive in Telegram: @name". The link is valid for 10 minutes and for one use.
5. **Try it.** Record a phrase on the watch. A few seconds after the upload (about 10 to 20 seconds with transcription) the chat shows a voice message with the text as its caption. The state of the queue: `docker exec <ID> /pigeondrop telegram status` (how many accounts are linked, how many recordings wait, what went wrong). Recordings accepted before the linking are not sent.
6. **Switching off.** A person unlinks themselves on the page (*Unlink*) or with `/stop` in the chat. For everyone at once: remove `TELEGRAM_BOT_TOKEN` and restart; the section disappears from the page and no recordings are queued; waiting ones stay in the table and go out when it is switched on again (if the linking is intact).

**Good to know.**
- **The voice and the text go to Telegram's servers.** The person does that themselves by linking a chat, and the page says so. A privacy policy for a public service has to say it too.
- **One token, one process.** Two processes with one token cannot receive updates at the same time (Telegram answers 409). While the old container is still alive during a deployment the new bot waits 15 seconds and retries; the log has one warning per failure. Linking during a deployment can take a few extra seconds.
- **A duplicate is possible.** If the request reached Telegram but the answer did not come back (a drop, a timeout), the recording is retried on schedule and the chat can end up with two identical messages: Telegram has no idempotency key.
- **The chat blocked the bot.** The link is switched off, the page says so, and recordings are not sent until the person links a chat again.

## Everyday operation

- **Deployment** is automatic: a push to `main` that touches `backend/` builds the image and restarts the application; the same for the speech service when `stt/` changes. Migrations are applied at start.
- **Rolling back:** in Coolify open the application, set **Tag** to the commit you want (`ghcr.io/<owner>/…-backend:<commit>`, the tags are on the package's page on GitHub) and press **Deploy**. Setting it back to `latest` works the same way. The pipeline does not know about a manual rollback: the `:deployed` tag stays on the last commit it deployed itself. Rolling the code back does not roll the migrations back: the older version has to work with the already updated schema.
- **Logs:** in Coolify (the Logs tab), one JSON line per event.
- **One copy.** Do not run two copies of the application: the rate limiters live in the process's memory and the migrations at start assume one process.
- **Backups.** The recordings on disk and their rows in the database belong together, so both must be copied: the volume `pigeondrop-data` and the database `pigeondrop` (`pg_dump`). The scripts and the Backrest plan in [`tools/backup`](../tools/backup/README.md) do this (installation, schedule, restore check).
- **Time:** the container runs in UTC.
- **Security:** registration is by invitation; passwords are stored as Argon2id; device tokens are stored only as hashes and revoked on the `/pair` page. The GitHub token with `read:packages` and the Coolify token live only on the server and in GitHub's secrets.

## If something goes wrong

**Every deployment fails within about ten seconds, for both applications at once.** The problem is most likely not the code but the pull: the Coolify API answers `failed` at once and the job logs *"The deployment ended as 'failed' - see its log in Coolify"*. Open the deployment's log in Coolify; if it says `unauthorized`/`denied` when pulling from `ghcr.io`, the **GHCR read token on the VPS has expired** (personal access tokens can have an expiry date). Create a new classic token with only `read:packages`, run `docker login ghcr.io` again as the user Coolify connects as (see the next item), and re-run the workflow. The old containers keep running meanwhile; a failed deployment does not stop them.

**Coolify: `error from registry: unauthorized` when pulling the image.** Coolify has no working credentials for `ghcr.io`. The pull runs inside a Coolify helper container that is given `~/.docker/config.json` of the user Coolify connects to the server as (**Servers → localhost → User**, usually `root`). Check it as that user (the tokens are hidden in the output):
```bash
jq 'del(.auths[]?.auth)' ~/.docker/config.json
```
It must read `{ "auths": { "ghcr.io": {} } }`. If `ghcr.io` is missing, repeat `docker login ghcr.io -u <your-github-user>` as the same user. If there is a `credsStore` or `credHelpers`, Docker keeps the password in a system store, not in the file, and Coolify cannot see it. Try the pull by hand: `docker pull ghcr.io/<owner>/pigeondrop-backend:latest`. If that also says `unauthorized`, the token is not a classic one, has no `read:packages`, has expired, or the user name was mistyped.

**The container is in the `Restarting` state.** The server exits with an error at start; the reason is in its log: `docker logs --tail 30 <ID>` (or the Logs tab in Coolify). Most often:

| In the log | Cause and fix |
|---|---|
| `configuration:` and a line like `DATABASE_URL is required` | The variable is missing or misspelt, or the value still has `<` `>` or spaces in it |
| `password authentication failed for user "pigeondrop"` | The password in `DATABASE_URL` does not match the role's password, or the role does not exist (PostgreSQL answers the same). In the PostgreSQL terminal: `\du`, then `ALTER ROLE pigeondrop WITH LOGIN PASSWORD '…'` (or `CREATE ROLE …`) and the same password in `DATABASE_URL`. Letters and digits only, or the address is parsed wrongly |
| `database "pigeondrop" does not exist` | The `CREATE DATABASE` of step 3 was not run |
| `no such host`, `connection refused`, `i/o timeout` | The wrong PostgreSQL container name in `DATABASE_URL`, or the application and the database are on different Docker networks |
| `permission denied` with a path `/data/…` | The storage is attached as a host folder (Directory); it must be a Volume Mount |

Database access can be tried before restarting, from a separate container:
```bash
docker run --rm --network coolify postgres:17 psql "postgres://pigeondrop:PASSWORD@DB-CONTAINER-NAME:5432/pigeondrop?sslmode=disable" -c "select 1"
```

**`docker exec` says "no such container" or "container is restarting".** You are addressing an old container: every deployment creates a new one with a new name. Find the current one with the command of step 7. If it is `Restarting`, see the previous item.

## What is verified and what is not

Verified on real GitHub Actions and Coolify (2026-10-04):
- The `deploy` job builds the image, pushes it to GHCR (the commit tag and `latest`), calls the Coolify API, waits for the result and prints `Deployed.`.
- Coolify pulls the private image (after `docker login` on the server), the application starts, applies the migrations and serves `/healthz` and `/pair` over HTTPS with a valid certificate.
- Registration by invitation, pairing a real watch, uploading a recording and playing it in the browser.

Verified only locally:
- The image in Docker: not run as root, on a clean volume, migrations, registration, pairing and upload, data surviving a restart, the health check passing; the memory ceilings (128 MiB is killed under a burst, 256 MiB survives). The image has also been started on an empty PostgreSQL 17 and on a database already migrated by the previous version.
- `.github/scripts/coolify-deploy.sh` was run against a fake Coolify API in every outcome (failure, cancellation, an old Coolify with `GET`, a wrong token, a hang, no secrets, Coolify unreachable); these checks run in CI too (the `deploy-script` job).

The Telegram bot (step 10): the whole chain (linking, queue, voice with text) was tried on a real server, the real model and real recordings, against a fake Bot API and **against the real Telegram** (2026-10-11); Telegram accepted the Ogg/Opus packing as a voice message with the right duration and caption. Whether `api.telegram.org` is reachable from a given VPS is not known in advance: `telegram check` shows it.

Not verified: a rollback by changing the tag in Coolify, and the behaviour with two deployments at once. The second application (step 9): the image is built and tried in CI, and `stt check` was tried against a real container with the right and a wrong token and with the container stopped; the deployment in Coolify itself, the *Network Aliases* field, the name `pigeondrop-stt` from the server's container and how Coolify behaves with two applications on one network were first tried on the author's Coolify. Backups (`tools/backup`) are installed and a restore was checked by the author.

## Checks locally

```bash
cd backend && make check                     # gofmt, vet, tests, generated code (needs PostgreSQL: scripts/dev-db.sh up)
docker build -t pigeondrop-backend backend   # the image builds
.github/scripts/test/test-coolify-deploy.sh  # the deploy script against a fake Coolify
```
