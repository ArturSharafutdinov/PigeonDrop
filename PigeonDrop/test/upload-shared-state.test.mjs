// utils/uploadQueue.js when several bundles carry their own copy of it.
//
// app.js and every page are separate bundles on the watch, each with its own copy of the queue module. Two copies
// are loaded here, one standing for app.js (whose BLE-reconnect trigger starts sweeps) and one for the main page
// (which writes recordings and shows the progress), over one shared `globalData`. What they have to agree on is
// in the shared state of the queue: the recording being written, the running sweep and the listener.

import fs from 'fs'; import os from 'os'; import path from 'path'
import { ctl, resetFds } from './stubs/fs.mjs'
import { device } from './stubs/upload-deps.mjs'
import { generate, fileUrl, stubPath, i18nModule } from './helpers/load.mjs'
import { framedRecording } from './helpers/ogg.mjs'

const FS = stubPath('fs.mjs'), DEPS = stubPath('upload-deps.mjs')
const RECORDINGS = generate('utils/recordings.js', { '@zos/fs': FS })
const STORAGE = generate('utils/storageState.js', { '@zos/fs': FS })
const QUEUE = generate('utils/uploadQueue.js', { '@zos/fs': FS, './recordings': RECORDINGS, './device': DEPS, './storageState': STORAGE, '../config': DEPS, '../i18n': i18nModule() })

const realSetTimeout = setTimeout
globalThis.setTimeout = (fn) => realSetTimeout(fn, 0)       // collapse every pacing and retry delay
const realNow = Date.now
let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const tick = () => new Promise((r) => realSetTimeout(r, 25))
const until = async (cond, ms = 4000) => { const t0 = realNow(); while (!cond() && realNow() - t0 < ms) await tick(); return cond() }

let n = 0
async function world() {
  n++
  device.token = 'pdd_test_token_0123456789'
  ctl.root = fs.mkdtempSync(path.join(os.tmpdir(), 'wd-')); ctl.shortRead = null; ctl.readCalls = 0
  ctl.failOpen = ctl.failStat = ctl.failWhole = ctl.failWrite = null; resetFds()

  // The phone, as far as the queue can tell: it answers a ping, acknowledges chunks and confirms the last one.
  const w = { chunks: [], gate: null, storage: undefined, failFor: null, states: [], later: [] }
  const builder = {
    request: async (msg) => {
      if (msg.type === 'ping') return { ok: true }
      w.chunks.push(msg.fileName + '#' + msg.index)
      if (w.failFor && w.failFor(msg)) throw new Error('Timed out in 30000ms.')
      if (w.gate) await w.gate
      return msg.index === msg.total - 1 ? { ok: true, done: true, storage: w.storage } : { ok: true }
    },
  }
  w.globalData = { messageBuilder: builder }
  globalThis.getApp = () => ({ _options: { globalData: w.globalData } })
  w.app = await import(fileUrl(QUEUE) + '?app' + n)      // what app.js bundles
  w.page = await import(fileUrl(QUEUE) + '?page' + n)    // what page/index.js bundles
  return w
}
function addRecording(name, bytes = 100) {
  const dir = path.join(ctl.root, 'voice_messages'); fs.mkdirSync(dir, { recursive: true })
  fs.writeFileSync(path.join(dir, name), framedRecording(bytes))
}
const uploaded = () => { const f = path.join(ctl.root, 'uploads.json'); return fs.existsSync(f) ? JSON.parse(fs.readFileSync(f, 'utf8')).uploaded : [] }
const files = () => fs.readdirSync(path.join(ctl.root, 'voice_messages'))
const hold = () => { let open; const gate = new Promise((r) => { open = r }); return { gate, open } }

console.log('1. a sweep started by one bundle does not take the recording another bundle is still writing')
{ const w = await world(); addRecording('voice_1000.opus'); addRecording('voice_2000.opus')
  w.page.setActiveRecording('voice_2000.opus')          // the main page is recording
  w.app.retryPendingUploads()                            // BLE reconnected: app.js sweeps
  await until(() => uploaded().length === 1)
  ok(JSON.stringify(uploaded()) === '["voice_1000.opus"]' && w.chunks.every((c) => c.startsWith('voice_1000')), 'only the finished recording was sent -> ' + w.chunks.join(' '))
  ok(w.app.pendingRecordings().length === 0 && w.page.pendingRecordings().length === 0, 'both copies agree there is nothing else waiting')
  w.page.setActiveRecording(null)
  ok(w.app.pendingRecordings().map((r) => r.fileName).join() === 'voice_2000.opus', 'when the recording ends, every copy sees it as waiting') }

console.log('2. the page hears about a sweep that another bundle runs')
{ const w = await world(); addRecording('voice_1000.opus')
  w.page.onUploadState((s) => w.states.push(s))
  w.app.retryPendingUploads()
  await until(() => w.states.some((s) => s.state === 'done'))
  ok(w.states.map((s) => s.state).join() === 'connecting,sending,done', 'connecting, sending, done reached the page -> ' + w.states.map((s) => s.state).join()) }

console.log('3. one sweep at a time, whichever bundle starts it')
{ const w = await world(); addRecording('voice_1000.opus', 40000)
  const g = hold(); w.gate = g.gate
  w.page.retryPendingUploads()                            // the page sweeps and waits for the phone
  await until(() => w.chunks.length === 1)
  w.app.retryPendingUploads()                             // BLE flaps: app.js would sweep too
  await tick(); await tick()
  ok(w.chunks.length === 1, 'the second bundle did not start a competing sweep -> ' + w.chunks.join(' '))
  ok(JSON.stringify(w.page.clearAllRecordings()) === '{"removed":0,"busy":true}' && files().length === 1, 'clearing the memory is refused as busy, from the other bundle too')
  g.open(); w.gate = null
  await until(() => uploaded().length === 1)
  await tick()
  addRecording('voice_2000.opus'); w.app.retryPendingUploads()
  await until(() => uploaded().length === 2)
  ok(uploaded().length === 2, 'once the sweep is over the next one starts normally') }

console.log('4. a sweep that has gone quiet for good does not block the others forever')
{ const w = await world(); addRecording('voice_1000.opus')
  w.gate = new Promise(() => {})                          // the phone never answers, no timeout ever fires (a page torn down)
  w.page.retryPendingUploads(); await until(() => w.chunks.length === 1)
  w.app.retryPendingUploads(); await tick()
  ok(w.chunks.length === 1, 'while it is recent, it still counts as running')
  Date.now = () => realNow() + 3 * 60 * 1000
  w.gate = null
  w.app.retryPendingUploads()
  await until(() => uploaded().length === 1)
  Date.now = realNow
  ok(JSON.stringify(uploaded()) === '["voice_1000.opus"]', 'three minutes later a new sweep is allowed and delivers') }

console.log('5. pruning never runs under a sweep')
{ const w = await world()
  for (let i = 1; i <= 52; i++) addRecording('voice_' + (1000 + i) + '.opus', 20)   // two more than the 50 that may wait
  const g = hold(); w.gate = g.gate
  w.page.retryPendingUploads(); await until(() => w.chunks.length === 1)
  w.app.pruneRecordings()
  ok(files().length === 52, 'the oldest waiting recordings (the ones being sent first) were not deleted -> ' + files().length + ' files')
  g.open(); w.gate = null
  await until(() => uploaded().length === 52, 8000)
  w.app.pruneRecordings()
  ok(files().length === 20, 'after the sweep the cap applies again (20 uploaded kept) -> ' + files().length) }

console.log('6. a storage warning is not lost when a later recording of the same sweep fails')
{ const w = await world(); addRecording('voice_1000.opus'); addRecording('voice_2000.opus')
  w.storage = { state: 'nearly_full' }
  w.failFor = (msg) => msg.fileName === 'voice_2000.opus'
  w.page.onUploadState((s) => w.states.push(s))
  w.page.retryPendingUploads()
  await until(() => w.states.some((s) => s.state === 'error'))
  const last = w.states.at(-1)
  ok(last.state === 'error' && last.kind === 'phone' && last.alert === 'nearly_full', 'the error carries the warning the first recording brought -> ' + JSON.stringify(last))
  ok(JSON.stringify(uploaded()) === '["voice_1000.opus"]', '(the first recording did go through, the second did not)') }

console.log('7. a warning that arrives while nobody listens is handed to the next page')
{ const w = await world(); addRecording('voice_1000.opus')
  w.storage = { state: 'full' }
  w.app.retryPendingUploads()                            // no page is open: app.js sweeps alone
  await until(() => uploaded().length === 1); await tick()
  const got = []
  w.page.onUploadState((s) => got.push(s))
  ok(got.length === 0, 'it is not delivered inside onUploadState (the page is still being built)')
  await until(() => got.length === 1)
  ok(JSON.stringify(got) === '[{"state":"alert","alert":"full"}]', 'then it is delivered once -> ' + JSON.stringify(got))
  w.page.onUploadState(null); w.page.onUploadState((s) => got.push(s)); await tick(); await tick()
  ok(got.length === 1, 'and not again for the next listener') }
{ const w = await world(); addRecording('voice_1000.opus'); addRecording('voice_2000.opus')
  w.storage = { state: 'nearly_full' }
  w.app.retryPendingUploads(); await until(() => uploaded().length === 2)
  addRecording('voice_3000.opus'); w.storage = { state: 'full' }
  w.app.retryPendingUploads(); await until(() => uploaded().length === 3); await tick()
  const got = []; w.page.onUploadState((s) => got.push(s)); await until(() => got.length > 0)
  ok(got.length === 1 && got[0].alert === 'full', 'kept warnings collapse into the strongest one -> ' + JSON.stringify(got)) }

console.log('8. without an app (no getApp) the queue still keeps its state, privately')
{ const w = await world(); addRecording('voice_1000.opus')
  delete globalThis.getApp
  w.page.setActiveRecording('voice_1000.opus')
  ok(w.page.pendingRecordings().length === 0, 'the active recording is excluded, nothing throws')
  w.page.setActiveRecording(null) }

console.log(failures === 0 ? '\nALL CHECKS PASS' : '\n' + failures + ' CHECK(S) FAILED')
if (failures) process.exitCode = 1
