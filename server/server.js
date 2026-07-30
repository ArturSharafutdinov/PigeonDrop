// Minimal test server for the PigeonDrop upload prototype. No dependencies -
// just Node's built-in http module, so `node server.js` is enough to run it.
//
// Expects: POST /messages
//   Content-Type: application/json
//   Body: { "fileName": "voice_<timestamp>.opus", "audio": "<base64>" }
//
// The watch's own fileName is only used for logging - saved files are named
// after the date/time the SERVER received them (not when the watch recorded
// them), since that's what actually matters when browsing uploads/ by hand.
//
// Decodes the base64 payload, saves it, and converts it to a .wav right away
// (see convert-to-wav.js) so a received recording can be played back
// immediately, without a separate manual step.

const http = require('http')
const fs = require('fs')
const path = require('path')
const { opus2wav } = require('./convert-to-wav')

const PORT = process.env.PORT || 3000
const UPLOADS_DIR = path.join(__dirname, 'uploads')

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

const server = http.createServer(async (req, res) => {
  console.log(req.method, req.url)

  if (req.method !== 'POST' || req.url !== '/messages') {
    sendJson(res, 404, { ok: false, error: 'not found' })
    return
  }

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
})

server.listen(PORT, () => {
  console.log(`PigeonDrop test server listening on http://localhost:${PORT}`)
})
