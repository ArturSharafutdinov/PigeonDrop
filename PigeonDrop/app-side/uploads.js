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
// base64 of the whole file. The body forwarded to the server is therefore exactly
// what the old single message carried, and the HTTP contract (API_SPEC.md) is
// unchanged.
//
// State is held in memory only: the Side Service has no dependable persistent
// storage or lifetime. If it is restarted mid-upload, the next chunk is answered
// with { restart: true } and the watch starts that recording over (the server
// de-duplicates by fileName, so a repeat is harmless).

const PENDING_TTL_MS = 10 * 60 * 1000 // abandoned partial uploads are dropped after this

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

function forwardToServer(fileName, audio, baseUrl) {
  return fetch({
    url: baseUrl + '/messages',
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ fileName: fileName, audio: audio }),
  })
    .then((res) => {
      const data = typeof res.body === 'string' ? JSON.parse(res.body) : res.body
      // fetch() resolves for any HTTP status, so the server's own { ok } flag is
      // what says whether the recording was really accepted.
      if (!data || data.ok !== true) {
        return { ok: false, error: 'server: ' + ((data && data.error) || 'rejected') }
      }
      return { ok: true, done: true, bytesReceived: data.bytesReceived, savedAs: data.savedAs, wavFile: data.wavFile }
    })
    .catch((err) => ({ ok: false, error: 'server: ' + String(err) }))
}

// message: { fileName, uploadId, index, total, data, serverUrl }
// Returns a Promise of the reply object the watch will receive.
export function handleUploadChunk(message, fallbackServerUrl) {
  const { fileName, uploadId, index, total, data, serverUrl } = message || {}

  const valid =
    typeof fileName === 'string' && fileName.length > 0 &&
    typeof uploadId === 'string' &&
    Number.isInteger(index) && Number.isInteger(total) &&
    total >= 1 && index >= 0 && index < total &&
    typeof data === 'string' && data.length > 0
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
  return forwardToServer(fileName, entry.parts.join(''), serverUrl || fallbackServerUrl)
}
