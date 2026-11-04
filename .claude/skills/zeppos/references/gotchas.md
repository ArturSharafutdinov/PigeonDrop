# Gotchas: where Zepp OS differs from its documentation, and traps that cost time

How to read this file. Each entry says **what happens**, **what to do**, and **where it comes from**:

- *docs checked* - compared with the documentation (`zepp-health/zeppos-docs`, commit `c08725b`, 2026-07-17) and the declarations
  (`@zeppos/device-types` 4.0.0) while writing this file.
- *device* - observed on a real watch (a round 480x480 device, Zepp OS 4.0) in the PigeonDrop project; not re-verified since.
  Treat these as strong leads, and re-test when the firmware or the API level changes.

Add new entries in the same form. If an entry turns out wrong, fix or remove it.

## Permissions and configuration

**Recording needs `device:os.mic` in `app.json` `permissions`.** Without it recording throws
`Permission Denied device:os.mic` at runtime. The permission is not mentioned anywhere in the documentation (searched for
"mic"/"microphone": no hits), so the error message is the only source. *docs checked + device.*
General rule: when a call fails with `Permission Denied <code>`, put `<code>` into `permissions`. The permission page covers only
the codes that the reference pages list.

**The declarations are incomplete.** `@zos/media` (Recorder, Player) and `@zos/crypto` are documented but absent from
`@zeppos/device-types` 4.0.0, so a type-checked project (`checkJs`) sees them as untyped. Do not conclude that an API is missing
because it is not in the index. *docs checked.*

## Audio

**A recording is not a standard Ogg/Opus file.** `Recorder` with `codec.OPUS` writes packets back to back as
`[8-byte header][Opus payload]`; the first 4 bytes of the header are the payload length, big-endian; the other 4 are unidentified.
Payloads are SILK wideband, mono, 20 ms per packet, decoding to 16 kHz 16-bit mono PCM. Standard tools (`ffmpeg`) do not open the
raw file. The current FAQ page (`guides/faq/opus-to-mp3.md`) shows the same layout (`readUint32BE(pos)`, payload at `pos + 8`); the
project's earlier notes recorded that page as describing a 12-byte prefix, which does not parse, so it has been corrected
since (or was misread). `codec.OPUS` is the only supported recorder format.
*docs checked + device.*

**Only one live `Player` at a time.** Creating a second `Player` (`create(id.PLAYER)`) throws
`cannot read property 'addEventListener' of undefined`. Create it lazily once and reuse it with `setSource` / `prepare`.
The `Player` has no `destroy`, and no way to remove a listener, so add the listeners once. Pages cannot share a module
variable (see "Every page is its own bundle" below), so with two pages that play audio the one `Player` has to live in
`getApp()._options.globalData`. The documentation does not mention this limit. *device (the limit) + docs checked.*

## Screen and power

**The screen sleeping kills a long task.** The system turns the screen off and about 10 s later closes the Mini Program, so an
unattended recording (or upload) is lost. Hold the screen on for the duration with `setPageBrightTime`, `pauseDropWristScreenOff`
and `pausePalmScreenOff` from `@zos/display` (reference pages in `reference/device-app-api/newAPI/display/`), always with an upper
bound, and close open files in `onDestroy` of the page. *device.*

**Round screens clip rectangles near the edge.** On a 480x480 round display (radius 240) keep element bounds inside roughly a
230 px radius of the centre; widgets near the top and bottom are cut off by the bezel. *device.*

**`QRCODE` needs its `bg_*` properties.** The widget takes `x/y/w/h` for the code and separate `bg_x/bg_y/bg_w/bg_h` for its
background. They are listed as optional (`reference/device-app-api/newAPI/ui/widget/QRCODE.mdx`), but leaving them out renders a
background far larger than the code, past the bezel. Always set both, centred. The documentation does not say what the default background is. *docs checked + device.*

## Files

**Close file descriptors as `closeSync({ fd })`.** The documentation also offers the shortcut `closeSync(fd)`
(`reference/device-app-api/newAPI/fs/closeSync.mdx`), but on the device the bare-number form leaves the descriptor open, and the
device allows only a few, so after several operations `openSync` fails with "cannot open file". *docs checked + device.*
Related habits that held up: treat an unreadable file as that file's own problem (skip it, keep it), never treat a failed
`statSync` as an empty file, and fall back to `readFileSync` when `openSync` fails.

## Device <-> phone communication

**Never send a large payload as one `MessageBuilder` message.** `call()` pushes every ~3.6 KB BLE packet in one synchronous loop
with no backpressure. A ~212 KB (base64) message overran the BLE send queue, `ble.send` returned `false`, and the exception was
swallowed by an unhandled promise: the send failed silently and the work involved made the app slow and the list page crash.
Send in chunks (15 KB worked), one `request()` at a time, waiting for the reply to each. If the chunks carry base64, the chunk size
in bytes must be a multiple of 3 so that the pieces concatenate. *device.*

**The upstream `MessageBuilder` ships with `DEBUG = true`.** `shared/message.js` and `shared/message-side.js` in the samples
(`$SAMPLES/application/2.0/todo-list/shared/`, `DEBUG = true`) hex-dump every BLE packet into the log, which makes large sends
crawl. Set it to `false` in the copy you vendor. The Side Service also has no global `Logger` in some contexts; the library needs a
fallback. *docs checked (samples) + device.*

**The Side Service's own `console.log` was never seen.** Early tests found no place where it shows up (the watch's "JS App Log"
shows device-side logs only). The reply channel to the device is the only way to observe what the Side Service did: return
the result, and put the reason into the error reply. The Zepp app's Developer Mode has a log screen for mini programs with a
*Side Service* tab (`guides/tools/zepp-app.mdx`); it does show the platform's own lines (handshakes, every message received,
the code the phone loaded). Whether the app's `console.log` lands there too is not verified: the one diagnostic build that
tried it never ran, see the next entry. *device (log screen) + docs checked.*

**The phone can run an old cached Side Service for the same app id, and installing a new preview does not replace it.** Seen once
(an iPhone, Side Service runtime 4.3.0): the watch app was the new build, but the phone loaded the Side Service of the very
first template build of the project. The watch's `ping` arrived (`receiveData`) and was never answered, so the device showed
"the phone does not answer". Re-scanning the preview QR code, with a higher version number too, did not replace the cached
code; a full reinstall of the Zepp app did. To tell it from a dead phone, open the *Side Service* log tab, press *Check
connection* on the watch and read the answer to `getWorkerCode` and the `appName` / `version` / `packageInfo.timeStamp` next to
it: if they are not your build, it is this. A lighter fix (deleting the mini program in Developer Mode, clearing the cache) was
not tried. See [`docs/troubleshooting.md`](../../../../docs/troubleshooting.md). *device.*

**The Side Service has no reliable storage or lifecycle** (`onInit` / `onRun` / `onDestroy`, started and stopped by the Zepp App).
Do not queue work there; keep persistent queues on the watch and make the phone side idempotent. *device.*

**`TransferFile` on the Side Service side exposed metadata only.** The inbox's `FileObject` gave name, path and size, with no way to
read the bytes, so it could not carry a watch -> phone upload. `MessageBuilder` messages do carry data. *device.*

**"No answer" is two different failures.** A device that gets no reply cannot tell a stuck Zepp App from a phone without internet.
Make them distinguishable: ping the phone first (any reply means it is there), and have the Side Service give its own network
calls a deadline that is always shorter than the device's wait, so that "the phone is there but the server is not" comes back as a
reply and silence means the phone side. *device (design that worked, values are estimates).*

## App structure

**Every page is its own bundle, so module variables are not shared between pages.** The build (`zeus build`) compiles `app.js`
and each page to a separate `.bin`, and each one carries its own copy of every module it imports: looking at the bytecode of a
PigeonDrop build, the upload queue's code (and its variable names) is present in `app.bin`, `page/index.bin` and `page/pair.bin`
alike. A `let` at the top of a module is therefore one variable per bundle. State that several pages (or `app.js`) must agree
on, such as "which recording is being written", "is a sweep running" or "who listens", belongs in
`getApp()._options.globalData`, which `App({ globalData })` creates for exactly this reason (the life-cycle guide says so:
"Initialize the app global data for use by all pages"). `globalData` lives as long as the app, a page's variables only as long
as the page: give shared flags an expiry so that a page torn down mid-task cannot leave one set for the rest of the session.
There is no `onShow`/`onHide` in the device-app `Page` life cycle (`onInit`, `build`, `onDestroy`), and the documentation does
not say whether `back()` rebuilds the page underneath. *build output inspected, not tested on a watch; docs checked.*

## Types and tooling

**`checkJs` with `@zeppos/device-types`.** A project can type-check against the declarations with `jsconfig.json`
(`"checkJs": true`, `"files": ["node_modules/@zeppos/device-types/dist/index.d.ts"]`) and a `global.d.ts` with
`/// <reference path="node_modules/@zeppos/device-types/dist/index.d.ts" />`. Imports of modules the package does not declare
(`@zos/media`, `@zos/crypto`) are then errors or `any`. *docs checked.*

**Samples are per API level.** `application/1.0` uses the old global style (`hmUI`, `hmFS`); `2.0` and later use `@zos/*`
imports. Check the level before copying a pattern, and check that every function you copy is at or below the app's
`runtime.apiVersion.minVersion`. *docs checked.*
