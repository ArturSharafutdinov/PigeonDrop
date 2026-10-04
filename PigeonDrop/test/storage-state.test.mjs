// utils/storageState.js: what the server last said about how full the account's storage is.

import fs from 'fs'; import os from 'os'; import path from 'path'
import { ctl } from './stubs/fs.mjs'
import { generate, fileUrl, stubPath } from './helpers/load.mjs'
const STORAGE = generate('utils/storageState.js', { '@zos/fs': stubPath('fs.mjs') })
let n = 0, failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const fresh = async () => { ctl.root = fs.mkdtempSync(path.join(os.tmpdir(), 'wd-')); return import(fileUrl(STORAGE) + '?t=' + ++n) }
const file = () => path.join(ctl.root, 'storage.json')

console.log('1. nothing said yet')
{ const S = await fresh()
  ok(S.getStorageState() === null, 'no warning')
  S.clearStorageState(); ok(true, 'clearing when there is nothing to clear does not throw') }

console.log('2. only the warnings are kept; "ok" takes the warning away')
{ const S = await fresh()
  S.saveStorageState('nearly_full'); ok(S.getStorageState() === 'nearly_full', 'nearly_full is kept')
  S.saveStorageState('full'); ok(S.getStorageState() === 'full', 'full replaces it')
  S.saveStorageState('ok'); ok(S.getStorageState() === null && !fs.existsSync(file()), 'ok removes the warning and the file')
  S.saveStorageState('ok'); ok(S.getStorageState() === null, 'ok again is harmless') }

console.log('3. the same answer is not written again')
{ const S = await fresh(); S.saveStorageState('full')
  const before = fs.statSync(file()).mtimeMs; fs.utimesSync(file(), new Date(1_000), new Date(1_000))
  S.saveStorageState('full'); ok(fs.statSync(file()).mtimeMs === 1_000 && before !== 1_000, 'the file was left alone (an upload answers every time)') }

console.log('4. only states this app knows are accepted')
{ const S = await fresh()
  for (const state of ['', 'FULL', 'almost', null, undefined, 3, {}, ['full']]) { S.saveStorageState(state) }
  ok(S.getStorageState() === null && !fs.existsSync(file()), 'nothing unknown is stored')
  S.saveStorageState('full'); S.saveStorageState('whatever')
  ok(S.getStorageState() === 'full', 'an unknown state does not disturb the one that is kept')
  const of = S.storageStateOf
  ok(of({ state: 'ok' }) === 'ok' && of({ state: 'nearly_full', usedBytes: 5 }) === 'nearly_full' && of({ state: 'full' }) === 'full', 'storageStateOf reads a server answer')
  for (const bad of [undefined, null, 'full', 5, [], {}, { state: 'exploded' }, { state: 7 }]) ok(of(bad) === null, 'storageStateOf(' + JSON.stringify(bad) + ') is null') }

console.log('5. a damaged file means "no warning", never a crash')
for (const [label, content] of [['not JSON', '{"state":'], ['empty', ''], ['null', 'null'], ['a string', '"full"'], ['no state', '{}'], ['ok kept by mistake', '{"state":"ok"}'], ['an unknown state', '{"state":"bad"}']]) {
  const S = await fresh(); fs.writeFileSync(file(), content)
  const orig = console.log; console.log = () => {}
  let state; try { state = S.getStorageState() } finally { console.log = orig }
  ok(state === null, label + ' -> no warning') }
{ const S = await fresh(); fs.writeFileSync(file(), '{"state":'); S.saveStorageState('full')
  ok(S.getStorageState() === 'full', 'the next answer repairs a damaged file') }

console.log('6. forgetting it when the watch is unpaired')
{ const S = await fresh(); S.saveStorageState('full'); S.clearStorageState()
  ok(S.getStorageState() === null && !fs.existsSync(file()), 'no warning and no file left') }

console.log('\n' + (failures ? failures + ' FAILURE(S)' : 'ALL CHECKS PASS'))
process.exit(failures ? 1 : 0)
