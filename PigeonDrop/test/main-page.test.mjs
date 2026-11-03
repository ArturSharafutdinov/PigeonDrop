// Main page (page/index.js): recording, stopwatch, screen hold while recording and while uploading.

import * as stubs from './stubs/main-page.mjs'
import { generate, fileUrl, stubPath } from './helpers/load.mjs'
let opts, tickFn = null, cleared = 0, now = 1_000_000
globalThis.Page = (o) => { opts = o }
globalThis.setInterval = (fn) => { tickFn = fn; return 7 }
globalThis.clearInterval = () => { cleared++; tickFn = null }
// Active timeouts only: clearTimeout removes them, so "firing everything left" is what the device would do.
let timeouts = [], nextTimeoutId = 100
globalThis.setTimeout = (fn, ms) => { const id = nextTimeoutId++; timeouts.push({ id, fn, ms }); return id }
globalThis.clearTimeout = (id) => { timeouts = timeouts.filter((t) => t.id !== id) }
const fireTimeouts = (ms) => { const due = timeouts.filter((t) => t.ms === ms); timeouts = timeouts.filter((t) => t.ms !== ms); due.forEach((t) => t.fn()) }
Date.now = () => now
const S = stubPath('main-page.mjs')
const LINK = generate('utils/linkProblem.js', {})   // the real texts: what the screen says is what is tested
const SCREEN = generate('utils/screen.js', { '@zos/display': S })   // the real hold logic on top of the stubbed display
const PAGE = generate('page/index.js', {
  '@zos/ui': S, '@zos/utils': S, '@zos/interaction': S, '@zos/media': S, '@zos/router': S,
  '../utils/screen': SCREEN, '../utils/player': S, '../utils/recordings': S, '../utils/uploadQueue': S, '../utils/device': S,
  '../utils/linkProblem': LINK, '../config': S,
})
await import(fileUrl(PAGE))
const mk = () => Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) })
const ok = (cond, msg) => { console.log((cond ? 'ok   ' : 'FAIL ') + msg); if (!cond) process.exitCode = 1 }
const last = () => stubs.texts[stubs.texts.length - 1]
const reset = () => { stubs.log.length = 0; stubs.texts.length = 0; cleared = 0 }
const released = () => stubs.log.includes('resetPageBrightTime') && stubs.log.includes('resetDropWristScreenOff') && stubs.log.includes('resetPalmScreenOff')
const heldFor = (ms) => stubs.log.includes('setPageBrightTime ' + (ms + 5000)) && stubs.log.includes('pauseDropWristScreenOff ' + ms) && stubs.log.includes('pausePalmScreenOff ' + ms)
const UPLOAD = 60000
const holdTimers = () => timeouts.filter((t) => t.ms === UPLOAD).length

console.log('--- 1. normal recording: screen held, stopwatch counts, stop starts the upload hold ---')
let page = mk(); page.build(); reset(); timeouts.length = 0
page.toggleRecording()
ok(heldFor(5000), 'recording holds the screen for the limit (5 s) + slack -> ' + stubs.log.slice(0, 4).join(' | '))
ok(stubs.log.indexOf('recorder.start') > stubs.log.indexOf('pausePalmScreenOff 5000'), 'screen held BEFORE recorder.start (no gap)')
ok(last() === 'Идёт запись\n00:00\nНажмите ещё раз,\nчтобы остановить', 'initial text shows 00:00 -> ' + JSON.stringify(last()))
now += 1000; tickFn(); ok(last().includes('\n00:01\n'), 'after 1 s shows 00:01')
now += 2400; tickFn(); ok(last().includes('\n00:03\n'), 'after 3.4 s shows 00:03 (wall clock, floored)')
reset()
page.toggleRecording()
ok(stubs.log.includes('recorder.stop'), 'stop: recorder stopped')
ok(heldFor(UPLOAD), 'stop: screen re-held for the upload (60 s) -> ' + stubs.log.filter((l) => /Bright|Off/.test(l)).join(' | '))
ok(holdTimers() === 1, 'a single 60 s timer will end the hold')
ok(stubs.log.lastIndexOf('resetPageBrightTime') < stubs.log.lastIndexOf('setPageBrightTime 65000'), 'the hold is re-armed (reset, then set), not left half-released')
ok(cleared === 1 && tickFn === null, 'stopwatch interval cleared')
ok(stubs.log.indexOf('pruneRecordings') > stubs.log.indexOf('recorder.stop') && stubs.log.includes('retryPendingUploads'), 'stop still prunes and kicks the upload queue')
ok(last().startsWith('Отправка...'), 'status after stop -> ' + JSON.stringify(last()))

console.log('--- 2. recording limit auto-stops, sends, and holds the screen for the upload ---')
page = mk(); page.build(); reset(); timeouts.length = 0; now = 2_000_000
page.toggleRecording()
for (let i = 1; i <= 4; i++) { now += 1000; tickFn() }
ok(page.state.recording === true && last().includes('\n00:04\n'), 'still recording at 4 s')
reset(); now += 1000; tickFn()
ok(page.state.recording === false && stubs.log.includes('recorder.stop') && stubs.log.includes('retryPendingUploads'), 'limit reached at 5 s: recording stopped and queued for upload')
ok(last().startsWith('Лимит записи\nдостигнут'), 'user told why -> ' + JSON.stringify(last()))
ok(heldFor(UPLOAD) && timeouts.length === 1 && cleared === 1, 'auto-stop also holds the screen for the upload; stopwatch cleared')

console.log('--- 3. page destroyed: mid-recording and mid-upload ---')
page = mk(); page.build(); reset(); timeouts.length = 0; now = 3_000_000
page.toggleRecording(); reset()
page.onDestroy()
ok(stubs.log.includes('recorder.stop') && released() && cleared === 1, 'onDestroy mid-recording stops recorder, releases screen, clears timer')
reset(); page.onDestroy(); ok(JSON.stringify(stubs.log) === '["onUploadState null"]' && cleared === 0, 'onDestroy while idle only unsubscribes from upload state -> ' + JSON.stringify(stubs.log))
page = mk(); page.build(); page.toggleRecording(); page.toggleRecording(); reset()
ok(page.state.uploadHoldTimer !== null, 'upload hold active before destroy')
page.onDestroy()
ok(released() && page.state.uploadHoldTimer === null && holdTimers() === 0, 'onDestroy mid-upload releases the screen and clears the 60 s timer')

console.log('--- 4. display call fails: recording still starts, failure is logged ---')
page = mk(); page.build(); reset(); stubs.setDisplayResult(-1)
const logged = []; const orig = console.log; console.log = (...a) => logged.push(a.join(' '))
page.toggleRecording(); console.log = orig
ok(page.state.recording === true, 'recording started despite display error')
ok(logged.filter((l) => l.startsWith('display call failed')).length === 3, 'all three failures logged -> ' + logged.filter((l) => l.startsWith('display')).join(' | '))
stubs.setDisplayResult(0)

console.log('--- 6. upload queue integration ---')
page = mk(); reset(); timeouts.length = 0; page.build()
ok(stubs.log.includes('onUploadState set'), 'build subscribes to upload state')
ok(!stubs.log.includes('retryPendingUploads') && timeouts.length === 1 && timeouts[0].ms === 600, 'app-open sweep is DEFERRED (600 ms), not run inside build()')
ok(page.state.uploadHoldTimer === null && !stubs.log.some((l) => /Bright|Off/.test(l)), 'build() itself holds nothing; the hold starts with the first chunk (see section 8)')
timeouts[0].fn(); ok(stubs.log.includes('retryPendingUploads'), '...and does run when the timer fires')
reset(); now = 6_000_000; page.toggleRecording()
ok(stubs.log.includes('setActive voice_1.opus'), 'start: queue told which file is being written -> ' + stubs.log.find((l) => l.startsWith('setActive')))
ok(stubs.log.indexOf('setActive voice_1.opus') < stubs.log.indexOf('recorder.start'), '...before the recorder starts')
const before = stubs.texts.length; stubs.listener({ state: 'sending', index: 1, count: 1, chunk: 3, total: 11 })
ok(stubs.texts.length === before, 'upload status is IGNORED while recording (never overwrites the stopwatch)')
reset(); page.toggleRecording()
ok(stubs.log.indexOf('setActive null') > stubs.log.indexOf('recorder.stop') && stubs.log.indexOf('setActive null') < stubs.log.indexOf('pruneRecordings'), 'stop: active file cleared after the recorder stops and BEFORE pruning/sweep')
reset(); stubs.listener({ state: 'sending', index: 2, count: 3, chunk: 4, total: 11 })
ok(last() === 'Отправка 2 из 3\nчасть 4 из 11', 'sending -> ' + JSON.stringify(last()))
stubs.listener({ state: 'done', pending: 2 }); ok(last() === 'В очереди: 2', 'done with leftovers -> ' + JSON.stringify(last()))
stubs.listener({ state: 'done', pending: 0 }); ok(last().startsWith('Отправлено'), 'done -> ' + JSON.stringify(last().split('\n')[0]))
stubs.listener({ state: 'error', message: 'Timed out in 60000ms.', pending: 3 })
ok(last() === 'Не удалось отправить\nTimed out in 60000ms.\nВ очереди: 3', 'error shown, not silent -> ' + JSON.stringify(last()))
stubs.listener({ state: 'connecting', pending: 2 }); ok(last() === 'Связь с телефоном...\nВ очереди: 2', 'connecting -> ' + JSON.stringify(last()))
// The two link problems get their own words, with what to do about each; every other failure keeps its own reason.
stubs.listener({ state: 'error', kind: 'phone', message: 'Timed out in 10000ms.', pending: 3 })
ok(last() === 'Нет связи с телефоном\nПерезапустите\nприложение Zepp\nВ очереди: 3', 'the phone does not answer: says so and tells to restart Zepp -> ' + JSON.stringify(last()))
stubs.listener({ state: 'error', kind: 'server', message: 'server: no answer in 30 s', pending: 1 })
ok(last() === 'Нет связи с сервером\nПроверьте интернет\nна телефоне\n(no answer in 30 s)\nВ очереди: 1', 'the phone cannot reach the server: says so, with the reason -> ' + JSON.stringify(last()))
ok(!last().includes('Zepp') && !last().includes('server:'), '...without the other problem\'s advice or the technical prefix')
stubs.listener({ state: 'error', kind: 'other', message: 'cannot open file', pending: 1 })
ok(last() === 'Не удалось отправить\ncannot open file\nВ очереди: 1', 'an unknown kind falls back to the reason -> ' + JSON.stringify(last()))
reset(); page.toggleRecording(); reset(); page.onDestroy()
ok(stubs.log.includes('onUploadState null') && stubs.log.includes('setActive null') && stubs.log.includes('recorder.stop'), 'onDestroy unsubscribes, closes the recording and clears the active file')

console.log('--- 7. screen hold while the recording is being sent ---')
// 7a. success: held through sending and leftovers, released when everything is sent
page = mk(); page.build(); page.toggleRecording(); page.toggleRecording(); reset()
stubs.listener({ state: 'sending', index: 1, count: 1, chunk: 1, total: 12 })
stubs.listener({ state: 'sending', index: 1, count: 1, chunk: 12, total: 12 })
ok(!released(), '7a: still held while chunks are being sent')
stubs.listener({ state: 'done', pending: 1 })
ok(!released() && page.state.uploadHoldTimer !== null, '7a: still held when "done" leaves something in the queue')
stubs.listener({ state: 'done', pending: 0 })
ok(released() && page.state.uploadHoldTimer === null && holdTimers() === 0, '7a: released as soon as everything is sent; the 60 s timer is gone')
ok(last().startsWith('Отправлено'), '7a: "Отправлено" is what the user sees -> ' + JSON.stringify(last().split('\n')[0]))
// 7b. failure: the reason stays readable until the 60 s window ends
page = mk(); page.build(); page.toggleRecording(); page.toggleRecording(); reset()
stubs.listener({ state: 'error', message: 'Timed out', pending: 1 })
ok(!released() && page.state.uploadHoldTimer !== null, '7b: screen stays on after an error so the reason can be read')
fireTimeouts(UPLOAD)
ok(released() && page.state.uploadHoldTimer === null, '7b: ...and is released when the 60 s window ends')
// 7c. nothing sent for 60 s (e.g. phone unreachable, no events at all): the cap still releases the screen
page = mk(); page.build(); page.toggleRecording(); page.toggleRecording(); reset()
fireTimeouts(UPLOAD)
ok(released(), '7c: hard 60 s cap releases the screen even if no upload event ever arrives')
// 7d. new recording during the hold: the hold timer must not release the screen mid-recording
page = mk(); page.build(); page.toggleRecording(); page.toggleRecording()
reset(); page.toggleRecording()
ok(page.state.uploadHoldTimer === null && holdTimers() === 0, '7d: starting a recording cancels the pending upload-hold timer')
ok(heldFor(5000), '7d: recording hold replaces the upload hold -> ' + stubs.log.filter((l) => /Bright|Off/.test(l)).join(' | '))
reset(); fireTimeouts(UPLOAD); stubs.listener({ state: 'done', pending: 0 })
ok(!released() && page.state.recording === true, '7d: late upload events during the new recording never release the screen')
// 7e. nothing to send after stop: no hold is kept
stubs.setPending(0)
page = mk(); page.build(); reset(); page.toggleRecording(); page.toggleRecording()
ok(released() && page.state.uploadHoldTimer === null && holdTimers() === 0, '7e: empty queue after stop -> screen released immediately, no timer left')
ok(last().startsWith('Отправлено'), '7e: status -> ' + JSON.stringify(last().split('\n')[0]))
stubs.setPending(1)
// 7f. recording started and stopped repeatedly: never more than one timer outstanding
page = mk(); page.build(); timeouts.length = 0
for (let i = 0; i < 4; i++) { page.toggleRecording(); page.toggleRecording() }
ok(holdTimers() === 1, '7f: after 4 quick record/stop cycles exactly one hold timer is outstanding -> ' + holdTimers())

console.log('--- 8. older queued recordings (app open / reconnect) hold the screen while they are sent ---')
now = 9_000_000
// 8a. sweep at app open: first chunk starts a hold, progress renews it, done releases it
page = mk(); page.build(); reset(); timeouts.length = 0
stubs.listener({ state: 'sending', index: 1, count: 2, chunk: 1, total: 11 })
ok(heldFor(UPLOAD) && holdTimers() === 1, '8a: first chunk of an old recording starts a 60 s hold -> ' + stubs.log.filter((l) => /Bright|Off/.test(l)).join(' | '))
reset(); now += 10_000; stubs.listener({ state: 'sending', index: 1, count: 2, chunk: 2, total: 11 })
ok(!stubs.log.some((l) => /Bright|Off|reset/.test(l)) && holdTimers() === 1, '8a: a chunk 10 s later does not re-arm the display (50 s still left)')
reset(); now += 25_000; stubs.listener({ state: 'sending', index: 1, count: 2, chunk: 3, total: 11 })
ok(heldFor(UPLOAD) && holdTimers() === 1, '8a: with under half the window left, progress renews it - and only one timer exists -> timers ' + holdTimers())
reset(); stubs.listener({ state: 'sending', index: 2, count: 2, chunk: 11, total: 11 })
ok(!released(), '8a: still held while the second old recording is sent')
stubs.listener({ state: 'done', pending: 0 })
ok(released() && holdTimers() === 0 && page.state.uploadHoldTimer === null, '8a: released as soon as the old recordings are all sent')
// 8b. a long queue is not cut off at 60 s because progress keeps renewing the window
page = mk(); page.build(); reset(); timeouts.length = 0; now = 10_000_000
let renewals = 0
for (let s = 0; s <= 300; s += 5) { const before = stubs.log.filter((l) => l === 'setPageBrightTime 65000').length; stubs.listener({ state: 'sending', index: 1, count: 9, chunk: 1, total: 11 }); if (stubs.log.filter((l) => l === 'setPageBrightTime 65000').length > before) renewals++; now += 5_000 }
ok(renewals >= 8 && renewals <= 12 && holdTimers() === 1, '8b: 5 minutes of steady progress -> ' + renewals + ' renewals (about one per 30 s), never a gap, one timer')
// 8c. progress stops (phone unreachable): the hold runs out on its own
reset(); fireTimeouts(UPLOAD)
ok(released() && page.state.uploadHoldTimer === null, '8c: no further chunks -> the 60 s window ends and the screen is released')
// 8d. error: reason stays readable until the window ends; a retry that makes progress holds again
page = mk(); page.build(); reset(); timeouts.length = 0
stubs.listener({ state: 'sending', index: 1, count: 1, chunk: 1, total: 4 }); reset(); stubs.listener({ state: 'error', message: 'Timed out', pending: 1 })
ok(!released() && last().startsWith('Не удалось отправить'), '8d: after an error the screen stays on and the reason is shown')
now += 40_000; reset(); stubs.listener({ state: 'sending', index: 1, count: 1, chunk: 1, total: 4 })
ok(heldFor(UPLOAD) && holdTimers() === 1, '8d: a retry that starts sending renews the hold (one timer)')
// 8e. a recording takes the screen over from an upload hold, and uploads during it never touch the hold
page = mk(); page.build(); timeouts.length = 0; stubs.listener({ state: 'sending', index: 1, count: 1, chunk: 1, total: 4 })
reset(); page.toggleRecording()
ok(heldFor(5000) && holdTimers() === 0, '8e: recording replaces the upload hold and cancels its timer')
reset(); stubs.listener({ state: 'sending', index: 1, count: 1, chunk: 2, total: 4 })
ok(holdTimers() === 0 && !stubs.log.some((l) => /Bright|Off/.test(l)), '8e: sweep progress during a recording changes nothing about the screen')

console.log('--- 9. a watch that is not paired ---')
// 9a. the recording is kept and the screen says what to do - not "sending", which would be untrue
stubs.setPaired(false)
page = mk(); page.build(); reset(); timeouts.length = 0; now = 11_000_000
page.toggleRecording(); reset(); page.toggleRecording()
ok(last() === 'Записано. Часы не\nпривязаны: Настройки\n→ Привязать часы', '9a: after stop -> ' + JSON.stringify(last()))
ok(stubs.log.includes('retryPendingUploads'), '9a: the queue is still kicked (it reports "unpaired" by itself)')
// 9b. the limit message is kept in front of it
page = mk(); page.build(); page.toggleRecording(); reset()
for (let i = 1; i <= 5; i++) { now += 1000; tickFn() }
ok(last().startsWith('Лимит записи\nдостигнут') && last().includes('не\nпривязаны'), '9b: limit + unpaired -> ' + JSON.stringify(last()))
// 9c. what the upload queue reports
reset(); stubs.listener({ state: 'unpaired', pending: 2, revoked: false })
ok(last() === 'Часы не привязаны\nНастройки →\nПривязать часы\nВ очереди: 2', '9c: never paired -> ' + JSON.stringify(last()))
stubs.listener({ state: 'unpaired', pending: 1, revoked: true })
ok(last() === 'Привязка отозвана\nНастройки →\nПривязать часы\nВ очереди: 1', '9c: revoked by the server -> ' + JSON.stringify(last()))
// 9d. ... and an "unpaired" report never overwrites the stopwatch
page = mk(); page.build(); page.toggleRecording(); const n9 = stubs.texts.length
stubs.listener({ state: 'unpaired', pending: 1, revoked: false })
ok(stubs.texts.length === n9, '9d: ignored while recording')
// 9e. paired again: the usual wording
stubs.setPaired(true)
page = mk(); page.build(); page.toggleRecording(); reset(); page.toggleRecording()
ok(last().startsWith('Отправка...'), '9e: paired -> ' + JSON.stringify(last()))

console.log('--- 10. the storage warning: a page of its own, opened when the server says so ---')
const pushes = () => stubs.log.filter((l) => l.startsWith('push '))
const WARNING = (state) => 'push page/storage {"state":"' + state + '"}'
// 10a. nothing at app open, and the old label on this page is gone
stubs.created.length = 0; page = mk(); reset(); page.build()
ok(pushes().length === 0, '10a: opening the app does not open the warning page')
ok(!stubs.created.some((o) => o.color === 0xffb74d) && stubs.created.filter((o) => o.type === 'TEXT').length === 1, '10a: the main page has one text label (the status), as before the warning existed')
// 10b. what the upload queue reports
page = mk(); page.build(); reset(); now = 12_000_000
stubs.listener({ state: 'done', pending: 0, alert: 'full' })
ok(JSON.stringify(pushes()) === JSON.stringify([WARNING('full')]), '10b: "done" with alert full opens the page with that state -> ' + JSON.stringify(pushes()))
ok(last().startsWith('Отправлено'), '10b: the status still says it was sent -> ' + JSON.stringify(last().split('\n')[0]))
reset(); stubs.listener({ state: 'done', pending: 0, alert: 'nearly_full' })
ok(JSON.stringify(pushes()) === JSON.stringify([WARNING('nearly_full')]), '10b: nearly_full -> ' + JSON.stringify(pushes()))
reset(); stubs.listener({ state: 'done', pending: 0, alert: null }); stubs.listener({ state: 'done', pending: 1 }); stubs.listener({ state: 'done', pending: 0, alert: undefined })
ok(pushes().length === 0, '10b: no alert, no page')
reset(); stubs.listener({ state: 'error', message: 'unreadable', pending: 1, alert: 'full' })
ok(JSON.stringify(pushes()) === JSON.stringify([WARNING('full')]) && last().startsWith('Не удалось отправить'), '10b: an alert that comes with an error still opens the page, and the error is still shown')
reset(); stubs.listener({ state: 'sending', index: 1, count: 1, chunk: 1, total: 4 }); stubs.listener({ state: 'unpaired', pending: 1, revoked: true }); stubs.listener({ state: 'unpaired', pending: 1, revoked: false })
ok(pushes().length === 0, '10b: sending and unpaired events never open it')
// 10c. never in the middle of a recording
page = mk(); page.build(); page.toggleRecording(); reset()
stubs.listener({ state: 'done', pending: 0, alert: 'nearly_full' })
ok(pushes().length === 0 && stubs.texts.length === 0, '10c: during a recording nothing opens and the stopwatch is left alone')
page.toggleRecording()
ok(JSON.stringify(pushes()) === JSON.stringify([WARNING('nearly_full')]), '10c: once the recording ends, the page opens with what was said -> ' + JSON.stringify(pushes()))
reset(); page.toggleRecording(); page.toggleRecording()
ok(pushes().length === 0, '10c: ...once, not at every later recording')
page = mk(); page.build(); page.toggleRecording(); reset()
stubs.listener({ state: 'done', pending: 0, alert: 'full' }); stubs.listener({ state: 'done', pending: 0, alert: 'nearly_full' })
page.toggleRecording()
ok(JSON.stringify(pushes()) === JSON.stringify([WARNING('full')]), '10c: if several arrive during a recording, the stronger one wins -> ' + JSON.stringify(pushes()))
page = mk(); page.build(); page.toggleRecording(); reset()
stubs.listener({ state: 'done', pending: 0, alert: 'nearly_full' }); stubs.listener({ state: 'done', pending: 0, alert: 'full' })
page.toggleRecording()
ok(JSON.stringify(pushes()) === JSON.stringify([WARNING('full')]), '10c: whichever order they come in -> ' + JSON.stringify(pushes()))

console.log('--- 11. "Отмена": a recording in progress can be thrown away ---')
{
  const find = (text) => stubs.widgets.find((w) => w.opts.text === text)
  const fresh = () => { stubs.widgets.length = 0; stubs.setPending(1); stubs.setDiscardWorks(true); const p = mk(); p.build(); reset(); timeouts.length = 0; return p }
  const corner = (o) => Math.max(Math.hypot(o.x - 240, o.y - 240), Math.hypot(o.x + o.w - 240, o.y - 240), Math.hypot(o.x - 240, o.y + o.h - 240), Math.hypot(o.x + o.w - 240, o.y + o.h - 240))

  page = fresh()
  ok(!find('Отмена').visible && find('Настройки').visible && find('Список записей').visible, '11a: idle: "Отмена" is hidden, the way to settings and the list is open')
  const cancel = find('Отмена')
  ok(corner(cancel.opts) < 230, '11a: the button is inside the round display\'s safe radius (' + Math.round(corner(cancel.opts)) + ' < 230)')
  ok(stubs.widgets.indexOf(cancel) > stubs.widgets.indexOf(find('Настройки')) && stubs.widgets.indexOf(cancel) > stubs.widgets.indexOf(find('Список записей')), '11a: it is created after the two it replaces, so it lies on top of them')
  ok(cancel.opts.y >= 250 && cancel.opts.y < 290, '11a: it sits right under the stopwatch text (y=' + cancel.opts.y + ')')
  page.toggleRecording()
  ok(find('Отмена').visible && !find('Настройки').visible && !find('Список записей').visible, '11b: recording: only "Отмена" is shown')
  find('Настройки').opts.click_func(); find('Список записей').opts.click_func()
  ok(!stubs.log.some((l) => l.startsWith('push ')), '11b: even if a hidden button were pressed, no page opens during a recording')
  page.toggleRecording()
  ok(!find('Отмена').visible && find('Настройки').visible && find('Список записей').visible, '11b: stopping the normal way brings the two buttons back')

  console.log('11c. cancel')
  page = fresh(); page.toggleRecording(); now += 2000; tickFn(); reset()
  find('Отмена').opts.click_func()
  ok(stubs.log.includes('recorder.stop'), '11c: the recorder is stopped')
  ok(stubs.log.includes('discard voice_1.opus'), '11c: the file is deleted -> ' + stubs.log.join(' | '))
  ok(stubs.log.indexOf('setActive null') > stubs.log.indexOf('discard voice_1.opus'), '11c: the file stays "the one being written" until it is gone, so no sweep can pick it up')
  ok(!stubs.log.includes('pruneRecordings') && !stubs.log.includes('retryPendingUploads'), '11c: nothing is pruned and no upload starts: the recording goes nowhere')
  ok(last() === 'Запись отменена\n\nКлик — записать\nДолгое нажатие —\nвоспроизвести последнюю', '11c: the screen is the main one again -> ' + JSON.stringify(last()))
  ok(page.state.recording === false && page.state.recorder === null && cleared === 1 && tickFn === null, '11c: not recording, the stopwatch is stopped')
  ok(released() && page.state.uploadHoldTimer === null && holdTimers() === 0, '11c: the screen is let go, no upload hold is armed (the recording\'s hold is gone)')
  ok(!find('Отмена').visible && find('Настройки').visible && find('Список записей').visible, '11c: the two buttons are back, "Отмена" is hidden')

  console.log('11d. what a cancel does not break')
  reset(); find('Отмена').opts.click_func()
  ok(stubs.log.length === 0, '11d: pressed with no recording, it does nothing -> ' + JSON.stringify(stubs.log))
  page.toggleRecording()
  ok(stubs.log.includes('recorder.start') && page.state.recording === true && find('Отмена').visible, '11d: a new recording starts normally after a cancel')
  reset(); page.toggleRecording()
  ok(stubs.log.includes('recorder.stop') && !stubs.log.some((l) => l.startsWith('discard')) && stubs.log.includes('retryPendingUploads'), '11d: ...and that one, stopped the normal way, is kept and sent')
  page = fresh(); page.toggleRecording(); page.toggleRecording(); reset()
  ok(page.state.uploadHoldTimer !== null, '11d: (an upload hold from the previous recording is running)')
  page.toggleRecording(); reset(); find('Отмена').opts.click_func()
  ok(page.state.uploadHoldTimer === null && holdTimers() === 0 && released(), '11d: cancelling the next recording leaves no stale upload-hold timer behind')

  console.log('11e. a storage warning that came during the recording')
  page = fresh(); page.toggleRecording(); stubs.listener({ state: 'sending', index: 1, count: 1, chunk: 1, total: 1, alert: 'full' }); reset()
  find('Отмена').opts.click_func()
  ok(stubs.log.some((l) => l.startsWith('push page/storage')), '11e: it is shown after the cancel, as after a stop -> ' + stubs.log.filter((l) => l.startsWith('push')).join())

  console.log('11f. the file cannot be deleted')
  page = fresh(); page.toggleRecording(); stubs.setDiscardWorks(false); reset()
  find('Отмена').opts.click_func()
  ok(last() === 'Не удалось отменить:\nзапись сохранена и\nбудет отправлена', '11f: the person is told the recording was kept, not that it is gone -> ' + JSON.stringify(last()))
  ok(stubs.log.includes('pruneRecordings') && stubs.log.includes('retryPendingUploads') && stubs.log.includes('setActive null'), '11f: it is then treated as an ordinary recording: queued for upload')
  ok(page.state.recording === false && find('Настройки').visible && !find('Отмена').visible, '11f: and the screen is the main one again')
  stubs.setDiscardWorks(true)
}

console.log('--- 12. playback goes through the shared player (utils/player.js) ---')
{
  const page = mk(); page.build(); reset(); stubs.playback.length = 0; stubs.detached.length = 0
  page.playLatestRecording()
  ok(stubs.playback.length === 1 && stubs.playback[0].file === 'x', '12a: the newest recording is handed to the shared player -> ' + JSON.stringify(stubs.playback.map((p) => p.file)))
  ok(last() === 'Воспроизведение...', '12a: the status says it is playing')
  const callbacks = stubs.playback[0].callbacks
  callbacks.onError()
  ok(last().startsWith('Ошибка чтения\nзаписи'), '12b: a file that cannot be prepared is reported on the screen -> ' + JSON.stringify(last().split('\n')[0]))
  callbacks.onComplete()
  ok(last() === page.idleText(), '12c: the end of playback brings the idle text back')
  page.playLatestRecording()
  ok(stubs.playback[1].callbacks === callbacks, '12d: playing again hands over the same callbacks, so they can be detached')
  page.onDestroy()
  ok(stubs.detached[stubs.detached.length - 1] === callbacks, '12e: destroying the page detaches them, so a late event cannot touch its widgets')
}
