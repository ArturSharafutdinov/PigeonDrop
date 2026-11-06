// utils/device.js: the device token the watch keeps after pairing, and the address of the person's own server if it has one.

import fs from 'fs'; import os from 'os'; import path from 'path'
import { ctl } from './stubs/fs.mjs'
import { generate, fileUrl, stubPath } from './helpers/load.mjs'
const DEVICE = generate('utils/device.js', { '@zos/fs': stubPath('fs.mjs'), './serverUrl': generate('utils/serverUrl.js', {}) })
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

console.log('5. a watch set up for the person\'s own server')
{ const D = await fresh()
  ok(D.isCustomServer() === false && D.getServerUrl() === null && D.serverLabel() === null, 'a new watch uses the PigeonDrop server')
  D.saveCustomServer({ serverUrl: ' HTTPS://My.Server.example:8443/drop/ ', token: 'tok_own' })
  ok(D.isCustomServer() === true && D.getServerUrl() === 'https://my.server.example:8443/drop', 'the address is kept in its normal form')
  ok(D.getDeviceToken() === 'tok_own' && D.isPaired() === true, 'with its token')
  ok(D.serverLabel() === 'my.server.example:8443', 'the screens get host and port, not the whole address')
  const saved = JSON.parse(fs.readFileSync(file(), 'utf8'))
  ok(saved.token === 'tok_own' && saved.serverUrl === 'https://my.server.example:8443/drop' && !('deviceId' in saved), 'one file holds both, and no device id of the PigeonDrop server') }

console.log('6. an address or a token the app would not accept is refused before anything is written')
{ const D = await fresh()
  const throws = (fn) => { try { fn(); return false } catch (e) { return true } }
  D.saveCustomServer({ serverUrl: 'https://good.example', token: 'tok_good' })
  const before = fs.readFileSync(file(), 'utf8')
  for (const [label, arg] of [['http', { serverUrl: 'http://bad.example', token: 'tok' }], ['no address', { token: 'tok' }], ['a login in the address', { serverUrl: 'https://u@bad.example', token: 'tok' }],
    ['no token', { serverUrl: 'https://bad.example' }], ['an empty token', { serverUrl: 'https://bad.example', token: '' }], ['a number for a token', { serverUrl: 'https://bad.example', token: 5 }],
    ['a space in the token', { serverUrl: 'https://bad.example', token: 'to ken' }], ['a newline in the token', { serverUrl: 'https://bad.example', token: 'tok\n' }], ['a token of 257 characters', { serverUrl: 'https://bad.example', token: 'a'.repeat(257) }]]) {
    ok(throws(() => D.saveCustomServer(arg)) && fs.readFileSync(file(), 'utf8') === before, label + ' -> refused, the earlier setup is untouched') }
  ok(!throws(() => D.saveCustomServer({ serverUrl: 'https://good.example', token: 'a'.repeat(256) })), 'a token of 256 characters is fine') }

console.log('7. pairing with the PigeonDrop server ends a setup for the own server')
{ const D = await fresh()
  D.saveCustomServer({ serverUrl: 'https://own.example', token: 'tok_own' })
  D.saveDevice({ token: 'pdd_account', deviceId: 'id-9' })
  ok(D.isCustomServer() === false && D.getServerUrl() === null && D.getDeviceToken() === 'pdd_account', 'no address is left, the account token is used')
  D.saveCustomServer({ serverUrl: 'https://other.example', token: 'tok_other' })
  ok(D.getServerUrl() === 'https://other.example' && D.getDeviceToken() === 'tok_other', 'and a setup for an own server replaces an account pairing') }

console.log('8. the own server refuses the token: the token goes, the address stays')
{ const D = await fresh()
  D.saveCustomServer({ serverUrl: 'https://own.example', token: 'tok_own' })
  D.dropCustomToken()
  ok(D.getDeviceToken() === null && D.isPaired() === false, 'no token')
  ok(D.isCustomServer() === true && D.getServerUrl() === 'https://own.example', 'but the watch is still set up for that server: it does not go back to PigeonDrop')
  ok(JSON.stringify(JSON.parse(fs.readFileSync(file(), 'utf8'))) === '{"serverUrl":"https://own.example"}', 'the file keeps only the address')
  D.saveCustomServer({ serverUrl: 'https://own.example', token: 'tok_new' })
  ok(D.getDeviceToken() === 'tok_new', 'a new token makes the watch work again')
  const E = await fresh()
  E.saveDevice({ token: 'pdd_account', deviceId: 'i' }); E.dropCustomToken()
  ok(E.getDeviceToken() === 'pdd_account', 'on a watch paired with an account it does nothing')
  const F = await fresh(); F.dropCustomToken()
  ok(!fs.existsSync(file()), 'on a new watch it creates nothing') }

console.log('9. a damaged address never turns into the PigeonDrop server')
for (const [label, content] of [['http', { token: 'tok', serverUrl: 'http://own.example' }], ['not in normal form', { token: 'tok', serverUrl: 'https://Own.Example/' }], ['a number', { token: 'tok', serverUrl: 5 }],
  ['empty', { token: 'tok', serverUrl: '' }], ['null', { token: 'tok', serverUrl: null }], ['a login in it', { token: 'tok', serverUrl: 'https://u:p@own.example' }]]) {
  const D = await fresh(); fs.writeFileSync(file(), JSON.stringify(content))
  ok(D.isCustomServer() === true && D.getServerUrl() === null && D.getDeviceToken() === null && D.isPaired() === false, label + ' -> set up for an own server, but no address and no token to send with') }

console.log('10. going back to PigeonDrop, and a damaged file')
{ const D = await fresh()
  D.saveCustomServer({ serverUrl: 'https://own.example', token: 'tok_own' }); D.clearDevice()
  ok(D.isCustomServer() === false && D.getServerUrl() === null && D.isPaired() === false && !fs.existsSync(file()), 'clearing leaves a watch that is not paired and uses PigeonDrop')
  D.saveCustomServer({ serverUrl: 'https://own.example', token: 'tok_own' }); fs.writeFileSync(file(), '{"token":"tok_own","serverUrl":')
  const logged = []; const orig = console.log; console.log = (...a) => logged.push(a.join(' '))
  let state; try { state = [D.isCustomServer(), D.getDeviceToken()] } finally { console.log = orig }
  ok(state[0] === false && state[1] === null, 'a half-written file means "not paired": nothing is sent, to anyone') }

console.log('\n' + (failures ? failures + ' FAILURE(S)' : 'ALL CHECKS PASS'))
process.exit(failures ? 1 : 0)
