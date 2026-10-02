// Upload protocol: chunked, acknowledged sending of recordings from the watch to the phone
// and on to the server (utils/uploadQueue.js + app-side/uploads.js), including every failure mode.

import fs from 'fs'; import os from 'os'; import path from 'path'; import crypto from 'crypto'
import { ctl, resetFds, fds } from './stubs/fs.mjs'
import { generate, fileUrl, stubPath } from './helpers/load.mjs'
// utils/uploadQueue.js and app-side/uploads.js are the real source files; only the platform pieces are stubbed.
const FS = stubPath('fs.mjs'), DEPS = stubPath('upload-deps.mjs')
const RECORDINGS = generate('utils/recordings.js', { '@zos/fs': FS })
const QUEUE = generate('utils/uploadQueue.js', { '@zos/fs': FS, './recordings': RECORDINGS, './account': DEPS, '../config': DEPS })
const UPLOADS = generate('app-side/uploads.js', {})
const realSetTimeout = setTimeout
globalThis.setTimeout = (fn) => realSetTimeout(fn, 0)           // collapse every pacing/retry delay
let T = 0, failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const tick = () => new Promise((r) => realSetTimeout(r, 25))
const until = async (cond, ms = 4000) => { const t0 = Date.now(); while (!cond() && Date.now() - t0 < ms) await tick(); return cond() }

async function world() {
  const n = ++T
  ctl.root = fs.mkdtempSync(path.join(os.tmpdir(), 'wd-')); ctl.shortRead = null; ctl.readCalls = 0; ctl.failOpen = ctl.failStat = ctl.failWhole = null; resetFds()
  const Q = await import(fileUrl(QUEUE) + '?t=' + n)
  const U = await import(fileUrl(UPLOADS) + '?t=' + n)
  const w = { Q, U, states: [], requests: [], posts: [], inflight: 0, maxInflight: 0, failRequests: new Set(), serverReply: null, serverThrows: false, hooks: {}, now: Date.now() }
  Q.onUploadState((s) => w.states.push(s))
  const builder = {
    request: async (msg, opts) => {
      w.inflight++; w.maxInflight = Math.max(w.maxInflight, w.inflight)
      try {
        const wire = JSON.parse(JSON.stringify(msg))                          // what actually crosses BLE
        w.requests.push({ file: wire.fileName, index: wire.index, total: wire.total, id: wire.uploadId, bytes: wire.data.length })
        if (w.hooks.before) await w.hooks.before(wire, w)
        if (w.failRequests.has(w.requests.length)) throw new Error('Timed out in ' + opts.timeout + 'ms.')
        return JSON.parse(JSON.stringify(await U.handleUploadChunk(wire, 'https://default.example')))
      } finally { w.inflight-- }
    },
  }
  globalThis.getApp = () => ({ _options: { globalData: { messageBuilder: builder } } })
  globalThis.fetch = async (o) => {
    const body = JSON.parse(o.body); w.posts.push({ url: o.url, ...body })
    if (w.serverThrows) throw new Error('Network down')
    const reply = w.serverReply || { ok: true, bytesReceived: Buffer.from(body.audio, 'base64').length, savedAs: 'a.opus', wavFile: 'a.wav' }
    return { status: 200, body: JSON.stringify(reply) }
  }
  return w
}
function addRecording(w, name, bytes) {
  const dir = path.join(ctl.root, 'voice_messages'); fs.mkdirSync(dir, { recursive: true })
  const buf = Buffer.isBuffer(bytes) ? bytes : crypto.randomBytes(bytes)
  fs.writeFileSync(path.join(dir, name), buf); return buf
}
const uploaded = () => { const f = path.join(ctl.root, 'uploads.json'); return fs.existsSync(f) ? JSON.parse(fs.readFileSync(f, 'utf8')).uploaded : [] }
const files = () => fs.readdirSync(path.join(ctl.root, 'voice_messages'))
const finished = (w) => until(() => w.states.some((s) => s.state === 'done' || s.state === 'error'))

console.log('1. 60 s recording (~160 KB): chunked, strictly sequential, server gets the identical body')
{ const w = await world(); const file = addRecording(w, 'voice_1000.opus', 163840)
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.requests.length === 11 && w.requests.every((r, i) => r.index === i), '11 chunks in order 0..10 (ceil(163840/15360)) -> ' + w.requests.map((r) => r.index).join(','))
  ok(w.maxInflight === 1, 'never more than one request in flight (max ' + w.maxInflight + ')')
  ok(w.requests.every((r) => r.bytes <= 20480 + 0), 'every chunk message is small: max base64 ' + Math.max(...w.requests.map((r) => r.bytes)) + ' chars (vs 218 454 in one piece)')
  ok(w.posts.length === 1 && w.posts[0].audio === file.toString('base64'), 'server received ONE POST whose audio is byte-identical base64 of the file')
  ok(w.posts[0].url === 'https://user-own.example/messages' && w.posts[0].fileName === 'voice_1000.opus', 'posted to the account serverUrl at /messages, fileName preserved')
  ok(JSON.stringify(uploaded()) === '["voice_1000.opus"]', 'marked uploaded only after the server confirmed')
  ok(w.states[0].state === 'sending' && w.states.at(-1).state === 'done' && w.states.at(-1).pending === 0, 'UI states: sending ... done(pending 0)')
  ok(w.states.filter((s) => s.state === 'sending').map((s) => s.chunk + '/' + s.total).join(' ') === Array.from({ length: 11 }, (_, i) => (i + 1) + '/11').join(' '), 'progress reports chunk 1/11 .. 11/11') }

console.log('2. size edge cases: 1 B, 100 B, exactly 1 chunk, 3 chunks, one byte over')
for (const size of [1, 100, 15360, 46080, 15361]) {
  const w = await world(); const file = addRecording(w, 'voice_5.opus', size); w.Q.retryPendingUploads(); await finished(w)
  ok(w.posts.length === 1 && w.posts[0].audio === file.toString('base64') && w.requests.length === Math.ceil(size / 15360), size + ' B -> ' + w.requests.length + ' chunk(s), body identical') }

console.log('3. transport failures: retried per chunk, then give up cleanly')
{ const w = await world(); const file = addRecording(w, 'voice_7.opus', 100000); w.failRequests = new Set([3, 4])    // chunk index 2 fails twice
  w.Q.retryPendingUploads(); await finished(w)
  ok(w.states.at(-1).state === 'done' && w.posts[0].audio === file.toString('base64'), 'two timeouts on one chunk are retried; upload completes with identical body')
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
  ok(w.states.at(-1).state === 'done' && w.posts.length === 1 && w.posts[0].audio === file.toString('base64'), 'watch restarted that recording from chunk 0 and delivered the identical body once')
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
{ const w = await world(); const U = w.U; const base = { fileName: 'f', uploadId: 'u', index: 0, total: 2, data: 'AAAA' }
  const bad = async (patch) => (await U.handleUploadChunk({ ...base, ...patch }, 'x')).ok === false
  ok((await bad({ index: 2 })) && (await bad({ index: -1 })) && (await bad({ total: 0 })) && (await bad({ data: '' })) && (await bad({ data: 5 })) && (await bad({ fileName: '' })) && (await bad({ index: 1.5 })), 'out-of-range / empty / non-string fields rejected')
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
  ok(w.states.at(-1).state === 'done' && w.posts.length === 1 && w.posts[0].audio === file.toString('base64'), 'delivered with an identical body without any descriptor -> ' + w.states.at(-1).state) }

console.log('13. one unreadable recording must not block the ones behind it')
{ const w = await world(); addRecording(w, 'voice_230.opus', 20000); const good = addRecording(w, 'voice_231.opus', 20000)
  ctl.failOpen = (p) => p.includes('voice_230'); ctl.failWhole = (p) => p.includes('voice_230')
  w.Q.retryPendingUploads(); await finished(w)
  const last = w.states.at(-1)
  ok(w.posts.length === 1 && w.posts[0].fileName === 'voice_231.opus' && w.posts[0].audio === good.toString('base64'), 'the readable recording behind it was still delivered')
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

console.log('\n' + (failures ? failures + ' FAILURE(S)' : 'ALL CHECKS PASS'))
process.exit(failures ? 1 : 0)
