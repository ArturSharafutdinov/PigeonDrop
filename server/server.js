// Minimal test server for the PigeonDrop upload prototype. No dependencies -
// just Node's built-in http module, so `node server.js` is enough to run it.
//
// Routes:
//   POST /messages          - upload a recording, { fileName, audio (base64) }
//   POST /accounts          - create/upsert an account, { code }
//   GET  /accounts/:code    - read an account's resolved serverUrl
//   PUT  /accounts/:code    - set an account's serverUrl, { customServerUrl }
//   GET  /setup             - serves public/setup.html (the QR-code target)
//
// For /messages: the watch's own fileName is only used for logging - saved
// files are named after the date/time the SERVER received them (not when the
// watch recorded them), since that's what actually matters when browsing
// uploads/ by hand. Decodes the base64 payload, saves it, and converts it to
// a .wav right away (see convert-to-wav.js) so a received recording can be
// played back immediately, without a separate manual step.
//
// For /accounts: see accounts-store.js and ARCHITECTURE.md - this lets a
// user configure a custom upload server from their phone (scan a QR shown on
// the watch) instead of the watch app being rebuilt with a new SERVER_URL.

const http = require('http')
const fs = require('fs')
const path = require('path')
const { opus2wav } = require('./convert-to-wav')
const accountsStore = require('./accounts-store')

const PORT = process.env.PORT || 3000
const UPLOADS_DIR = path.join(__dirname, 'uploads')
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

  const audioBuffer = Buffer.from(audio, 'base64')
  const savedName = receivedAtFileName() + '.opus'
  const savePath = path.join(UPLOADS_DIR, savedName)
  fs.writeFileSync(savePath, audioBuffer)
  console.log('saved', savedName, '(from watch:', fileName + ')', '-', audioBuffer.length, 'bytes')

  let wavFile = null
  try {
    const wavPath = savePath.replace(/\.opus$/, '.wav')
    opus2wav(savePath, wavPath)
    wavFile = path.basename(wavPath)
    console.log('converted to', wavFile)
  } catch (err) {
    // Recording itself is safely saved either way - conversion failing
    // (e.g. a file that isn't actually in the expected packet format)
    // shouldn't fail the whole upload.
    console.log('conversion failed for', savedName, '-', err.message)
  }

  sendJson(res, 200, { ok: true, bytesReceived: audioBuffer.length, savedAs: savedName, wavFile })
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

const server = http.createServer(async (req, res) => {
  console.log(req.method, req.url)

  const { pathname } = new URL(req.url, 'http://localhost')
  const accountMatch = pathname.match(/^\/accounts\/([^/]+)$/)

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
    } else {
      sendJson(res, 404, { ok: false, error: 'not found' })
    }
  } catch (err) {
    console.log('unhandled error handling', req.method, req.url, '-', err.message)
    sendJson(res, 500, { ok: false, error: 'internal error' })
  }
})

server.listen(PORT, () => {
  console.log(`PigeonDrop test server listening on http://localhost:${PORT}`)
})
