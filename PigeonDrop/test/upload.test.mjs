// Upload protocol: chunked, acknowledged sending of recordings from the watch to the phone
// and on to the server (utils/uploadQueue.js + app-side/uploads.js), including every failure mode.

import fs from 'fs'; import os from 'os'; import path from 'path'; import crypto from 'crypto'
import { ctl, resetFds, fds } from './stubs/fs.mjs'
import { device } from './stubs/upload-deps.mjs'
import { generate, fileUrl, stubPath } from './helpers/load.mjs'
import { framedRecording, readOgg, sameAudio, samePackets, zeppPackets } from './helpers/ogg.mjs'
// utils/uploadQueue.js and app-side/uploads.js are the real source files; only the platform pieces are stubbed.
const FS = stubPath('fs.mjs'), DEPS = stubPath('upload-deps.mjs')
const RECORDINGS = generate('utils/recordings.js', { '@zos/fs': FS })
const STORAGE = generate('utils/storageState.js', { '@zos/fs': FS })
const QUEUE = generate('utils/uploadQueue.js', { '@zos/fs': FS, './recordings': RECORDINGS, './device': DEPS, './storageState': STORAGE, '../config': DEPS })
const OGG = generate('utils/oggOpus.js', {})
const BASE64 = generate('app-side/base64.js', {})
const UPLOADS = generate('app-side/uploads.js', { '../utils/oggOpus': OGG, './base64': BASE64 })
const realSetTimeout = setTimeout
globalThis.setTimeout = (fn) => realSetTimeout(fn, 0)           // collapse every pacing/retry delay
let T = 0, failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const tick = () => new Promise((r) => realSetTimeout(r, 25))
const until = async (cond, ms = 4000) => { const t0 = Date.now(); while (!cond() && Date.now() - t0 < ms) await tick(); return cond() }

async function world() {
  const n = ++T
  device.token = 'pdd_test_token_0123456789'; device.cleared = 0
  ctl.root = fs.mkdtempSync(path.join(os.tmpdir(), 'wd-')); ctl.shortRead = null; ctl.readCalls = 0; ctl.failOpen = ctl.failStat = ctl.failWhole = ctl.failWrite = null; resetFds()
  const Q = await import(fileUrl(QUEUE) + '?t=' + n)
  const U = await import(fileUrl(UPLOADS) + '?t=' + n)
  const w = { Q, U, states: [], requests: [], posts: [], inflight: 0, maxInflight: 0, failRequests: new Set(), serverReply: null, serverStatus: 200, serverThrows: false, replyFor: null, hooks: {}, now: Date.now() }
  Q.onUploadState((s) => w.states.push(s))
  const builder = {
    request: async (msg, opts) => {
      w.inflight++; w.maxInflight = Math.max(w.maxInflight, w.inflight)
      try {
        const wire = JSON.parse(JSON.stringify(msg))                          // what actually crosses BLE
        w.requests.push({ file: wire.fileName, index: wire.index, total: wire.total, id: wire.uploadId, bytes: wire.data.length, token: wire.deviceToken })
        if (w.hooks.before) await w.hooks.before(wire, w)
        if (w.failRequests.has(w.requests.length)) throw new Error('Timed out in ' + opts.timeout + 'ms.')
        return JSON.parse(JSON.stringify(await U.handleUploadChunk(wire, 'https://default.example')))
      } finally { w.inflight-- }
    },
  }
  globalThis.getApp = () => ({ _options: { globalData: { messageBuilder: builder } } })
  globalThis.fetch = async (o) => {
    const body = JSON.parse(o.body); w.posts.push({ url: o.url, auth: o.headers && o.headers.Authorization, ...body })
    if (w.serverThrows) throw new Error('Network down')
    const custom = w.replyFor && w.replyFor(body, w.posts.length)   // { status, reply } for this very POST, or nothing
    const reply = (custom && custom.reply) || w.serverReply || { ok: true, id: 'm-1', messageId: body.messageId, bytesReceived: Buffer.from(body.audio, 'base64').length, duplicate: false }
    return { status: custom ? custom.status : w.serverStatus, body: typeof reply === 'string' ? reply : JSON.stringify(reply) }
  }
  return w
}
function addRecording(w, name, bytes) {
  const dir = path.join(ctl.root, 'voice_messages'); fs.mkdirSync(dir, { recursive: true })
  // A recording in Zepp's real framing (the phone now wraps it into Ogg/Opus, which needs valid packets); 0 means an empty file.
  const buf = Buffer.isBuffer(bytes) ? bytes : bytes === 0 ? Buffer.alloc(0) : framedRecording(bytes)
  fs.writeFileSync(path.join(dir, name), buf); return buf
}
const uploaded = () => { const f = path.join(ctl.root, 'uploads.json'); return fs.existsSync(f) ? JSON.parse(fs.readFileSync(f, 'utf8')).uploaded : [] }
const files = () => fs.readdirSync(path.join(ctl.root, 'voice_messages'))
const finished = (w) => until(() => w.states.some((s) => s.state === 'done' || s.state === 'error'))

console.log('1. 60 s recording (~160 KB): chunked, strictly sequential, the server gets the same audio wrapped as Ogg/Opus')
{ const w = await world(); const file = addRecording(w, 'voice_1000.opus', 163840)
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.requests.length === 11 && w.requests.every((r, i) => r.index === i), '11 chunks in order 0..10 (ceil(163840/15360)) -> ' + w.requests.map((r) => r.index).join(','))
  ok(w.maxInflight === 1, 'never more than one request in flight (max ' + w.maxInflight + ')')
  ok(w.requests.every((r) => r.bytes <= 20480 + 0), 'every chunk message is small: max base64 ' + Math.max(...w.requests.map((r) => r.bytes)) + ' chars (vs 218 454 in one piece)')
  ok(w.posts.length === 1 && sameAudio(w.posts[0].audio, file), 'server received ONE POST whose audio is a clean Ogg/Opus file carrying exactly the recording\'s packets')
  ok(w.posts[0].url === 'https://default.example/v1/messages' && w.posts[0].messageId === 'voice_1000.opus', 'posted to the server at /v1/messages, the file name sent as messageId')
  ok(JSON.stringify(uploaded()) === '["voice_1000.opus"]', 'marked uploaded only after the server confirmed')
  ok(w.states[0].state === 'sending' && w.states.at(-1).state === 'done' && w.states.at(-1).pending === 0, 'UI states: sending ... done(pending 0)')
  ok(w.states.filter((s) => s.state === 'sending').map((s) => s.chunk + '/' + s.total).join(' ') === Array.from({ length: 11 }, (_, i) => (i + 1) + '/11').join(' '), 'progress reports chunk 1/11 .. 11/11') }

console.log('2. size edge cases: 1 B, 100 B, exactly 1 chunk, 3 chunks, one byte over')
for (const size of [9, 100, 15360, 46080, 15361]) {
  const w = await world(); const file = addRecording(w, 'voice_5.opus', size); w.Q.retryPendingUploads(); await finished(w)
  ok(w.posts.length === 1 && sameAudio(w.posts[0].audio, file) && w.requests.length === Math.ceil(size / 15360), size + ' B -> ' + w.requests.length + ' chunk(s), same audio') }

console.log('3. transport failures: retried per chunk, then give up cleanly')
{ const w = await world(); const file = addRecording(w, 'voice_7.opus', 100000); w.failRequests = new Set([3, 4])    // chunk index 2 fails twice
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'done' && sameAudio(w.posts[0].audio, file), 'two timeouts on one chunk are retried; upload completes with the same audio')
  ok(w.requests.filter((r) => r.index === 2).length === 3, 'that chunk was requested 3 times (1 + 2 retries)') }
{ const w = await world(); addRecording(w, 'voice_8.opus', 100000); addRecording(w, 'voice_9.opus', 20000); w.failRequests = new Set([3, 4, 5])
  w.Q.retryPendingUploads(); await finished(w)
  const last = w.states.at(-1)
  ok(last.state === 'error' && /Timed out/.test(last.message) && last.pending === 2, 'three failures -> error state with reason, pending=2 -> ' + JSON.stringify(last))
  ok(w.posts.length === 0 && uploaded().length === 0, 'nothing posted, nothing marked uploaded')
  ok(!w.requests.some((r) => r.file === 'voice_9.opus'), 'sweep STOPPED: the second recording was not attempted')
  w.failRequests = new Set(); w.requests.length = 0; w.Q.retryPendingUploads(); await until(() => uploaded().length === 2)
  ok(JSON.stringify(uploaded()) === '["voice_8.opus","voice_9.opus"]', 'next trigger retries from scratch and both recordings go through') }

console.log('4. phone lost its partial copy mid-upload (Side Service restarted)')
{ const w = await world(); const file = addRecording(w, 'voice_11.opus', 100000); let wiped = false
  w.hooks.before = async (wire) => { if (wire.index === 4 && !wiped) { wiped = true; w.U.clearPendingUploads() } }
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'done' && w.posts.length === 1 && sameAudio(w.posts[0].audio, file), 'watch restarted that recording from chunk 0 and delivered the same audio once')
  ok(w.requests.map((r) => r.index).join(',') === '0,1,2,3,4,0,1,2,3,4,5,6' , 'request sequence shows the restart -> ' + w.requests.map((r) => r.index).join(',')) }

console.log('5. server problems are not mistaken for success')
{ const w = await world(); addRecording(w, 'voice_12.opus', 30000); w.serverReply = { ok: false, error: 'invalid JSON body' }
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'error' && /server: invalid JSON body/.test(w.states.at(-1).message) && uploaded().length === 0, 'HTTP-level rejection -> not marked uploaded; reason shown -> ' + w.states.at(-1).message) }
{ const w = await world(); addRecording(w, 'voice_13.opus', 30000); w.serverThrows = true
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'error' && /Network down/.test(w.states.at(-1).message) && uploaded().length === 0, 'server unreachable -> not marked uploaded -> ' + w.states.at(-1).message) }

console.log('6. a recording still being written is left alone')
{ const w = await world(); addRecording(w, 'voice_20.opus', 40000); addRecording(w, 'voice_21.opus', 40000)
  w.Q.setActiveRecording('voice_21.opus')
  ok(w.Q.pendingRecordings().map((i) => i.fileName).join() === 'voice_20.opus', 'pendingRecordings() excludes the active file')
  w.Q.retryPendingUploads(); await finished(w)
  ok(JSON.stringify(uploaded()) === '["voice_20.opus"]' && !w.requests.some((r) => r.file === 'voice_21.opus'), 'sweep uploaded only the finished one')
  for (let i = 0; i < 60; i++) addRecording(w, 'voice_' + (100 + i) + '.opus', 10)
  w.Q.pruneRecordings(); ok(files().includes('voice_21.opus'), 'pruning (cap 50 pending) never deletes the active recording') }

console.log('7. empty file is discarded; the rest still goes out; ordering is oldest-first, one at a time')
{ const w = await world(); addRecording(w, 'voice_30.opus', 0); addRecording(w, 'voice_32.opus', 20000); addRecording(w, 'voice_31.opus', 20000)
  w.Q.retryPendingUploads(); await finished(w)
  ok(!files().includes('voice_30.opus'), 'zero-byte recording removed')
  const order = w.requests.map((r) => r.file); const grouped = order.join().replace(/(voice_\d+\.opus)(,\1)+/g, '$1')
  ok(grouped === 'voice_31.opus,voice_32.opus', 'oldest first, all chunks of one before the next -> ' + grouped)
  ok(w.maxInflight === 1 && w.states.at(-1).state === 'done', 'strictly sequential; sweep ends done') }

console.log('8. a recording finished DURING a sweep is picked up right after')
{ const w = await world(); addRecording(w, 'voice_40.opus', 40000); let added = false
  w.hooks.before = async () => { if (!added) { added = true; addRecording(w, 'voice_41.opus', 20000) } }
  w.Q.retryPendingUploads(); await until(() => uploaded().length === 2)
  ok(JSON.stringify(uploaded()) === '["voice_40.opus","voice_41.opus"]', 'both uploaded without waiting for another trigger') }

console.log('9. read problems never send corrupted data')
{ const w = await world(); addRecording(w, 'voice_50.opus', 50000); ctl.shortRead = (i) => i === 1
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'error' && /read \d+ of \d+ bytes/.test(w.states.at(-1).message) && w.posts.length === 0, 'short read -> error, nothing forwarded -> ' + w.states.at(-1).message) }

console.log('10. phone-side validation, idempotency, expiry')
{ const w = await world(); const U = w.U; const base = { fileName: 'f', uploadId: 'u', index: 0, total: 2, data: 'AAAA', deviceToken: 'pdd_x' }
  const bad = async (patch) => (await U.handleUploadChunk({ ...base, ...patch }, 'x')).ok === false
  ok((await bad({ index: 2 })) && (await bad({ index: -1 })) && (await bad({ total: 0 })) && (await bad({ data: '' })) && (await bad({ data: 5 })) && (await bad({ fileName: '' })) && (await bad({ index: 1.5 })) && (await bad({ deviceToken: '' })) && (await bad({ deviceToken: undefined })) && (await bad({ deviceToken: 5 })), 'out-of-range / empty / non-string fields rejected, and a chunk without a device token')
  ok((await U.handleUploadChunk(null, 'x')).ok === false, 'null message rejected')
  await U.handleUploadChunk(base, 'x'); const dup = await U.handleUploadChunk(base, 'x')
  ok(dup.ok && dup.received === 1, 'duplicate chunk does not double-count')
  const mid = await U.handleUploadChunk({ ...base, index: 1, uploadId: 'OTHER' }, 'x')
  ok(mid.ok === false && mid.restart === true, 'chunk of a different upload id -> restart requested')
  const realNow = Date.now; await U.handleUploadChunk(base, 'x'); Date.now = () => realNow() + 11 * 60 * 1000
  const late = await U.handleUploadChunk({ ...base, index: 1 }, 'x'); Date.now = realNow
  ok(late.ok === false && late.restart === true, 'partial upload older than 10 min is dropped -> restart requested') }

console.log('11. descriptors are released: more recordings than the device has descriptors, one sweep')
{ const w = await world(); for (let i = 0; i < 8; i++) addRecording(w, 'voice_' + (200 + i) + '.opus', 20000)
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'done' && uploaded().length === 8, '8 recordings with a 4-descriptor table all go out -> ' + w.states.at(-1).state + ', uploaded ' + uploaded().length)
  ok(fds.size === 0, 'no descriptor left open after the sweep (' + fds.size + ' open)') }
{ const w = await world(); addRecording(w, 'voice_210.opus', 20000); w.failRequests = new Set([1, 2, 3])
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'error' && fds.size === 0, 'descriptor is released on a failed upload too (' + fds.size + ' open)') }

console.log('12. file cannot be opened: falls back to reading it whole')
{ const w = await world(); const file = addRecording(w, 'voice_220.opus', 100000); ctl.failOpen = () => true
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'done' && w.posts.length === 1 && sameAudio(w.posts[0].audio, file), 'delivered with the same audio without any descriptor -> ' + w.states.at(-1).state) }

console.log('13. one unreadable recording must not block the ones behind it')
{ const w = await world(); addRecording(w, 'voice_230.opus', 20000); const good = addRecording(w, 'voice_231.opus', 20000)
  ctl.failOpen = (p) => p.includes('voice_230'); ctl.failWhole = (p) => p.includes('voice_230')
  w.Q.retryPendingUploads(); await finished(w)
  const last = w.states.at(-1)
  ok(w.posts.length === 1 && w.posts[0].messageId === 'voice_231.opus' && sameAudio(w.posts[0].audio, good), 'the readable recording behind it was still delivered')
  ok(last.state === 'error' && /cannot open file/.test(last.message) && last.pending === 1, 'the failure is still reported, with the queue count -> ' + JSON.stringify(last))
  ok(files().includes('voice_230.opus') && JSON.stringify(uploaded()) === '["voice_231.opus"]', 'the unreadable one is kept (not deleted, not marked uploaded)')
  ok(fds.size === 0, 'no descriptors leaked') }

console.log('14. a stat that fails never deletes a recording')
{ const w = await world(); addRecording(w, 'voice_240.opus', 20000); ctl.failStat = () => true
  w.Q.retryPendingUploads(); await finished(w)
  ok(files().includes('voice_240.opus') && w.states.at(-1).state === 'error' && /cannot read file size/.test(w.states.at(-1).message), 'stat failure -> file kept, error shown -> ' + w.states.at(-1).message) }

console.log('15. "Очистить память": clearAllRecordings')
{ const w = await world(); addRecording(w, 'voice_250.opus', 100); addRecording(w, 'voice_251.opus', 100); addRecording(w, 'voice_252.opus', 100)
  w.Q.markUploaded('voice_250.opus'); w.Q.setActiveRecording('voice_252.opus')
  const r = w.Q.clearAllRecordings()
  ok(r.removed === 2 && !r.busy && JSON.stringify(files()) === '["voice_252.opus"]', 'removes every recording except the one being written -> ' + JSON.stringify(r) + ' ' + JSON.stringify(files()))
  ok(uploaded().length === 0, 'the uploaded index is reset')
  w.Q.setActiveRecording(null); ok(w.Q.pendingRecordings().length === 1, 'queue now holds only what was left')
  const w2 = await world(); addRecording(w2, 'voice_260.opus', 100000); w2.hooks.before = async () => { if (!w2.cleared) { w2.cleared = w2.Q.clearAllRecordings() } }
  w2.Q.retryPendingUploads(); await finished(w2)
  ok(w2.cleared && w2.cleared.busy === true && w2.cleared.removed === 0, 'refuses while an upload is running -> ' + JSON.stringify(w2.cleared))
  const w3 = await world(); const empty = w3.Q.clearAllRecordings(); ok(empty.removed === 0 && !empty.busy, 'works on an empty watch') }

console.log('16. a recording the phone cannot wrap is that file\'s own problem: skipped, not blocking')
{ const w = await world()
  const garbage = Buffer.alloc(20000, 7); garbage.writeUInt32BE(0xffffffff, 0)                  // a length prefix that runs far past the file
  addRecording(w, 'voice_300.opus', garbage); const good = addRecording(w, 'voice_301.opus', 20000)
  w.Q.retryPendingUploads(); await finished(w)
  const last = w.states.at(-1)
  ok(w.posts.length === 1 && w.posts[0].messageId === 'voice_301.opus' && sameAudio(w.posts[0].audio, good), 'the good recording behind it was delivered')
  ok(last.state === 'error' && /cannot pack recording/.test(last.message) && last.pending === 1, 'the failure is reported with the queue count -> ' + JSON.stringify(last))
  ok(files().includes('voice_300.opus') && JSON.stringify(uploaded()) === '["voice_301.opus"]', 'the unpackable file is kept, not marked uploaded')
  ok(w.requests.filter((r) => r.file === 'voice_300.opus').length === 2, 'it is not retried or restarted in a loop (its own 2 chunks only) -> ' + w.requests.filter((r) => r.file === 'voice_300.opus').length) }

console.log('17. a recording cut mid-packet is trimmed and still delivered')
{ const w = await world(); const whole = framedRecording(40000); const cut = whole.subarray(0, whole.length - 5)
  addRecording(w, 'voice_310.opus', cut); w.Q.retryPendingUploads(); await finished(w)
  const ogg = readOgg(Buffer.from(w.posts[0].audio, 'base64'))
  ok(w.states.at(-1).state === 'done' && ogg.problems.length === 0, 'delivered as a clean Ogg file -> ' + w.states.at(-1).state)
  ok(samePackets(ogg.audio, zeppPackets(cut)) && ogg.audio.length === zeppPackets(whole).length - 1, 'all complete packets kept, only the partial last one dropped (' + ogg.audio.length + ' packets)') }

console.log('18. what the server is sent')
{ const w = await world(); addRecording(w, 'voice_320.opus', 3000); w.Q.retryPendingUploads(); await finished(w)
  const post = w.posts[0]; const bytes = Buffer.from(post.audio, 'base64')
  ok(Object.keys(post).filter((k) => k !== 'url' && k !== 'auth').sort().join() === 'audio,messageId', 'the request body is just { messageId, audio } -> ' + Object.keys(post).join())
  ok(bytes.toString('latin1', 0, 4) === 'OggS' && bytes.toString('latin1', 28, 36) === 'OpusHead', 'audio is an Ogg file that starts with an OpusHead packet')
  const ogg = readOgg(bytes); const head = ogg.head
  ok(head[9] === 1 && head.readUInt32LE(12) === 16000 && head.readUInt16LE(10) === 0 && head[18] === 0, 'OpusHead: mono, input rate 16000, pre-skip 0, mapping family 0')
  ok(ogg.tags.toString('latin1', 12, 12 + ogg.tags.readUInt32LE(8)) === 'pigeondrop', 'OpusTags names this app as the vendor')
  const last = ogg.pages[ogg.pages.length - 1]
  ok(last.granule === BigInt(ogg.audio.length * 960), 'the last granule position counts every packet as 20 ms at 48 kHz (' + last.granule + ')') }

console.log('19. every chunk carries the device token; the server receives it as a bearer token')
{ const w = await world(); addRecording(w, 'voice_400.opus', 40000); w.Q.retryPendingUploads(); await finished(w)
  ok(w.requests.length > 1 && w.requests.every((r) => r.token === 'pdd_test_token_0123456789'), 'all ' + w.requests.length + ' chunk messages carry the token')
  ok(w.posts.length === 1 && w.posts[0].auth === 'Bearer pdd_test_token_0123456789', 'the one POST to the server has Authorization: Bearer <token> -> ' + w.posts[0].auth)
  ok(!JSON.stringify(w.posts[0]).includes('"deviceToken"') && !w.posts[0].audio.includes('pdd_'), 'the token is not in the request body') }
{ const w = await world(); addRecording(w, 'voice_401.opus', 20000); device.token = 'pdd_another_token_AAAAAAAAAAAA'
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.posts[0].auth === 'Bearer pdd_another_token_AAAAAAAAAAAA', 'the token is read when the upload starts, so a watch paired again uses the new one') }

console.log('20. a watch that is not paired sends nothing, says why, and keeps its recordings')
{ const w = await world(); device.token = null; addRecording(w, 'voice_410.opus', 20000)
  w.Q.retryPendingUploads(); await tick()
  ok(w.requests.length === 0 && w.posts.length === 0 && uploaded().length === 0, 'nothing left the watch')
  ok(w.states.length === 1 && w.states[0].state === 'unpaired' && w.states[0].pending === 1 && w.states[0].revoked === false, 'one "unpaired" state with the queue size -> ' + JSON.stringify(w.states))
  ok(files().includes('voice_410.opus'), 'the recording is kept')
  w.Q.retryPendingUploads(); await tick(); ok(w.states.length === 2, 'every trigger reports it again (app open, reconnect, new recording)')
  device.token = 'pdd_test_token_0123456789'; w.Q.retryPendingUploads(); await until(() => uploaded().length === 1)
  ok(JSON.stringify(uploaded()) === '["voice_410.opus"]', 'and it goes out as soon as the watch is paired') }
{ const w = await world(); device.token = null; w.Q.retryPendingUploads(); await tick()
  ok(w.states.length === 0, 'with nothing queued, being unpaired is silent') }

console.log('21. the server refuses the token (device revoked): the watch forgets it and says so')
for (const [label, status, reply] of [['error code in the body', 200, { ok: false, code: 'unauthorized', error: 'missing or invalid device token' }], ['HTTP 401 with a non-JSON body', 401, '<html>Unauthorized</html>']]) {
  const w = await world(); addRecording(w, 'voice_420.opus', 20000); addRecording(w, 'voice_421.opus', 20000)
  w.serverStatus = status; w.serverReply = reply
  w.Q.retryPendingUploads(); await finished(w)
  const last = w.states.at(-1)
  ok(last.state === 'unpaired' && last.revoked === true && last.pending === 2, label + ' -> state ' + JSON.stringify(last))
  ok(device.token === null && device.cleared === 1, label + ': the token was forgotten (clearDevice called once)')
  ok(uploaded().length === 0 && files().length === 2, label + ': nothing marked uploaded, both recordings kept')
  ok(!w.requests.some((r) => r.file === 'voice_421.opus'), label + ': the sweep stopped at the first recording')
  const before = w.requests.length; w.Q.retryPendingUploads(); await tick()
  ok(w.requests.length === before && w.states.at(-1).state === 'unpaired' && w.states.at(-1).revoked === false, label + ': the next trigger sends nothing and reports plainly "not paired"') }

console.log('22. other server answers')
{ const w = await world(); addRecording(w, 'voice_430.opus', 20000); w.serverReply = { ok: true, id: 'x', messageId: 'voice_430.opus', bytesReceived: 5, duplicate: true }
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'done' && JSON.stringify(uploaded()) === '["voice_430.opus"]', 'a duplicate answer (the server already had it) counts as delivered') }
{ const w = await world(); addRecording(w, 'voice_431.opus', 20000); w.serverStatus = 500; w.serverReply = { ok: false, code: 'internal', error: 'internal error' }
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'error' && /internal error/.test(w.states.at(-1).message) && device.token !== null && uploaded().length === 0, 'a server error is an error, and the token is kept') }
{ const w = await world(); addRecording(w, 'voice_432.opus', 20000); w.serverReply = { ok: false, code: 'invalid_request', error: 'audio is not valid base64' }
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'error' && device.token !== null, 'a refused request is not mistaken for a revoked device') }

console.log('23. the server says how full the account is: remembered, and reported as an alert only when it is news')
const storageFile = () => path.join(ctl.root, 'storage.json')
const storedState = () => (fs.existsSync(storageFile()) ? JSON.parse(fs.readFileSync(storageFile(), 'utf8')).state : null)
const accepted = (name, extra) => ({ ok: true, id: 'm-' + name, messageId: name, bytesReceived: 5, duplicate: false, ...extra })
// One sweep of one recording whose answer says `says`, on a watch that had `before` remembered; what the sweep reported.
const sweepWith = async (before, says, extra) => {
  const w = await world(); addRecording(w, 'voice_5' + (++T) + '.opus', 20000)
  if (before) fs.writeFileSync(storageFile(), JSON.stringify({ state: before }))
  w.serverReply = accepted('x', says === undefined ? {} : { storage: says, ...extra })
  w.Q.retryPendingUploads(); await finished(w)
  return { w, last: w.states.at(-1) }
}
for (const [before, says, alert, kept, label] of [
  [null, 'nearly_full', 'nearly_full', 'nearly_full', 'it just became nearly full: alert'],
  ['nearly_full', 'nearly_full', null, 'nearly_full', 'still nearly full: no new alert (not at every upload)'],
  ['full', 'nearly_full', null, 'nearly_full', 'better than before but not yet ok: no alert'],
  ['nearly_full', 'full', 'full', 'full', 'old recordings were just deleted: alert'],
  [null, 'full', 'full', 'full', 'straight to full: alert'],
  ['full', 'full', 'full', 'full', 'deleting again: alert again (each deletion is news)'],
  ['full', 'ok', null, null, 'room again: no alert, and the warning is forgotten'],
  [null, 'ok', null, null, 'ok stays quiet'],
  ['nearly_full', 'ok', null, null, 'room again after nearly full'],
]) {
  const { last } = await sweepWith(before, { state: says, usedBytes: 1, limitBytes: 100 })
  ok(last.state === 'done' && last.alert === alert && storedState() === kept, 'was ' + before + ', server says ' + says + ' -> alert ' + alert + ', remembered ' + kept + ' (' + label + ')') }
{ const { last } = await sweepWith('full', undefined)    // the default answer has no storage at all (an older server)
  ok(last.state === 'done' && last.alert === null && storedState() === 'full', 'no word about storage: no alert, what was remembered is left as it was') }
{ const { last } = await sweepWith(null, { state: 'exploded' })
  ok(last.alert === null && storedState() === null, 'a state this app does not know is ignored') }
{ const w = await world(); addRecording(w, 'voice_550.opus', 20000)
  w.serverReply = accepted('x', { duplicate: true, storage: { state: 'full' } }); w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).alert === 'full', 'a repeat of an upload (the server already had it) can alert too') }
{ // a queue of recordings gives one alert, the strongest
  for (const [first, second, want] of [['nearly_full', 'full', 'full'], ['full', 'nearly_full', 'full'], ['nearly_full', 'nearly_full', 'nearly_full'], ['full', 'full', 'full']]) {
    const w = await world(); addRecording(w, 'voice_560.opus', 20000); addRecording(w, 'voice_561.opus', 20000)
    w.replyFor = (body, n) => ({ status: 200, reply: accepted(body.messageId, { storage: { state: n === 1 ? first : second } }) })
    w.Q.retryPendingUploads(); await finished(w)
    ok(w.states.filter((s) => s.state === 'done').length === 1 && w.states.at(-1).alert === want, 'two recordings, answers ' + first + ' then ' + second + ' -> one "done" with alert ' + want + ' (' + w.states.at(-1).alert + ')') } }
{ // one world, two sweeps: what the first one learned is not reported again by the second
  const w = await world(); addRecording(w, 'voice_570.opus', 20000)
  w.serverReply = accepted('a', { storage: { state: 'full' } })
  w.Q.retryPendingUploads(); await finished(w)
  w.states.length = 0; addRecording(w, 'voice_571.opus', 20000); w.serverReply = accepted('b')
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'done' && w.states.at(-1).alert === null, 'a later sweep with no word about storage has no alert -> ' + JSON.stringify(w.states.at(-1))) }
{ // a sweep that ends with a skipped (unreadable) file still reports what it learned
  const w = await world(); addRecording(w, 'voice_580.opus', 20000); addRecording(w, 'voice_581.opus', 20000)
  w.replyFor = (body) => (body.messageId === 'voice_580.opus' ? { status: 413, reply: { ok: false, code: 'payload_too_large' } } : { status: 200, reply: accepted(body.messageId, { storage: { state: 'full' } }) })
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'error' && w.states.at(-1).alert === 'full', 'an error ending with a skipped file still carries the alert -> ' + JSON.stringify(w.states.at(-1))) }
{ const w = await world(); addRecording(w, 'voice_590.opus', 20000); ctl.failWrite = (p) => p === 'storage.json'
  w.serverReply = accepted('x', { storage: { state: 'full' } })
  const orig = console.log; console.log = () => {}
  try { w.Q.retryPendingUploads(); await finished(w) } finally { console.log = orig }
  ok(w.states.at(-1).state === 'done' && w.states.at(-1).alert === 'full' && JSON.stringify(uploaded()) === '["voice_590.opus"]', 'a watch that cannot keep the state still uploads, marks the recording sent, and still raises the alert') }

console.log('24. the server refuses a recording as too large: that file is skipped, the others go on')
for (const [label, status, reply] of [['413 with the error code', 413, { ok: false, code: 'payload_too_large', error: 'a recording may be at most 524288 bytes' }], ['413 from a proxy, not JSON', 413, '<html>Request Entity Too Large</html>'], ['the code in a 200-looking body', 200, { ok: false, code: 'payload_too_large', error: 'too big' }]]) {
  const w = await world(); addRecording(w, 'voice_600.opus', 20000); addRecording(w, 'voice_601.opus', 20000)
  w.replyFor = (body) => (body.messageId === 'voice_600.opus' ? { status, reply } : null)
  w.Q.retryPendingUploads(); await finished(w)
  const last = w.states.at(-1)
  ok(w.posts.length === 2 && JSON.stringify(uploaded()) === '["voice_601.opus"]', label + ': the next recording was still sent, the large one was not marked sent')
  ok(last.state === 'error' && last.message === 'server: recording too large' && last.pending === 1, label + ' -> ' + JSON.stringify(last))
  ok(files().includes('voice_600.opus') && device.token !== null && device.cleared === 0, label + ': kept on the watch, token kept') }

console.log('25. the server says this watch uploads too often (429): stop, say so, keep everything, try again later')
for (const [label, status, reply] of [['429 with the error code', 429, { ok: false, code: 'too_many_attempts', error: 'too many attempts' }], ['429 from a proxy, not JSON', 429, 'Too Many Requests'], ['the code in a 200-looking body', 200, { ok: false, code: 'too_many_attempts', error: 'too many attempts' }]]) {
  const w = await world(); addRecording(w, 'voice_610.opus', 20000); addRecording(w, 'voice_611.opus', 20000)
  w.serverStatus = status; w.serverReply = reply
  w.Q.retryPendingUploads(); await finished(w)
  const last = w.states.at(-1)
  ok(last.state === 'error' && last.message === 'Слишком много загрузок.\nПовторите позже' && last.pending === 2, label + ' -> ' + JSON.stringify(last))
  ok(w.posts.length === 1 && uploaded().length === 0 && files().length === 2, label + ': the sweep stopped at the first refusal, nothing was marked sent or deleted')
  ok(device.token !== null && device.cleared === 0, label + ': the watch is still paired')
  w.serverStatus = 200; w.serverReply = null
  w.Q.retryPendingUploads(); await until(() => uploaded().length === 2)
  ok(uploaded().length === 2, label + ': the next trigger delivers both once the server allows it') }

console.log('26. the watch is unpaired (token refused): the storage warning is forgotten with it')
{ const w = await world(); addRecording(w, 'voice_620.opus', 20000); fs.writeFileSync(storageFile(), '{"state":"full"}')
  w.serverStatus = 401; w.serverReply = '<html>Unauthorized</html>'
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'unpaired' && w.states.at(-1).revoked === true && !fs.existsSync(storageFile()), 'revoked: the warning about the old account is gone') }
{ const w = await world(); device.token = null; addRecording(w, 'voice_630.opus', 20000); fs.writeFileSync(storageFile(), '{"state":"full"}')
  w.Q.retryPendingUploads(); await tick()
  ok(fs.existsSync(storageFile()), 'a watch that was merely never paired this session keeps what it knew') }

console.log('\n' + (failures ? failures + ' FAILURE(S)' : 'ALL CHECKS PASS'))
process.exit(failures ? 1 : 0)
