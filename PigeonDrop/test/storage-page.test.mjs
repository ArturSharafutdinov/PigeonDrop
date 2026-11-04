// page/storage.js: the page that warns the account's storage is nearly full or full - buzz, stay up, go back.

import fs from 'fs'; import path from 'path'; import { fileURLToPath } from 'url'
import * as stubs from './stubs/storage-page.mjs'
import { generate, fileUrl, stubPath, i18nModule } from './helpers/load.mjs'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }

let opts
globalThis.Page = (o) => { opts = o }
let timers = [], nextId = 1
globalThis.setTimeout = (fn, ms) => { const id = nextId++; timers.push({ id, fn, ms }); return id }
globalThis.clearTimeout = (id) => { timers = timers.filter((t) => t.id !== id) }
const S = stubPath('storage-page.mjs')
const PAGE = generate('page/storage.js', { '@zos/ui': S, '@zos/utils': S, '@zos/router': S, '@zos/sensor': S, '../utils/storageState': S, '../utils/screen': S, '../i18n': i18nModule() })
await import(fileUrl(PAGE))

const open = (params) => {
  stubs.widgets.length = 0; stubs.calls.length = 0; stubs.holds.length = 0; stubs.vib.instances.length = 0; stubs.vib.failOn = null
  stubs.nav.backThrows = false; stubs.store.state = null; timers = []
  const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) })
  page.onInit(params); page.build()
  return page
}
const texts = () => stubs.widgets.map((w) => w.text)
const fire = (ms) => { const due = timers.filter((t) => t.ms === ms); timers = timers.filter((t) => t.ms !== ms); due.forEach((t) => t.fn()) }
const params = (state) => JSON.stringify({ state })

console.log('1. nearly full: what is shown, the buzz, the screen, and the way back')
{ const page = open(params('nearly_full'))
  ok(texts().length === 2 && texts()[0] === 'Storage\nis almost full' && /Clear it on the website/.test(texts()[1]) && /start to be deleted/.test(texts()[1]), '1: a title and an explanation -> ' + JSON.stringify(texts()))
  ok(stubs.vib.instances.length === 1 && JSON.stringify(stubs.vib.instances[0].log) === '["setMode 1000","start"]', '1: one buzz of the 1-second kind (VIBRATOR_SCENE_DURATION_LONG), started when the page opens -> ' + JSON.stringify(stubs.vib.instances[0].log))
  ok(JSON.stringify(stubs.holds) === '[10000]', '1: the screen is held for the 10 seconds the page is up -> ' + JSON.stringify(stubs.holds))
  ok(timers.length === 1 && timers[0].ms === 10000, '1: one timer, 10 s -> ' + JSON.stringify(timers.map((t) => t.ms)))
  ok(!stubs.calls.includes('back'), '1: it does not leave before that'); fire(10000)
  ok(stubs.calls.filter((c) => c === 'back').length === 1, '1: after 10 s it goes back to the main page, once') }

console.log('2. full: its own words')
{ open(params('full'))
  ok(texts()[0] === 'Storage\nis full' && /oldest recordings are being deleted/.test(texts()[1]) && !/almost/.test(texts().join(' ')), '2: ' + JSON.stringify(texts()))
  ok(texts()[1] !== (open(params('nearly_full')), texts()[1]), '2: differs from the nearly-full text') }

console.log('3. the state comes with the page, or from what the queue remembered, or there is nothing to say')
for (const [label, p, stored, want] of [
  ['an unreadable parameter, a remembered state', 'not json', 'full', 'Storage\nis full'],
  ['no parameter, a remembered state', undefined, 'nearly_full', 'Storage\nis almost full'],
  ['an unknown state in the parameter, a remembered one', params('weird'), 'full', 'Storage\nis full'],
  ['the parameter wins over what is remembered', params('nearly_full'), 'full', 'Storage\nis almost full'],
]) {
  stubs.store.state = null
  stubs.widgets.length = 0; stubs.calls.length = 0
  const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) }); stubs.store.state = stored
  page.onInit(p); page.build()
  ok(texts()[0] === want, label + ' -> ' + JSON.stringify(texts()[0])) }
for (const [label, p] of [['no parameter and nothing remembered', undefined], ['an unknown state and nothing remembered', params('ok')], ['an empty parameter', '']]) {
  const page = open(p)
  ok(stubs.widgets.length === 0 && stubs.vib.instances.length === 0 && stubs.holds.length === 0, label + ': nothing is shown, nothing buzzes, the screen is not held')
  ok(timers.length === 1 && timers[0].ms === 0, label + ': it leaves at once')
  fire(0); ok(stubs.calls.filter((c) => c === 'back').length === 1, label + ': back called once') }

console.log('4. leaving early: the timer and the buzz go with the page')
{ const page = open(params('full')); const v = stubs.vib.instances[0]
  page.onDestroy()
  ok(timers.length === 0 && v.log.at(-1) === 'stop', '4: onDestroy clears the timer and stops the vibration -> ' + JSON.stringify(v.log))
  ok(!stubs.calls.includes('back'), '4: and does not call back by itself afterwards')
  page.onDestroy(); ok(v.log.filter((l) => l === 'stop').length === 1, '4: destroying twice stops the vibration once') }
{ const page = open(params('full')); fire(10000); page.onDestroy()
  ok(stubs.calls.filter((c) => c === 'back').length === 1 && timers.length === 0, '4: leaving by itself and then being destroyed calls back once') }
{ const page = open(params('full')); page.leave(); page.leave(); fire(10000)
  ok(stubs.calls.filter((c) => c === 'back').length === 1, '4: however it is reached, back is called once') }

console.log('5. a watch or runtime that fails on one thing still shows the warning')
for (const failOn of ['new', 'setMode', 'start']) {
  stubs.widgets.length = 0; stubs.calls.length = 0; stubs.holds.length = 0; stubs.vib.instances.length = 0; timers = []
  stubs.vib.failOn = failOn; stubs.nav.backThrows = false
  const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) })
  const orig = console.log; const logged = []; console.log = (...a) => logged.push(a.join(' '))
  try { page.onInit(params('full')); page.build() } finally { console.log = orig }
  ok(texts().length === 2 && JSON.stringify(stubs.holds) === '[10000]' && timers.length === 1, 'the vibrator fails on ' + failOn + ': the text, the screen hold and the way back are still there')
  ok(logged.some((l) => /vibration failed/.test(l)), 'the vibrator fails on ' + failOn + ': it is logged') }
{ stubs.widgets.length = 0; stubs.calls.length = 0; timers = []; stubs.vib.failOn = 'stop'
  const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) }); page.onInit(params('full')); page.build()
  const orig = console.log; console.log = () => {}
  try { page.onDestroy() } finally { console.log = orig }
  ok(timers.length === 0, 'a vibrator that cannot be stopped does not break onDestroy') }
{ stubs.vib.failOn = null; const page = open(params('full')); stubs.nav.backThrows = true
  const orig = console.log; console.log = () => {}
  try { fire(10000) } finally { console.log = orig }
  ok(stubs.calls.includes('back'), 'a back() that throws does not crash the page') }

console.log('6. it fits the round display')
{ open(params('full')); const cx = 240, cy = 240
  for (const w of stubs.widgets) {
    const far = [[w.x, w.y], [w.x + w.w, w.y], [w.x, w.y + w.h], [w.x + w.w, w.y + w.h]].map(([x, y]) => Math.hypot(x - cx, y - cy))
    ok(Math.max(...far) <= 230, 'the box at y=' + w.y + ' has its corners at most ' + Math.round(Math.max(...far)) + ' px from the centre (limit 230)') }
  ok(stubs.widgets[0].y + stubs.widgets[0].h <= stubs.widgets[1].y, 'the title ends before the explanation starts')
  ok(stubs.widgets.every((w) => w.text_style === 1), 'both wrap their text') }

console.log('7. the page is part of the app')
{ const manifest = JSON.parse(fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'app.json'), 'utf8'))
  const pages = manifest.targets.default.module.page.pages
  ok(pages.includes('page/storage') && fs.existsSync(path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'page', 'storage.js')), '7: page/storage is listed in app.json and exists -> ' + JSON.stringify(pages)) }

console.log('\n' + (failures ? failures + ' FAILURE(S)' : 'ALL CHECKS PASS'))
process.exit(failures ? 1 : 0)
