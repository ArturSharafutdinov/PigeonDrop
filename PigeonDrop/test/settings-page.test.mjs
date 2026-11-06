// Settings page (page/settings.js): the "Clear memory" button and its confirmation.

import * as stubs from './stubs/settings-page.mjs'
import { generate, fileUrl, stubPath, i18nModule } from './helpers/load.mjs'
let opts, timeouts = [], nid = 1
globalThis.Page = (o) => { opts = o }
globalThis.setTimeout = (fn, ms) => { const id = nid++; timeouts.push({ id, fn, ms }); return id }
globalThis.clearTimeout = (id) => { timeouts = timeouts.filter((t) => t.id !== id) }
const S = stubPath('settings-page.mjs')
const PAGE = generate('page/settings.js', {
  '@zos/ui': S, '@zos/utils': S, '@zos/router': S,
  '../utils/device': S, '../utils/recordings': S, '../utils/uploadQueue': S, '../i18n': i18nModule(),
})
await import(fileUrl(PAGE))
const ok = (c, m) => { console.log((c ? 'ok   ' : 'FAIL ') + m); if (!c) process.exitCode = 1 }
const mk = () => Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) })
const page = mk(); page.build()
const status = () => stubs.widgets[0].text
const btn = stubs.widgets.find((w) => w.opts.text === 'Clear memory')
ok(!!btn, 'the button "Clear memory" exists')
const o = btn.opts
const corner = (x, y) => Math.hypot(x - 240, y - 240)
ok(Math.max(corner(o.x, o.y + o.h), corner(o.x + o.w, o.y + o.h)) < 230, 'button stays inside the round-display safe radius (' + Math.round(Math.max(corner(o.x, o.y + o.h), corner(o.x + o.w, o.y + o.h))) + ' < 230)')
ok(stubs.widgets.filter((w) => w.type === 'BUTTON').every((w, i, a) => i === 0 || w.opts.y >= a[i - 1].opts.y + a[i - 1].opts.h), 'buttons do not overlap')
btn.opts.click_func()
ok(btn.text === 'Press again' && status() === 'Delete 3 recordings?\nPress again' && stubs.calls.length === 0, 'first press only asks for confirmation, deletes nothing -> ' + JSON.stringify(status()))
ok(timeouts.length === 1 && timeouts[0].ms === 5000, 'confirmation expires after 5 s')
timeouts[0].fn()
ok(btn.text === 'Clear memory' && status().startsWith('Watch is not paired') && stubs.calls.length === 0 && page.state.clearTimer === null, 'expiry: label and status restored, still nothing deleted')
btn.opts.click_func(); btn.opts.click_func()
ok(stubs.calls.join() === 'clearAllRecordings' && status() === 'Memory cleared\nRecordings deleted: 3', 'second press within 5 s clears -> ' + JSON.stringify(status()))
ok(btn.text === 'Clear memory' && timeouts.length === 0, 'label restored, timer cancelled')
stubs.setClearResult({ removed: 0, busy: true }); btn.opts.click_func(); btn.opts.click_func()
ok(status() === 'Upload in progress.\nWait and try again', 'busy (upload running) is explained, not silent -> ' + JSON.stringify(status()))
btn.opts.click_func(); page.onDestroy()
ok(timeouts.length === 0 && page.state.clearTimer === null, 'onDestroy drops a pending confirmation timer')

// Pairing: the other button, and what the status line says about it.
const fresh = () => { stubs.widgets.length = 0; stubs.pushed.length = 0; const p = mk(); p.build(); return p }
fresh()
ok(stubs.widgets.filter((w) => w.type === 'BUTTON').map((w) => w.opts.text).join('|') === 'Pair the watch|Check connection|Clear memory', 'the buttons are pairing, clearing and the connection check (server setup and sync are gone)')
stubs.widgets.find((w) => w.opts.text === 'Pair the watch').opts.click_func()
ok(stubs.pushed.length === 1 && stubs.pushed[0].url === 'page/pair', '"Pair the watch" opens the pairing screen -> ' + JSON.stringify(stubs.pushed))
ok(stubs.widgets[0].text === 'Watch is not paired\nto an account', 'status says the watch is not paired -> ' + JSON.stringify(stubs.widgets[0].text))
stubs.setPaired(true); fresh()
ok(stubs.widgets[0].text === 'Watch is paired\nto an account', 'and once paired, that it is -> ' + JSON.stringify(stubs.widgets[0].text))
{ const b = stubs.widgets.find((w) => w.opts.text === 'Clear memory'); b.opts.click_func(); timeouts[0].fn()
  ok(stubs.widgets[0].text === 'Watch is paired\nto an account', 'after the clear confirmation expires, the status line shows the pairing state again') }

// Check connection: the phone walks the way to the server and the status line says where it broke.
{
  const sent = []; let answer
  globalThis.getApp = () => ({ _options: { globalData: { messageBuilder: { request: (m, o) => { sent.push({ m, o }); return typeof answer === 'function' ? answer() : Promise.resolve(answer) } } } } })
  const flush = () => new Promise((r) => setImmediate(r))
  const run = async (a) => { answer = a; const p = fresh(); sent.length = 0; stubs.widgets.find((w) => w.opts.text === 'Check connection').opts.click_func(); const during = stubs.widgets[0].text; await flush(); return { p, during, text: stubs.widgets[0].text } }
  const b = fresh() && stubs.widgets.find((w) => w.opts.text === 'Check connection').opts
  const corner2 = (x, y) => Math.hypot(x - 240, y - 240)
  ok(Math.max(corner2(b.x, b.y + b.h), corner2(b.x + b.w, b.y + b.h)) < 230, 'the check button stays inside the safe radius')
  let r = await run({ ok: true, stage: 'upload', paired: true })
  ok(r.during === 'Checking...' && sent[0].m.type === 'checkServer' && sent[0].m.deviceToken === 'tok' && sent[0].o.timeout === 25000, 'asks the phone, with the token, and says it is checking -> ' + JSON.stringify(sent[0]))
  ok(r.text === 'All good:\nserver is reachable,\nwatch is paired', 'all fine -> ' + JSON.stringify(r.text))
  r = await run({ ok: false, stage: 'server', error: 'server: no answer in 10 s' }); ok(r.text === 'Server is unreachable\n(no answer in 10 s)', 'server unreachable, with the reason -> ' + JSON.stringify(r.text))
  r = await run({ ok: false, stage: 'server', error: 'HTTP 503' }); ok(r.text === 'Server is unreachable\n(HTTP 503)', 'server unwell -> ' + JSON.stringify(r.text))
  r = await run({ ok: false, stage: 'token', error: 'HTTP 401' }); ok(r.text.startsWith('Server is reachable,\nbut'), 'token refused -> ' + JSON.stringify(r.text))
  r = await run({ ok: false, stage: 'upload', error: 'HTTP 500' }); ok(r.text === 'Server error\n(HTTP 500)', 'upload route fails -> ' + JSON.stringify(r.text))
  r = await run(() => Promise.reject(new Error('Timed out'))); ok(r.text === 'Phone does not answer\nRestart the\nZepp app', 'no answer from the phone -> ' + JSON.stringify(r.text))
  stubs.setPaired(false)
  r = await run({ ok: true, stage: 'server', paired: false }); ok(sent[0].m.deviceToken === '' && r.text === 'Server is reachable,\nwatch is not paired', 'not paired: only the server is checked -> ' + JSON.stringify(r.text))
  // A watch set up for a server of the person's own is checked against that server, not the PigeonDrop one.
  stubs.setPaired(true); stubs.setCustom(true, 'https://own.example')
  r = await run({ ok: true, stage: 'upload', paired: true })
  ok(sent[0].m.serverUrl === 'https://own.example' && sent[0].m.deviceToken === 'tok', 'an own server: its address goes with the request, beside the token -> ' + JSON.stringify(sent[0].m))
  stubs.setCustom(true, null)
  r = await run({ ok: false, stage: 'server', error: 'bad server address' })
  ok(Object.prototype.hasOwnProperty.call(sent[0].m, 'serverUrl') && sent[0].m.serverUrl === null, 'a damaged address is sent as a null address (the phone refuses it), not left out (which would mean PigeonDrop)')
  stubs.setCustom(false, null)
  r = await run({ ok: true, stage: 'upload', paired: true })
  ok(!Object.prototype.hasOwnProperty.call(sent[0].m, 'serverUrl'), 'the PigeonDrop server: no address in the request')
  stubs.setPaired(false)
  { answer = () => new Promise(() => {}); const p = fresh(); sent.length = 0; const btn2 = stubs.widgets.find((w) => w.opts.text === 'Check connection'); btn2.opts.click_func(); btn2.opts.click_func()
    ok(sent.length === 1, 'a second press while checking sends nothing') }
}
