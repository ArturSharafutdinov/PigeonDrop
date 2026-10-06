// Phone half of the chunked recording upload (the watch half is
// utils/uploadQueue.js).
//
// BLE is slow and its send queue is small, so the watch no longer pushes a whole
// recording in one message - a 60 s recording is ~160 KB, ~212 KB as base64, and
// that overran the send queue silently. Instead the watch sends numbered chunks
// with request(), waits for this module's reply to each one (that wait is the
// flow control), and the reply to the LAST chunk carries the server's answer.
//
// Each chunk is a slice of the raw file whose length is a multiple of 3 bytes, so
// its base64 string stands alone and the strings simply concatenate into the
// base64 of the whole file.
//
// That file is Zepp's private recording format. Once it is complete it is wrapped
// into a standard Ogg/Opus file (../utils/oggOpus.js, no re-encoding) before
// being sent, so that every server receives something ordinary tools can play.
//
// The server knows whose recording it is from the device token the watch got when it
// was paired. The watch sends the token with its chunks; this module keeps none of it
// (it has no dependable storage) and presents it as `Authorization: Bearer` with the
// final request, POST /v1/messages { messageId, audio: base64 } (API_SPEC.md). A token
// the server refuses is reported as `unauthorized` so the watch can forget it. A server that cannot be reached
// (no internet on the phone, no answer in time, or a gateway error because the server behind the proxy is down) is
// reported as `noServer`, so the watch can say that, and not "no answer from the phone". Other answers the watch
// must tell apart: `fileError` (413, the recording is over the size limit and no retry will change that),
// `rateLimited` (429, this watch uploads too often; later is fine), and on success `storage`, how full the
// account is, which the watch turns into a warning (utils/storageState.js).
//
// State is held in memory only: the Side Service has no dependable persistent
// storage or lifetime. If it is restarted mid-upload, the next chunk is answered
// with { restart: true } and the watch starts that recording over (the server
// de-duplicates by fileName, so a repeat is harmless).

import { zeppToOggOpus } from '../utils/oggOpus'
import { base64ToBytes, bytesToBase64 } from './base64'
import { withTimeout } from './timeout'

const PENDING_TTL_MS = 10 * 60 * 1000 // abandoned partial uploads are dropped after this

// How long the phone waits for the server before telling the watch it cannot reach it. The watch waits for the
// reply to the last chunk for longer (LAST_CHUNK_TIMEOUT_MS in utils/uploadQueue.js), so this answer arrives in time.
const SERVER_TIMEOUT_MS = 30 * 1000

// fileName -> { uploadId, total, parts: string[], received, startedAt }
let pending = {}

function dropStale(now) {
  const names = Object.keys(pending)
  for (let i = 0; i < names.length; i++) {
    if (now - pending[names[i]].startedAt > PENDING_TTL_MS) {
      delete pending[names[i]]
    }
  }
}

// Test hook: simulates the Side Service being restarted (all chunks forgotten).
export function clearPendingUploads() {
  pending = {}
}

function parseBody(res) {
  return typeof res.body === 'string' ? JSON.parse(res.body) : res.body
}

function forwardToServer(fileName, audio, baseUrl, deviceToken) {
  return withTimeout(
    fetch({
      url: baseUrl + '/v1/messages',
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + deviceToken },
      body: JSON.stringify({ messageId: fileName, audio: audio }),
    }),
    SERVER_TIMEOUT_MS,
  )
    .then((res) => {
      // The token is unknown or revoked: nothing this watch sends will be accepted until it is paired again.
      // The status says so where the runtime reports one (checked before the body is read: a proxy's own 401
      // page is not JSON), and so does the server's error code.
      const refused = { ok: false, unauthorized: true, error: 'server: device is not paired' }
      const tooLarge = { ok: false, fileError: true, error: 'server: recording too large' }
      const tooOften = { ok: false, rateLimited: true, error: 'server: too many uploads' }
      if (res.status === 401) return refused
      if (res.status === 413) return tooLarge
      if (res.status === 429) return tooOften
      // The proxy in front of the server answers when the server behind it is down (a restart, a deploy).
      if (res.status === 502 || res.status === 503 || res.status === 504) {
        return { ok: false, noServer: true, error: 'server: HTTP ' + res.status }
      }
      let data
      try {
        data = parseBody(res)
      } catch (e) {
        return { ok: false, error: 'server: HTTP ' + res.status + ', not JSON' }
      }
      if (data && data.code === 'unauthorized') return refused
      if (data && data.code === 'payload_too_large') return tooLarge
      if (data && data.code === 'too_many_attempts') return tooOften
      // fetch() resolves for any HTTP status, so the server's own { ok } flag is
      // what says whether the recording was really accepted.
      if (!data || data.ok !== true) {
        return { ok: false, error: 'server: ' + ((data && data.error) || 'rejected') }
      }
      return { ok: true, done: true, bytesReceived: data.bytesReceived, id: data.id, duplicate: data.duplicate, storage: data.storage }
    })
    // Only the request itself can end up here now: no connection, or no answer in time.
    .catch((err) => ({ ok: false, noServer: true, error: 'server: ' + String((err && err.message) || err) }))
}

// message: { fileName, uploadId, index, total, data, deviceToken }
// Returns a Promise of the reply object the watch will receive.
export function handleUploadChunk(message, serverUrl) {
  const { fileName, uploadId, index, total, data, deviceToken } = message || {}

  const valid =
    typeof fileName === 'string' && fileName.length > 0 &&
    typeof uploadId === 'string' &&
    Number.isInteger(index) && Number.isInteger(total) &&
    total >= 1 && index >= 0 && index < total &&
    typeof data === 'string' && data.length > 0 &&
    typeof deviceToken === 'string' && deviceToken.length > 0
  if (!valid) {
    return Promise.resolve({ ok: false, error: 'bad chunk' })
  }

  const now = Date.now()
  dropStale(now)

  let entry = pending[fileName]
  if (index === 0) {
    // A new attempt always starts at chunk 0 and replaces any earlier leftovers.
    entry = pending[fileName] = { uploadId: uploadId, total: total, parts: [], received: 0, startedAt: now }
  } else if (!entry || entry.uploadId !== uploadId || entry.total !== total) {
    // We lost this upload (restart, TTL) - tell the watch to start it over.
    return Promise.resolve({ ok: false, restart: true, error: 'upload state lost' })
  }

  if (entry.parts[index] === undefined) {
    entry.parts[index] = data
    entry.received++
  }

  if (index < total - 1) {
    return Promise.resolve({ ok: true, done: false, received: entry.received })
  }

  // Last chunk: everything must be here before anything is forwarded.
  delete pending[fileName]
  if (entry.received !== total) {
    return Promise.resolve({ ok: false, restart: true, error: 'incomplete upload' })
  }

  // A recording that can't be wrapped is the file's own problem, not the phone's
  // or the server's: the reply says so (fileError), and the watch skips that file
  // instead of stopping the whole queue on it.
  let audio
  try {
    const packed = zeppToOggOpus(base64ToBytes(entry.parts.join('')))
    if (packed.droppedBytes > 0) {
      console.log('recording', fileName, 'ended with', packed.droppedBytes, 'unusable bytes, trimmed')
    }
    audio = bytesToBase64(packed.ogg)
  } catch (err) {
    return Promise.resolve({ ok: false, fileError: true, error: 'cannot pack recording: ' + String((err && err.message) || err) })
  }
  return forwardToServer(fileName, audio, serverUrl, deviceToken)
}
