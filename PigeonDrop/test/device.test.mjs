// utils/device.js: the device token the watch keeps after pairing.

import fs from 'fs'; import os from 'os'; import path from 'path'
import { ctl } from './stubs/fs.mjs'
import { generate, fileUrl, stubPath } from './helpers/load.mjs'
const DEVICE = generate('utils/device.js', { '@zos/fs': stubPath('fs.mjs') })
let n = 0, failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const fresh = async () => { ctl.root = fs.mkdtempSync(path.join(os.tmpdir(), 'wd-')); return import(fileUrl(DEVICE) + '?t=' + ++n) }
const file = () => path.join(ctl.root, 'device.json')

console.log('1. a watch that was never paired')
{ const D = await fresh()
  ok(D.getDeviceToken() === null && D.isPaired() === false, 'no token, not paired')
  D.clearDevice(); ok(true, 'clearing when there is nothing to clear does not throw') }

console.log('2. pairing stores the token; pairing again replaces it')
{ const D = await fresh()
  D.saveDevice({ token: 'pdd_first', deviceId: 'id-1' })
  ok(D.getDeviceToken() === 'pdd_first' && D.isPaired() === true, 'the token is kept')
  ok(JSON.parse(fs.readFileSync(file(), 'utf8')).deviceId === 'id-1', 'with the device id beside it')
  D.saveDevice({ token: 'pdd_second', deviceId: 'id-2' })
  ok(D.getDeviceToken() === 'pdd_second' && JSON.parse(fs.readFileSync(file(), 'utf8')).deviceId === 'id-2', 'a second pairing replaces both') }

console.log('3. the server revoked the device: the token is forgotten')
{ const D = await fresh()
  D.saveDevice({ token: 'pdd_x', deviceId: 'id' }); D.clearDevice()
  ok(D.getDeviceToken() === null && !D.isPaired() && !fs.existsSync(file()), 'no token and no file left') }

console.log('4. a damaged file means "not paired", never a crash')
for (const [label, content] of [['not JSON', '{"token":'], ['empty', ''], ['null', 'null'], ['an array', '[]'], ['a string', '"pdd_x"'], ['no token', '{}'], ['a number for a token', '{"token":5}'], ['an empty token', '{"token":""}']]) {
  const D = await fresh(); fs.writeFileSync(file(), content)
  const logged = []; const orig = console.log; console.log = (...a) => logged.push(a.join(' '))
  let token; try { token = D.getDeviceToken() } finally { console.log = orig }
  ok(token === null && D.isPaired() === false, label + ' -> not paired') }
{ const D = await fresh(); fs.writeFileSync(file(), '{"token":'); D.saveDevice({ token: 'pdd_fixed', deviceId: 'i' })
  ok(D.getDeviceToken() === 'pdd_fixed', 'pairing again repairs a damaged file') }

console.log('\n' + (failures ? failures + ' FAILURE(S)' : 'ALL CHECKS PASS'))
process.exit(failures ? 1 : 0)
