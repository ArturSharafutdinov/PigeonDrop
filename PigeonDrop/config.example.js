// Template for config.js - copy this file to config.js and fill in your own
// values. config.js is gitignored (not tracked) specifically so your real
// server URL never ends up in the repo - see CLAUDE.md.
//
//   cp config.example.js config.js

// Maximum number of already-uploaded voice recordings kept on the watch at
// once, for on-watch browsing/playback (see utils/uploadQueue.js#pruneRecordings).
// Not-yet-uploaded recordings are governed by MAX_PENDING_RECORDINGS below,
// not this constant - a recording is never deleted just for being old if it
// hasn't made it to the server yet.
export const MAX_RECORDINGS = 20

// Hard ceiling on not-yet-uploaded recordings, so watch storage can't fill
// up completely if the server is unreachable for a long time.
export const MAX_PENDING_RECORDINGS = 50

// Longest single recording, in seconds. While recording, the screen is kept on
// (Zepp OS would otherwise close the app ~10 s after the display sleeps and the
// recording would be lost), so this limit bounds how long a recording left
// running by mistake can hold the screen on and drain the battery. When it is
// reached the recording is stopped and sent automatically. 60 s is the product
// limit for now: it keeps a recording near 160 KB, which is what the chunked
// upload and the phone-side Ogg packing were sized and tested around.
export const MAX_RECORDING_SECONDS = 60

// Bootstrap server address (see /server in the repo root, or server/DEPLOY.md
// for a real deployment). This is also where the account/config server lives
// - registerAccount and sync requests (see utils/account.js and
// page/settings.js) go here too. Once the user has synced at least once, the
// actual upload target is whatever serverUrl their account resolved to
// (utils/account.js#getServerUrl) - this constant is only the fallback used
// before that first sync.
export const SERVER_URL = 'https://your-server.example.com'

// Base URL for the web setup page the QR code points to (see
// page/settings.js). Kept as a separate constant from SERVER_URL, even
// though they're usually the same address, since the account API and the
// web/config page could end up hosted separately later.
export const WEB_APP_BASE_URL = SERVER_URL
