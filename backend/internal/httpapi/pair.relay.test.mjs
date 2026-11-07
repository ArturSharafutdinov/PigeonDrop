// pair.relay.js, the checking and sealing of the pairing page's "Personal server" tab, tried on its own with Node
// (run by pair_test.go, or: node --test pair.relay.test.mjs). It is tested against the very files the watch is tested
// with: the test vector of the envelope and the table of addresses (PigeonDrop/test/fixtures), and against a second
// implementation of the watch's side written with Node's crypto, so that the page and the watch are checked against each
// other and not against one reading of the format.

import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { createDecipheriv, createHmac, randomBytes } from 'node:crypto'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const FIXTURES = path.join(HERE, '..', '..', '..', 'PigeonDrop', 'test', 'fixtures')
const VECTOR = JSON.parse(fs.readFileSync(path.join(FIXTURES, 'relay-envelope-vector.json'), 'utf8'))
const CASES = JSON.parse(fs.readFileSync(path.join(FIXTURES, 'server-url-cases.json'), 'utf8'))

vm.runInThisContext(fs.readFileSync(path.join(HERE, 'pair.relay.js'), 'utf8'), { filename: 'pair.relay.js' })
const R = globalThis.PairRelay

const hex = (h) => Uint8Array.from(Buffer.from(h, 'hex'))
const b64u = (bytes) => Buffer.from(bytes).toString('base64url')
const hmac = (key, data) => new Uint8Array(createHmac('sha256', Buffer.from(key)).update(Buffer.from(data)).digest())
const te = new TextEncoder()

// What the watch does (its code is PigeonDrop/utils/relayCrypto.js; this is a separate reading of the same format).
function open(secret, userCode, env) {
  const ct = Buffer.from(env.ct, 'base64url')
  const kEnc = hmac(secret, te.encode('pigeondrop/relay/v1/enc')).slice(0, 16)
  const kMac = hmac(secret, te.encode('pigeondrop/relay/v1/mac'))
  const code = userCode.replace(/-/g, '').toUpperCase()
  const expected = hmac(kMac, Buffer.concat([te.encode('v1'), Buffer.from([0]), te.encode(code), Buffer.from([0]), ct]))
  assert.equal(b64u(expected), env.tag, 'the tag')
  const decipher = createDecipheriv('aes-128-cbc', kEnc, randomBytes(16)) // any IV: only the first block depends on it
  decipher.setAutoPadding(false)
  const plain = Buffer.concat([decipher.update(ct), decipher.final()])
  const pad = plain[plain.length - 1]
  assert.ok(pad >= 1 && pad <= 16 && plain.subarray(plain.length - pad).every((b) => b === pad), 'the padding')
  return JSON.parse(plain.subarray(16, plain.length - pad).toString('utf8'))
}

test('the table of addresses shared with the watch gives the same answers', () => {
  assert.ok(CASES.filter((c) => c.ok).length >= 20 && CASES.filter((c) => !c.ok).length >= 50)
  const wrong = []
  for (const c of CASES) {
    const r = R.normalizeServerUrl(c.input)
    const same = c.ok ? r.ok === true && r.url === c.url && r.host === c.host : r.ok === false && r.reason === c.reason
    if (!same) wrong.push({ input: c.input, got: r, want: c.ok ? { url: c.url, host: c.host } : { reason: c.reason } })
  }
  assert.deepEqual(wrong, [])
})

test('the same address twice gives the same answer', () => {
  for (const c of CASES.filter((x) => x.ok)) {
    const again = R.normalizeServerUrl(c.url)
    assert.deepEqual(again, { ok: true, url: c.url, host: c.host })
  }
})

test('the rules for a token', () => {
  assert.equal(R.tokenProblem('pdt_test_0123456789abcdef'), null)
  assert.equal(R.tokenProblem('x'.repeat(256)), null)
  assert.equal(R.tokenProblem('t\u00f6k\u00e9n-\u4ee4\u724c'), null)
  assert.equal(R.tokenProblem(''), 'empty')
  assert.equal(R.tokenProblem(undefined), 'empty')
  assert.equal(R.tokenProblem(5), 'empty')
  assert.equal(R.tokenProblem('x'.repeat(257)), 'too_long')
  for (const bad of ['two words', 'a\tb', 'a\nb', 'a\u0000b', 'a\u200ab', ' lead', 'trail ']) assert.equal(R.tokenProblem(bad), 'bad_characters', JSON.stringify(bad))
})

test('the vector: these inputs give exactly this envelope', async () => {
  const env = await R.seal({
    secret: hex(VECTOR.secretHex), userCode: VECTOR.userCode, first: hex(VECTOR.firstBlockHex),
    url: JSON.parse(VECTOR.plaintextJson).url, token: JSON.parse(VECTOR.plaintextJson).token,
  })
  assert.deepEqual(env, VECTOR.envelope)
})

test('a user code written another way gives the same envelope', async () => {
  const o = { secret: hex(VECTOR.secretHex), first: hex(VECTOR.firstBlockHex), url: 'https://example.com/pigeondrop', token: 'pdt_test_0123456789abcdef' }
  for (const code of ['bcdf-ghjk', 'BCDFGHJK', 'Bcdf-Ghjk']) assert.deepEqual(await R.seal({ ...o, userCode: code }), VECTOR.envelope, code)
  assert.notDeepEqual(await R.seal({ ...o, userCode: 'BCDF-GHJL' }), VECTOR.envelope)
})

test('what the page seals, the watch opens', async () => {
  const cases = [
    ['https://my.server.example/pigeondrop', 'tok-123'],
    ['https://my.server.example:8443/a/b-c_d.e~f', 'pdt_AbC-123_xyz'],
    ['https://x.example', 'a"b\\c/d\'e'],
    ['https://x.example', 't\u00f6k\u00e9n-\u043a\u043b\u044e\u0447-\u4ee4\u724c-\ud83d\ude00'],
  ]
  for (const [url, token] of cases) {
    const secret = new Uint8Array(randomBytes(16))
    const env = await R.seal({ secret, userCode: 'WXZB-CDFG', url, token })
    assert.deepEqual(open(secret, 'wxzb-cdfg', env), { url, token })
    assert.equal(env.v, 1)
    assert.match(env.ct, /^[A-Za-z0-9_-]+$/)
    assert.match(env.tag, /^[A-Za-z0-9_-]{43}$/)
    assert.equal(Buffer.from(env.ct, 'base64url').length % 16, 0)
  }
})

test('every envelope is different, and the first block is random', async () => {
  const secret = new Uint8Array(randomBytes(16))
  const seen = new Set()
  for (let i = 0; i < 20; i++) seen.add((await R.seal({ secret, userCode: 'BCDF-GHJK', url: 'https://x.example', token: 'same' })).ct)
  assert.equal(seen.size, 20)
})

test('the largest address and token still fit the 1024 bytes the server and the watch accept', async () => {
  const url = 'https://' + 'a'.repeat(63) + '.example.com/' + 'p'.repeat(116)
  assert.equal(R.normalizeServerUrl(url).ok, true)
  assert.equal(url.length, 200)
  for (const token of ['\u4ee4'.repeat(256), '"'.repeat(256), '\u0434'.repeat(256), 'x'.repeat(256)]) {
    const secret = new Uint8Array(randomBytes(16))
    const env = await R.seal({ secret, userCode: 'BCDF-GHJK', url, token })
    const size = Buffer.from(env.ct, 'base64url').length
    assert.ok(size >= 32 && size <= 1024, `ct is ${size} bytes`)
    assert.deepEqual(open(secret, 'BCDF-GHJK', env), { url, token })
  }
})

test('sealing refuses what it cannot do', async () => {
  const ok = { secret: new Uint8Array(16), userCode: 'BCDF-GHJK', url: 'https://x.example', token: 't' }
  await assert.rejects(R.seal({ ...ok, secret: new Uint8Array(15) }))
  await assert.rejects(R.seal({ ...ok, secret: null }))
  await assert.rejects(R.seal({ ...ok, userCode: '' }))
  await assert.rejects(R.seal({ ...ok, userCode: 'BCDF GHJK' }))
  await assert.rejects(R.seal({ ...ok, userCode: null }))
})

test('the key in the address', () => {
  const k = hex(VECTOR.secretHex)
  assert.deepEqual(R.keyFromFragment('#k=' + VECTOR.secretInQrFragment), k)
  assert.deepEqual(R.keyFromFragment('#x=1&k=' + VECTOR.secretInQrFragment), k)
  assert.deepEqual(R.keyFromFragment('#k=' + VECTOR.secretInQrFragment + '&x=1'), k)
  assert.equal(b64u(k), VECTOR.secretInQrFragment)
  for (const bad of [undefined, null, 5, '', '#', '#k=', '#k', '#K=' + VECTOR.secretInQrFragment, '#key=' + VECTOR.secretInQrFragment,
    '#k=' + VECTOR.secretInQrFragment.slice(1), '#k=' + VECTOR.secretInQrFragment + 'A', '#k=' + VECTOR.secretInQrFragment + '=',
    '#k=' + VECTOR.secretInQrFragment.slice(0, -1) + 'B', // spare bits set
    '#k=' + VECTOR.secretInQrFragment.replace(/./, '+'), '#k=' + 'A'.repeat(22) + '#k=' + 'A'.repeat(22),
    '#k=' + VECTOR.secretInQrFragment + '&k=' + VECTOR.secretInQrFragment]) assert.equal(R.keyFromFragment(bad), null, JSON.stringify(bad))
  const key = R.keyFromFragment('#k=AAAAAAAAAAAAAAAAAAAAAA')
  assert.equal(key.length, 16)
})

test('the pairing code', () => {
  assert.equal(R.canonicalUserCode('BCDF-GHJK'), 'BCDFGHJK')
  assert.equal(R.canonicalUserCode('bcdf-ghjk'), 'BCDFGHJK')
  for (const bad of [undefined, null, 5, '', '-', 'BCDF GHJK', 'BCDF_GHJK', 'B'.repeat(33), '\u0411CDF-GHJK']) assert.equal(R.canonicalUserCode(bad), null, JSON.stringify(bad))
})

test('base64url is strict and round-trips', () => {
  for (let n = 0; n < 40; n++) {
    const bytes = new Uint8Array(randomBytes(n))
    assert.equal(R.base64urlEncode(bytes), Buffer.from(bytes).toString('base64url'))
    assert.deepEqual(R.base64urlDecode(R.base64urlEncode(bytes)), bytes)
  }
  for (const bad of ['AAAA=', 'A', 'AAAAA', 'AA+A', 'AA/A', 'AB', 'AAB', null, 5]) assert.equal(R.base64urlDecode(bad), null, JSON.stringify(bad))
})

test('a browser without WebCrypto is noticed', async () => {
  assert.equal(R.canEncrypt(), true)
  const saved = Object.getOwnPropertyDescriptor(globalThis, 'crypto')
  Object.defineProperty(globalThis, 'crypto', { value: undefined, configurable: true })
  try {
    assert.equal(R.canEncrypt(), false)
    await assert.rejects(R.seal({ secret: new Uint8Array(16), userCode: 'BCDF-GHJK', url: 'https://x.example', token: 't' }))
  } finally {
    Object.defineProperty(globalThis, 'crypto', saved)
  }
  assert.equal(R.canEncrypt(), true)
})
