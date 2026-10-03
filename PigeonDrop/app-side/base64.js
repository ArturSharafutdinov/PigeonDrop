// Base64 <-> bytes for the Side Service. Buffer is used when the phone's runtime
// has it (it did on the test phone), with a plain-JS fallback so packing a
// recording never depends on that: the Side Service runs on both Android and iOS
// and what each provides isn't documented.

const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'

const HAS_BUFFER = () => typeof Buffer !== 'undefined' && typeof Buffer.from === 'function'

// text: base64 string -> Uint8Array. Characters outside the alphabet (padding,
// whitespace) are skipped.
export function base64ToBytes(text) {
  if (HAS_BUFFER()) {
    try {
      const decoded = Buffer.from(text, 'base64')
      const bytes = new Uint8Array(decoded.length)
      for (let i = 0; i < decoded.length; i++) bytes[i] = decoded[i]
      return bytes
    } catch (err) {
      // fall through to the manual decoder
    }
  }

  const lookup = {}
  for (let i = 0; i < 64; i++) lookup[ALPHABET.charAt(i)] = i

  const out = new Uint8Array(Math.floor((text.length * 3) / 4))
  let length = 0
  let bits = 0
  let acc = 0
  for (let i = 0; i < text.length; i++) {
    const value = lookup[text.charAt(i)]
    if (value === undefined) continue
    acc = ((acc << 6) | value) & 0xffffff
    bits += 6
    if (bits >= 8) {
      bits -= 8
      out[length++] = (acc >>> bits) & 255
    }
  }
  return out.subarray(0, length)
}

// bytes: Uint8Array -> base64 string.
export function bytesToBase64(bytes) {
  if (HAS_BUFFER()) {
    try {
      return Buffer.from(bytes).toString('base64')
    } catch (err) {
      // fall through to the manual encoder
    }
  }

  const pieces = []
  for (let i = 0; i < bytes.length; i += 3) {
    const b0 = bytes[i]
    const b1 = i + 1 < bytes.length ? bytes[i + 1] : 0
    const b2 = i + 2 < bytes.length ? bytes[i + 2] : 0
    pieces.push(
      ALPHABET.charAt(b0 >> 2) +
        ALPHABET.charAt(((b0 & 3) << 4) | (b1 >> 4)) +
        (i + 1 < bytes.length ? ALPHABET.charAt(((b1 & 15) << 2) | (b2 >> 6)) : '=') +
        (i + 2 < bytes.length ? ALPHABET.charAt(b2 & 63) : '='),
    )
  }
  return pieces.join('')
}
