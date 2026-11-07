// app-side/index.js: which server each request from the watch is for.
// Recordings and the connection check go to the watch's own `serverUrl` when it has one, otherwise to the PigeonDrop
// server of this build; an address that cannot be used is an error and never the PigeonDrop server; pairing is
// always with the PigeonDrop server.

import { generate, fileUrl, stubPath } from './helpers/load.mjs'
import { log, handlers } from './stubs/app-side-index.mjs'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const S = stubPath('app-side-index.mjs')
let service
globalThis.AppSideService = (options) => { service = options }
await import(fileUrl(generate('app-side/index.js', {
  '../shared/message-side': S, '../config': S, './uploads': S, './pairing': S, './serverCheck': S, './serverConfig': S,
  '../utils/serverUrl': generate('utils/serverUrl.js', {}),
})))
service.onInit()

// What the watch's message arrives as, and what the Side Service answers: { calls made, the reply }.
async function ask(payload) {
  log.length = 0
  let reply
  handlers.request({ request: { payload }, response: ({ data }) => { reply = data } })
  await new Promise((resolve) => setImmediate(resolve))
  return { calls: log.map((c) => c.fn + '@' + c.url), reply }
}
const DEFAULT = 'https://default.example'

console.log('1. recordings')
{ let r = await ask({ type: 'uploadChunk' })
  ok(r.calls.join() === 'uploadChunk@' + DEFAULT, 'no serverUrl -> the PigeonDrop server of this build')
  r = await ask({ type: 'uploadChunk', serverUrl: ' HTTPS://Own.Example:8443/drop/ ' })
  ok(r.calls.join() === 'uploadChunk@https://own.example:8443/drop', 'a serverUrl -> that server, in normal form')
  for (const bad of ['http://own.example', 'own.example', '', null, 5, 'https://u@own.example']) {
    r = await ask({ type: 'uploadChunk', serverUrl: bad })
    ok(r.calls.length === 0 && r.reply.ok === false && r.reply.error === 'bad server address', JSON.stringify(bad) + ' -> an error, and nothing is sent to any server') } }

console.log('2. the connection check')
{ let r = await ask({ type: 'checkServer', deviceToken: 't' })
  ok(r.calls.join() === 'checkServer@' + DEFAULT, 'no serverUrl -> the PigeonDrop server')
  r = await ask({ type: 'checkServer', deviceToken: 't', serverUrl: 'https://own.example' })
  ok(r.calls.join() === 'checkServer@https://own.example', 'a serverUrl -> that server')
  r = await ask({ type: 'checkServer', deviceToken: 't', serverUrl: null })
  ok(r.calls.length === 0 && r.reply.ok === false && r.reply.stage === 'server' && r.reply.error === 'bad server address', 'a damaged address -> the check says so, and asks no server') }

console.log('3. pairing is with the PigeonDrop server whatever the message says')
{ let r = await ask({ type: 'pairStart', clientName: 'x', serverUrl: 'https://own.example' })
  ok(r.calls.join() === 'pairStart@' + DEFAULT, 'pairStart')
  r = await ask({ type: 'pairPoll', deviceCode: 'x', serverUrl: 'https://own.example' })
  ok(r.calls.join() === 'pairPoll@' + DEFAULT, 'pairPoll') }

console.log('4. the address and token entered on the phone')
{ const r = await ask({ type: 'getServerConfig' })
  ok(r.calls.join() === 'getServerConfig@undefined' && r.reply.ok === true && r.reply.configured === false, 'getServerConfig is answered by the Settings App reader') }

console.log('5. other requests')
{ let r = await ask({ type: 'ping' })
  ok(r.reply.ok === true && r.calls.length === 0, 'ping')
  r = await ask({ type: 'nonsense' })
  ok(r.reply.ok === false && /unknown request type/.test(r.reply.error), 'an unknown request is an error') }

process.exit(failures ? 1 : 0)
