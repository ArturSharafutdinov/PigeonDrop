// utils/serverUrl.js: which addresses of a person's own server are taken, and which server a request is for.
// The table of cases is test/fixtures/server-url-cases.json, shared with the web page that hands an address to the watch.

import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { generate, fileUrl } from './helpers/load.mjs'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const { normalizeServerUrl, resolveServer, isUsableToken } = await import(fileUrl(generate('utils/serverUrl.js', {})))
const CASES = JSON.parse(fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), 'fixtures', 'server-url-cases.json'), 'utf8'))

console.log('1. the table of cases (' + CASES.length + ')')
{
  let wrong = 0
  for (const c of CASES) {
    const r = normalizeServerUrl(c.input)
    const same = c.ok ? r.ok === true && r.url === c.url && r.host === c.host : r.ok === false && r.reason === c.reason
    if (!same) { wrong++; console.log('  FAIL ' + JSON.stringify(c.input) + ' -> ' + JSON.stringify(r) + ', expected ' + JSON.stringify(c.ok ? { url: c.url, host: c.host } : { reason: c.reason })) }
  }
  ok(wrong === 0, 'every case gives the expected answer')
  const reasons = ['not_text', 'empty', 'too_long', 'bad_characters', 'no_scheme', 'not_https', 'has_query', 'has_fragment', 'has_credentials', 'bad_host', 'bad_port', 'bad_path']
  const seen = new Set(CASES.filter((c) => !c.ok).map((c) => c.reason))
  ok(reasons.every((r) => seen.has(r)) && [...seen].every((r) => reasons.includes(r)), 'the table has a case for every reason, and only those reasons')
  ok(CASES.filter((c) => c.ok).length >= 20 && CASES.filter((c) => !c.ok).length >= 50, 'both good and bad addresses are well covered')
}

console.log('2. an accepted address is already in its final form')
{
  const good = CASES.filter((c) => c.ok)
  ok(good.every((c) => { const again = normalizeServerUrl(c.url); return again.ok && again.url === c.url && again.host === c.host }), 'normalizing it again changes nothing')
  ok(good.every((c) => /^https:\/\/[a-z0-9.-]+(:[0-9]+)?(\/[A-Za-z0-9._~-]+)*$/.test(c.url)), 'it is https://host[:port][/path] with no trailing slash, query, fragment or login')
}

console.log('3. which server a request is for')
{
  const DEFAULT = 'https://default.example'
  let r = resolveServer({ fileName: 'a.opus' }, DEFAULT)
  ok(r.ok === true && r.url === DEFAULT && r.custom === false, 'no serverUrl in the message -> the default server')
  r = resolveServer(null, DEFAULT); ok(r.ok === true && r.url === DEFAULT && r.custom === false, 'no message at all -> the default server')
  r = resolveServer({ serverUrl: ' HTTPS://My.Server.example:8443/drop/ ' }, DEFAULT)
  ok(r.ok === true && r.url === 'https://my.server.example:8443/drop' && r.custom === true, 'a good serverUrl is used, in normal form -> ' + JSON.stringify(r))
  // The rule that matters: an address the person set that cannot be used is an error, never the default server.
  for (const bad of ['', '   ', 'http://my.server.example', 'my.server.example', 'https://user@my.server.example', 'https://my.server.example?x=1', null, undefined, 0, false, {}, []]) {
    r = resolveServer({ serverUrl: bad }, DEFAULT)
    ok(r.ok === false && r.url === undefined && r.error === 'bad server address', 'serverUrl ' + JSON.stringify(bad) + ' -> an error, not the default server')
  }
  r = resolveServer(Object.assign(Object.create(null), { serverUrl: undefined }), DEFAULT)
  ok(r.ok === false, 'the field present but undefined counts as set')
}

console.log('4. the access token')
{ ok(['a', 'pdt_0123456789abcdef', 'A.b-c_d~e+f/g=', 'x'.repeat(256)].every(isUsableToken), 'ordinary tokens and one of 256 characters are fine')
  ok(['', ' ', 'to ken', 'tok\n', 'tok\t', 'tok\u0000', 'tok\u200a', 'x'.repeat(257), null, undefined, 5, {}, []].every((t) => !isUsableToken(t)), 'empty, with whitespace or control characters, too long, or not text: refused') }

process.exit(failures ? 1 : 0)
