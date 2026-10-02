// Persistent upload queue: tracks which recordings have actually made it to
// the server, retries the ones that haven't, and enforces two separate
// storage caps (see config.js). This exists because BLE delivery is unreliable:
// the phone can be out of range, the Side Service can be restarted, the server
// can be down. The Side Service can't be trusted to hold this state itself - its
// process lifetime is undocumented and it has no confirmed persistent storage
// beyond a small settingsStorage key-value store - so the watch (which has
// proven @zos/fs storage) is the only source of truth.
//
// Sending: a recording is NOT sent as one message. A 60 s recording is ~160 KB
// (~212 KB as base64); pushed as a single MessageBuilder call() it became ~60 BLE
// packets queued in one synchronous loop, which overran the BLE send queue and
// failed silently. It is sent instead as numbered chunks of CHUNK_BYTES with
// request(), one at a time: the phone's reply to each chunk is the flow control,
// the reply to the last chunk carries the server's answer, and only that reply
// marks the recording uploaded. See app-side/uploads.js for the phone half.
//
// A recording that cannot be read (file won't open, stat fails, short read) is a
// problem of that one file, not of the phone or the server: the sweep skips it,
// keeps sending the recordings behind it, and reports the failure at the end. It
// stays queued; clearAllRecordings() is the way to get rid of it.
//
// Depends one-way on ./recordings (not the other way around) so there's no
// circular import between "what recordings exist" and "which are uploaded".

import { readFileSync, writeFileSync, rmSync, openSync, readSync, closeSync, statSync, O_RDONLY } from '@zos/fs'
import { RECORDINGS_DIR, listRecordings } from './recordings'
import { getServerUrl } from './account'
import { MAX_RECORDINGS, MAX_PENDING_RECORDINGS } from '../config'

const UPLOADS_INDEX_FILE = 'uploads.json'

// Raw file bytes per chunk. MUST stay a multiple of 3: every chunk is then
// base64-encoded on its own without '=' padding, so the phone can join the
// strings into the base64 of the whole file without decoding anything.
const CHUNK_BYTES = 15 * 1024
const CHUNK_TIMEOUT_MS = 60 * 1000
const CHUNK_RETRIES = 2 // extra attempts per chunk before the recording is given up on
const CHUNK_RETRY_DELAY_MS = 1000
const RETRY_PACING_MS = 300 // pause between two recordings in one sweep

let sweepInFlight = false
let activeRecording = null // file name of the recording still being written, if any
let stateListener = null

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

// A recording not present in the index is, by definition, still pending - so
// there's nothing to separately track for "pending", and nothing to clean up
// beyond forgetUpload() below once a file is actually deleted.
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
// longer exist - called whenever a file is actually deleted.
function forgetUpload(fileName) {
  const index = readIndex()
  const pos = index.uploaded.indexOf(fileName)
  if (pos !== -1) {
    index.uploaded.splice(pos, 1)
    writeIndex(index)
  }
}

// Tells the queue which recording is still being written. Such a file is a
// half-finished recording: the retry sweep must neither upload it (it would be
// marked uploaded while incomplete) nor let pruning delete it. Pass null when
// the recording has ended.
export function setActiveRecording(fileName) {
  activeRecording = fileName
}

// Recordings still waiting for the server, newest first. Reads the upload index
// once, not once per file.
export function pendingRecordings() {
  const uploaded = readIndex().uploaded
  return listRecordings().filter((item) => item.fileName !== activeRecording && uploaded.indexOf(item.fileName) === -1)
}

// One listener at most (the main page). Receives:
//   { state: 'sending', index, count, chunk, total }  recording `index` of `count`, chunk `chunk` of `total`
//   { state: 'done', pending }                        the sweep finished
//   { state: 'error', message, pending }              the sweep stopped on a failure
// Pass null to unsubscribe.
export function onUploadState(callback) {
  stateListener = callback
}

function emit(state) {
  if (!stateListener) return
  try {
    stateListener(state)
  } catch (e) {
    // A UI problem must never break uploading.
    console.log('upload state listener failed:', e)
  }
}

// Two independent caps: MAX_RECORDINGS bounds the already-uploaded history kept
// for on-watch browsing, MAX_PENDING_RECORDINGS is a hard ceiling on not-yet-
// uploaded recordings so storage can't fill up completely if the server is
// unreachable for a long time - a recording is never deleted just for being old
// while it's still waiting to reach the server, only once that second cap is
// actually exceeded. A recording still being written is never touched.
export function pruneRecordings() {
  const uploaded = readIndex().uploaded
  const recordings = listRecordings().filter((item) => item.fileName !== activeRecording)
  const done = recordings.filter((item) => uploaded.indexOf(item.fileName) !== -1)
  const waiting = recordings.filter((item) => uploaded.indexOf(item.fileName) === -1)

  done
    .slice(MAX_RECORDINGS)
    .concat(waiting.slice(MAX_PENDING_RECORDINGS))
    .forEach((item) => {
      rmSync({ path: RECORDINGS_DIR + '/' + item.fileName })
      forgetUpload(item.fileName)
    })
}

// The "Очистить память" action (page/settings.js): deletes every recording on the
// watch and forgets the upload history, which also empties the send queue. A
// recording still being written is left alone. Refuses (busy) while an upload is
// running, because that upload holds one of the files open and would otherwise
// record a result for a file that no longer exists.
export function clearAllRecordings() {
  if (sweepInFlight) return { removed: 0, busy: true }

  let removed = 0
  listRecordings()
    .filter((item) => item.fileName !== activeRecording)
    .forEach((item) => {
      try {
        rmSync({ path: RECORDINGS_DIR + '/' + item.fileName })
        removed++
      } catch (e) {
        console.log('clearAllRecordings: cannot remove', item.fileName, e)
      }
    })
  writeIndex({ uploaded: [] })
  return { removed, busy: false }
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

// A problem with one particular file. Marked so the sweep can tell it from a
// transport/server failure (see retryPendingUploads).
function fileError(message) {
  const err = new Error(message)
  err.local = true
  return err
}

function encode(bytes) {
  return Buffer.from(bytes).toString('base64')
}

// Chunk-by-chunk access to one recording: read(index) returns that chunk
// base64-encoded, close() releases the file. Normally each slice is read straight
// from the file, so memory stays flat however long the recording is. If the file
// cannot be opened that way, the whole file is read once and sliced instead - a
// recording is at most MAX_RECORDING_SECONDS long, so this is affordable, and it
// beats giving up on a file the watch can plainly read.
function openReader(path, size) {
  const fd = openSync({ path, flag: O_RDONLY })

  if (typeof fd === 'number' && fd >= 0) {
    return {
      read(index) {
        const expected = Math.min(CHUNK_BYTES, size - index * CHUNK_BYTES)
        const buffer = new ArrayBuffer(CHUNK_BYTES)
        const bytesRead = readSync({ fd, buffer, options: { offset: 0, length: CHUNK_BYTES, position: index * CHUNK_BYTES } })

        // A short read anywhere but the end would put '=' padding mid-stream and
        // corrupt the joined base64, so it is an error rather than something to send.
        if (bytesRead !== expected) {
          throw fileError('read ' + bytesRead + ' of ' + expected + ' bytes at chunk ' + index)
        }
        return encode(buffer.slice(0, bytesRead))
      },
      // The documented form is closeSync({ fd }). Passing the bare number does not
      // close the descriptor, and the device only has a handful of them: after a
      // few recordings openSync starts failing.
      close: () => closeSync({ fd }),
    }
  }

  const whole = readFileSync({ path })
  if (!whole || whole.byteLength !== size) {
    throw fileError('cannot open file (' + fd + ')')
  }
  return {
    read: (index) => encode(whole.slice(index * CHUNK_BYTES, Math.min(size, (index + 1) * CHUNK_BYTES))),
    close: () => {},
  }
}

// One chunk, retried a couple of times on transport errors (timeout, BLE blip).
function requestChunk(messageBuilder, message, retriesLeft) {
  return messageBuilder.request(message, { timeout: CHUNK_TIMEOUT_MS }).catch((err) => {
    if (retriesLeft <= 0) throw err
    return delay(CHUNK_RETRY_DELAY_MS).then(() => requestChunk(messageBuilder, message, retriesLeft - 1))
  })
}

// Sends one recording chunk by chunk. Resolves with the phone's final reply, or
// { skip: true } for an empty file (nothing to send). Rejects on failure; the error
// has .restart = true if the phone lost its partial copy of the upload, and
// .local = true if the problem is with this file itself.
function sendRecording(item, onChunk) {
  const { messageBuilder } = getApp()._options.globalData
  const path = item.filePath.replace('data://', '')

  let reader
  let size
  let total
  try {
    const info = statSync({ path })
    // Not the same as an empty file: a failed stat must never get a recording deleted.
    if (!info) throw fileError('cannot read file size')
    if (!info.size) return Promise.resolve({ skip: true })

    size = info.size
    total = Math.ceil(size / CHUNK_BYTES)
    reader = openReader(path, size)
  } catch (err) {
    return Promise.reject(err)
  }

  const serverUrl = getServerUrl() // null until an account has been synced; the phone then uses its default
  const uploadId = String(Date.now()) // identifies this attempt, so a stale partial upload is never mixed in

  const sendFrom = (index) => {
    let message
    try {
      message = { type: 'uploadChunk', fileName: item.fileName, uploadId, index, total, data: reader.read(index), serverUrl }
    } catch (err) {
      return Promise.reject(err)
    }
    onChunk(index, total)

    return requestChunk(messageBuilder, message, CHUNK_RETRIES).then((reply) => {
      if (!reply || reply.ok !== true) {
        const err = new Error((reply && reply.error) || 'phone rejected the chunk')
        err.restart = !!(reply && reply.restart)
        throw err
      }
      if (index < total - 1) return sendFrom(index + 1)
      if (reply.done !== true) throw new Error('server did not confirm')
      return reply
    })
  }

  return sendFrom(0).then(
    (reply) => {
      reader.close()
      return reply
    },
    (err) => {
      reader.close()
      throw err
    },
  )
}

// A phone that lost its partial copy (Side Service restarted) answers 'restart':
// start that recording over once, from chunk 0, under a new upload id.
function sendWithRestart(item, onChunk, restartsLeft) {
  return sendRecording(item, onChunk).catch((err) => {
    if (err && err.restart && restartsLeft > 0) return sendWithRestart(item, onChunk, restartsLeft - 1)
    throw err
  })
}

function shortReason(err) {
  const text = String((err && err.message) || err)
  return text.length > 44 ? text.slice(0, 44) + '...' : text
}

// Sends every not-yet-uploaded recording, oldest first, strictly one at a time
// and one chunk at a time. sweepInFlight stops the three retry triggers (new
// recording, BLE reconnect, app open) from starting a second, competing sweep.
// A transport/server failure stops the sweep: if the phone or server is
// unreachable, hammering every other pending recording at it would only waste
// battery - the next trigger tries again. A failure of one file itself (see
// fileError) only skips that file.
export function retryPendingUploads() {
  if (sweepInFlight) return

  const pending = pendingRecordings().sort((a, b) => a.timestamp - b.timestamp)
  if (pending.length === 0) return

  sweepInFlight = true
  const unreadable = [] // files this sweep could not read, reported when it ends

  const sendFrom = (i) => {
    if (i >= pending.length) return Promise.resolve()
    const item = pending[i]
    if (item.fileName === activeRecording) return sendFrom(i + 1)

    const next = () => delay(RETRY_PACING_MS).then(() => sendFrom(i + 1))

    return sendWithRestart(
      item,
      (chunk, total) => emit({ state: 'sending', index: i + 1, count: pending.length, chunk: chunk + 1, total }),
      1,
    ).then(
      (reply) => {
        if (reply && reply.skip) {
          // Empty file (a recording aborted before any audio) - it can't be sent or played.
          rmSync({ path: RECORDINGS_DIR + '/' + item.fileName })
          forgetUpload(item.fileName)
        } else {
          markUploaded(item.fileName)
        }
        return next()
      },
      (err) => {
        if (!err || !err.local) throw err
        console.log('retryPendingUploads: skipping unreadable', item.fileName, err)
        unreadable.push(err)
        return next()
      },
    )
  }

  sendFrom(0)
    .then(() => {
      sweepInFlight = false
      if (unreadable.length > 0) {
        emit({ state: 'error', message: shortReason(unreadable[0]), pending: pendingRecordings().length })
      } else {
        emit({ state: 'done', pending: pendingRecordings().length })
      }
      // A recording finished while this sweep was running wasn't in its snapshot,
      // so give those (and only those) their own pass. Anything that was in the
      // snapshot is never retried here, so a failed index write can't loop forever.
      const sent = pending.map((item) => item.fileName)
      if (pendingRecordings().some((item) => sent.indexOf(item.fileName) === -1)) retryPendingUploads()
    })
    .catch((err) => {
      sweepInFlight = false
      console.log('retryPendingUploads: sweep stopped:', err)
      emit({ state: 'error', message: shortReason(err), pending: pendingRecordings().length })
    })
}
