// End-to-end tests: start the real server as a child process on a free port,
// with its own temporary STORAGE_DIR, and talk to it over HTTP. Nothing here
// touches the developer's own server/data or server/uploads.
//
// Recordings are synthetic (a sine tone encoded with opusscript and wrapped in
// the watch's packet framing - see convert-to-wav.js), so no real voice data is
// needed in the repository.

const { test, before, after } = require('node:test')
const assert = require('node:assert/strict')
const { spawn } = require('node:child_process')
const fs = require('node:fs')
const net = require('node:net')
const os = require('node:os')
const path = require('node:path')
const { pathToFileURL } = require('node:url')
const OpusScript = require('opusscript')

const SERVER_DIR = path.join(__dirname, '..')
const PUBLIC_URL = 'https://pigeondrop.test'

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address()
      probe.close(() => resolve(port))
    })
  })
}

// Starts server.js; resolves once it reports that it is listening.
function startServer(storageDir, port) {
  const proc = spawn(process.execPath, ['server.js'], {
    cwd: SERVER_DIR,
    env: { ...process.env, PORT: String(port), STORAGE_DIR: storageDir, DEFAULT_SERVER_URL: PUBLIC_URL },
    stdio: ['ignore', 'pipe', 'inherit'],
  })
  const exited = new Promise((resolve) => proc.once('exit', (code, signal) => resolve({ code, signal })))

  return new Promise((resolve, reject) => {
    let out = ''
    proc.stdout.on('data', (chunk) => {
      out += chunk
      if (out.includes('listening')) resolve({ proc, exited, base: 'http://127.0.0.1:' + port })
    })
    exited.then(({ code }) => reject(new Error('server exited early with code ' + code)))
  })
}

// 20 ms frames of a 440 Hz tone: [4-byte big-endian length][4 unused bytes][opus packet], back to back.
function makeRecording(seconds) {
  const encoder = new OpusScript(16000, 1, OpusScript.Application.VOIP)
  const frameSamples = 320
  const parts = []
  for (let frame = 0; frame < seconds * 50; frame++) {
    const pcm = Buffer.alloc(frameSamples * 2)
    for (let i = 0; i < frameSamples; i++) {
      pcm.writeInt16LE(Math.round(8000 * Math.sin((2 * Math.PI * 440 * (frame * frameSamples + i)) / 16000)), i * 2)
    }
    const packet = Buffer.from(encoder.encode(pcm, frameSamples))
    const header = Buffer.alloc(8)
    header.writeUInt32BE(packet.length, 0)
    parts.push(header, packet)
  }
  encoder.delete()
  return Buffer.concat(parts)
}

const json = (base, method, route, body) =>
  fetch(base + route, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body),
  }).then(async (res) => ({ status: res.status, body: await res.json() }))

let storageDir
let port
let server
let packer // the app's own Ogg/Opus packer (PigeonDrop/utils/oggOpus.js), exactly as the phone runs it

before(async () => {
  packer = await import(pathToFileURL(path.join(SERVER_DIR, '..', 'PigeonDrop', 'utils', 'oggOpus.js')).href)
  storageDir = fs.mkdtempSync(path.join(os.tmpdir(), 'pigeondrop-server-'))
  port = await freePort()
  server = await startServer(storageDir, port)
})

after(async () => {
  if (server) {
    server.proc.kill('SIGKILL')
    await server.exited
  }
  fs.rmSync(storageDir, { recursive: true, force: true })
})

test('GET /setup serves the setup page', async () => {
  const res = await fetch(server.base + '/setup')
  assert.equal(res.status, 200)
  assert.match(res.headers.get('content-type'), /text\/html/)
  assert.match(await res.text(), /<html/i)
})

test('unknown routes answer 404 JSON', async () => {
  const res = await json(server.base, 'GET', '/nope')
  assert.equal(res.status, 404)
  assert.equal(res.body.ok, false)
})

test('accounts: create, read, set a custom server, validation, unknown code', async () => {
  const created = await json(server.base, 'POST', '/accounts', { code: 'abc-123' })
  assert.equal(created.status, 200)
  assert.equal(created.body.ok, true)
  assert.equal(created.body.serverUrl, PUBLIC_URL, 'a new account resolves to the default server')

  const again = await json(server.base, 'POST', '/accounts', { code: 'abc-123' })
  assert.equal(again.body.ok, true, 'creating twice is harmless')

  const custom = await json(server.base, 'PUT', '/accounts/abc-123', { customServerUrl: 'https://mine.example' })
  assert.equal(custom.body.serverUrl, 'https://mine.example')
  assert.equal((await json(server.base, 'GET', '/accounts/abc-123')).body.serverUrl, 'https://mine.example')

  const reset = await json(server.base, 'PUT', '/accounts/abc-123', { customServerUrl: null })
  assert.equal(reset.body.serverUrl, PUBLIC_URL, 'null goes back to the default')

  assert.equal((await json(server.base, 'PUT', '/accounts/abc-123', { customServerUrl: 'ftp://x' })).status, 400)
  assert.equal((await json(server.base, 'PUT', '/accounts/missing', { customServerUrl: null })).status, 404)
  assert.equal((await json(server.base, 'GET', '/accounts/missing')).status, 404)
  assert.equal((await json(server.base, 'POST', '/accounts', {})).status, 400)
  assert.equal((await json(server.base, 'POST', '/accounts', '{broken')).status, 400)
})

test('POST /messages saves the recording, converts it to wav, lists it, and is idempotent', async () => {
  const recording = makeRecording(2)
  const body = { fileName: 'voice_1.opus', audio: recording.toString('base64') }

  const first = await json(server.base, 'POST', '/messages', body)
  assert.equal(first.status, 200)
  assert.equal(first.body.ok, true)
  assert.equal(first.body.bytesReceived, recording.length)
  assert.ok(first.body.savedAs.endsWith('.opus') && first.body.wavFile.endsWith('.wav'))

  const saved = fs.readFileSync(path.join(storageDir, 'uploads', first.body.savedAs))
  assert.ok(saved.equals(recording), 'the saved .opus is byte-identical to what was sent')

  const wav = Buffer.from(await (await fetch(server.base + '/uploads/' + first.body.wavFile)).arrayBuffer())
  assert.equal(wav.toString('ascii', 0, 4), 'RIFF')
  assert.equal(wav.toString('ascii', 8, 12), 'WAVE')
  assert.equal(wav.readUInt16LE(22), 1, 'mono')
  assert.equal(wav.readUInt32LE(24), 16000, '16 kHz')
  assert.equal((wav.length - 44) / 32000, 2, 'two seconds of 16-bit samples')

  const second = await json(server.base, 'POST', '/messages', body)
  assert.equal(second.body.ok, true)
  assert.equal(second.body.savedAs, first.body.savedAs, 'a repeat of the same fileName returns the original result')
  const files = fs.readdirSync(path.join(storageDir, 'uploads'))
  assert.equal(files.filter((name) => name.endsWith('.opus')).length, 1, 'and does not save a duplicate')

  const listing = await (await fetch(server.base + '/uploads')).text()
  assert.ok(listing.includes(first.body.savedAs) && listing.includes(first.body.wavFile))
})

const wavSamples = async (fileName) => {
  const wav = Buffer.from(await (await fetch(server.base + '/uploads/' + fileName)).arrayBuffer())
  return wav.subarray(44)
}
const upload = (fileName, bytes) => json(server.base, 'POST', '/messages', { fileName, audio: Buffer.from(bytes).toString('base64') })

test('an Ogg/Opus upload is saved as is, served as audio, and decodes to the same sound as the Zepp original', async () => {
  const zepp = makeRecording(3)
  const packed = packer.zeppToOggOpus(new Uint8Array(zepp))
  assert.equal(packed.packets, 150)
  assert.equal(packed.droppedBytes, 0)

  const legacy = await upload('legacy_3.opus', zepp)
  const ogg = await upload('ogg_3.opus', packed.ogg)
  assert.equal(ogg.body.ok, true)
  assert.ok(ogg.body.wavFile, 'the Ogg upload was converted')
  assert.equal(ogg.body.bytesReceived, packed.ogg.length)

  const saved = fs.readFileSync(path.join(storageDir, 'uploads', ogg.body.savedAs))
  assert.ok(saved.equals(Buffer.from(packed.ogg)), 'the saved .opus is byte-identical to the Ogg that was sent')

  const legacyWav = await wavSamples(legacy.body.wavFile)
  const oggWav = await wavSamples(ogg.body.wavFile)
  assert.equal(oggWav.length, 3 * 32000)
  assert.ok(oggWav.equals(legacyWav), 'the same recording sounds identical whether it arrives as Ogg/Opus or in Zepp framing')

  const oggRes = await fetch(server.base + '/uploads/' + ogg.body.savedAs)
  assert.equal(oggRes.headers.get('content-type'), 'audio/ogg', 'browsers can play the Ogg directly')
  const legacyRes = await fetch(server.base + '/uploads/' + legacy.body.savedAs)
  assert.equal(legacyRes.headers.get('content-type'), 'application/octet-stream', 'a Zepp-framed file is not claimed to be audio')
})

test('a damaged Ogg upload is still stored, but is not converted and does not crash the server', async () => {
  const ogg = Buffer.from(packer.zeppToOggOpus(new Uint8Array(makeRecording(2))).ogg)

  const flipped = Buffer.from(ogg)
  flipped[flipped.length - 10] ^= 0xff // inside the last page: its checksum no longer matches
  const bad = await upload('ogg_flipped.opus', flipped)
  assert.equal(bad.body.ok, true)
  assert.equal(bad.body.wavFile, null, 'a failed checksum is refused rather than decoded into noise')

  const cut = await upload('ogg_cut.opus', ogg.subarray(0, ogg.length - 30))
  assert.equal(cut.body.ok, true)
  assert.equal(cut.body.wavFile, null, 'a truncated Ogg is not decoded')

  const health = await fetch(server.base + '/setup')
  assert.equal(health.status, 200, 'and the server is still up')
})

test('a recording cut short mid-packet is trimmed, not lost (both formats)', async () => {
  const zepp = makeRecording(2)
  const cutAt = zepp.length - 13 // somewhere inside the last packet
  const truncated = zepp.subarray(0, cutAt)

  const packed = packer.zeppToOggOpus(new Uint8Array(truncated))
  assert.equal(packed.packets, 99, 'the last, incomplete packet is dropped and the 99 complete ones are kept')
  assert.ok(packed.droppedBytes > 0 && packed.droppedBytes < 13 + 200)

  const viaOgg = await upload('ogg_trimmed.opus', packed.ogg)
  const viaZepp = await upload('zepp_trimmed.opus', truncated)
  assert.ok(viaOgg.body.wavFile && viaZepp.body.wavFile, 'both paths still produce a wav')
  const a = await wavSamples(viaOgg.body.wavFile)
  assert.equal(a.length, 99 * 640)
  assert.ok(a.equals(await wavSamples(viaZepp.body.wavFile)))

  assert.throws(() => packer.zeppToOggOpus(new Uint8Array(zepp.subarray(0, 7))), /no audio packets/, 'a file with no complete packet cannot be wrapped')
})

test('POST /messages rejects malformed requests', async () => {
  assert.equal((await json(server.base, 'POST', '/messages', '{broken')).status, 400)
  assert.equal((await json(server.base, 'POST', '/messages', { fileName: 'x.opus' })).status, 400)
  assert.equal((await json(server.base, 'POST', '/messages', { audio: 'AAAA' })).status, 400)
})

test('a 60 s recording (the size that once overran the phone) is accepted whole', async () => {
  const recording = makeRecording(60)
  assert.ok(recording.length > 50 * 1024, 'the synthetic recording is big enough to matter (' + recording.length + ' bytes)')
  const res = await json(server.base, 'POST', '/messages', { fileName: 'voice_60.opus', audio: recording.toString('base64') })
  assert.equal(res.body.ok, true)
  assert.equal(res.body.bytesReceived, recording.length)
  assert.ok(res.body.wavFile, 'and converted')
})

test('GET /uploads/:name refuses path tricks and missing files', async () => {
  assert.equal((await fetch(server.base + '/uploads/..%2Fserver.js')).status, 400)
  assert.equal((await fetch(server.base + '/uploads/..%2F..%2Fetc%2Fpasswd')).status, 400)
  assert.equal((await fetch(server.base + '/uploads/nothing.wav')).status, 404)
})

test('state survives a restart (what the /data volume is for)', async () => {
  const accountBefore = await json(server.base, 'GET', '/accounts/abc-123')
  const processedBefore = await json(server.base, 'POST', '/messages', { fileName: 'voice_1.opus', audio: 'AAAA' })
  assert.equal(processedBefore.body.ok, true)

  server.proc.kill('SIGTERM')
  await server.exited
  server = await startServer(storageDir, port)

  const accountAfter = await json(server.base, 'GET', '/accounts/abc-123')
  assert.deepEqual(accountAfter.body, accountBefore.body, 'the account is still there')
  const processedAfter = await json(server.base, 'POST', '/messages', { fileName: 'voice_1.opus', audio: 'AAAA' })
  assert.equal(processedAfter.body.savedAs, processedBefore.body.savedAs, 'upload history is still there')
})

test('SIGTERM stops the server promptly (a redeploy must not wait for Docker to kill it)', async () => {
  const started = Date.now()
  server.proc.kill('SIGTERM')
  const { code } = await server.exited
  assert.equal(code, 0)
  assert.ok(Date.now() - started < 3000, 'exited in ' + (Date.now() - started) + ' ms')
  server = null
})
