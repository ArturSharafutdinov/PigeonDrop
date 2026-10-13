// Settings page (page/settings.js): the "Очистить память" button and its confirmation.

import * as stubs from './stubs/settings-page.mjs'
import { generate, fileUrl, stubPath } from './helpers/load.mjs'
let opts, timeouts = [], nid = 1
globalThis.Page = (o) => { opts = o }
globalThis.setTimeout = (fn, ms) => { const id = nid++; timeouts.push({ id, fn, ms }); return id }
globalThis.clearTimeout = (id) => { timeouts = timeouts.filter((t) => t.id !== id) }
const S = stubPath('settings-page.mjs')
const PAGE = generate('page/settings.js', {
  '@zos/ui': S, '@zos/utils': S, '@zos/router': S,
  '../utils/device': S, '../utils/recordings': S, '../utils/uploadQueue': S,
})
await import(fileUrl(PAGE))
const ok = (c, m) => { console.log((c ? 'ok   ' : 'FAIL ') + m); if (!c) process.exitCode = 1 }
const mk = () => Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) })
const page = mk(); page.build()
const status = () => stubs.widgets[0].text
const btn = stubs.widgets.find((w) => w.opts.text === 'Очистить память')
ok(!!btn, 'the button "Очистить память" exists')
const o = btn.opts
const corner = (x, y) => Math.hypot(x - 240, y - 240)
ok(Math.max(corner(o.x, o.y + o.h), corner(o.x + o.w, o.y + o.h)) < 230, 'button stays inside the round-display safe radius (' + Math.round(Math.max(corner(o.x, o.y + o.h), corner(o.x + o.w, o.y + o.h))) + ' < 230)')
ok(stubs.widgets.filter((w) => w.type === 'BUTTON').every((w, i, a) => i === 0 || w.opts.y >= a[i - 1].opts.y + a[i - 1].opts.h), 'buttons do not overlap')
btn.opts.click_func()
ok(btn.text === 'Нажмите ещё раз' && status() === 'Удалить все записи: 3\nНажмите ещё раз' && stubs.calls.length === 0, 'first press only asks for confirmation, deletes nothing -> ' + JSON.stringify(status()))
ok(timeouts.length === 1 && timeouts[0].ms === 5000, 'confirmation expires after 5 s')
timeouts[0].fn()
ok(btn.text === 'Очистить память' && status().startsWith('Часы не привязаны') && stubs.calls.length === 0 && page.state.clearTimer === null, 'expiry: label and status restored, still nothing deleted')
btn.opts.click_func(); btn.opts.click_func()
ok(stubs.calls.join() === 'clearAllRecordings' && status() === 'Память очищена\nУдалено записей: 3', 'second press within 5 s clears -> ' + JSON.stringify(status()))
ok(btn.text === 'Очистить память' && timeouts.length === 0, 'label restored, timer cancelled')
stubs.setClearResult({ removed: 0, busy: true }); btn.opts.click_func(); btn.opts.click_func()
ok(status() === 'Идёт отправка.\nПодождите и повторите', 'busy (upload running) is explained, not silent -> ' + JSON.stringify(status()))
btn.opts.click_func(); page.onDestroy()
ok(timeouts.length === 0 && page.state.clearTimer === null, 'onDestroy drops a pending confirmation timer')

// Pairing: the other button, and what the status line says about it.
const fresh = () => { stubs.widgets.length = 0; stubs.pushed.length = 0; const p = mk(); p.build(); return p }
fresh()
ok(stubs.widgets.filter((w) => w.type === 'BUTTON').map((w) => w.opts.text).join('|') === 'Привязать часы|Проверить связь|Очистить память', 'the buttons are pairing, clearing and the connection check (server setup and sync are gone)')
stubs.widgets.find((w) => w.opts.text === 'Привязать часы').opts.click_func()
ok(stubs.pushed.length === 1 && stubs.pushed[0].url === 'page/pair', '"Привязать часы" opens the pairing screen -> ' + JSON.stringify(stubs.pushed))
ok(stubs.widgets[0].text === 'Часы не привязаны\nк аккаунту', 'status says the watch is not paired -> ' + JSON.stringify(stubs.widgets[0].text))
stubs.setPaired(true); fresh()
ok(stubs.widgets[0].text === 'Часы привязаны\nк аккаунту', 'and once paired, that it is -> ' + JSON.stringify(stubs.widgets[0].text))
{ const b = stubs.widgets.find((w) => w.opts.text === 'Очистить память'); b.opts.click_func(); timeouts[0].fn()
  ok(stubs.widgets[0].text === 'Часы привязаны\nк аккаунту', 'after the clear confirmation expires, the status line shows the pairing state again') }

// Проверить связь: the phone walks the way to the server and the status line says where it broke.
{
  const sent = []; let answer
  globalThis.getApp = () => ({ _options: { globalData: { messageBuilder: { request: (m, o) => { sent.push({ m, o }); return typeof answer === 'function' ? answer() : Promise.resolve(answer) } } } } })
  const flush = () => new Promise((r) => setImmediate(r))
  const run = async (a) => { answer = a; const p = fresh(); sent.length = 0; stubs.widgets.find((w) => w.opts.text === 'Проверить связь').opts.click_func(); const during = stubs.widgets[0].text; await flush(); return { p, during, text: stubs.widgets[0].text } }
  const b = fresh() && stubs.widgets.find((w) => w.opts.text === 'Проверить связь').opts
  const corner2 = (x, y) => Math.hypot(x - 240, y - 240)
  ok(Math.max(corner2(b.x, b.y + b.h), corner2(b.x + b.w, b.y + b.h)) < 230, 'the check button stays inside the safe radius')
  let r = await run({ ok: true, stage: 'upload', paired: true })
  ok(r.during === 'Проверяю связь...' && sent[0].m.type === 'checkServer' && sent[0].m.deviceToken === 'tok' && sent[0].o.timeout === 25000, 'asks the phone, with the token, and says it is checking -> ' + JSON.stringify(sent[0]))
  ok(r.text === 'Связь в порядке:\nсервер доступен,\nчасы привязаны', 'all fine -> ' + JSON.stringify(r.text))
  r = await run({ ok: false, stage: 'server', error: 'server: no answer in 10 s' }); ok(r.text === 'Сервер недоступен\n(no answer in 10 s)', 'server unreachable, with the reason -> ' + JSON.stringify(r.text))
  r = await run({ ok: false, stage: 'server', error: 'HTTP 503' }); ok(r.text === 'Сервер недоступен\n(HTTP 503)', 'server unwell -> ' + JSON.stringify(r.text))
  r = await run({ ok: false, stage: 'token', error: 'HTTP 401' }); ok(r.text.startsWith('Сервер доступен, но'), 'token refused -> ' + JSON.stringify(r.text))
  r = await run({ ok: false, stage: 'upload', error: 'HTTP 500' }); ok(r.text === 'Ошибка сервера\n(HTTP 500)', 'upload route fails -> ' + JSON.stringify(r.text))
  r = await run(() => Promise.reject(new Error('Timed out'))); ok(r.text === 'Телефон не отвечает\nПерезапустите\nприложение Zepp', 'no answer from the phone -> ' + JSON.stringify(r.text))
  stubs.setPaired(false)
  r = await run({ ok: true, stage: 'server', paired: false }); ok(sent[0].m.deviceToken === '' && r.text === 'Сервер доступен,\nчасы не привязаны', 'not paired: only the server is checked -> ' + JSON.stringify(r.text))
  { answer = () => new Promise(() => {}); const p = fresh(); sent.length = 0; const btn2 = stubs.widgets.find((w) => w.opts.text === 'Проверить связь'); btn2.opts.click_func(); btn2.opts.click_func()
    ok(sent.length === 1, 'a second press while checking sends nothing') }
}
