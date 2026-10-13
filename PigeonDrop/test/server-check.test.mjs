// "Проверить связь с сервером", the phone's half (app-side/serverCheck.js).

import { generate, fileUrl } from './helpers/load.mjs'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const TIMEOUT = generate('app-side/timeout.js', {})
const CHECK = generate('app-side/serverCheck.js', { './timeout': TIMEOUT })
const { checkServer } = await import(fileUrl(CHECK))
const calls = []
let health, upload
globalThis.fetch = async (o) => {
  calls.push(o)
  const a = o.url.endsWith('/healthz') ? health : upload
  if (a instanceof Error) throw a
  return { status: a }
}
const run = async (h, u, token = 'tok') => { calls.length = 0; health = h; upload = u; return checkServer({ deviceToken: token }, 'https://s.example') }

let r = await run(200, 400)
ok(r.ok === true && r.paired === true && calls.length === 2, 'healthy server, accepted token -> ' + JSON.stringify(r))
ok(calls[0].url === 'https://s.example/healthz' && calls[1].url === 'https://s.example/v1/messages' && calls[1].headers.Authorization === 'Bearer tok' && calls[1].body === '{}', 'checks /healthz, then the token with an empty upload that stores nothing')
r = await run(new Error('Network down'), 400); ok(r.ok === false && r.stage === 'server' && /Network down/.test(r.error) && calls.length === 1, 'no connection -> stage server -> ' + JSON.stringify(r))
r = await run(503, 400); ok(r.stage === 'server' && r.error === 'HTTP 503' && calls.length === 1, 'server up but unwell -> stage server -> ' + JSON.stringify(r))
r = await run(200, 401); ok(r.ok === false && r.stage === 'token', 'token refused -> stage token -> ' + JSON.stringify(r))
r = await run(200, 502); ok(r.stage === 'upload' && r.error === 'HTTP 502', 'upload route answers 502 -> stage upload -> ' + JSON.stringify(r))
r = await run(200, new Error('reset')); ok(r.stage === 'upload' && /reset/.test(r.error), 'upload request fails -> stage upload -> ' + JSON.stringify(r))
r = await run(200, 400, ''); ok(r.ok === true && r.paired === false && calls.length === 1, 'no token: only the server is checked -> ' + JSON.stringify(r))
process.exit(failures ? 1 : 0)
