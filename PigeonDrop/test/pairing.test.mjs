// Pairing the watch with an account: the phone's half (app-side/pairing.js) and the watch's screen (page/pair.js).

import * as stubs from './stubs/pair-page.mjs'
import * as zc from './stubs/zos-crypto.mjs'
import { generate, fileUrl, stubPath, i18nModule } from './helpers/load.mjs'
import { sealWithWebCrypto, sealJson, payload, b64u, newSecretBytes } from './helpers/relay.mjs'
import { randomBytes } from 'node:crypto'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }

// ---- the phone's half ------------------------------------------------------------------------
const TIMEOUT = generate('app-side/timeout.js', {})
const PAIRING = generate('app-side/pairing.js', { './timeout': TIMEOUT })
const P = await import(fileUrl(PAIRING))
const fetches = []
let serverAnswer = null
globalThis.fetch = async (o) => {
  fetches.push({ url: o.url, method: o.method, headers: o.headers, body: JSON.parse(o.body) })
  if (serverAnswer instanceof Error) throw serverAnswer
  return typeof serverAnswer === 'function' ? serverAnswer(o) : { status: 200, body: typeof serverAnswer === 'string' ? serverAnswer : JSON.stringify(serverAnswer) }
}
const answer = (a) => { serverAnswer = a; fetches.length = 0 }
const CODES = { deviceCode: 'dev-secret', userCode: 'BCDF-GHJK', verificationUri: 'https://s.example/pair', verificationUriComplete: 'https://s.example/pair?code=BCDF-GHJK', expiresIn: 600, interval: 5 }

console.log('1. startPairing: asks the server for codes and hands the watch what it needs')
{ answer(CODES); const r = await P.startPairing({ clientName: 'Amazfit Balance' }, 'https://s.example')
  ok(fetches.length === 1 && fetches[0].url === 'https://s.example/v1/device/code' && fetches[0].method === 'POST' && fetches[0].headers['Content-Type'] === 'application/json', 'POST /v1/device/code as JSON')
  ok(JSON.stringify(fetches[0].body) === '{"clientName":"Amazfit Balance"}', 'the watch\'s name is sent -> ' + JSON.stringify(fetches[0].body))
  ok(r.ok === true && r.deviceCode === 'dev-secret' && r.userCode === 'BCDF-GHJK' && r.verificationUriComplete.endsWith('?code=BCDF-GHJK') && r.expiresIn === 600 && r.interval === 5, 'reply carries the codes, the address for the QR code and the timings -> ' + JSON.stringify(r))
  ok(!('verificationUri' in r), 'nothing else is passed along') }
{ answer(CODES); await P.startPairing({}, 'https://s.example'); await P.startPairing(null, 'https://s.example'); await P.startPairing({ clientName: 5 }, 'https://s.example')
  ok(fetches.every((f) => JSON.stringify(f.body) === '{}'), 'no usable name -> an empty object, not a crash') }
{ answer(CODES); await P.startPairing({ clientName: 'x'.repeat(200) }, 'https://s.example')
  ok(fetches[0].body.clientName.length === 64, 'an over-long name is cut to the 64 characters the server accepts') }

console.log('2. startPairing: failures come back as replies, never as exceptions')
{ answer({ ok: false, code: 'too_many_attempts', error: 'too many failed attempts, try again later' }); const r = await P.startPairing({}, 'https://s.example')
  ok(r.ok === false && r.code === 'too_many_attempts' && /too many/.test(r.error), 'the server\'s refusal keeps its code -> ' + JSON.stringify(r)) }
{ answer(new Error('Network down')); const r = await P.startPairing({}, 'https://s.example')
  ok(r.ok === false && r.code === undefined && /Network down/.test(r.error), 'unreachable server -> ok:false without a code -> ' + JSON.stringify(r))
  ok(r.noServer === true, '...marked as "the phone cannot reach the server", which the watch tells from a phone that does not answer') }
{ const realSetTimeout = globalThis.setTimeout, asked = []; globalThis.setTimeout = (fn, ms) => { asked.push(ms); return realSetTimeout(fn, 0) }   // the deadline passes at once
  answer(() => new Promise(() => {})); const start = await P.startPairing({}, 'https://s.example'); const poll = await P.pollPairing({ deviceCode: 'x' }, 'https://s.example')
  globalThis.setTimeout = realSetTimeout
  ok(asked.length === 2 && asked.every((ms) => ms === 10000), 'the phone gives the server 10 s, less than the 15 s the watch waits for these requests (REQUEST_TIMEOUT in page/pair.js) -> ' + JSON.stringify(asked))
  ok(start.ok === false && start.noServer === true && /no answer/.test(start.error), 'a request that never finishes ends at the phone\'s own deadline: no server, not silence -> ' + JSON.stringify(start))
  ok(poll.ok === false && poll.noServer === true, 'polling too -> ' + JSON.stringify(poll)) }
{ answer(() => ({ status: 502, body: '<html>Bad Gateway</html>' })); const r = await P.startPairing({}, 'https://s.example')
  ok(r.ok === false && r.noServer === true, 'a gateway\'s error page is "no server" as well -> ' + JSON.stringify(r)) }
{ answer({ ok: false, code: 'too_many_attempts', error: 'x' }); const r = await P.startPairing({}, 'https://s.example')
  ok(r.noServer === undefined, 'a refusal from the server itself is not "no server" -> ' + JSON.stringify(r)) }
for (const [label, body] of [['an HTML page', '<html>bad gateway</html>'], ['an empty body', ''], ['JSON of another shape', { hello: 'world' }], ['a code without the rest', { deviceCode: 'x' }]]) {
  answer(body); const r = await P.startPairing({}, 'https://s.example')
  ok(r.ok === false && typeof r.error === 'string', label + ' -> ok:false -> ' + JSON.stringify(r)) }

console.log('3. pollPairing')
{ answer({ status: 'pending', interval: 5 }); const r = await P.pollPairing({ deviceCode: 'dev-secret' }, 'https://s.example')
  ok(fetches[0].url === 'https://s.example/v1/device/token' && JSON.stringify(fetches[0].body) === '{"deviceCode":"dev-secret"}', 'POST /v1/device/token with the device code')
  ok(JSON.stringify(r) === '{"ok":true,"status":"pending","interval":5}', 'pending -> ' + JSON.stringify(r)) }
{ answer({ status: 'approved', deviceToken: 'pdd_abc', deviceId: 'id-9' }); const r = await P.pollPairing({ deviceCode: 'dev-secret' }, 'https://s.example')
  ok(r.ok === true && r.status === 'approved' && r.deviceToken === 'pdd_abc' && r.deviceId === 'id-9', 'approved -> the token reaches the watch -> ' + JSON.stringify(r)) }
{ answer({ status: 'approved' }); const r = await P.pollPairing({ deviceCode: 'x' }, 'https://s.example')
  ok(r.ok === false, 'approved without a token is not success') }
for (const code of ['expired_token', 'invalid_device_code']) {
  answer({ ok: false, code, error: 'whatever' }); const r = await P.pollPairing({ deviceCode: 'x' }, 'https://s.example')
  ok(r.ok === false && r.code === code, code + ' keeps its code so the watch can tell it from a network problem') }
{ const envelope = { v: 1, ct: 'c'.repeat(64), tag: 't'.repeat(43) }
  answer({ status: 'relayed', envelope: { ...envelope, note: 'not passed on' }, extra: 'nor this' }); const r = await P.pollPairing({ deviceCode: 'dev-secret' }, 'https://s.example')
  ok(JSON.stringify(r) === JSON.stringify({ ok: true, status: 'relayed', envelope }), 'relayed -> the envelope reaches the watch as it is, and nothing else does -> ' + JSON.stringify(r))
  for (const [label, bad] of [['no envelope', undefined], ['an envelope that is text', 'abc'], ['a version that is text', { ...envelope, v: '1' }], ['a ct that is not text', { ...envelope, ct: 5 }], ['a tag that is not text', { ...envelope, tag: null }], ['a ct that is far too long', { ...envelope, ct: 'c'.repeat(2001) }], ['a tag that is far too long', { ...envelope, tag: 't'.repeat(201) }]]) {
    answer({ status: 'relayed', envelope: bad }); const refused = await P.pollPairing({ deviceCode: 'x' }, 'https://s.example')
    ok(refused.ok === false && refused.status === undefined && !JSON.stringify(refused).includes('ccc'), 'relayed with ' + label + ' is not success, and says nothing of the content -> ' + JSON.stringify(refused)) } }
{ answer(new Error('Timed out')); const r = await P.pollPairing({ deviceCode: 'x' }, 'https://s.example'); ok(r.ok === false && r.code === undefined, 'a network problem has no code') }
{ answer(CODES); fetches.length = 0
  for (const bad of [null, {}, { deviceCode: '' }, { deviceCode: 5 }]) { const r = await P.pollPairing(bad, 'https://s.example'); ok(r.ok === false, 'a poll without a device code is refused: ' + JSON.stringify(bad)) }
  ok(fetches.length === 0, '...without calling the server') }

// ---- the watch's screen ----------------------------------------------------------------------
let opts, now = 5_000_000, nextId = 1, timers = []
globalThis.Page = (o) => { opts = o }
globalThis.setTimeout = (fn, ms) => { const id = nextId++; timers.push({ id, fn, ms, at: now + ms }); return id }
globalThis.clearTimeout = (id) => { timers = timers.filter((t) => t.id !== id) }
Date.now = () => now
const S = stubPath('pair-page.mjs')
const I18N = i18nModule()
const LINK = generate('utils/linkProblem.js', { '../i18n': I18N })   // the real texts: what the screen says is what is tested
const RELAY_FILE = generate('utils/relayCrypto.js', { '@zos/crypto': stubPath('zos-crypto.mjs'), './serverUrl': generate('utils/serverUrl.js', {}) })
const RELAY = await import(fileUrl(RELAY_FILE))   // the module the page uses: the same file, so the same instance
const PAGE = generate('page/pair.js', { '@zos/ui': S, '@zos/utils': S, '@zos/router': S, '@zos/device': S, '../utils/device': S, '../utils/storageState': S, '../utils/uploadQueue': S, '../utils/screen': S, '../utils/linkProblem': LINK, '../utils/relayCrypto': RELAY_FILE, '../i18n': I18N })
await import(fileUrl(PAGE))

// A scripted phone: each request waits for the test to answer it.
function scenario() {
  const s = { requests: [], timeouts: [], pending: [] }
  globalThis.getApp = () => ({ _options: { globalData: { messageBuilder: { request: (msg, opts) => new Promise((resolve, reject) => { s.requests.push(msg); s.timeouts.push(opts && opts.timeout); s.pending.push({ resolve, reject }) }) } } } })
  s.reply = async (value) => { s.pending.shift().resolve(value); await flush() }
  s.fail = async (err) => { s.pending.shift().reject(err); await flush() }
  return s
}
const flush = () => new Promise((r) => setImmediate(r))
const fresh = () => { stubs.widgets.length = 0; stubs.calls.length = 0; stubs.saved.length = 0; stubs.holds.length = 0; stubs.replaced.length = 0; timers = []; stubs.info.throws = false; stubs.info.value = { deviceName: 'Amazfit Test' }
  Object.assign(zc.behaviour, { generatedKeys: 'random', hmacFails: false, aesFails: false, throws: false, needKeyEncrypt: undefined, keyLength: 16 }); zc.resetKeys(); RELAY.forgetSelfTest()
  const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) }); return page }
const fire = async (ms) => { const due = timers.filter((t) => t.ms === ms); timers = timers.filter((t) => t.ms !== ms); due.forEach((t) => t.fn()); await flush() }
const next = async () => { const t = timers.shift(); now = Math.max(now, t.at); t.fn(); await flush(); return t.ms }
const status = () => stubs.widgets.find((w) => w.opts.y === 355).text
const qr = () => stubs.widgets.find((w) => w.type === 'QRCODE')
const timerDelays = () => timers.map((t) => t.ms)

console.log('4. the happy path: code, QR, polling, approval, token saved')
{ const ph = scenario(); const page = fresh(); page.build()
  ok(stubs.holds.length === 1 && stubs.holds[0] >= 10 * 60 * 1000 && stubs.holds[0] <= 15 * 60 * 1000, 'the screen is held for about the code\'s lifetime, no more -> ' + stubs.holds[0] + ' ms')
  ok(ph.requests.length === 1 && ph.requests[0].type === 'pairStart' && ph.requests[0].clientName === 'Amazfit Test', 'asks the phone for a code, naming the watch -> ' + JSON.stringify(ph.requests[0]))
  ok(ph.timeouts[0] === 15000 && ph.timeouts[0] > 10000, 'the watch waits 15 s for it: longer than the 10 s the phone gives the server, so "no server" always comes back as an answer -> ' + ph.timeouts[0])
  ok(!qr() && status() === 'Connecting to phone\nand server...', 'before the answer: no QR yet, "connecting" shown')
  await ph.reply({ ok: true, ...CODES })
  ok(qr() && /^https:\/\/s\.example\/pair\?code=BCDF-GHJK#k=[A-Za-z0-9_-]{22}$/.test(qr().opts.content), 'QR code points at the address the server gave, with the key for the Personal server tab after the # -> ' + (qr() && qr().opts.content))
  ok(/BCDF-GHJK/.test(status()), 'the code is also shown as text -> ' + JSON.stringify(status()))
  const o = qr().opts, corner = (x, y) => Math.hypot(x - 240, y - 240)
  ok(o.bg_w > 0 && o.bg_h > 0 && Math.max(corner(o.bg_x, o.bg_y), corner(o.bg_x + o.bg_w, o.bg_y + o.bg_h)) < 230, 'the QR\'s background is set and stays inside the round display (' + Math.round(Math.max(corner(o.bg_x, o.bg_y), corner(o.bg_x + o.bg_w, o.bg_y + o.bg_h))) + ' < 230)')
  ok(JSON.stringify(timerDelays()) === '[5000]', 'first poll after the interval -> ' + JSON.stringify(timerDelays()))
  await fire(5000)
  ok(ph.requests.length === 2 && ph.requests[1].type === 'pairPoll' && ph.requests[1].deviceCode === 'dev-secret', 'polls with the device code')
  await ph.reply({ ok: true, status: 'pending', interval: 5 })
  ok(JSON.stringify(timerDelays()) === '[5000]' && stubs.saved.length === 0, 'still waiting: next poll scheduled, nothing saved')
  await fire(5000); await ph.reply({ ok: true, status: 'approved', deviceToken: 'pdd_secret_token', deviceId: 'dev-id' })
  ok(stubs.saved.length === 1 && stubs.saved[0].token === 'pdd_secret_token' && stubs.saved[0].deviceId === 'dev-id', 'approved: the token is saved -> ' + JSON.stringify(stubs.saved))
  ok(/paired/.test(status()) && !status().includes('pdd_'), 'the screen says so, without showing the token -> ' + JSON.stringify(status()))
  ok(stubs.calls.includes('retryPendingUploads') && stubs.calls.lastIndexOf('releaseScreen') > stubs.calls.indexOf('holdScreen'), 'recordings that were waiting are sent, and the screen is released')
  ok(stubs.calls.indexOf('saveDevice') < stubs.calls.indexOf('clearStorageState') && stubs.calls.indexOf('clearStorageState') < stubs.calls.indexOf('retryPendingUploads'), 'the storage warning about the previous account is forgotten before the first upload for the new one -> ' + stubs.calls.filter((c) => /Device|Storage|retry/.test(c)).join(' '))
  ok(JSON.stringify(timerDelays()) === '[2500]', 'then, after a moment, it leaves the screen -> ' + JSON.stringify(timerDelays()))
  ok(!stubs.calls.includes('back'), 'it does not leave before that'); await fire(2500)
  ok(stubs.calls.includes('back'), '...and goes back to settings')
  ok(ph.requests.length === 3, 'no polls after success') }

console.log('5. polling pace')
for (const [interval, want] of [[5, 5000], [1, 3000], [0, 5000], [undefined, 5000], [10, 10000]]) {
  const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: true, ...CODES, interval })
  ok(timerDelays()[0] === want, 'server interval ' + interval + ' -> first poll after ' + timerDelays()[0] + ' ms (>= 3000, default 5000)') }
{ const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: true, ...CODES }); await fire(5000)
  await ph.reply({ ok: true, status: 'pending', interval: 1 })
  ok(timerDelays()[0] === 3000, 'a pending answer asking for 1 s is still held to the 3 s minimum') }

console.log('6. network trouble while waiting')
{ const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: true, ...CODES }); await fire(5000)
  await ph.fail(new Error('Timed out in 15000ms.'))
  ok(JSON.stringify(timerDelays()) === '[5000]' && stubs.saved.length === 0, 'a timeout is not the end: it polls again after the default pace')
  await fire(5000); await ph.reply({ ok: false, error: 'server: Network down' })
  ok(JSON.stringify(timerDelays()) === '[5000]', 'an unreachable server too')
  await fire(5000); await ph.reply({ ok: false, code: 'too_many_attempts', error: 'slow down' })
  ok(JSON.stringify(timerDelays()) === '[5000]', 'and being asked to slow down')
  await fire(5000); await ph.reply({ ok: true, status: 'approved', deviceToken: 'pdd_t', deviceId: 'i' })
  ok(stubs.saved.length === 1, 'after all that, approval still gets through') }
{ const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: true, ...CODES }); await fire(5000)
  await ph.reply({ ok: true, status: 'approved' })
  ok(stubs.saved.length === 0, 'an "approved" answer without a token is not taken for success') }

console.log('7. the code runs out')
for (const code of ['expired_token', 'invalid_device_code']) {
  const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: true, ...CODES }); await fire(5000)
  const polls = ph.requests.length; await ph.reply({ ok: false, code, error: 'x' })
  ok(/expired/.test(status()) && timers.length === 0 && stubs.calls.lastIndexOf('releaseScreen') > stubs.calls.indexOf('holdScreen'), code + ': the screen says the code ran out, polling stops, the screen is released -> ' + JSON.stringify(status()))
  ok(stubs.saved.length === 0 && ph.requests.length === polls, code + ': nothing saved, nothing requested again') }
{ const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: true, ...CODES, expiresIn: 20 }); const polls = ph.requests.length
  now += 25_000; await fire(5000)
  ok(ph.requests.length === polls && /expired/.test(status()), 'a code past its own lifetime is not polled at all -> requests ' + ph.requests.length) }

console.log('8. no code to start with')
{ const ph = scenario(); const page = fresh(); page.build()
  await ph.reply({ ok: false, noServer: true, error: 'server: Network down' })
  ok(/unreachable/.test(status()) && JSON.stringify(timerDelays()) === '[4000]' && !qr(), 'the first failure is shown and retried after 4 s, with no QR -> ' + JSON.stringify(status()))
  await fire(4000); ok(ph.requests.length === 2 && ph.requests[1].type === 'pairStart', 'it asks for a code again')
  await ph.fail(new Error('Timed out')); await fire(4000); await ph.reply({ ok: true, ...CODES })
  ok(qr() && /BCDF-GHJK/.test(status()), 'once the phone answers, the code appears') }
{ const ph = scenario(); const page = fresh(); page.build()
  for (let i = 0; i < 6; i++) { await ph.reply({ ok: false, error: 'rejected' }); if (timers.length) await fire(4000) }
  ok(ph.requests.length === 6 && timers.length === 0 && /Could not get/.test(status()), 'after 6 failed attempts it gives up with a message instead of retrying forever -> ' + ph.requests.length + ' requests')
  ok(stubs.calls.lastIndexOf('releaseScreen') > stubs.calls.indexOf('holdScreen'), 'and releases the screen') }
{ const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: false, code: 'too_many_attempts', error: 'x' })
  ok(/Too many/.test(status()) && timerDelays()[0] === 4000, 'too many attempts is explained, then retried') }

console.log('8b. the two link problems are told apart, with what to do about each')
{ const ph = scenario(); const page = fresh(); page.build(); await ph.fail(new Error('Timed out in 15000ms.'))
  ok(status() === 'Phone is not reachable\nRestart the\nZepp app', 'the phone does not answer (the request times out): restart Zepp -> ' + JSON.stringify(status()))
  await fire(4000); await ph.reply({ ok: false, noServer: true, error: 'server: no answer in 10 s' })
  ok(status() === 'Server is unreachable\nCheck the internet\non the phone', 'the phone answers that it cannot reach the server: check the internet -> ' + JSON.stringify(status()))
  await fire(4000); await ph.reply({ ok: false, code: 'invalid_request', error: 'x' })
  ok(status() === 'Could not get\na pairing code', 'any other refusal has neither piece of advice -> ' + JSON.stringify(status()))
  await fire(4000); await ph.reply({ ok: true, ...CODES })
  ok(qr() && /BCDF-GHJK/.test(status()), 'and when the phone is back, the code appears') }
{ const ph = scenario(); const page = fresh(); page.build()
  for (let i = 0; i < 6; i++) { await ph.fail(new Error('Timed out in 15000ms.')); if (timers.length) await fire(4000) }
  ok(ph.requests.length === 6 && timers.length === 0 && status() === 'Phone is not reachable\nRestart the\nZepp app', 'giving up keeps the reason on the screen, so the person knows what to fix -> ' + JSON.stringify(status())) }
{ const ph = scenario(); const page = fresh(); page.build()
  for (let i = 0; i < 6; i++) { await ph.reply({ ok: false, noServer: true, error: 'server: Network down' }); if (timers.length) await fire(4000) }
  ok(timers.length === 0 && status() === 'Server is unreachable\nCheck the internet\non the phone', 'the same for a server that cannot be reached -> ' + JSON.stringify(status())) }

console.log('9. leaving the screen')
{ const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: true, ...CODES }); page.onDestroy()
  ok(timers.length === 0 && stubs.calls.lastIndexOf('releaseScreen') > stubs.calls.indexOf('holdScreen'), 'onDestroy cancels the pending poll and releases the screen') }
{ const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: true, ...CODES }); await fire(5000); page.onDestroy()
  const texts = stubs.widgets.map((w) => w.text); await ph.reply({ ok: true, status: 'approved', deviceToken: 'pdd_late', deviceId: 'i' })
  ok(stubs.saved.length === 0 && timers.length === 0 && JSON.stringify(stubs.widgets.map((w) => w.text)) === JSON.stringify(texts), 'an answer that arrives after the screen is gone changes nothing') }
{ const ph = scenario(); const page = fresh(); page.build(); page.onDestroy(); await ph.reply({ ok: true, ...CODES })
  ok(!qr() && timers.length === 0, 'nor does a code that arrives after the screen is gone') }

console.log('11. Personal server settings arrive through the pairing page')
const URL_OWN = 'https://my.server.example:8443/drop', TOKEN_OWN = 'own-token-0123456789'
const fragmentOf = () => qr().opts.content.split('#k=')[1]
const secretOf = () => Uint8Array.from(Buffer.from(fragmentOf(), 'base64url'))
const printed = []
const realLog = console.log
async function startAndScan() { const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: true, ...CODES }); return { ph, page } }
{ const { ph, page } = await startAndScan()
  const secret = secretOf()
  ok(secret.length === 16 && page.state.secret && Buffer.compare(Buffer.from(page.state.secret), Buffer.from(secret)) === 0, 'the QR fragment is the watch\'s 16-byte secret')
  await fire(5000)
  const envelope = await sealWithWebCrypto({ secret, userCode: 'BCDF-GHJK', first: new Uint8Array(randomBytes(16)), json: payload(URL_OWN, TOKEN_OWN) })
  console.log = (...a) => { printed.push(a.join(' ')); }
  await ph.reply({ ok: true, status: 'relayed', envelope })
  console.log = realLog
  ok(stubs.replaced.length === 1 && stubs.replaced[0].url === 'page/server', 'a good envelope opens the confirmation screen -> ' + JSON.stringify(stubs.replaced.map((r) => r.url)))
  ok(JSON.stringify(stubs.replaced[0].params) === JSON.stringify({ mode: 'personal', serverUrl: URL_OWN, token: TOKEN_OWN }), 'it gets the address and the token (to be confirmed there, not saved here)')
  ok(stubs.saved.length === 0 && !stubs.calls.includes('saveDevice') && !stubs.calls.includes('retryPendingUploads') && !stubs.calls.includes('back'), 'nothing is saved, nothing is sent and the screen does not go back by itself')
  ok(timers.length === 0 && ph.requests.length === 2 && page.state.secret === null, 'polling stops and the secret is forgotten')
  ok(stubs.calls.lastIndexOf('releaseScreen') > stubs.calls.indexOf('holdScreen'), 'the held screen is released')
  ok(printed.length === 0, 'nothing was logged on success -> ' + JSON.stringify(printed)) }

for (const [label, make] of [
  ['an envelope made for another pairing', (secret) => sealWithWebCrypto({ secret, userCode: 'BCDF-GHJL', first: new Uint8Array(16), json: payload(URL_OWN, TOKEN_OWN) })],
  ['an envelope made with another secret', () => sealWithWebCrypto({ secret: newSecretBytes(), userCode: 'BCDF-GHJK', first: new Uint8Array(16), json: payload(URL_OWN, TOKEN_OWN) })],
  ['an envelope with a changed tag', async (secret) => { const e = await sealWithWebCrypto({ secret, userCode: 'BCDF-GHJK', first: new Uint8Array(16), json: payload(URL_OWN, TOKEN_OWN) }); const tag = Buffer.from(e.tag, 'base64url'); tag[0] ^= 1; return { ...e, tag: b64u(tag) } }],
  ['an envelope of another version', async (secret) => ({ ...(await sealWithWebCrypto({ secret, userCode: 'BCDF-GHJK', first: new Uint8Array(16), json: payload(URL_OWN, TOKEN_OWN) })), v: 2 })],
  ['an envelope with an address that is not https', (secret) => sealJson({ secret, userCode: 'BCDF-GHJK', json: payload('http://my.server.example', TOKEN_OWN) })],
  ['an envelope with a token that has a space', (secret) => sealJson({ secret, userCode: 'BCDF-GHJK', json: payload(URL_OWN, 'two words') })],
  ['something that is not an envelope', () => 'hello'],
  ['nothing', () => undefined],
]) {
  const { ph, page } = await startAndScan(); await fire(5000)
  const envelope = await make(secretOf())
  printed.length = 0; console.log = (...a) => { printed.push(a.join(' ')) }
  await ph.reply({ ok: true, status: 'relayed', envelope })
  console.log = realLog
  ok(stubs.replaced.length === 0 && stubs.saved.length === 0, label + ': nothing is opened, nothing is saved')
  ok(status() === 'Could not accept\nthe settings.\nOpen this screen\nand start again' && timers.length === 0 && page.state.secret === null, label + ': the screen says so, polling stops, the secret is forgotten -> ' + JSON.stringify(status()))
  ok(stubs.calls.lastIndexOf('releaseScreen') > stubs.calls.indexOf('holdScreen'), label + ': the screen is released')
  ok(printed.length === 1 && /not accepted: [a-z ]+$/.test(printed[0]) && !printed[0].includes(TOKEN_OWN) && !printed[0].includes('my.server') && !printed[0].includes(JSON.stringify(envelope) || 'x'.repeat(40)), label + ': the log says only which check failed -> ' + JSON.stringify(printed)) }

{ // a watch that cannot do the cryptography shows the plain QR code and still pairs the ordinary way
  const ph = scenario(); const page = fresh(); zc.behaviour.generatedKeys = 'none'; page.build(); await ph.reply({ ok: true, ...CODES })
  ok(qr() && qr().opts.content === 'https://s.example/pair?code=BCDF-GHJK' && page.state.secret === null, 'without a source of randomness the QR code is the plain one, with no #')
  await fire(5000); await ph.reply({ ok: true, status: 'approved', deviceToken: 'pdd_t', deviceId: 'i' })
  ok(stubs.saved.length === 1, '...and pairing with an account works as before') }
{ // ... and if an envelope comes all the same, there is no secret to open it with
  const ph = scenario(); const page = fresh(); zc.behaviour.generatedKeys = 'none'; page.build(); await ph.reply({ ok: true, ...CODES }); await fire(5000)
  const env = await sealWithWebCrypto({ secret: newSecretBytes(), userCode: 'BCDF-GHJK', first: new Uint8Array(16), json: payload(URL_OWN, TOKEN_OWN) })
  console.log = () => {}; await ph.reply({ ok: true, status: 'relayed', envelope: env }); console.log = realLog
  ok(stubs.replaced.length === 0 && /Could not accept/.test(status()), 'an envelope for a watch that made no secret is refused') }
{ const ph = scenario(); const page = fresh(); zc.behaviour.throws = true; page.build(); await ph.reply({ ok: true, ...CODES })
  ok(qr() && qr().opts.content === 'https://s.example/pair?code=BCDF-GHJK', 'a platform that throws still gets its plain QR code') }
{ const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: true, ...CODES, verificationUriComplete: 'https://s.example/pair?code=BCDF-GHJK#already' })
  ok(qr().opts.content === 'https://s.example/pair?code=BCDF-GHJK#already' && page.state.secret === null, 'an address that already has a fragment is left as the server gave it') }
{ const seen = new Set(); for (let i = 0; i < 5; i++) { const ph = scenario(); const page = fresh(); page.build(); await ph.reply({ ok: true, ...CODES }); seen.add(fragmentOf()) }
  ok(seen.size === 5, 'every attempt has its own secret (5 attempts, 5 different QR codes)') }
{ const { ph, page } = await startAndScan(); await fire(5000)
  const envelope = await sealWithWebCrypto({ secret: secretOf(), userCode: 'BCDF-GHJK', first: new Uint8Array(16), json: payload(URL_OWN, TOKEN_OWN) })
  page.onDestroy(); ok(page.state.secret === null, 'leaving the screen forgets the secret'); await ph.reply({ ok: true, status: 'relayed', envelope })
  ok(stubs.replaced.length === 0 && timers.length === 0, 'an envelope that arrives after the screen is gone changes nothing') }
{ const { ph, page } = await startAndScan(); await fire(5000)
  ok(page.state.secret !== null, 'the secret is kept while the code lives'); await ph.reply({ ok: false, code: 'expired_token', error: 'x' })
  ok(page.state.secret === null, 'and forgotten when the code runs out') }
{ const { ph, page } = await startAndScan(); await fire(5000)
  const envelope = await sealWithWebCrypto({ secret: secretOf(), userCode: 'bcdf-ghjk', first: new Uint8Array(16), json: payload(URL_OWN, TOKEN_OWN) })
  await ph.reply({ ok: true, status: 'relayed', envelope })
  ok(stubs.replaced.length === 1, 'the case and the dash of the user code do not matter (the page may send it either way)') }

console.log('10. the watch\'s name')
{ const ph = scenario(); const page = fresh(); stubs.info.throws = true; page.build()
  ok(ph.requests[0].clientName === 'Amazfit', 'without device info it still pairs, as "Amazfit"') }
{ const ph = scenario(); const page = fresh(); stubs.info.value = {}; page.build()
  ok(ph.requests[0].clientName === 'Amazfit', 'an empty device name falls back too') }

console.log('\n' + (failures ? failures + ' FAILURE(S)' : 'ALL CHECKS PASS'))
process.exit(failures ? 1 : 0)
