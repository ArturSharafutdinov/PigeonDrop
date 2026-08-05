// Minimal JSON-file-backed store recording which watch fileNames have
// already been processed by POST /messages, so a retried upload (e.g. the
// watch resending because the phone's success ack got lost over the same
// unreliable BLE channel it came in on) is a safe no-op instead of a
// duplicate save + re-conversion. No database, no locking - matches
// accounts-store.js's role for this prototype (see CLAUDE.md: server/ is
// explicitly not production infrastructure).

const fs = require('fs')
const path = require('path')
const { log } = require('./logger')
const { STORAGE_DIR } = require('./storage')

const DATA_DIR = path.join(STORAGE_DIR, 'data')
const UPLOADS_FILE = path.join(DATA_DIR, 'uploads.json')

function readAll() {
  if (!fs.existsSync(UPLOADS_FILE)) return {}

  try {
    return JSON.parse(fs.readFileSync(UPLOADS_FILE, 'utf8'))
  } catch (err) {
    // A corrupt file shouldn't take down the whole server - treat it as empty.
    log('uploads-store: failed to parse uploads.json -', err.message)
    return {}
  }
}

function writeAll(uploads) {
  if (!fs.existsSync(DATA_DIR)) fs.mkdirSync(DATA_DIR, { recursive: true })
  fs.writeFileSync(UPLOADS_FILE, JSON.stringify(uploads, null, 2))
}

function getProcessed(fileName) {
  return readAll()[fileName] || null
}

function markProcessed(fileName, result) {
  const uploads = readAll()
  uploads[fileName] = { ...result, receivedAt: new Date().toISOString() }
  writeAll(uploads)
}

module.exports = { getProcessed, markProcessed }
