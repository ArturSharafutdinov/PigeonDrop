// utils/relayCrypto.js: opening the envelope the pairing page makes for the watch (Personal Server settings), the secret
// that goes into the QR code, and the watch's check of its own crypto. The envelope is made here by a second, independent
// implementation (what the web page does: WebCrypto), so that the two ends are tested against each other and against the
// shared vector (test/fixtures/relay-envelope-vector.json), not against one reading of the format.

import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { randomBytes } from 'node:crypto'
import * as zc from './stubs/zos-crypto.mjs'
import { generate, fileUrl, stubPath } from './helpers/load.mjs'
import { te, hex, b64u, cat, keysOf, tagOf, sealWithWebCrypto, sealPadded, pkcs7, sealJson, payload, newSecretBytes } from './helpers/relay.mjs'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const HERE = path.dirname(fileURLToPath(import.meta.url))
const VECTOR = JSON.parse(fs.readFileSync(path.join(HERE, 'fixtures', 'relay-envelope-vector.json'), 'utf8'))
const SOURCE = fs.readFileSync(path.join(HERE, '..', 'utils', 'relayCrypto.js'), 'utf8')

const R = await import(fileUrl(generate('utils/relayCrypto.js', { '@zos/crypto': stubPath('zos-crypto.mjs'), './serverUrl': generate('utils/serverUrl.js', {}) })))
const logged = []
console.log = new Proxy(console.log, { apply: (target, self, args) => { if (!String(args[0]).match(/^(\d+\.|  ok|  FAIL|\n)/)) logged.push(args); return Reflect.apply(target, self, args) } })

// ---- the other end: see test/helpers/relay.mjs ----------------------------------------------------------------------
const S = hex(VECTOR.secretHex), CODE = VECTOR.userCode
const reasonOf = (r) => (r.ok ? 'ok' : r.reason)
const reset = () => { Object.assign(zc.behaviour, { generatedKeys: 'random', hmacFails: false, aesFails: false, throws: false, needKeyEncrypt: undefined, keyLength: 16 }); zc.resetKeys(); R.forgetSelfTest() }

console.log('1. base64url: one spelling for every text')
{ let same = true
  for (let n = 0; n < 60; n++) { const bytes = new Uint8Array(randomBytes(n)); const text = R.base64urlEncode(bytes); const back = R.base64urlDecode(text)
    if (text !== Buffer.from(bytes).toString('base64url') || !back || Buffer.compare(Buffer.from(back), Buffer.from(bytes)) !== 0) same = false }
  ok(same, 'encodes like Node and decodes back, for lengths 0 to 59')
  ok(R.base64urlEncode(S) === VECTOR.secretInQrFragment && R.base64urlEncode(S).length === 22, 'the vector secret is the 22 characters of the QR fragment -> ' + R.base64urlEncode(S))
  for (const bad of ['AAAA=', 'AA==', 'A', 'AAAAA', 'AA+A', 'AA/A', 'AA A', 'AA\nA', 'ÄAAA', 'AB', 'AAB', '', null, undefined, 5, {}]) {
    const r = R.base64urlDecode(bad)
    // '' decodes to nothing and is fine as base64; the envelope's length checks refuse it later.
    ok(bad === '' ? r && r.length === 0 : r === null, 'refused: ' + JSON.stringify(bad)) } }

console.log('2. the shared vector')
{ const mine = await sealWithWebCrypto({ secret: S, userCode: CODE, first: hex(VECTOR.firstBlockHex), json: VECTOR.plaintextJson })
  ok(mine.ct === VECTOR.envelope.ct && mine.tag === VECTOR.envelope.tag, 'the page-side implementation in this test makes exactly the vector\'s envelope')
  const r = R.openEnvelope(S, CODE, VECTOR.envelope)
  ok(r.ok && r.serverUrl === 'https://example.com/pigeondrop' && r.host === 'example.com' && r.token === 'pdt_test_0123456789abcdef', 'the watch opens it -> ' + JSON.stringify({ ...r, token: r.ok ? '...' : undefined }))
  for (const needle of [VECTOR.secretInQrFragment, VECTOR.envelope.ct, VECTOR.envelope.tag, VECTOR.userCode, JSON.parse(VECTOR.plaintextJson).url, JSON.parse(VECTOR.plaintextJson).token]) {
    ok(SOURCE.includes(needle), 'the watch\'s built-in check uses the fixture\'s ' + needle.slice(0, 18) + '...') } }

console.log('3. envelopes made by the page open, whatever the content')
{ const cases = {
    'a plain address and token': ['https://my.server.example/pigeondrop', 'tok-123'],
    'a port and a path': ['https://my.server.example:8443/a/b-c_d.e~f', 'pdt_AbC-123_xyz'],
    'an address given in capitals (the page sends it normalized, the watch accepts either)': ['HTTPS://My.Server.Example/Drop/', 'tok'],
    'the longest address (200 characters)': ['https://' + 'a'.repeat(63) + '.example.com/' + 'p'.repeat(116), 't'],
    'the longest token': ['https://x.example', 'T'.repeat(256)],
    'quotes and backslashes in the token (JSON escapes)': ['https://x.example', 'a"b\\c/d\'e'],
    'letters outside ASCII in the token (UTF-8)': ['https://x.example', 'tökén-ключ-令牌-😀'],
  }
  for (const [name, [url, token]] of Object.entries(cases)) {
    for (let i = 0; i < 3; i++) { // the first block and the secret are different every time
      const secret = newSecretBytes(), first = new Uint8Array(randomBytes(16))
      const r = R.openEnvelope(secret, 'bcdf-ghjk', await sealWithWebCrypto({ secret, userCode: 'BCDF-GHJK', first, json: payload(url, token) }))
      if (!(r.ok && r.token === token)) { ok(false, name + ' -> ' + JSON.stringify(r)); break }
      if (i === 2) ok(true, name) } }
  const secret = newSecretBytes()
  const extra = R.openEnvelope(secret, CODE, await sealWithWebCrypto({ secret, userCode: CODE, first: new Uint8Array(16), json: JSON.stringify({ url: 'https://x.example', token: 't', note: 'later versions may add fields' }) }))
  ok(extra.ok && extra.token === 't', 'fields it does not know are ignored')
  let allOpen = true
  for (let i = 0; i < 40; i++) { const r = R.openEnvelope(S, CODE, VECTOR.envelope); if (!r.ok) allOpen = false } // the stub picks a new IV for every AES instance
  ok(allOpen, 'the IV the platform happens to use never changes the result (40 openings, 40 different IVs)') }

console.log('4. refusals, in the order the checks are made')
{ const env = VECTOR.envelope
  const secretOf = (s) => reasonOf(R.openEnvelope(s, CODE, env))
  ok(secretOf(null) === 'secret' && secretOf(new Uint8Array(15)) === 'secret' && secretOf(new Uint8Array(17)) === 'secret' && secretOf('AAECAwQFBgcICQoLDA0ODw') === 'secret' && secretOf([...S]) === 'secret', 'a secret that is not 16 bytes -> secret')
  const versionOf = (e) => reasonOf(R.openEnvelope(S, CODE, e))
  ok([null, undefined, 'x', 5, {}, { ...env, v: 2 }, { ...env, v: 0 }, { ...env, v: '1' }, { ...env, v: 1.5 }, { ct: env.ct, tag: env.tag }].every((e) => versionOf(e) === 'version'), 'no envelope, or a version other than the number 1 -> version')
  const ct = Buffer.from(env.ct, 'base64url'), blocks = (n) => b64u(new Uint8Array(n))
  const shape = { 'ct is not text': { ...env, ct: 5 }, 'tag is not text': { ...env, tag: null }, 'ct missing': { v: 1, tag: env.tag }, 'ct with padding': { ...env, ct: env.ct + '=' }, 'ct in standard base64': { ...env, ct: env.ct.replace(/-/g, '+') },
    'ct not whole blocks': { ...env, ct: b64u(ct.subarray(0, ct.length - 1)) }, 'ct is one block': { ...env, ct: blocks(16) }, 'ct is empty': { ...env, ct: '' }, 'ct is 1040 bytes': { ...env, ct: blocks(1040) }, 'ct has spare bits set': { ...env, ct: blocks(32).slice(0, -1) + 'B' },
    'tag of 31 bytes': { ...env, tag: blocks(31) }, 'tag of 33 bytes': { ...env, tag: blocks(33) }, 'tag empty': { ...env, tag: '' } }
  for (const [name, e] of Object.entries(shape)) ok(versionOf(e) === 'shape', name + ' -> shape')
  ok(reasonOf(R.openEnvelope(S, CODE, { ...env, ct: blocks(1024) })) === 'tag', 'the largest ciphertext allowed is not refused for its size')
  for (const code of ['', '   ', null, undefined, 5, {}, 'BCDF GHJK', 'БCDF-GHJK', 'B'.repeat(33)]) ok(reasonOf(R.openEnvelope(S, code, env)) === 'code' || reasonOf(R.openEnvelope(S, code, env)) === 'tag', 'an unusable user code is refused: ' + JSON.stringify(code))
  ok(reasonOf(R.openEnvelope(S, '', env)) === 'code' && reasonOf(R.openEnvelope(S, null, env)) === 'code', 'no user code -> code')

  const tagFails = (name, e, secret = S, code = CODE) => ok(reasonOf(R.openEnvelope(secret, code, e)) === 'tag', name + ' -> tag')
  tagFails('another user code', env, S, 'BCDF-GHJL'); tagFails('another secret', env, hex('000102030405060708090a0b0c0d0e10'))
  ok(R.openEnvelope(S, 'bcdfghjk', env).ok && R.openEnvelope(S, 'Bcdf-Ghjk', env).ok, 'the case and the dash of the user code do not matter')
  let allRejected = true
  for (let i = 0; i < ct.length; i++) { const bad = Uint8Array.from(ct); bad[i] ^= 0x80; if (reasonOf(R.openEnvelope(S, CODE, { ...env, ct: b64u(bad) })) !== 'tag') allRejected = false }
  ok(allRejected, 'a bit changed in any of the ' + ct.length + ' bytes of the ciphertext -> tag')
  const tag = Buffer.from(env.tag, 'base64url'); allRejected = true
  for (let i = 0; i < tag.length; i++) { const bad = Uint8Array.from(tag); bad[i] ^= 1; if (reasonOf(R.openEnvelope(S, CODE, { ...env, tag: b64u(bad) })) !== 'tag') allRejected = false }
  ok(allRejected, 'a bit changed in any of the 32 bytes of the tag, the first and the last included -> tag')
  const swapped = { ...env, ct: b64u(cat(ct.subarray(16, 32), ct.subarray(0, 16), ct.subarray(32))) }; tagFails('blocks swapped', swapped)
  tagFails('the last block cut off', { ...env, ct: b64u(ct.subarray(0, ct.length - 16)) })
  zc.log.instances.length = 0; R.openEnvelope(S, CODE, { ...env, tag: b64u(new Uint8Array(32)) })
  ok(!zc.log.instances.some((i) => i.algorithmId === zc.alg.AES_CBC), 'a wrong tag is found before anything is decrypted (no AES instance was made)')
  zc.log.instances.length = 0; R.openEnvelope(S, CODE, env)
  ok(zc.log.instances.some((i) => i.algorithmId === zc.alg.AES_CBC && i.options.key_bit_length === 128), '...and a right one is decrypted, with a 128-bit key')

  // Envelopes whose tag is right: the content is what is wrong.
  const valid = payload('https://x.example', 'tok')
  const withFirst = (bytes) => sealPadded({ secret: S, userCode: CODE, padded: bytes })
  const body = cat(new Uint8Array(16), te.encode(valid)), fill = (16 - (body.length % 16)) % 16
  const aligned = cat(body, new Uint8Array(fill === 0 ? 16 : fill)) // zero bytes where the padding should be
  ok(reasonOf(R.openEnvelope(S, CODE, withFirst(aligned))) === 'padding', 'padding of zero -> padding')
  const lenWith = (n) => { const base = cat(new Uint8Array(16), te.encode(valid)); const total = Math.ceil((base.length + 1) / 16) * 16; const out = new Uint8Array(total); out.set(base); out.fill(n, base.length); return out }
  const okPad = pkcs7(cat(new Uint8Array(16), te.encode(valid))), padN = okPad[okPad.length - 1]
  ok(R.openEnvelope(S, CODE, withFirst(okPad)).ok, 'the ordinary padding is accepted')
  const inconsistent = Uint8Array.from(okPad); inconsistent[inconsistent.length - 2] = padN === 1 ? 7 : padN ^ 1
  ok(padN > 1 && reasonOf(R.openEnvelope(S, CODE, withFirst(inconsistent))) === 'padding', 'padding bytes that disagree with the last one -> padding')
  ok(reasonOf(R.openEnvelope(S, CODE, withFirst(lenWith(17)))) === 'padding' && reasonOf(R.openEnvelope(S, CODE, withFirst(lenWith(255)))) === 'padding' && reasonOf(R.openEnvelope(S, CODE, withFirst(lenWith(0)))) === 'padding', 'out-of-range padding -> padding')
  { // a pad of 17 is not PKCS#7 for 16-byte blocks, even when all of its 17 bytes agree and what is left is good JSON
    const base = te.encode(valid); let json = base; while ((16 + json.length + 17) % 16 !== 0) json = cat(json, te.encode(' '))
    const padded = cat(new Uint8Array(16), json, new Uint8Array(17).fill(17))
    ok(padded.length % 16 === 0 && reasonOf(R.openEnvelope(S, CODE, withFirst(padded))) === 'padding', 'a pad of 17 -> padding') }
  const only = sealPadded({ secret: S, userCode: CODE, padded: new Uint8Array(32).fill(16) }) // nothing but the first block and padding
  ok(reasonOf(R.openEnvelope(S, CODE, only)) === 'json', 'an empty message -> json')

  const jsonReason = (json) => reasonOf(R.openEnvelope(S, CODE, sealJson({ secret: S, userCode: CODE, json })))
  const raw = (...bytes) => Uint8Array.from(bytes)
  for (const [name, bad] of Object.entries({ 'a lone continuation byte': raw(0x7b, 0x80), 'an overlong "/" (2 bytes)': raw(0xc0, 0xaf), 'an overlong "/" (3 bytes)': raw(0xe0, 0x80, 0xaf), 'an overlong "/" (4 bytes)': raw(0xf0, 0x80, 0x80, 0xaf), 'a lead byte without its continuation': raw(0xc3, 0x28), 'a 3-byte character with a bad second byte': raw(0xe2, 0x28, 0xa1), 'a 4-byte character with a bad last byte': raw(0xf0, 0x90, 0x80, 0x28), 'a surrogate': raw(0xed, 0xa0, 0x80), 'above U+10FFFF': raw(0xf4, 0x90, 0x80, 0x80), 'a cut-off character': raw(0xe2, 0x82), 'a byte that never occurs': raw(0xff) })) {
    ok(jsonReason(bad) === 'text', name + ' -> text') }
  for (const [name, json] of Object.entries({ 'not JSON': 'hello', 'cut off': '{"url":"https://x.example"', 'an array': '[]', 'null': 'null', 'a number': '5', 'a string': '"x"', 'single quotes': "{'url':'https://x.example','token':'t'}" })) ok(jsonReason(json) === 'json', name + ' -> json')
  const addr = (url) => jsonReason(payload(url, 'tok'))
  for (const url of [undefined, null, 5, '', 'http://x.example', 'x.example', 'https://u:p@x.example', 'https://x.example?a=1', 'https://x.example#f', 'ftp://x.example', 'https://x.example/a b', 'https://[::1]/', 'https://x.example:99999']) ok(addr(url) === 'address', 'address ' + JSON.stringify(url) + ' -> address')
  const tok = (token) => jsonReason(payload('https://x.example', token))
  for (const token of [undefined, null, 5, '', ' ', 'a b', 'a\tb', 'T'.repeat(257), 'a\u0000b', ['x']]) ok(tok(token) === 'token', 'token ' + String(JSON.stringify(token)).slice(0, 20) + ' -> token')
  ok(jsonReason('{"token":"t"}') === 'address' && jsonReason('{"url":"https://x.example"}') === 'token', 'a missing field -> address / token')
  ok(R.openEnvelope(S, CODE, sealJson({ secret: S, userCode: CODE, json: valid })).ok, 'and the same builder with good content opens') }

console.log('5. the secret for the QR code')
{ reset(); const a = R.newSecret(), b = R.newSecret()
  ok(a instanceof Uint8Array && a.length === 16 && b.length === 16 && Buffer.compare(Buffer.from(a), Buffer.from(b)) !== 0, 'a new secret is 16 bytes and differs every time')
  const seen = new Set(); for (let i = 0; i < 200; i++) seen.add(b64u(R.newSecret())); ok(seen.size === 200, '200 secrets, 200 different ones')
  zc.log.createChiper = 0; R.newSecret(); ok(zc.log.createChiper === 2, 'two keys are drawn from the platform for each -> ' + zc.log.createChiper)
  const f = R.secretFragment(a); ok(/^k=[A-Za-z0-9_-]{22}$/.test(f) && Buffer.compare(Buffer.from(R.base64urlDecode(f.slice(2))), Buffer.from(a)) === 0, 'the QR fragment is k= and 22 characters that decode to the secret -> ' + f.length + ' characters')
  // Where the randomness comes from.
  const realRandom = Math.random; Math.random = () => 0.5
  const c = R.newSecret(), d = R.newSecret(); Math.random = realRandom
  ok(Buffer.compare(Buffer.from(c), Buffer.from(d)) !== 0, 'with Math.random stuck, the secrets still differ: the platform\'s keys carry them')
  zc.behaviour.generatedKeys = 'none'; ok(R.newSecret() === null, 'no keys from the platform -> no secret (Math.random alone is never enough)')
  zc.behaviour.generatedKeys = 'zero'; ok(R.newSecret() === null, 'keys of all zeros -> no secret')
  zc.behaviour.generatedKeys = 'same'; ok(R.newSecret() === null, 'a generator that gives the same key twice -> no secret')
  zc.behaviour.generatedKeys = 'zero-first'; zc.log.createChiper = 0; ok(R.newSecret() === null, 'one key of all zeros, the other not -> no secret')
  zc.behaviour.generatedKeys = 'random'; zc.behaviour.keyLength = 15; ok(R.newSecret() === null, 'keys shorter than 16 bytes -> no secret')
  zc.behaviour.keyLength = 32; ok(R.newSecret() instanceof Uint8Array, 'longer keys are fine')
  zc.behaviour.keyLength = 16; zc.behaviour.hmacFails = true; ok(R.newSecret() === null, 'HMAC not available -> no secret')
  zc.behaviour.hmacFails = false; zc.behaviour.aesFails = true; ok(R.newSecret() === null, 'AES not available -> no secret')
  zc.behaviour.aesFails = false; zc.behaviour.throws = true; ok(R.newSecret() === null, 'the platform throwing -> no secret, no exception')
  reset() }

console.log('6. the watch checks its own crypto')
{ reset(); ok(R.selfTest().ok === true, 'a platform that behaves as documented passes')
  const before = zc.log.instances.length; const again = R.selfTest(); ok(again.ok && zc.log.instances.length === before, 'the answer is kept for the rest of the screen\'s life (no second run)')
  R.forgetSelfTest(); R.selfTest(); ok(zc.log.instances.length > before, '...and a screen opened again tests again')
  for (const spelling of [false, 0]) { reset(); zc.behaviour.needKeyEncrypt = spelling
    ok(R.selfTest().ok, 'a platform that needs key_encrypt: ' + JSON.stringify(spelling) + ' for a given key is found out')
    ok(R.openEnvelope(S, CODE, VECTOR.envelope).ok, '...and that spelling is used from then on')
    const used = zc.log.instances.filter((i) => i.options.private_key).slice(-3); ok(used.length === 3 && used.every((i) => i.options.key_encrypt === spelling), '...for every key it is given') }
  reset(); zc.behaviour.needKeyEncrypt = false; ok(!R.openEnvelope(S, CODE, VECTOR.envelope).ok, 'without the test, the first spelling is used and such a platform would fail (the test is what saves it)')
  reset(); zc.behaviour.hmacFails = true; ok(R.selfTest().reason === 'crypto' && R.startRelay() === null, 'no HMAC -> not ok, no relay')
  reset(); zc.behaviour.aesFails = true; ok(R.selfTest().reason === 'crypto', 'no AES -> not ok')
  reset(); zc.behaviour.throws = true; ok(R.selfTest().reason === 'crypto', 'a platform that throws -> not ok')
  reset(); zc.behaviour.generatedKeys = 'none'; ok(R.selfTest().reason === 'random' && R.startRelay() === null, 'no generated keys -> not ok ("random")')
  reset(); zc.behaviour.needKeyEncrypt = 'a spelling nobody tries'; ok(R.selfTest().reason === 'crypto' && R.startRelay() === null, 'a platform whose crypto cannot open the known envelope gives no relay, even though it hands out random keys')
  reset(); zc.behaviour.generatedKeys = 'same'; ok(R.selfTest().reason === 'random', 'a generator that repeats itself -> not ok')
  reset() }

console.log('7. what the pairing screen asks for')
{ reset(); const relay = R.startRelay()
  ok(relay && relay.secret.length === 16 && relay.fragment === R.secretFragment(relay.secret), 'startRelay gives a secret and the text for the QR fragment')
  const other = R.startRelay(); ok(Buffer.compare(Buffer.from(relay.secret), Buffer.from(other.secret)) !== 0, 'a new one each time')
  const sealed = await sealWithWebCrypto({ secret: relay.secret, userCode: 'WXZB-CDFG', first: new Uint8Array(randomBytes(16)), json: payload('https://mine.example/d', 'secret-token') })
  ok(R.openEnvelope(relay.secret, 'WXZB-CDFG', sealed).token === 'secret-token', 'an envelope made for that secret opens with it')
  reset(); R.selfTest(); zc.behaviour.generatedKeys = 'none'; ok(R.startRelay() === null, 'if the platform stops giving keys after the test, there is no relay') ; reset() }

console.log('8. nothing is written to the log')
{ ok(logged.length === 0, 'the module logged nothing in all of the above -> ' + JSON.stringify(logged).slice(0, 80)) }

console.log('\n' + (failures ? failures + ' FAILURE(S)' : 'ALL CHECKS PASS'))
process.exit(failures ? 1 : 0)
