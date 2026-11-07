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
// Every chunk carries the device token (utils/device.js) that the watch received when it was
// paired; the phone presents it to the server with the final request. A watch that has not
// been paired sends nothing: recordings stay queued, and the sweep reports state 'unpaired' so
// the screen can say how to fix it. If the server refuses the token (the device was revoked)
// the token is forgotten and the same thing happens: it can never work again.
//
// Every answer to an upload says how full the account's storage is. The latest is remembered
// (utils/storageState.js), and when it is worse than the one before, or says that old recordings were just
// deleted, the sweep reports an `alert` with its last event, so the screen can warn the person (page/storage.js).
// A server that says "too many uploads" (429) stops the sweep like any other
// server problem and the screen says so; a recording the server calls too large (413) is skipped like an
// unreadable one, since sending it again changes nothing.
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
import { getDeviceToken, isPaired, clearDevice, getServerUrl, isCustomServer, dropCustomToken } from './device'
import { storageStateOf, getStorageState, saveStorageState, clearStorageState } from './storageState'
import { MAX_RECORDINGS, MAX_PENDING_RECORDINGS } from '../config'
import { t } from '../i18n'

const UPLOADS_INDEX_FILE = 'uploads.json'

// Raw file bytes per chunk. MUST stay a multiple of 3: every chunk is then
// base64-encoded on its own without '=' padding, so the phone can join the
// strings into the base64 of the whole file without decoding anything.
const CHUNK_BYTES = 15 * 1024
// A chunk that is not the last is acknowledged by the phone at once, so a phone that is there answers within
// seconds. The last chunk's reply carries the server's answer: the phone waits up to 30 s for the server
// (app-side/uploads.js) before it answers "cannot reach it", so this wait must be longer than that.
const CHUNK_TIMEOUT_MS = 30 * 1000
const LAST_CHUNK_TIMEOUT_MS = 60 * 1000
const CHUNK_RETRIES = 2 // extra attempts per chunk before the recording is given up on
const CHUNK_RETRY_DELAY_MS = 1000
// Before a sweep sends anything the phone is asked whether it is there at all (a request any reply to
// satisfies), so that a Zepp app that does not answer is found in seconds, not after the first chunk's retries.
const PING_TIMEOUT_MS = 10 * 1000
const PING_RETRIES = 1
const RETRY_PACING_MS = 300 // pause between two recordings in one sweep

// A sweep that shows no sign of life for this long is taken to be dead (its page was torn down, say), so that it cannot
// keep every later sweep out for the rest of the session. It is longer than the slowest single request.
const SWEEP_LEASE_MS = 2 * 60 * 1000

let sweepAlert = null // the warning worth interrupting the person for, from the answers of the running sweep (null: none)

// What every copy of this module has to agree on:
//   activeRecording  file name of the recording still being written, if any
//   stateListener    the page that shows the progress (see onUploadState)
//   sweepUntil       a sweep is running until this time; every sign of life renews it, the end of the sweep clears it
//   unreportedAlert  a storage warning that came while nobody listened, kept for the next listener
//
// app.js and every page are separate bundles (a .bin each in the built package), and each carries its own copy of this
// file, so plain module variables would be separate per bundle. A sweep started by the BLE-reconnect trigger in app.js
// would then not know which recording the main page is still writing (and upload it half-written), would not be kept out
// by a sweep the page is running, and would report its progress to nobody. The state therefore lives in the app's global
// data, where the BLE connection also lives. Where there is no app (before App() has run), a private object is used.
function newShared() {
  return { activeRecording: null, stateListener: null, sweepUntil: 0, unreportedAlert: null }
}

const privateShared = newShared()

function shared() {
  try {
    const globals = getApp()._options.globalData
    if (!globals.uploads) globals.uploads = newShared()
    return globals.uploads
  } catch (e) {
    return privateShared
  }
}

function sweepRunning() {
  return Date.now() < shared().sweepUntil
}

function sweepAlive() {
  shared().sweepUntil = Date.now() + SWEEP_LEASE_MS
}

function sweepOver() {
  shared().sweepUntil = 0
}

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
  shared().activeRecording = fileName
}

// Recordings still waiting for the server, newest first. Reads the upload index
// once, not once per file.
export function pendingRecordings() {
  const uploaded = readIndex().uploaded
  const active = shared().activeRecording
  return listRecordings().filter((item) => item.fileName !== active && uploaded.indexOf(item.fileName) === -1)
}

// One listener at most (the main page). Receives:
//   { state: 'sending', index, count, chunk, total }  recording `index` of `count`, chunk `chunk` of `total`
//   { state: 'done', pending, alert }                 the sweep finished; alert is null, or 'nearly_full' or
//                                                     'full' when the server's answers say the account's storage
//                                                     just got nearly full (once), or that old recordings were
//                                                     deleted to make room (every time): the screen warns about it
//   { state: 'connecting', pending }                  a sweep started: the phone is being asked whether it is there
//   { state: 'error', message, pending, kind }        the sweep stopped on a failure; kind is 'phone' (the phone did
//                                                     not answer), 'server' (the phone could not reach the server)
//                                                     or absent (anything else: `message` is the reason)
//   { state: 'unpaired', pending, revoked, custom }   nothing was sent because the watch has no token to send;
//                                                     revoked is true when the server just refused its token;
//                                                     custom is true when the watch is set up for the person's own
//                                                     server (utils/device.js): then it is a token that is missing,
//                                                     and the watch has not gone back to the PigeonDrop server
//   { state: 'alert', alert }                         a storage warning from a sweep that ran while nobody was
//                                                     listening (no page open); only `alert` is meant
// `sending`, `done` and `error` may carry an `alert` as well. Pass null to unsubscribe.
export function onUploadState(callback) {
  const state = shared()
  state.stateListener = callback

  // The warning is only news once (the next sweep sees no change), so one that came with nobody listening is kept and
  // handed over now. Not inside this call: the page is still being built.
  if (callback && state.unreportedAlert) {
    const alert = state.unreportedAlert
    state.unreportedAlert = null
    setTimeout(() => emit({ state: 'alert', alert }), 0)
  }
}

function emit(state) {
  const listener = shared().stateListener
  if (!listener) {
    if (state.alert) keepAlert(state.alert)
    return
  }
  try {
    listener(state)
  } catch (e) {
    // A UI problem must never break uploading.
    console.log('upload state listener failed:', e)
  }
}

// 'full' (old recordings were deleted) outranks 'nearly_full'.
function keepAlert(alert) {
  const state = shared()
  if (state.unreportedAlert !== 'full') state.unreportedAlert = alert
}

// Two independent caps: MAX_RECORDINGS bounds the already-uploaded history kept
// for on-watch browsing, MAX_PENDING_RECORDINGS is a hard ceiling on not-yet-
// uploaded recordings so storage can't fill up completely if the server is
// unreachable for a long time - a recording is never deleted just for being old
// while it's still waiting to reach the server, only once that second cap is
// actually exceeded. A recording still being written is never touched.
export function pruneRecordings() {
  // A sweep sends the oldest waiting recordings first, which are the very ones the waiting cap deletes: never under its feet.
  // The cap is applied by the next call.
  if (sweepRunning()) return

  const uploaded = readIndex().uploaded
  const active = shared().activeRecording
  const recordings = listRecordings().filter((item) => item.fileName !== active)
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

// The "Clear memory" action (page/settings.js): deletes every recording on the
// watch and forgets the upload history, which also empties the send queue. A
// recording still being written is left alone. Refuses (busy) while an upload is
// running, because that upload holds one of the files open and would otherwise
// record a result for a file that no longer exists.
export function clearAllRecordings() {
  if (sweepRunning()) return { removed: 0, busy: true }

  let removed = 0
  const active = shared().activeRecording
  listRecordings()
    .filter((item) => item.fileName !== active)
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

// One request to the phone, retried a couple of times on transport errors (timeout, BLE blip). When the retries
// run out the error says the phone did not answer (`noPhone`): the phone is where the break is, not the server.
function requestPhone(messageBuilder, message, timeoutMs, retriesLeft) {
  sweepAlive()
  return messageBuilder.request(message, { timeout: timeoutMs }).catch((err) => {
    if (retriesLeft <= 0) {
      const failure = err instanceof Error ? err : new Error(String(err))
      failure.noPhone = true
      throw failure
    }
    return delay(CHUNK_RETRY_DELAY_MS).then(() => requestPhone(messageBuilder, message, timeoutMs, retriesLeft - 1))
  })
}

function requestChunk(messageBuilder, message, isLast) {
  return requestPhone(messageBuilder, message, isLast ? LAST_CHUNK_TIMEOUT_MS : CHUNK_TIMEOUT_MS, CHUNK_RETRIES)
}

// Is anybody there on the phone? Any reply is a yes (even an error: an older Side Service does not know `ping`);
// only silence is a no.
function checkPhone(messageBuilder) {
  return requestPhone(messageBuilder, { type: 'ping' }, PING_TIMEOUT_MS, PING_RETRIES)
}

// Sends one recording chunk by chunk. Resolves with the phone's final reply, or
// { skip: true } for an empty file (nothing to send). Rejects on failure; the error
// has .restart = true if the phone lost its partial copy of the upload,
// .local = true if the problem is with this file itself, .unauthorized = true if
// the server refused the device token, .rateLimited = true if it asked the watch to slow down,
// .noPhone = true if the phone did not answer, and .noServer = true if the phone answered that it
// cannot reach the server.
function sendRecording(item, onChunk) {
  const { messageBuilder } = getApp()._options.globalData
  const path = item.filePath.replace('data://', '')

  const deviceToken = getDeviceToken()
  if (!deviceToken) {
    const err = new Error('watch is not paired')
    err.unpaired = true
    return Promise.reject(err)
  }
  const serverUrl = getServerUrl() // null for the PigeonDrop server; a token without an address cannot exist for an own server

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

  const uploadId = String(Date.now()) // identifies this attempt, so a stale partial upload is never mixed in

  const sendFrom = (index) => {
    let message
    try {
      message = { type: 'uploadChunk', fileName: item.fileName, uploadId, index, total, data: reader.read(index), deviceToken }
      // The phone sends to the PigeonDrop server unless told otherwise; a watch set up for the person's own server tells it.
      if (serverUrl !== null) message.serverUrl = serverUrl
    } catch (err) {
      return Promise.reject(err)
    }
    onChunk(index, total)

    return requestChunk(messageBuilder, message, index === total - 1).then((reply) => {
      if (!reply || reply.ok !== true) {
        const err = new Error((reply && reply.error) || 'phone rejected the chunk')
        err.restart = !!(reply && reply.restart)
        // The phone could not wrap this particular recording: a problem of the file, not of the link.
        err.local = !!(reply && reply.fileError)
        // The server refused the device token: this watch is no longer paired.
        err.unauthorized = !!(reply && reply.unauthorized)
        // The server says this watch uploads too often: nothing is wrong with the file or the token.
        err.rateLimited = !!(reply && reply.rateLimited)
        // The phone is there but cannot reach the server (no internet, no answer in time, the server is down).
        err.noServer = !!(reply && reply.noServer)
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

// Remembers how full the server says the account's storage is (the answer to an upload carries it) and decides
// whether it is worth interrupting the person: when it just became nearly full (once, not at every upload
// while it stays so), and whenever the server had to delete old recordings to take this one (every time, since
// each deletion is news). The sweep keeps the strongest of its alerts, so a queue of recordings gives one.
function noteStorage(storage) {
  const state = storageStateOf(storage)
  if (state === null) return

  let before = null
  try {
    before = getStorageState()
    saveStorageState(state)
  } catch (e) {
    // The warning is a courtesy; a storage problem on the watch must never stop uploading.
    console.log('upload queue: could not keep the storage state:', e)
  }

  const worse = state === 'full' || (state === 'nearly_full' && before !== 'nearly_full' && before !== 'full')
  if (worse && (sweepAlert === null || state === 'full')) sweepAlert = state
}

function shortReason(err) {
  const text = String((err && err.message) || err)
  return text.length > 44 ? text.slice(0, 44) + '...' : text
}

// Sends every not-yet-uploaded recording, oldest first, strictly one at a time
// and one chunk at a time. A running sweep (see sweepRunning) stops the three retry triggers (new
// recording, BLE reconnect, app open) from starting a second, competing sweep.
// A transport/server failure stops the sweep: if the phone or server is
// unreachable, hammering every other pending recording at it would only waste
// battery - the next trigger tries again. A failure of one file itself (see
// fileError) only skips that file.
export function retryPendingUploads() {
  if (sweepRunning()) return

  const pending = pendingRecordings().sort((a, b) => a.timestamp - b.timestamp)
  if (pending.length === 0) return

  // Nothing can be sent without a device token; say so instead of trying and failing on every chunk.
  if (!isPaired()) {
    emit({ state: 'unpaired', pending: pending.length, revoked: false, custom: isCustomServer() })
    return
  }

  sweepAlive()
  sweepAlert = null
  const unreadable = [] // files this sweep could not read, reported when it ends
  emit({ state: 'connecting', pending: pending.length })

  const sendFrom = (i) => {
    if (i >= pending.length) return Promise.resolve()
    const item = pending[i]
    if (item.fileName === shared().activeRecording) return sendFrom(i + 1)

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
          noteStorage(reply && reply.storage)
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

  // The phone is asked before anything is read or sent; a phone that does not answer ends the sweep here.
  let phoneCheck
  try {
    phoneCheck = checkPhone(getApp()._options.globalData.messageBuilder)
  } catch (err) {
    phoneCheck = Promise.reject(err)
  }
  phoneCheck
    .then(() => sendFrom(0))
    .then(() => {
      sweepOver()
      if (unreadable.length > 0) {
        emit({ state: 'error', message: shortReason(unreadable[0]), pending: pendingRecordings().length, alert: sweepAlert })
      } else {
        emit({ state: 'done', pending: pendingRecordings().length, alert: sweepAlert })
      }
      // A recording finished while this sweep was running wasn't in its snapshot,
      // so give those (and only those) their own pass. Anything that was in the
      // snapshot is never retried here, so a failed index write can't loop forever.
      const sent = pending.map((item) => item.fileName)
      if (pendingRecordings().some((item) => sent.indexOf(item.fileName) === -1)) retryPendingUploads()
    })
    .catch((err) => {
      sweepOver()
      console.log('retryPendingUploads: sweep stopped:', err)
      if (err && err.unauthorized) {
        // The person's own server refused the token: forget the token but keep the address. Clearing everything would
        // turn this watch into one that uses the PigeonDrop server, and the recordings would go where the person did not choose.
        const custom = isCustomServer()
        if (custom) dropCustomToken()
        else clearDevice()
        clearStorageState() // it was about the account this watch was paired with
        emit({ state: 'unpaired', pending: pendingRecordings().length, revoked: true, custom })
        return
      }
      if (err && err.unpaired) {
        emit({ state: 'unpaired', pending: pendingRecordings().length, revoked: false, custom: isCustomServer() })
        return
      }
      if (err && err.rateLimited) {
        emit({ state: 'error', message: t('link.rate_limited'), pending: pendingRecordings().length, alert: sweepAlert })
        return
      }
      // Which link broke, for the screen (utils/linkProblem.js): `message` stays the technical reason.
      const kind = err && err.noPhone ? 'phone' : err && err.noServer ? 'server' : undefined
      emit({ state: 'error', kind: kind, message: shortReason(err), pending: pendingRecordings().length, alert: sweepAlert })
    })
}
