// Shared helpers for managing voice recordings stored on the watch.
// Both page/index.js (recording) and page/list.js (playback) rely on this
// module so the file naming/storage scheme only needs to live in one place.

import { readdirSync, mkdirSync, rmSync } from '@zos/fs'

// Sub-folder inside the Mini Program's private /data directory where all
// recordings live, e.g. data://voice_messages/voice_1785056400000.opus
export const RECORDINGS_DIR = 'voice_messages'

// Every recording file looks like: voice_<timestamp-in-ms>.opus
// The timestamp is embedded in the filename (instead of a separate index or
// metadata file) so a recording's creation time can always be recovered just
// by reading the directory - no extra bookkeeping file to keep in sync.
const FILE_PREFIX = 'voice_'
const FILE_SUFFIX = '.opus'

// Left-pads a number with a leading zero (e.g. 5 -> "05"). Used for
// human-readable date/time formatting below.
function pad(n) {
  return n < 10 ? '0' + n : '' + n
}

// Creates the recordings folder if it doesn't exist yet. Safe to call on
// every recording/listing - mkdirSync throws if the folder is already there,
// which is exactly the case we want to ignore.
export function ensureRecordingsDir() {
  try {
    mkdirSync({ path: RECORDINGS_DIR })
  } catch (e) {
    // Folder already exists - nothing to do.
  }
}

// Deletes one recording's file (a cancelled recording). True when the file is gone afterwards, which is judged
// from the folder, not from what rmSync says: a file that was never created counts as gone.
export function discardRecording(fileName) {
  try {
    rmSync({ path: RECORDINGS_DIR + '/' + fileName })
  } catch (e) {
    // Judged below.
  }
  try {
    return (readdirSync({ path: RECORDINGS_DIR }) || []).indexOf(fileName) === -1
  } catch (e) {
    return false
  }
}

// Builds a brand new, unique file path for a recording that's about to start.
// Uses the current time (ms since epoch) as the filename so recordings sort
// naturally and never collide.
export function newRecordingPath() {
  ensureRecordingsDir()
  const fileName = FILE_PREFIX + Date.now() + FILE_SUFFIX
  return 'data://' + RECORDINGS_DIR + '/' + fileName
}

// Formats a timestamp (ms since epoch) as "DD.MM.YYYY HH:MM:SS" for display
// in the recordings list.
export function formatTimestamp(timestamp) {
  const date = new Date(timestamp)

  return (
    pad(date.getDate()) +
    '.' +
    pad(date.getMonth() + 1) +
    '.' +
    date.getFullYear() +
    ' ' +
    pad(date.getHours()) +
    ':' +
    pad(date.getMinutes()) +
    ':' +
    pad(date.getSeconds())
  )
}

// Reads every saved recording from disk and returns them as plain objects,
// newest first. This is the single source of truth both the main page
// (for "play the latest one") and the list page (for "show me everything")
// read from - neither of them touches the file system directly.
export function listRecordings() {
  ensureRecordingsDir()
  const files = readdirSync({ path: RECORDINGS_DIR }) || []

  return files
    .filter(function (name) {
      // Ignore anything that isn't one of our recording files, in case the
      // folder ever ends up with unrelated files in it.
      return name.indexOf(FILE_PREFIX) === 0 && name.indexOf(FILE_SUFFIX) === name.length - FILE_SUFFIX.length
    })
    .map(function (name) {
      // The timestamp is the only piece of information we need out of the
      // filename - everything else (label, path) is derived from it.
      const timestamp = parseInt(name.slice(FILE_PREFIX.length, -FILE_SUFFIX.length), 10)

      return {
        fileName: name,
        filePath: 'data://' + RECORDINGS_DIR + '/' + name,
        timestamp: timestamp,
        label: formatTimestamp(timestamp),
      }
    })
    .filter(function (item) {
      // Guards against a malformed filename producing NaN and breaking the sort below.
      return !isNaN(item.timestamp)
    })
    .sort(function (a, b) {
      return b.timestamp - a.timestamp // newest first
    })
}
