# Where to look: question -> documentation page

Paths are relative to `$DOCS` (the `docs/` folder of `zepp-health/zeppos-docs`, printed by `scripts/setup.sh`) unless they start
with `$SAMPLES`. The documentation is Markdown/MDX; to find a page by a word, `grep -rli 'word' $DOCS`. Older API levels live
only in the documentation repository's `versioned_docs/` (not fetched by `setup.sh`).

## Start here

| Question | Page |
|---|---|
| What is a Mini Program made of, how do the parts talk | `guides/architecture/arc.mdx` |
| Which files go where | `guides/architecture/folder-structure.mdx` |
| Create, run, preview, install the first app | `guides/quick-start/` (`create`, `development`, `simulator-dev`, `preview`, `environment`) |
| What `app.json` may contain (pages, permissions, targets, apiVersion, i18n, side service, settings) | `reference/app-json.mdx` |
| Which API exists since which level; what to use for an older watch | `guides/framework/device/compatibility.md`, `guides/version-info/` (`new-features-21`, `-30`, `-35`, `-40`, `-42`, `migration-guide`, `version-choose`, `new-api`) |
| Which devices exist, their screens and API levels | `reference/related-resources/device-list.mdx` |

## Device app (runs on the watch)

| Topic | Page |
|---|---|
| App and page lifecycle, `App()`, `Page()` | `guides/framework/device/app.md`, `page.mdx`, `life-cycle.md` |
| Permissions (`app.json` `permissions`) | `guides/framework/device/permission.md` |
| Screens, layout, adapting to several screen sizes | `guides/framework/device/layout.md`, `screen-adaption.md`, `guides/best-practice/multi-screen-adaption.mdx` |
| Files (`/data`, `/assets`) | `guides/framework/device/fs.md`, `reference/device-app-api/newAPI/fs/` |
| Navigation between pages | `guides/framework/device/router.md`, `reference/device-app-api/newAPI/router/` |
| Bluetooth from the device (raw `ble`, central role) | `guides/framework/device/ble-central.md`, `reference/device-app-api/newAPI/ble/` |
| System events, notifications | `guides/framework/device/system-event.md`, `system-notification.md` |
| Widgets: every widget's properties and events | `reference/device-app-api/newAPI/ui/widget/<NAME>.mdx` (BUTTON, TEXT, IMG, QRCODE, SCROLL_LIST, VIEW_CONTAINER, ...); common calls in `reference/device-app-api/newAPI/ui/` (`createWidget`, `setProperty`, `addEventListener`, `deleteWidget`) |
| Audio: `Recorder`, `Player` | `reference/device-app-api/newAPI/media/` (`create`, `Recorder`, `Player`) |
| Screen on/off and brightness | `reference/device-app-api/newAPI/display/` (`setPageBrightTime`, `pauseDropWristScreenOff`, `pausePalmScreenOff`, `setWakeUpRelaunch`, ...) |
| Sensors (heart rate, steps, sleep, ...) | `reference/device-app-api/newAPI/sensor/<Name>.mdx` |
| Storage (small key/value) | `reference/device-app-api/newAPI/storage/`, `guides/best-practice/persistence-storage.mdx` |
| Vibration, buzzer, system sounds | `reference/device-app-api/newAPI/sensor/` (`Vibrator`, `Buzzer`, `SystemSounds`) |
| Globals (`Buffer`, timers, `console`) | `reference/device-app-api/newAPI/global/` |
| Background work | `guides/framework/device/app-service.md`, `reference/device-app-api/newAPI/app-service/` |
| Shortcut card / secondary widget | `guides/framework/device/secondary-widget.md` |
| Custom keyboard (API 4.2) | `guides/keyboard/` |
| Physical keys | `reference/related-resources/physical-keys.mdx` |
| Translations | `guides/best-practice/i18n.mdx`, `reference/related-resources/language-list.mdx` |

## Side Service and the phone

| Topic | Page |
|---|---|
| What the Side Service is and is not | `guides/framework/side-service/intro.md`, `register.md` |
| Talking to the device: `MessageBuilder` | `guides/best-practice/bluetooth-communication.mdx`; the library itself: `$SAMPLES/application/2.0/todo-list/shared/` (3.0 and 4.0 samples do not carry it) |
| Raw messaging API, `fetch`, file download, image conversion | `reference/side-service-api/` (`messaging`, `fetch`, `download-file`, `image-convert`, `transfer-file`, `settings-storage`, `global`) |
| Settings App (UI in the Zepp App) | `guides/framework/app-settings/`, `reference/app-settings-api/` |
| Moving files between phone and watch | `reference/side-service-api/transfer-file.mdx`, `reference/device-app-api/newAPI/transfer-file/TransferFile.mdx` |

## Practice, tools, shipping

| Topic | Page |
|---|---|
| Project structure and code organization | `guides/best-practice/code-organization.mdx` |
| Debugging, logging, catching errors | `guides/best-practice/debug.mdx`, `error-catch.mdx`, `guides/faq/upload-log.md` |
| Communication between pages | `guides/best-practice/cross-page-communications.mdx` |
| Widget groups | `guides/best-practice/widget-group.mdx` |
| New devices | `guides/best-practice/code-adaptations-for-new-devices.mdx` |
| `zeus` CLI | `guides/tools/cli/index.md` (+ `release-note.md`) |
| Simulator | `guides/tools/simulator/` |
| Zepp App developer mode, API level of a device | `guides/tools/zepp-app.mdx` |
| Frequently asked (install errors, icons, login, preview, opus conversion) | `guides/faq/` (`index.md` lists them) |
| Publishing | `distribute/index.md` |
| UX/visual design rules (round screens, icons, touch targets) | `designs/` |
| Watchfaces (a different app type) | `watchface/`, `distribute/watchface.md` |
| Sample apps described | `samples/app/` (`calories`, `fetchAPI`, `showcase`, `toDoList`); the code is in `$SAMPLES/application/<api level>/` |

## Samples (`$SAMPLES`)

- `application/<level>/<name>`: `1.0`, `2.0`, `3.0`, `4.0`, `4.2`; names include `hello-world`, `todo-list`, `fetch-api`,
  `calories`, `showcase` (2.0 only), `post-health-data` (2.0 only), `download` and `notification` (3.0 only),
  `simple-keyboard` and `t9-keyboard` (4.2).
- `watchface/`, `workout-extensions/`.
- Each sample is a complete project (`app.json`, `app.js`, `page/`, `app-side/`, `setting/`); `grep -rn '<function>' $SAMPLES/application/4.0`
  shows real calls.
