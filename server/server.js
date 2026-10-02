// Minimal test server for the PigeonDrop upload prototype. No dependencies -
// just Node's built-in http module, so `node server.js` is enough to run it.
//
// Routes:
//   POST /messages          - upload a recording, { fileName, audio (base64) }
//   POST /accounts          - create/upsert an account, { code }
//   GET  /accounts/:code    - read an account's resolved serverUrl
//   PUT  /accounts/:code    - set an account's serverUrl, { customServerUrl }
//   GET  /setup             - serves public/setup.html (the QR-code target)
//   GET  /uploads           - HTML listing of saved recordings, newest first
//   GET  /uploads/:name     - download/play a single saved .opus or .wav
//
// /uploads[/:name] are manual-access-only - the watch/phone never call them,
// they exist so a human can listen to what landed on the server from a
// browser, without opening the uploads folder. No auth, same as everything
// else in this prototype - anyone with the URL and a filename can fetch a
// recording, which matters as soon as the server is reachable through a
// tunnel and isn't just localhost anymore.
//
// For /messages: saved files are named after the date/time the SERVER
// received them (not when the watch recorded them), since that's what
// actually matters when browsing uploads/ by hand - but the watch's fileName
// is also the idempotency key (see uploads-store.js): the watch's own
// upload-success ack can get lost over the same unreliable BLE channel it
// arrived on, causing it to resend a recording that already made it to the
// server - a repeat POST with a fileName already seen returns the original
// result instead of saving a duplicate. Decodes the base64 payload, saves
// it, and converts it to a .wav right away (see convert-to-wav.js) so a
// received recording can be played back immediately, without a separate
// manual step.
//
// For /accounts: see accounts-store.js and ARCHITECTURE.md - this lets a
// user configure a custom upload server from their phone (scan a QR shown on
// the watch) instead of the watch app being rebuilt with a new SERVER_URL.

const http = require('http')
const fs = require('fs')
const path = require('path')
const { opus2wav } = require('./convert-to-wav')
const accountsStore = require('./accounts-store')
const uploadsStore = require('./uploads-store')
const { log } = require('./logger')
const { STORAGE_DIR } = require('./storage')

const PORT = process.env.PORT || 3000
const UPLOADS_DIR = path.join(STORAGE_DIR, 'uploads')
// public/ is served source code, not persistent state - stays relative to
// the app's own directory regardless of where STORAGE_DIR points.
const PUBLIC_DIR = path.join(__dirname, 'public')

// This server doubles as both the recording-upload target and the account
// server that the watch's QR/sync flow talks to - so "the default server"
// and "the server accounts live on" are the same address by design (see
// ARCHITECTURE.md). Update alongside PigeonDrop/config.js's SERVER_URL /
// WEB_APP_BASE_URL whenever the ngrok tunnel restarts.
const DEFAULT_SERVER_URL = process.env.DEFAULT_SERVER_URL || 'http://localhost:' + PORT

if (!fs.existsSync(UPLOADS_DIR)) {
  fs.mkdirSync(UPLOADS_DIR)
}

// Last-resort safety net: this is a prototype server that should stay up
// for the watch/phone to keep retrying against, even if something in a
// request handler goes wrong in a way the per-request try/catch below
// doesn't cover (e.g. an error surfacing on a later event-loop tick, after
// the handler that triggered it has already returned - see the fix to
// opus2wav in convert-to-wav.js for a concrete example of that shape of bug).
// Logging and continuing is the right call for a single local test server;
// it would not be for a real multi-user production service.
process.on('uncaughtException', (err) => {
  log('uncaughtException (server kept running):', err)
})
process.on('unhandledRejection', (err) => {
  log('unhandledRejection (server kept running):', err)
})

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = []
    req.on('data', (chunk) => chunks.push(chunk))
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')))
    req.on('error', reject)
  })
}

function sendJson(res, statusCode, data) {
  const body = JSON.stringify(data)
  res.writeHead(statusCode, { 'Content-Type': 'application/json' })
  res.end(body)
}

function pad(n, width = 2) {
  return String(n).padStart(width, '0')
}

// "2026-07-30_16-05-42-123" - sortable by name, filesystem-safe (no colons),
// millisecond suffix so two uploads in the same second never collide.
function receivedAtFileName() {
  const d = new Date()

  return (
    d.getFullYear() +
    '-' +
    pad(d.getMonth() + 1) +
    '-' +
    pad(d.getDate()) +
    '_' +
    pad(d.getHours()) +
    '-' +
    pad(d.getMinutes()) +
    '-' +
    pad(d.getSeconds()) +
    '-' +
    pad(d.getMilliseconds(), 3)
  )
}

async function handleUploadMessage(req, res) {
  let payload
  try {
    const rawBody = await readBody(req)
    payload = JSON.parse(rawBody)
  } catch (err) {
    sendJson(res, 400, { ok: false, error: 'invalid JSON body' })
    return
  }

  const { fileName, audio } = payload

  if (!fileName || !audio) {
    sendJson(res, 400, { ok: false, error: 'expected { fileName, audio }' })
    return
  }

  const alreadyProcessed = uploadsStore.getProcessed(fileName)
  if (alreadyProcessed) {
    log('duplicate upload for', fileName, '- returning original result')
    sendJson(res, 200, { ok: true, ...alreadyProcessed })
    return
  }

  // Defensive re-create, not just a startup-time check - cheap insurance
  // against whatever intermittently made uploads/ unavailable and crashed
  // the process before opus2wav's completion was actually awaited (see below).
  if (!fs.existsSync(UPLOADS_DIR)) {
    fs.mkdirSync(UPLOADS_DIR, { recursive: true })
  }

  const audioBuffer = Buffer.from(audio, 'base64')
  const savedName = receivedAtFileName() + '.opus'
  const savePath = path.join(UPLOADS_DIR, savedName)
  fs.writeFileSync(savePath, audioBuffer)
  log('saved', savedName, '(from watch:', fileName + ')', '-', audioBuffer.length, 'bytes')

  let wavFile = null
  try {
    const wavPath = savePath.replace(/\.opus$/, '.wav')
    // Awaited - opus2wav's write is asynchronous (wav.FileWriter opens the
    // file on its own event-loop tick), so without awaiting this, a failure
    // surfaces later as an unhandled stream 'error' event instead of landing
    // in this catch block - which is exactly what was crashing the process.
    await opus2wav(savePath, wavPath)
    wavFile = path.basename(wavPath)
    log('converted to', wavFile)
  } catch (err) {
    // Recording itself is safely saved either way - conversion failing
    // (e.g. a file that isn't actually in the expected packet format)
    // shouldn't fail the whole upload.
    log('conversion failed for', savedName, '-', err.message)
  }

  const result = { bytesReceived: audioBuffer.length, savedAs: savedName, wavFile }
  uploadsStore.markProcessed(fileName, result)
  sendJson(res, 200, { ok: true, ...result })
}

// Creates (or, if it already exists, just returns) an account for the given
// code. Idempotent by design - both the watch (right after generating a
// code) and the web setup page (defensively, on load) call this, and
// whichever gets there first should win without the other erroring out.
async function handleCreateAccount(req, res) {
  let payload
  try {
    payload = JSON.parse(await readBody(req))
  } catch (err) {
    sendJson(res, 400, { ok: false, error: 'invalid JSON body' })
    return
  }

  const { code } = payload
  if (!code) {
    sendJson(res, 400, { ok: false, error: 'expected { code }' })
    return
  }

  const account = accountsStore.upsertAccount(code)
  sendJson(res, 200, { ok: true, ...accountsStore.toPublicShape(account, DEFAULT_SERVER_URL) })
}

function handleGetAccount(req, res, code) {
  const account = accountsStore.getAccount(code)

  if (!account) {
    sendJson(res, 404, { ok: false, error: 'account not found' })
    return
  }

  sendJson(res, 200, { ok: true, ...accountsStore.toPublicShape(account, DEFAULT_SERVER_URL) })
}

async function handleUpdateAccount(req, res, code) {
  let payload
  try {
    payload = JSON.parse(await readBody(req))
  } catch (err) {
    sendJson(res, 400, { ok: false, error: 'invalid JSON body' })
    return
  }

  const { customServerUrl } = payload

  // Light sanity check only, not full URL validation - null ("use default")
  // or a string that at least looks like an http(s) URL.
  if (customServerUrl !== null && !/^https?:\/\//.test(customServerUrl || '')) {
    sendJson(res, 400, { ok: false, error: 'customServerUrl must be null or an http(s) URL' })
    return
  }

  const account = accountsStore.updateServerUrl(code, customServerUrl)

  if (!account) {
    sendJson(res, 404, { ok: false, error: 'account not found' })
    return
  }

  sendJson(res, 200, { ok: true, ...accountsStore.toPublicShape(account, DEFAULT_SERVER_URL) })
}

function handleSetupPage(req, res) {
  const filePath = path.join(PUBLIC_DIR, 'setup.html')
  res.writeHead(200, { 'Content-Type': 'text/html' })
  res.end(fs.readFileSync(filePath, 'utf8'))
}

function escapeHtml(str) {
  return str.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]))
}

// Filenames are receivedAtFileName()-based (see handleUploadMessage), which
// sorts chronologically as plain strings - no need to parse dates, a
// reverse alphabetical sort is already newest-first.
function handleListUploads(req, res) {
  const files = fs
    .readdirSync(UPLOADS_DIR)
    .filter((name) => name.endsWith('.wav') || name.endsWith('.opus'))
    .sort()
    .reverse()

  const rows = files
    .map((name) => `<li><a href="/uploads/${encodeURIComponent(name)}">${escapeHtml(name)}</a></li>`)
    .join('\n')

  const html = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>PigeonDrop uploads</title></head>
<body>
<h1>Uploads (${files.length})</h1>
<ul>${rows || '<li>(empty)</li>'}</ul>
</body>
</html>`

  res.writeHead(200, { 'Content-Type': 'text/html' })
  res.end(html)
}

const UPLOAD_CONTENT_TYPES = { '.wav': 'audio/wav', '.opus': 'application/octet-stream' }

function handleDownloadUpload(req, res, fileName) {
  // Reject anything that isn't a plain filename within UPLOADS_DIR itself -
  // no path traversal via '..' or a nested path.
  if (fileName.includes('/') || fileName.includes('..')) {
    sendJson(res, 400, { ok: false, error: 'invalid filename' })
    return
  }

  const filePath = path.join(UPLOADS_DIR, fileName)
  if (!fs.existsSync(filePath)) {
    sendJson(res, 404, { ok: false, error: 'file not found' })
    return
  }

  const contentType = UPLOAD_CONTENT_TYPES[path.extname(fileName)] || 'application/octet-stream'
  res.writeHead(200, { 'Content-Type': contentType })
  res.end(fs.readFileSync(filePath))
}

const server = http.createServer(async (req, res) => {
  log(req.method, req.url)

  const { pathname } = new URL(req.url, 'http://localhost')
  const accountMatch = pathname.match(/^\/accounts\/([^/]+)$/)
  const uploadFileMatch = pathname.match(/^\/uploads\/([^/]+)$/)

  try {
    if (req.method === 'POST' && pathname === '/messages') {
      await handleUploadMessage(req, res)
    } else if (req.method === 'POST' && pathname === '/accounts') {
      await handleCreateAccount(req, res)
    } else if (req.method === 'GET' && accountMatch) {
      handleGetAccount(req, res, decodeURIComponent(accountMatch[1]))
    } else if (req.method === 'PUT' && accountMatch) {
      await handleUpdateAccount(req, res, decodeURIComponent(accountMatch[1]))
    } else if (req.method === 'GET' && pathname === '/setup') {
      handleSetupPage(req, res)
    } else if (req.method === 'GET' && pathname === '/uploads') {
      handleListUploads(req, res)
    } else if (req.method === 'GET' && uploadFileMatch) {
      handleDownloadUpload(req, res, decodeURIComponent(uploadFileMatch[1]))
    } else {
      sendJson(res, 404, { ok: false, error: 'not found' })
    }
  } catch (err) {
    log('unhandled error handling', req.method, req.url, '-', err.message)
    sendJson(res, 500, { ok: false, error: 'internal error' })
  }
})

server.listen(PORT, () => {
  log(`PigeonDrop test server listening on http://localhost:${PORT}`)
})

// Docker (and so Coolify) stops the old container with SIGTERM on every redeploy.
// As PID 1 Node ignores that signal unless it has a handler, so without this
// Docker would wait 10 s and then kill the process, possibly mid-upload. Stop
// accepting connections, let requests already in flight finish, then exit; the
// timer is the backstop for a connection that never closes.
function shutdown(signal) {
  log(signal, 'received - shutting down')
  server.close(() => process.exit(0))
  setTimeout(() => process.exit(0), 8000).unref()
}
process.on('SIGTERM', () => shutdown('SIGTERM'))
process.on('SIGINT', () => shutdown('SIGINT'))
