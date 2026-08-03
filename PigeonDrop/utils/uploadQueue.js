// Persistent upload queue: tracks which recordings have actually made it to
// the server, retries the ones that haven't, and enforces two separate
// storage caps (see config.js). This exists because MessageBuilder's call()
// gives no delivery guarantee at all - it can fail completely silently if
// the watch isn't BLE-connected to the phone, and there's no built-in retry
// or reconnect-triggered resend anywhere in shared/message.js. The Side
// Service can't be trusted to hold this state itself either: its process
// lifetime is undocumented and it has no confirmed persistent storage
// beyond a small settingsStorage key-value store - so the watch (which has
// proven @zos/fs storage) is the only source of truth, and the Side Service
// stays exactly what it already was: a stateless relay that tries a fetch()
// per item and reports back.
//
// Depends one-way on ./recordings (not the other way around) so there's no
// circular import between "what recordings exist" and "which are uploaded".

import { readFileSync, writeFileSync, rmSync } from '@zos/fs'
import { RECORDINGS_DIR, listRecordings } from './recordings'
import { getServerUrl } from './account'
import { MAX_RECORDINGS, MAX_PENDING_RECORDINGS } from '../config'

const UPLOADS_INDEX_FILE = 'uploads.json'

// Pacing between sequential retries in retryPendingUploads() - MessageBuilder
// has no queue/backpressure of its own, so sending several call()s back to
// back risks interleaving their BLE chunk sequences on the one shared
// channel. This value is an untested guess, not a documented safe number -
// tune it against a real device with several pending recordings queued up.
const RETRY_PACING_MS = 300

let sweepInFlight = false

function readIndex() {
  const raw = readFileSync({ path: UPLOADS_INDEX_FILE, options: { encoding: 'utf8' } })
  if (!raw) return { uploaded: [] }

  try {
    const parsed = JSON.parse(raw)
    return { uploaded: Array.isArray(parsed.uploaded) ? parsed.uploaded : [] }
  } catch (e) {
    // Corrupt/partial file shouldn't crash the app - just start fresh.
    console.log('utils/uploadQueue: failed to parse uploads.json, resetting')
    return { uploaded: [] }
  }
}

function writeIndex(index) {
  writeFileSync({ path: UPLOADS_INDEX_FILE, data: JSON.stringify(index), options: { encoding: 'utf8' } })
}

// A recording not present here is, by definition, still pending - so there's
// nothing to separately track for "pending", and nothing to clean up beyond
// forgetUpload() below once a file is actually deleted.
export function isUploaded(fileName) {
  return readIndex().uploaded.indexOf(fileName) !== -1
}

export function markUploaded(fileName) {
  const index = readIndex()
  if (index.uploaded.indexOf(fileName) === -1) {
    index.uploaded.push(fileName)
    writeIndex(index)
  }
}

// Keeps uploads.json from growing forever with entries for files that no
// longer exist - called whenever pruneRecordings() deletes a file.
function forgetUpload(fileName) {
  const index = readIndex()
  const pos = index.uploaded.indexOf(fileName)
  if (pos !== -1) {
    index.uploaded.splice(pos, 1)
    writeIndex(index)
  }
}

// Replaces the old pruneOldRecordings (utils/recordings.js) now that the
// prune policy depends on upload status, which recordings.js deliberately
// doesn't know about. Two independent caps: MAX_RECORDINGS bounds the
// already-uploaded history kept for on-watch browsing, MAX_PENDING_RECORDINGS
// is a hard ceiling on not-yet-uploaded recordings so storage can't fill up
// completely if the server is unreachable for a long time - a recording is
// never deleted just for being old while it's still waiting to reach the
// server, only once that second cap is actually exceeded.
export function pruneRecordings() {
  const recordings = listRecordings()
  const uploaded = recordings.filter((item) => isUploaded(item.fileName))
  const pending = recordings.filter((item) => !isUploaded(item.fileName))

  const excess = uploaded.slice(MAX_RECORDINGS).concat(pending.slice(MAX_PENDING_RECORDINGS))

  excess.forEach((item) => {
    rmSync({ path: RECORDINGS_DIR + '/' + item.fileName })
    forgetUpload(item.fileName)
  })
}

// Sends one recording to the Side Service over MessageBuilder - the same
// logic that used to live in page/index.js#sendRecordingViaMessage, moved
// here so both a fresh recording and the retry sweep below can share it.
// Fire-and-forget: this gives no delivery guarantee (see the module header),
// the real confirmation arrives later via app.js's ack listener calling
// markUploaded().
export function uploadRecording(item) {
  const relativePath = item.filePath.replace('data://', '')
  const arrayBuffer = readFileSync({ path: relativePath })

  if (!arrayBuffer) {
    console.log('uploadRecording: failed to read file', relativePath)
    return
  }

  const audio = Buffer.from(arrayBuffer).toString('base64')
  // null until the user has synced an account at least once - the Side
  // Service falls back to config.js's bootstrap SERVER_URL in that case.
  const serverUrl = getServerUrl()

  const { messageBuilder } = getApp()._options.globalData
  messageBuilder.call({ fileName: item.fileName, audio, serverUrl })
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

// Retries every not-yet-uploaded recording, oldest first, one at a time -
// not all at once, since MessageBuilder has no queue/backpressure of its own
// (see RETRY_PACING_MS above). sweepInFlight stops the three retry triggers
// (new recording, BLE reconnect, app open) from starting a second, competing
// sweep if two of them fire close together.
export function retryPendingUploads() {
  if (sweepInFlight) return

  const pending = listRecordings()
    .filter((item) => !isUploaded(item.fileName))
    .sort((a, b) => a.timestamp - b.timestamp)

  if (pending.length === 0) return

  sweepInFlight = true

  pending
    .reduce((chain, item) => chain.then(() => uploadRecording(item)).then(() => delay(RETRY_PACING_MS)), Promise.resolve())
    .then(() => {
      sweepInFlight = false
    })
    .catch((err) => {
      // Should be unreachable - uploadRecording() doesn't itself throw - but
      // guard against a stuck sweepInFlight flag if something unexpected does.
      console.log('retryPendingUploads: sweep failed', err)
      sweepInFlight = false
    })
}
