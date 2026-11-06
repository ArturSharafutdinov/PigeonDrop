// page/connection.js: where the watch sends its recordings, and the ways to change that.

import * as stubs from './stubs/connection-pages.mjs'
import { generate, fileUrl, stubPath, i18nModule } from './helpers/load.mjs'
import * as settingsStub from './stubs/settings.mjs'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const S = stubPath('connection-pages.mjs')
let opts
globalThis.Page = (o) => { opts = o }
await import(fileUrl(generate('page/connection.js', {
  '@zos/ui': S, '@zos/utils': S, '@zos/router': S, '../utils/device': S, '../utils/serverUrl': generate('utils/serverUrl.js', {}), '../i18n': i18nModule(),
})))
settingsStub.setLanguage(settingsStub.ENGLISH)
const flush = () => new Promise((resolve) => setImmediate(resolve))
let sent, answer
globalThis.getApp = () => ({ _options: { globalData: { messageBuilder: { request: (m, o) => { sent.push({ m, o }); return typeof answer === 'function' ? answer() : Promise.resolve(answer) } } } } })
function open(settings = {}) {
  Object.assign(stubs.state, { paired: true, custom: false, label: null }, settings)
  stubs.widgets.length = 0; stubs.calls.length = 0; sent = []
  const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) })
  page.build()
  return page
}
const buttons = () => stubs.widgets.filter((w) => w.type === 'BUTTON')
const label = (text) => buttons().find((w) => w.opts.text === text)
const status = () => stubs.widgets[0].text

console.log('1. what the screen says about where the recordings go')
open({ paired: true }); ok(status() === 'Recordings go to\nPigeonDrop', 'paired with an account -> ' + JSON.stringify(status()))
open({ paired: false }); ok(status() === 'PigeonDrop:\nwatch is not paired', 'not paired -> ' + JSON.stringify(status()))
open({ custom: true, paired: true, label: 'own.example:8443' }); ok(status() === 'Recordings go to\nyour Personal Server:\nown.example:8443', 'an own server -> ' + JSON.stringify(status()))
open({ custom: true, paired: false, label: 'own.example' }); ok(status() === 'Personal Server:\nown.example\nno token', 'an own server without a token -> ' + JSON.stringify(status()))
open({ custom: true, paired: false, label: null }); ok(status() === 'Personal Server:\n?\nno token', 'a damaged address -> ' + JSON.stringify(status()))

console.log('2. the buttons')
open(); ok(buttons().map((w) => w.opts.text).join('|') === 'Pair via QR code|Personal Server', 'on a PigeonDrop watch: pairing and the own server (no way "back" to where it already is)')
open({ custom: true, label: 'own.example' }); ok(buttons().map((w) => w.opts.text).join('|') === 'Pair via QR code|Personal Server|Back to PigeonDrop', 'on an own-server watch: and the way back')
{ const safe = (w) => Math.max(...[[w.opts.x, w.opts.y], [w.opts.x + w.opts.w, w.opts.y], [w.opts.x, w.opts.y + w.opts.h], [w.opts.x + w.opts.w, w.opts.y + w.opts.h]].map(([x, y]) => Math.hypot(x - 240, y - 240))) < 230
  ok(buttons().every(safe), 'every button stays inside the round display\'s safe radius') }
open(); label('Pair via QR code').opts.click_func()
ok(stubs.calls.length === 1 && stubs.calls[0].fn === 'replace' && stubs.calls[0].url === 'page/pair', '"Pair via QR code" replaces this page with the pairing page (so the page the person comes back to is built afresh)')
open({ custom: true, label: 'own.example' }); label('Back to PigeonDrop').opts.click_func()
ok(stubs.calls.length === 1 && stubs.calls[0].url === 'page/server' && stubs.calls[0].params.mode === 'reset', '"Back to PigeonDrop" asks for the confirmation')

console.log('3. "Personal Server": fetch what was entered on the phone')
{ answer = { ok: true, configured: true, serverUrl: 'https://own.example:8443/drop', token: 'tok_own' }
  const p = open(); label('Personal Server').opts.click_func(); label('Personal Server').opts.click_func()
  ok(status() === 'Reading settings\nfrom the phone...' && sent.length === 1 && sent[0].m.type === 'getServerConfig' && sent[0].o.timeout === 15000, 'asks the phone once (a second press changes nothing), and says so -> ' + JSON.stringify(sent[0]))
  await flush()
  ok(stubs.calls.length === 1 && stubs.calls[0].url === 'page/server' && stubs.calls[0].params.mode === 'personal' && stubs.calls[0].params.serverUrl === 'https://own.example:8443/drop' && stubs.calls[0].params.token === 'tok_own', 'a usable answer goes on to the confirmation screen, with the address and token') }
for (const [text, reply, said] of [
  ['nothing entered', { ok: true, configured: false }, 'Nothing entered.\nEnter the address and\ntoken in the Zepp app'],
  ['the phone found it unusable', { ok: false, code: 'invalid_url', error: 'address: not_https' }, 'The address or token\nis not valid.\nCheck the Zepp app'],
  ['no answer from the phone', () => Promise.reject(new Error('Timed out')), 'Phone does not answer\nRestart the\nZepp app'],
  ['an empty answer', undefined, 'Phone does not answer\nRestart the\nZepp app'],
  ['an address not in its normal form', { ok: true, configured: true, serverUrl: 'https://Own.Example/', token: 'tok' }, 'The address or token\nis not valid.\nCheck the Zepp app'],
  ['an http address', { ok: true, configured: true, serverUrl: 'http://own.example', token: 'tok' }, 'The address or token\nis not valid.\nCheck the Zepp app'],
  ['a token with a space', { ok: true, configured: true, serverUrl: 'https://own.example', token: 'to ken' }, 'The address or token\nis not valid.\nCheck the Zepp app'],
  ['no token in the answer', { ok: true, configured: true, serverUrl: 'https://own.example' }, 'The address or token\nis not valid.\nCheck the Zepp app'],
]) {
  answer = reply; open(); label('Personal Server').opts.click_func(); await flush()
  ok(status() === said && stubs.calls.length === 0, text + ' -> ' + JSON.stringify(status()) + ', nothing opened')
}
{ answer = { ok: true, configured: false }; const p = open(); label('Personal Server').opts.click_func(); await flush()
  label('Personal Server').opts.click_func(); await flush()
  ok(sent.length === 2, 'after an answer the button can be used again') }
{ answer = { ok: true, configured: true, serverUrl: 'https://own.example', token: 'tok' }; const p = open(); label('Personal Server').opts.click_func(); p.onDestroy(); await flush()
  ok(stubs.calls.length === 0, 'an answer that arrives after the screen is gone does nothing') }

process.exit(failures ? 1 : 0)
