// app-side/serverConfig.js: what the phone hands the watch when the person asks for the address and token they
// entered in the Settings App.

import { generate, fileUrl } from './helpers/load.mjs'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const { getServerConfig } = await import(fileUrl(generate('app-side/serverConfig.js', { '../utils/serverUrl': generate('utils/serverUrl.js', {}) })))
let stored = {}
globalThis.settings = { settingsStorage: { getItem: (key) => (key in stored ? stored[key] : undefined) } }
const ask = (values) => { stored = values; return getServerConfig() }

console.log('1. nothing entered')
for (const [label, values] of [['no fields', {}], ['empty fields', { serverUrl: '', serverToken: '' }], ['blanks', { serverUrl: '  ', serverToken: '\n' }]]) {
  const r = ask(values); ok(r.ok === true && r.configured === false && !('token' in r), label + ' -> not configured')
}

console.log('2. both entered and usable')
{ let r = ask({ serverUrl: 'https://my.server.example', serverToken: 'tok_123' })
  ok(r.ok === true && r.configured === true && r.serverUrl === 'https://my.server.example' && r.token === 'tok_123', 'the address and the token are handed over -> ' + JSON.stringify(r).replace('tok_123', '<token>'))
  r = ask({ serverUrl: ' HTTPS://My.Server.example:8443/drop/ ', serverToken: ' tok_123\n' })
  ok(r.configured === true && r.serverUrl === 'https://my.server.example:8443/drop' && r.token === 'tok_123', 'pasted text is trimmed and the address is in its normal form') }

console.log('3. something entered but not usable: said so, nothing handed over')
for (const [label, values, code] of [
  ['http', { serverUrl: 'http://my.server.example', serverToken: 'tok' }, 'invalid_url'],
  ['no scheme', { serverUrl: 'my.server.example', serverToken: 'tok' }, 'invalid_url'],
  ['a login in the address', { serverUrl: 'https://u:p@my.server.example', serverToken: 'tok' }, 'invalid_url'],
  ['an address but no token', { serverUrl: 'https://my.server.example', serverToken: '' }, 'invalid_token'],
  ['a token but no address', { serverToken: 'tok' }, 'invalid_url'],
  ['a token with a space inside', { serverUrl: 'https://my.server.example', serverToken: 'to ken' }, 'invalid_token'],
  ['a token of 257 characters', { serverUrl: 'https://my.server.example', serverToken: 'a'.repeat(257) }, 'invalid_token'],
]) {
  const r = ask(values); ok(r.ok === false && r.code === code && !('serverUrl' in r) && !('token' in r), label + ' -> ' + code)
}

console.log('4. a phone where the storage cannot be read')
{ globalThis.settings = { settingsStorage: { getItem: () => { throw new Error('no storage') } } }
  let r = getServerConfig(); ok(r.ok === true && r.configured === false, 'a throwing storage means nothing entered')
  delete globalThis.settings
  r = getServerConfig(); ok(r.ok === true && r.configured === false, '...and so does a missing one') }

process.exit(failures ? 1 : 0)
