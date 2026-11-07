// Settings for a server of the person's own, handed over through the pairing page ("Personal server" tab): the watch's
// half. The page encrypts the server's address and access token in the browser with a key only this watch has, the
// server relays the envelope without being able to read it, and openEnvelope() opens it here. The format (envelope v1)
// is in docs/architecture.md; test/fixtures/relay-envelope-vector.json is the test vector the page and the watch share.
//
//   secret S   16 random bytes, new for every pairing attempt (newSecret). It goes from this watch into the QR code,
//              after the "#", which a browser never sends anywhere, and is forgotten when the attempt ends.
//   keys       K_enc = HMAC-SHA-256(S, "pigeondrop/relay/v1/enc")[0..16), K_mac = HMAC-SHA-256(S, "pigeondrop/relay/v1/mac")
//   envelope   { v: 1, ct, tag }, base64url without padding. tag = HMAC-SHA-256(K_mac, "v1" 0x00 USERCODE 0x00 ct) binds
//              the envelope to the pairing it was made for; ct = AES-128-CBC(K_enc, 16 random bytes + JSON + PKCS#7).
//
// Two limits of the watch's @zos/crypto shape this, both from its documentation (nothing here has run on a real watch
// yet, which is why selfTest() exists):
//   - AES-CBC takes no IV and does no padding. So the page puts 16 random bytes before the JSON and encrypts with a zero
//     IV; here the first block of the result (garbage, whatever IV the watch used) is dropped, and the PKCS#7 padding
//     is removed by hand. In CBC every other block depends only on the key and the block before it.
//   - getRandomValues exists from API_LEVEL 5.0 and this app targets 4.0, so it is not used. The random bytes come from
//     the keys AES-CBC generates for itself (createChiper, API_LEVEL 3.0); a little local noise is mixed in, but is never
//     the only source (newSecret refuses to work without the generated keys).
//
// openEnvelope() checks, in this order: the version, the shape, the tag (before anything is decrypted), and only then
// decrypts, removes the padding, parses, and checks the address and the token with the same rules as everywhere else.
// It never logs and never returns a reason that depends on what was inside.

import { createCrypto, alg } from '@zos/crypto'
import { normalizeServerUrl, isUsableToken } from './serverUrl'

const VERSION = 1
const SECRET_BYTES = 16
const BLOCK = 16
const MIN_CIPHERTEXT = 32 // the empty first block, and at least one block of the JSON
const MAX_CIPHERTEXT = 1024
const TAG_BYTES = 32

const ZERO = new Uint8Array(1)
const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_'

// The vector of test/fixtures/relay-envelope-vector.json, used by selfTest() on the watch itself.
// (test/relay-crypto.test.mjs checks that these are the fixture's values.)
const VECTOR = {
  secret: 'AAECAwQFBgcICQoLDA0ODw',
  userCode: 'BCDF-GHJK',
  envelope: {
    v: 1,
    ct: 'IL_xkljjZ373IqfWkbZjC328P8jRq32lFMPq5pZFZDQ-UqD4ot53WwbcwZU6PM87BeY6usCE6PS9f8OBH6hB_vOOa0D-yvOrGjEstMDqypkefhka1V_xX-rcHj0v5AyZ',
    tag: '6Qz7houIH8CgMhvCsYYdZV1iALg_KeSLYet7k2OIECY',
  },
  serverUrl: 'https://example.com/pigeondrop',
  token: 'pdt_test_0123456789abcdef',
}

// Whether the platform wants key_encrypt (re-encrypting a key with the hardware key) spelled out for a key it is given
// is not documented, and the default is not stated. selfTest() finds the spelling that opens the test vector.
const KEY_VARIANTS = [{}, { key_encrypt: false }, { key_encrypt: 0 }]
let keyVariant = KEY_VARIANTS[0]

function keyed(options) {
  return Object.assign({}, options, keyVariant)
}

// ---- bytes -----------------------------------------------------------------------------------------------------------

function bytesOf(buffer, length) {
  const all = new Uint8Array(buffer)
  const n = typeof length === 'number' && length >= 0 && length <= all.length ? length : all.length
  const out = new Uint8Array(n)
  for (let i = 0; i < n; i++) out[i] = all[i]
  return out
}

function concat(parts) {
  let total = 0
  for (let i = 0; i < parts.length; i++) total += parts[i].length
  const out = new Uint8Array(total)
  let at = 0
  for (let i = 0; i < parts.length; i++) {
    out.set(parts[i], at)
    at += parts[i].length
  }
  return out
}

function bytesFromList(list) {
  const out = new Uint8Array(list.length)
  for (let i = 0; i < list.length; i++) out[i] = list[i]
  return out
}

function slice(bytes, from, to) {
  const out = new Uint8Array(to - from)
  for (let i = from; i < to; i++) out[i - from] = bytes[i]
  return out
}

function ascii(text) {
  const out = new Uint8Array(text.length)
  for (let i = 0; i < text.length; i++) out[i] = text.charCodeAt(i) & 0x7f
  return out
}

function sameBytes(a, b) {
  if (a.length !== b.length) return false
  let diff = 0
  for (let i = 0; i < a.length; i++) diff |= a[i] ^ b[i] // no early exit: how long a wrong tag matches is not told
  return diff === 0
}

function isZero(bytes) {
  for (let i = 0; i < bytes.length; i++) if (bytes[i] !== 0) return false
  return true
}

// base64url without padding. Strict: only the alphabet, no "=" and no spare bits set, so that a text has one spelling.
export function base64urlEncode(bytes) {
  let out = ''
  for (let i = 0; i < bytes.length; i += 3) {
    const n = (bytes[i] << 16) | ((i + 1 < bytes.length ? bytes[i + 1] : 0) << 8) | (i + 2 < bytes.length ? bytes[i + 2] : 0)
    out += ALPHABET[(n >> 18) & 63] + ALPHABET[(n >> 12) & 63]
    if (i + 1 < bytes.length) out += ALPHABET[(n >> 6) & 63]
    if (i + 2 < bytes.length) out += ALPHABET[n & 63]
  }
  return out
}

export function base64urlDecode(text) {
  if (typeof text !== 'string' || text.length % 4 === 1) return null
  const values = []
  for (let i = 0; i < text.length; i++) {
    const v = ALPHABET.indexOf(text[i])
    if (v < 0) return null
    values.push(v)
  }
  const spare = text.length % 4 === 2 ? 0x0f : text.length % 4 === 3 ? 0x03 : 0
  if (spare !== 0 && (values[values.length - 1] & spare) !== 0) return null
  const out = new Uint8Array(Math.floor((text.length * 3) / 4))
  let at = 0
  for (let i = 0; i < values.length; i += 4) {
    const n = (values[i] << 18) | ((values[i + 1] || 0) << 12) | ((values[i + 2] || 0) << 6) | (values[i + 3] || 0)
    if (at < out.length) out[at++] = (n >> 16) & 255
    if (at < out.length) out[at++] = (n >> 8) & 255
    if (at < out.length) out[at++] = n & 255
  }
  return out
}

// Strict UTF-8 (no overlong forms, no surrogates, nothing above U+10FFFF); null for bytes that are not text.
function utf8Decode(bytes) {
  let out = ''
  let i = 0
  while (i < bytes.length) {
    const b = bytes[i]
    let cp
    let extra
    let min
    if (b < 0x80) {
      cp = b
      extra = 0
      min = 0
    } else if (b >= 0xc2 && b <= 0xdf) {
      cp = b & 0x1f
      extra = 1
      min = 0x80
    } else if (b >= 0xe0 && b <= 0xef) {
      cp = b & 0x0f
      extra = 2
      min = 0x800
    } else if (b >= 0xf0 && b <= 0xf4) {
      cp = b & 0x07
      extra = 3
      min = 0x10000
    } else {
      return null
    }
    for (let k = 1; k <= extra; k++) {
      const c = bytes[i + k]
      if (c === undefined || (c & 0xc0) !== 0x80) return null
      cp = (cp << 6) | (c & 0x3f)
    }
    if (cp < min || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff)) return null
    if (cp >= 0x10000) {
      const v = cp - 0x10000
      out += String.fromCharCode(0xd800 + (v >> 10), 0xdc00 + (v & 0x3ff))
    } else {
      out += String.fromCharCode(cp)
    }
    i += extra + 1
  }
  return out
}

// ---- the two primitives the platform gives -----------------------------------------------------------------------------

function hmacSha256(key, data) {
  try {
    const hmac = createCrypto(alg.HMAC_SHA_256, keyed({ private_key: key }))
    const result = hmac ? hmac.encrypt(data) : undefined
    return result && result.md_content ? bytesOf(result.md_content, result.length) : null
  } catch (e) {
    return null
  }
}

// Decrypts whole blocks. The IV the platform uses is not ours to choose and does not matter (see the header).
function aesCbcDecrypt(key, data) {
  try {
    const aes = createCrypto(alg.AES_CBC, keyed({ key_bit_length: 128, private_key: key }))
    const result = aes ? aes.decrypt(data) : undefined
    return result && result.data ? bytesOf(result.data, result.length) : null
  } catch (e) {
    return null
  }
}

// A key AES-CBC makes for itself: 16 bytes or more, or null.
function generatedKey() {
  try {
    const aes = createCrypto(alg.AES_CBC, keyed({ key_bit_length: 128 }))
    const info = aes ? aes.createChiper() : undefined
    if (!info || !info.private_key) return null
    const key = bytesOf(info.private_key, info.private_key_length)
    return key.length >= SECRET_BYTES && !isZero(key) ? key : null
  } catch (e) {
    return null
  }
}

// ---- the envelope ----------------------------------------------------------------------------------------------------

function fail(reason) {
  return { ok: false, reason }
}

// "BCDF-GHJK" and "bcdfghjk" are the same code. Anything that is not letters and digits is refused (null).
function canonicalUserCode(userCode) {
  if (typeof userCode !== 'string') return null
  const code = userCode.replace(/-/g, '').toUpperCase()
  return /^[A-Z0-9]{1,32}$/.test(code) ? code : null
}

// Opens an envelope for the pairing with this user code, with the secret that was in the QR code.
// Returns { ok: true, serverUrl, host, token } or { ok: false, reason }; the reason is a short code for tests and logs
// ('secret' 'version' 'shape' 'code' 'crypto' 'tag' 'padding' 'text' 'json' 'address' 'token'), never part of the content.
export function openEnvelope(secret, userCode, envelope) {
  if (!(secret instanceof Uint8Array) || secret.length !== SECRET_BYTES) return fail('secret')
  if (!envelope || typeof envelope !== 'object' || envelope.v !== VERSION) return fail('version')
  const ct = base64urlDecode(envelope.ct)
  const tag = base64urlDecode(envelope.tag)
  if (!ct || !tag || ct.length % BLOCK !== 0 || ct.length < MIN_CIPHERTEXT || ct.length > MAX_CIPHERTEXT || tag.length !== TAG_BYTES) {
    return fail('shape')
  }
  const code = canonicalUserCode(userCode)
  if (code === null) return fail('code')

  const kEnc = hmacSha256(secret, ascii('pigeondrop/relay/v1/enc'))
  const kMac = hmacSha256(secret, ascii('pigeondrop/relay/v1/mac'))
  if (!kEnc || !kMac) return fail('crypto')
  const expected = hmacSha256(kMac, concat([ascii('v1'), ZERO, ascii(code), ZERO, ct]))
  if (!expected) return fail('crypto')
  if (!sameBytes(tag, expected)) return fail('tag')

  const plain = aesCbcDecrypt(slice(kEnc, 0, 16), ct)
  if (!plain || plain.length !== ct.length) return fail('crypto')
  const pad = plain[plain.length - 1]
  if (pad < 1 || pad > BLOCK || pad > plain.length - BLOCK) return fail('padding')
  for (let i = plain.length - pad; i < plain.length; i++) if (plain[i] !== pad) return fail('padding')

  const text = utf8Decode(slice(plain, BLOCK, plain.length - pad))
  if (text === null) return fail('text')
  let parsed
  try {
    parsed = JSON.parse(text)
  } catch (e) {
    return fail('json')
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return fail('json')
  const checked = normalizeServerUrl(parsed.url)
  if (!checked.ok) return fail('address')
  if (!isUsableToken(parsed.token)) return fail('token')
  return { ok: true, serverUrl: checked.url, host: checked.host, token: parsed.token }
}

// ---- the secret ------------------------------------------------------------------------------------------------------

// A new secret for one pairing attempt: 16 bytes, or null if this watch has no source of randomness worth trusting.
// The strong source is the keys AES-CBC generates; two are drawn and must differ (a generator that repeats itself
// would be no source at all). Local noise (Math.random and the clock) is mixed in but cannot stand in for them.
export function newSecret() {
  const first = generatedKey()
  const second = generatedKey()
  if (!first || !second || sameBytes(first, second)) return null

  const noise = []
  for (let i = 0; i < 16; i++) noise.push(Math.floor(Math.random() * 256))
  const now = Date.now()
  for (let i = 0; i < 6; i++) noise.push(Math.floor(now / Math.pow(2, 8 * i)) % 256)

  const mixed = hmacSha256(concat([first, second, bytesFromList(noise)]), ascii('pigeondrop/relay/v1/secret'))
  return mixed ? slice(mixed, 0, SECRET_BYTES) : null
}

// What the QR code carries after the "#": k=<the secret, base64url>.
export function secretFragment(secret) {
  return 'k=' + base64urlEncode(secret)
}

// ---- checking this watch ---------------------------------------------------------------------------------------------

function vectorOpens() {
  const secret = base64urlDecode(VECTOR.secret)
  const opened = secret ? openEnvelope(secret, VECTOR.userCode, VECTOR.envelope) : fail('secret')
  return opened.ok && opened.serverUrl === VECTOR.serverUrl && opened.token === VECTOR.token
}

// Whether this watch can take part: its HMAC and AES must open a known envelope, and it must give different secrets.
// Nothing about @zos/crypto's behaviour with a given key is documented beyond the signatures, and nothing here has run on
// a real watch, so the watch proves it to itself instead of trusting it. Returns { ok: true } or { ok: false, reason }.
let tested = null
export function selfTest() {
  if (tested !== null) return tested
  for (let i = 0; i < KEY_VARIANTS.length; i++) {
    keyVariant = KEY_VARIANTS[i]
    if (vectorOpens()) {
      const a = newSecret()
      const b = newSecret()
      tested = a && b && !sameBytes(a, b) ? { ok: true } : { ok: false, reason: 'random' }
      if (!tested.ok) keyVariant = KEY_VARIANTS[0]
      return tested
    }
  }
  keyVariant = KEY_VARIANTS[0]
  tested = { ok: false, reason: 'crypto' }
  return tested
}

// The test hook: forget the result of selfTest() (a page that is opened again tests again).
export function forgetSelfTest() {
  tested = null
  keyVariant = KEY_VARIANTS[0]
}

// What the pairing screen needs: a secret and the text for the QR code's fragment, or null when this watch cannot do it
// (the QR code is then the plain one and the web page says the watch needs an update).
export function startRelay() {
  if (!selfTest().ok) return null
  const secret = newSecret()
  return secret ? { secret, fragment: secretFragment(secret) } : null
}
