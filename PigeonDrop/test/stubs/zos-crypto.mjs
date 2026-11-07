// Stands in for @zos/crypto, built on Node's crypto and made to behave the way the platform's documentation says the
// real one does, as far as that matters here:
//   - AES-CBC takes whole blocks only, has no padding, and no way to set the IV (this stub picks a random one for every
//     instance, so a decryption of the first block is garbage, as it would be with an IV we do not know);
//   - createChiper() makes a key for the instance when none is given;
//   - HMAC takes the key as private_key, and results are { md_content: ArrayBuffer, length }.
// `behaviour` lets a test break things: no generated keys, a generator that repeats itself, an HMAC that fails, and a
// platform that wants key_encrypt spelled out in some way before it accepts a given key.
import { createHmac, createDecipheriv, createCipheriv, randomBytes } from 'node:crypto'

export const alg = { MD5: 1, HMACMD5: 2, SHA_256: 3, HMAC_SHA_256: 4, SHA_1: 5, HMAC_SHA_1: 6, AES_CBC: 7 }
export const behaviour = { generatedKeys: 'random', hmacFails: false, aesFails: false, throws: false, needKeyEncrypt: undefined, keyLength: 16 }
export const log = { instances: [], createChiper: 0 }

const toU8 = (x) => (x instanceof Uint8Array ? x : x instanceof ArrayBuffer ? new Uint8Array(x) : Uint8Array.from(x))
const toBuffer = (u8) => u8.buffer.slice(u8.byteOffset, u8.byteOffset + u8.byteLength)
let fixedKey = null
export const resetKeys = () => { fixedKey = null }

// A platform that wants key_encrypt spelled out: with another spelling the key is not used as given.
function keyAccepted(options) {
  if (behaviour.needKeyEncrypt === undefined) return true
  return 'key_encrypt' in options && options.key_encrypt === behaviour.needKeyEncrypt
}

export function createCrypto(algorithmId, options = {}) {
  if (behaviour.throws) throw new Error('crypto is not available')
  log.instances.push({ algorithmId, options })
  if (algorithmId === alg.HMAC_SHA_256) {
    if (behaviour.hmacFails) return undefined
    return {
      encrypt(data) {
        if (!options.private_key || !keyAccepted(options)) return { md_content: toBuffer(createHmac('sha256', Buffer.from('wrong key')).update(Buffer.from(toU8(data))).digest()), length: 32 }
        const out = createHmac('sha256', Buffer.from(toU8(options.private_key))).update(Buffer.from(toU8(data))).digest()
        return { md_content: toBuffer(new Uint8Array(out)), length: out.length }
      },
    }
  }
  if (algorithmId === alg.AES_CBC) {
    if (behaviour.aesFails) return undefined
    let key = options.private_key ? toU8(options.private_key) : null
    const iv = randomBytes(16) // the platform's IV: not ours to know
    return {
      createChiper() {
        log.createChiper++
        if (behaviour.generatedKeys === 'none') return undefined
        if (behaviour.generatedKeys === 'zero') key = new Uint8Array(behaviour.keyLength)
        else if (behaviour.generatedKeys === 'zero-first' && log.createChiper === 1) key = new Uint8Array(behaviour.keyLength)
        else if (behaviour.generatedKeys === 'same') { fixedKey = fixedKey || new Uint8Array(randomBytes(behaviour.keyLength)); key = fixedKey }
        else key = new Uint8Array(randomBytes(behaviour.keyLength))
        return { private_key_length: key.length, private_key: toBuffer(key) }
      },
      decrypt(data) {
        const bytes = toU8(data)
        if (bytes.length % 16 !== 0) return undefined
        const useKey = keyAccepted(options) ? key : new Uint8Array(16)
        const decipher = createDecipheriv('aes-128-cbc', Buffer.from(useKey), iv)
        decipher.setAutoPadding(false)
        const out = Buffer.concat([decipher.update(Buffer.from(bytes)), decipher.final()])
        return { data: toBuffer(new Uint8Array(out)), length: out.length }
      },
      encrypt(data) {
        const bytes = toU8(data)
        if (bytes.length % 16 !== 0) return undefined
        const cipher = createCipheriv('aes-128-cbc', Buffer.from(key), iv)
        cipher.setAutoPadding(false)
        const out = Buffer.concat([cipher.update(Buffer.from(bytes)), cipher.final()])
        return { data: toBuffer(new Uint8Array(out)), length: out.length }
      },
    }
  }
  return undefined
}
