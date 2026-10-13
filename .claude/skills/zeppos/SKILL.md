---
name: zeppos
description: Zepp OS (Amazfit/Zepp smartwatch) Mini Program development - device app, Side Service, Settings App, app.json, the zeus CLI, the simulator, `@zos/*` APIs (ui, fs, ble, media, sensor, router, display, storage, ...), MessageBuilder, API_LEVEL, round-screen layout and real-device quirks. Use it before writing or changing any code that imports `@zos/*`, touches app.json, `app-side/`, `setting/` or `page/` of a Zepp OS app, or when a watch behaves differently from what the docs say.
---

# Zepp OS development

The official documentation, the official samples and the API declarations are not in your memory with enough precision, and the
firmware differs from them in places. Look things up instead of recalling them, and treat `references/gotchas.md` as the part
that is not in the documentation at all.

## Set up the sources (once per session)

```bash
.claude/skills/zeppos/scripts/setup.sh      # path relative to the repository that holds the skill
```

It takes seconds, prints four paths and is safe to repeat. Use them as `$DOCS`, `$SAMPLES`, `$DTS`, `$API_INDEX` below:

| Variable | What it is |
|---|---|
| `DOCS` | `docs/` of `zepp-health/zeppos-docs` (Markdown/MDX, English), the official documentation, current API level only |
| `SAMPLES` | `zepp-health/zeppos-samples`: complete small apps per API level (`application/4.0/...`), watchfaces, workout extensions |
| `DTS` | `index.d.ts` of `@zeppos/device-types`: TypeScript declarations of the device API with API_LEVEL tags |
| `API_INDEX` | one line per export of `DTS` with signature, description, API_LEVEL and the line in `DTS` |

`setup.sh --update` pulls newer documentation and samples. No network: set `ZEPPOS_DOCS_DIR`, `ZEPPOS_SAMPLES_DIR`,
`ZEPPOS_DEVICE_TYPES` to existing copies (see the header of the script).

## How to look something up

1. **"Does this function exist, what is its signature, since which API_LEVEL?"** `grep -n 'name' $API_INDEX`, then read the
   declaration: `sed -n 'FROM,TOPp' $DTS` (the index prints the lines; for a function, `types L<n>` points to its `Option` and
   `Result` types). The declarations are the most exact source for names and types.
2. **"How does it behave?"** The reference page is `$DOCS/reference/device-app-api/newAPI/<module>/<name>.mdx` (module without
   `@zos/`, e.g. `fs/openSync.mdx`; widgets in `ui/widget/<WIDGET>.mdx`). `references/docs-map.md` maps topics to guides.
3. **"How is it done in a real app?"** `grep -rn 'name' $SAMPLES/application/4.0` (use the API level the app targets;
   `ls $SAMPLES/application`). Prefer a sample over inventing a usage.
4. **Not found in the declarations?** The declarations lag behind the firmware: `@zos/media` (Recorder, Player) and `@zos/crypto`
   are documented but not declared in `@zeppos/device-types` 4.0.0. Search `$DOCS` before concluding that something does not exist.
5. **Documentation and device disagree?** The device wins. Check `references/gotchas.md` first; when you find a new
   difference, add it there (what you did, what happened, on what device and API level) so it is not found twice.

## Orientation

A Mini Program is up to four separately running parts, configured in `app.json` (`configVersion: v3`):

- **Device app** - JavaScript on the watch: `app.js` (`App({...})`) and pages (`Page({...})`, one file per page, listed in
  `targets.<name>.module.page.pages`). UI is built imperatively with `createWidget(widget.X, {...})` from `@zos/ui` and
  placed in pixels. Files are the app's private `/data` (`@zos/fs`); there is no browser, DOM or Node.
- **Side Service** (`app-side/index.js`, `AppSideService({...})`) - runs on the phone inside the Zepp App, no UI, no reliable
  lifecycle or storage. It is the only part with network access (`fetch`). Talks to the device app over Bluetooth.
- **Settings App** (`setting/index.js`) - a UI inside the Zepp App built with its own widget set (`reference/app-settings-api`).
- **App Service / widgets** (`AppService`, `SecondaryWidget`, `AppWidget`) - background and shortcut-card parts, not used by every app.

Device app <-> Side Service messages go through BLE, whose raw API is binary and size-limited; apps wrap it in
`MessageBuilder`, a library vendored into the app as `shared/message.js` and `shared/message-side.js` (not an npm package; the
copy to start from is `$SAMPLES/application/2.0/todo-list/shared/`; usage in
`$DOCS/guides/best-practice/bluetooth-communication.mdx`). Large payloads must be sent in pieces (see gotchas).

Things that decide whether code works at all:

- **API_LEVEL.** Every API and many parameters are tagged with the level that introduced them. `app.json`
  `runtime.apiVersion.minVersion` (and `compatible`, `target`) say which firmware the app installs on; code must not use an
  API above it (`$DOCS/guides/framework/device/compatibility.md`).
- **Permissions.** APIs needing authorization list a permission code in their reference; it must be in `app.json`
  `permissions` (`$DOCS/guides/framework/device/permission.md`). Not all required codes are documented (see gotchas).
- **Screens.** Targets are keyed by screen type (`targets.<name>`, `platforms` with `st` and `dw`), `assets/<name>/` holds the
  matching resources. Round screens clip rectangles near the edge.
- **Tools.** `zeus create | dev | preview | build` (`@zeppos/zeus-cli`, `$DOCS/guides/tools/cli/index.md`). `zeus dev` needs the
  simulator; `zeus preview` prints a QR code scanned in the Zepp App's developer mode and needs a login; `zeus build` makes the
  `.zab`. Whether a command may be run (especially `preview`) is the project's decision: read its CLAUDE.md first.

## Rules when writing code

- Imports come from `@zos/<module>` (v2+ API); the old global `hmUI`/`hmFS` style is API v1 and appears in old samples and
  blog posts. Check the sample's API level before copying from it.
- Look up a function in the declarations before using it; do not write one from memory, and do not guess parameter names.
- After editing a page or the layout, remember there is no way to see the screen from here: say what was not looked at.
- Anything learned on a real device that the documentation does not say goes into `references/gotchas.md`.

## Files

- `references/gotchas.md` - differences between documentation and devices, and traps that cost time.
- `references/docs-map.md` - which documentation page answers which question.
- `scripts/setup.sh`, `scripts/build-api-index.mjs` - prepare the sources and build the index.
