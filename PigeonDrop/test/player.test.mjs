// utils/player.js: one Player for the whole app, shared by the pages through the app's global data.

import * as stubs from './stubs/player.mjs'
import { generate, fileUrl, stubPath } from './helpers/load.mjs'

const globalData = {}
globalThis.getApp = () => ({ _options: { globalData } })

// Every page is its own bundle on the watch, so each page gets its own copy of the module (and of any
// variable in it). The stub throws on a second Player, like the hardware.
const loadCopy = () => import(fileUrl(generate('utils/player.js', { '@zos/media': stubPath('player.mjs') })))
const pageA = await loadCopy()
const pageB = await loadCopy()

const ok = (cond, msg) => { console.log((cond ? 'ok   ' : 'FAIL ') + msg); if (!cond) process.exitCode = 1 }
const calls = () => stubs.log.join(' | ')
const recorder = () => {
  const seen = []
  return { seen, onError: () => seen.push('error'), onComplete: () => seen.push('complete') }
}

console.log('--- 1. nothing has been played yet ---')
let threw = false
try { pageA.detachPlayback(recorder()) } catch (e) { threw = true }
ok(!threw && stubs.created.length === 0, 'detaching before the first playback is harmless and creates no Player')

console.log('--- 2. the first page to play creates the Player ---')
const a = recorder()
pageA.playAudio('data://a.opus', a)
ok(stubs.created.length === 1, 'exactly one Player was created')
ok(calls() === 'listen PREPARE | listen COMPLETE | setSource data://a.opus | prepare', 'listeners added once, then source set and prepared -> ' + calls())

console.log('--- 3. another page reuses it ---')
stubs.log.length = 0
const b = recorder()
let second = null
try { pageB.playAudio('data://b.opus', b) } catch (e) { second = e }
ok(second === null && stubs.created.length === 1, 'a second page (its own copy of the module) did not create a second Player -> ' + (second && second.message))
ok(calls() === 'setSource data://b.opus | prepare', 'it only pointed the Player at its file, with no new listeners -> ' + calls())

console.log('--- 4. events go to the page that is playing ---')
const player = stubs.created[0]
stubs.log.length = 0
player.emit('PREPARE', true)
ok(calls() === 'start' && a.seen.length === 0 && b.seen.length === 0, 'a prepared file starts playing -> ' + calls())
player.emit('PREPARE', false)
ok(b.seen.join() === 'error' && a.seen.length === 0, 'a file that cannot be prepared is reported to the current page only -> ' + b.seen.join())
stubs.log.length = 0
player.emit('COMPLETE')
ok(calls() === 'stop' && b.seen.join() === 'error,complete' && a.seen.length === 0, 'the end of playback stops the Player and reaches the current page only')

console.log('--- 5. a page that wants no callbacks takes over cleanly ---')
pageA.playAudio('data://list.opus')
const before = b.seen.length
player.emit('PREPARE', false); player.emit('COMPLETE')
ok(b.seen.length === before, 'the previous page is no longer called, and nothing breaks without callbacks')

console.log('--- 6. a page that goes away ---')
const c = recorder()
pageB.playAudio('data://c.opus', c)
pageB.detachPlayback(c)
player.emit('COMPLETE')
ok(c.seen.length === 0, 'after detachPlayback() its callbacks are not called')
const d = recorder(), e = recorder()
pageA.playAudio('data://d.opus', d)
pageB.playAudio('data://e.opus', e)
pageA.detachPlayback(d)
player.emit('COMPLETE')
ok(e.seen.join() === 'complete', 'a page that was already replaced cannot detach the one that replaced it')
