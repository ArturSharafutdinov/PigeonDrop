// The other end of the relay envelope: what the web page does, written separately from the watch's code (utils/relayCrypto.js),
// so that tests check the two against each other and not against one reading of the format. Shared by the tests of the
// crypto and of the pairing screen.
import { webcrypto, createCipheriv, createHmac, randomBytes } from 'node:crypto'

const subtle = webcrypto.subtle
export const te = new TextEncoder()
export const hex = (h) => Uint8Array.from(Buffer.from(h, 'hex'))
export const b64u = (bytes) => Buffer.from(bytes).toString('base64url')
export const cat = (...parts) => Uint8Array.from(Buffer.concat(parts.map((p) => Buffer.from(p))))
export const hmac = (key, data) => new Uint8Array(createHmac('sha256', Buffer.from(key)).update(Buffer.from(data)).digest())
export const keysOf = (secret) => ({ kEnc: hmac(secret, te.encode('pigeondrop/relay/v1/enc')).slice(0, 16), kMac: hmac(secret, te.encode('pigeondrop/relay/v1/mac')) })
export const canonical = (code) => code.replace(/-/g, '').toUpperCase()
export const tagOf = (kMac, code, ct) => hmac(kMac, cat(te.encode('v1'), [0], te.encode(canonical(code)), [0], ct))

// The page's way, with WebCrypto (padding added by the library).
export async function sealWithWebCrypto({ secret, userCode, first, json }) {
  const { kEnc, kMac } = keysOf(secret)
  const key = await subtle.importKey('raw', kEnc, { name: 'AES-CBC' }, false, ['encrypt'])
  const ct = new Uint8Array(await subtle.encrypt({ name: 'AES-CBC', iv: new Uint8Array(16) }, key, cat(first, te.encode(json))))
  return { v: 1, ct: b64u(ct), tag: b64u(tagOf(kMac, userCode, ct)) }
}
// Raw blocks, for envelopes that are well made except for one thing a test wants wrong.
export function sealPadded({ secret, userCode, padded, v = 1 }) {
  const { kEnc, kMac } = keysOf(secret)
  const cipher = createCipheriv('aes-128-cbc', Buffer.from(kEnc), Buffer.alloc(16))
  cipher.setAutoPadding(false)
  const ct = new Uint8Array(Buffer.concat([cipher.update(Buffer.from(padded)), cipher.final()]))
  return { v, ct: b64u(ct), tag: b64u(tagOf(kMac, userCode, ct)) }
}
export function pkcs7(bytes) { const n = 16 - (bytes.length % 16); return cat(bytes, new Uint8Array(n).fill(n)) }
export const sealJson = ({ secret, userCode, json, first = new Uint8Array(randomBytes(16)) }) => sealPadded({ secret, userCode, padded: pkcs7(cat(first, typeof json === 'string' ? te.encode(json) : json)) })
export const payload = (url, token) => JSON.stringify({ url, token })
export const newSecretBytes = () => new Uint8Array(randomBytes(16))
