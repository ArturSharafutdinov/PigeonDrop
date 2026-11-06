// page/server.js: the confirmation before the watch sends its recordings somewhere else.

import * as stubs from './stubs/connection-pages.mjs'
import { generate, fileUrl, stubPath, i18nModule } from './helpers/load.mjs'
import * as settingsStub from './stubs/settings.mjs'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const S = stubPath('connection-pages.mjs')
let opts
globalThis.Page = (o) => { opts = o }
const timeouts = []
globalThis.setTimeout = (fn, ms) => { timeouts.push({ fn, ms }); return timeouts.length }
globalThis.clearTimeout = (id) => { if (timeouts[id - 1]) timeouts[id - 1].cleared = true }
await import(fileUrl(generate('page/server.js', {
  '@zos/ui': S, '@zos/utils': S, '@zos/router': S, '../utils/device': S, '../utils/serverUrl': generate('utils/serverUrl.js', {}),
  '../utils/storageState': S, '../utils/uploadQueue': S, '../i18n': i18nModule(),
})))
settingsStub.setLanguage(settingsStub.ENGLISH)
const PERSONAL = { mode: 'personal', serverUrl: 'https://own.example:8443/drop', token: 'tok_own' }
function open(params, pendingCount = 3) {
  stubs.widgets.length = 0; stubs.calls.length = 0; timeouts.length = 0; stubs.state.failSave = false; stubs.setPending(pendingCount)
  const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) })
  page.onInit(typeof params === 'object' && params !== null ? JSON.stringify(params) : params)
  page.build()
  return page
}
const buttons = () => stubs.widgets.filter((w) => w.type === 'BUTTON')
const press = (text) => buttons().find((w) => w.opts.text === text).opts.click_func()
const status = () => stubs.widgets[0].text
const called = (fn) => stubs.calls.filter((c) => c.fn === fn)

console.log('1. a parameter this screen may not act on: it leaves at once, having changed nothing')
for (const [label, params] of [['no parameter', undefined], ['not JSON', 'garbage'], ['null', 'null'], ['an unknown mode', { mode: 'format' }], ['no mode', { serverUrl: 'https://own.example', token: 't' }],
  ['http', { mode: 'personal', serverUrl: 'http://own.example', token: 't' }], ['an address not in normal form', { mode: 'personal', serverUrl: 'https://Own.Example/', token: 't' }],
  ['no token', { mode: 'personal', serverUrl: 'https://own.example' }], ['a token with a space', { mode: 'personal', serverUrl: 'https://own.example', token: 'to ken' }]]) {
  open(params)
  ok(stubs.widgets.length === 0 && timeouts.length === 1 && timeouts[0].ms === 0, label + ' -> nothing shown, leaves')
  timeouts[0].fn()
  ok(stubs.calls.length === 1 && stubs.calls[0].fn === 'replace' && stubs.calls[0].url === 'page/connection' && called('saveCustomServer').length === 0 && called('clearDevice').length === 0, '   and goes back to the connection screen, nothing saved or cleared')
}
open(PERSONAL); ok(stubs.widgets.length === 3, 'a parameter given as an object (not a string) works too')
{ stubs.widgets.length = 0; const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) }); page.onInit({ ...PERSONAL }); page.build(); ok(stubs.widgets.length === 3, '...') }

console.log('2. a personal server: shows where the recordings would go, and saves only after "Use this server"')
open(PERSONAL, 3)
ok(status() === 'Send recordings to\nown.example:8443\nQueued: 3', 'the host and port, and how many recordings are waiting -> ' + JSON.stringify(status()))
ok(!status().includes('/drop') && !status().includes('tok_own'), 'neither the path nor the token is on the screen')
ok(buttons().map((w) => w.opts.text).join('|') === 'Use this server|Cancel', 'the buttons -> ' + buttons().map((w) => w.opts.text).join('|'))
ok(stubs.calls.length === 0, 'nothing is saved yet')
press('Use this server')
ok(called('saveCustomServer').length === 1 && called('saveCustomServer')[0].serverUrl === 'https://own.example:8443/drop' && called('saveCustomServer')[0].token === 'tok_own', 'saved with the address and the token')
ok(called('clearStorageState').length === 1 && called('retryPendingUploads').length === 1 && called('clearDevice').length === 0, 'the old storage warning is forgotten and the waiting recordings are sent to the new server')
ok(stubs.calls.findIndex((c) => c.fn === 'saveCustomServer') < stubs.calls.findIndex((c) => c.fn === 'retryPendingUploads'), '...after the setting is saved, not before')
ok(status() === 'Done.\nRecordings go to\nown.example:8443', 'says it is done -> ' + JSON.stringify(status()))
press('Use this server'); press('Cancel')
ok(called('saveCustomServer').length === 1 && called('replace').length === 0, 'a second press, or Cancel, after the choice does nothing')
ok(timeouts.length === 1 && timeouts[0].ms === 2500 && !stubs.calls.some((c) => c.fn === 'back'), 'it leaves after 2.5 s...')
timeouts[0].fn(); ok(called('back').length === 1, '...by going back')

console.log('3. Cancel')
{ open(PERSONAL); press('Cancel'); press('Cancel')
  ok(called('replace').length === 1 && called('replace')[0].url === 'page/connection' && called('saveCustomServer').length === 0 && called('clearDevice').length === 0, 'goes back to the connection screen, once, and changes nothing') }

console.log('4. saving fails: said so, can be tried again, nothing sent')
{ const page = open(PERSONAL); stubs.state.failSave = true
  const log = console.log; console.log = () => {}
  try { press('Use this server') } finally { console.log = log }
  ok(status() === 'Could not save.\nTry again' && called('retryPendingUploads').length === 0 && called('clearStorageState').length === 0 && timeouts.length === 0, 'the screen says so; no recording is sent; it does not leave -> ' + JSON.stringify(status()))
  stubs.state.failSave = false; press('Use this server')
  ok(called('saveCustomServer').length === 1 && status().startsWith('Done.'), 'a second press works') }

console.log('5. going back to PigeonDrop')
{ open({ mode: 'reset' }, 4)
  ok(status() === 'Go back to PigeonDrop?\nQueued: 4\nPair the watch again\nto send them', 'says what happens and how many recordings wait -> ' + JSON.stringify(status()))
  ok(buttons().map((w) => w.opts.text).join('|') === 'Go back|Cancel', 'the buttons -> ' + buttons().map((w) => w.opts.text).join('|'))
  press('Go back')
  ok(called('clearDevice').length === 1 && called('saveCustomServer').length === 0 && called('clearStorageState').length === 1 && called('retryPendingUploads').length === 1, 'the watch forgets the server and its token; the queue is kicked (it will report "not paired")')
  ok(status() === 'Done.\nPair the watch\nto send recordings', 'and says to pair again -> ' + JSON.stringify(status())) }

console.log('6. the layout')
{ open({ mode: 'personal', serverUrl: 'https://' + 'a'.repeat(60) + '.example.com', token: 't' })
  ok(stubs.widgets[0].opts.text_size === 20, 'a long host is shown in a smaller size')
  open(PERSONAL); ok(stubs.widgets[0].opts.text_size === 24, 'a short one in the larger')
  const safe = (w) => Math.max(...[[w.opts.x, w.opts.y], [w.opts.x + w.opts.w, w.opts.y], [w.opts.x, w.opts.y + w.opts.h], [w.opts.x + w.opts.w, w.opts.y + w.opts.h]].map(([x, y]) => Math.hypot(x - 240, y - 240))) < 230
  ok(buttons().every(safe), 'both buttons stay inside the round display\'s safe radius') }

console.log('7. leaving the screen')
{ const page = open(PERSONAL); press('Use this server'); page.onDestroy()
  ok(timeouts[0].cleared === true, 'the pending timer is cancelled when the screen is destroyed') }

process.exit(failures ? 1 : 0)
