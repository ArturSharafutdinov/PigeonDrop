# Troubleshooting

Problems that cost real time while building and running PigeonDrop: what was seen, what it turned out to mean, and what fixed it. Each entry says how sure we are, because several causes were found by elimination and not proven.

- [The watch says the phone is not reachable, but Bluetooth works](#the-watch-says-the-phone-is-not-reachable-but-bluetooth-works)
- [Every deployment fails within seconds, although the image is pushed](#every-deployment-fails-within-seconds-although-the-image-is-pushed)

## The watch says the phone is not reachable, but Bluetooth works

**What was seen** (2026-11-04, an iPhone, the Zepp app with Side Service runtime 4.3.0, a Zepp OS 4.0 watch): *Settings → Check connection* answered "Phone does not answer. Restart the Zepp app", and pairing or an upload answered "Phone is not reachable. Restart the Zepp app" (the texts are `settings.check_phone` and `link.phone` in [`PigeonDrop/i18n/en.js`](../PigeonDrop/i18n/en.js)). A few hours earlier the same app had uploaded recordings.

**What the text means.** The watch asked the phone twice (`ping`, 10 s each) and got no reply of any kind. That does not mean that Bluetooth is down: in this case the phone received every message. What was missing was an *answer from the Side Service*.

**How to tell what the phone is running.** The Zepp app has a log screen for mini programs:

1. In the Zepp app turn on Developer Mode: *Profile → Settings → About*, tap the Zepp icon seven times.
2. Tap the mini program's icon to open its log screen, choose the *Side Service* tab and press the button at the bottom right to start collecting.
3. On the watch press *Check connection*.
4. In the log find the answer to `getWorkerCode` (the Side Service code the phone loaded) and the app info next to it (`appName`, `version`, `packageInfo.timeStamp`).

In the failing case the phone loaded a **stale copy**: `appName` "Hello World", version 1.0.0, a time stamp of the project's very first template build, and a Side Service that waits for files through `transferFile` and has never heard of `ping`, `checkServer` or `uploadChunk`. The watch's messages arrived (`receiveData` in the log) and nothing answered them. The watch app itself was new: installing a preview replaced the app on the watch, but the phone kept running its old cached Side Service for the same app id.

**What did not help:**

- restarting the Zepp app, switching Bluetooth off and on, restarting the phone and the watch;
- installing the watch app again with `zeus preview`, several times, including a build with a higher version number and an older build that used to work. The phone went on loading the stale Side Service.

**What helped:** reinstalling the Zepp app on the phone completely (remove it, install it again, sign in and let the watch reconnect), then installing the watch app again with `zeus preview`. After that, *Check connection*, pairing and uploads worked again.

**What is not known.** How the stale package got back onto the phone (the app had worked a few hours before). Whether something lighter than a full reinstall would also have fixed it: deleting the mini program in Developer Mode, clearing the Zepp app's cache, signing out and in. Those were suggested, but only the full reinstall is verified. If you try them, try them before the reinstall and note the result here.

**Why the log matters.** From the watch, "the phone has an old Side Service" and "the phone is not there" look identical. Only the Side Service tab of the log tells them apart. The technical notes are in [`gotchas.md`](../.claude/skills/zeppos/references/gotchas.md).

## Every deployment fails within seconds, although the image is pushed

**What was seen:** in CI both deploy jobs (the server and the speech recognition service) stop after about ten seconds with "The deployment ended as 'failed' - see its log in Coolify". The steps before it, including *Push the image*, succeed. The old containers keep running.

**What it meant:** the GitHub Container Registry read token on the VPS had expired, so Coolify could not pull the image. [`deployment.md`](deployment.md), section *If something goes wrong*, describes this and how to read the deployment's log in Coolify (`unauthorized` / `denied` when pulling from `ghcr.io`).

**What helped:**

1. Create a new **classic** personal access token with the single scope `read:packages` and a long expiry. Write the expiry date down: when it passes, this failure comes back.
2. Check the token without Docker, from any machine (`200` means the registry accepts it):

   ```bash
   read -rs GHCR_TOKEN
   ```

   ```bash
   curl -s -o /dev/null -w '%{http_code}\n' -u "<user>:$GHCR_TOKEN" 'https://ghcr.io/token?service=ghcr.io&scope=repository:<owner>/pigeondrop-backend:pull'
   ```

3. On the VPS, as the user Coolify connects as, log in **through a pipe** and not at the password prompt:

   ```bash
   read -rs GHCR_TOKEN
   ```

   ```bash
   echo "$GHCR_TOKEN" | docker login ghcr.io -u <user> --password-stdin
   ```

   Typing or pasting the same working token at the interactive prompt kept answering `denied`. The cause was not found; a stray character from the paste is the likely one.
4. Try `docker pull ghcr.io/<owner>/pigeondrop-backend:latest` by hand, then re-run the failed deploy jobs.
