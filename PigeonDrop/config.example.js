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

// The PigeonDrop server: where the watch is paired (page/pair.js) and where recordings are
// uploaded (app-side/uploads.js). Put the address of your own deployment here - see
// backend/README.md and docs/deployment.md. No trailing slash.
export const SERVER_URL = 'https://your-server.example.com'
