// Reference tools for the Ogg/Opus tests. The Ogg reader here is written from the
// specs (RFC 3533 pages, RFC 7845 Opus mapping) and deliberately shares no code with
// utils/oggOpus.js (the packer) or the server's reader (backend/internal/audio), so
// that a mistake in one of them cannot hide behind the same mistake in its checker.

import crypto from 'node:crypto'

// Ogg's CRC-32, done bit by bit: polynomial 0x04C11DB7, no reflection, init 0, no final xor.
export function oggCrc32(bytes) {
  let crc = 0
  for (const byte of bytes) {
    crc = (crc ^ (byte << 24)) >>> 0
    for (let bit = 0; bit < 8; bit++) {
      crc = crc & 0x80000000 ? ((crc << 1) ^ 0x04c11db7) >>> 0 : (crc << 1) >>> 0
    }
  }
  return crc
}

// Parses a whole Ogg file. Returns { pages, packets, crcOk, headersOk, audio, problems }:
//   pages    one entry per page: headerType, granule (BigInt), serial, sequence, segments, bodyLength
//   packets  every packet, rebuilt from the lacing values
//   audio    the packets after OpusHead and OpusTags
//   problems human-readable list of everything that is wrong (empty = a clean file)
export function readOgg(input) {
  const bytes = Buffer.from(input)
  const pages = []
  const packets = []
  const problems = []
  let carry = []
  let pos = 0

  while (pos < bytes.length) {
    if (bytes.length - pos < 27 || bytes.toString('latin1', pos, pos + 4) !== 'OggS') {
      problems.push('no OggS at byte ' + pos)
      break
    }
    const count = bytes[pos + 26]
    const lacing = [...bytes.subarray(pos + 27, pos + 27 + count)]
    const bodyLength = lacing.reduce((sum, n) => sum + n, 0)
    const length = 27 + count + bodyLength
    if (pos + length > bytes.length) {
      problems.push('page at byte ' + pos + ' is cut off')
      break
    }

    const page = Buffer.from(bytes.subarray(pos, pos + length))
    const stored = page.readUInt32LE(22)
    page.writeUInt32LE(0, 22)
    if (oggCrc32(page) !== stored) problems.push('bad checksum on page ' + pages.length)

    pages.push({
      headerType: bytes[pos + 5],
      granule: bytes.readBigUInt64LE(pos + 6),
      serial: bytes.readUInt32LE(pos + 14),
      sequence: bytes.readUInt32LE(pos + 18),
      segments: count,
      bodyLength,
    })

    let at = pos + 27 + count
    for (const lace of lacing) {
      carry.push(bytes.subarray(at, at + lace))
      at += lace
      if (lace < 255) {
        packets.push(Buffer.concat(carry))
        carry = []
      }
    }
    pos += length
  }
  if (carry.length) problems.push('the stream ends inside a packet')

  const head = packets[0]
  const tags = packets[1]
  if (!head || head.toString('latin1', 0, 8) !== 'OpusHead' || head.length !== 19) problems.push('first packet is not a 19-byte OpusHead')
  if (!tags || tags.toString('latin1', 0, 8) !== 'OpusTags') problems.push('second packet is not OpusTags')
  if (pages.length && pages[0].headerType !== 0x02) problems.push('first page is not marked as the beginning of the stream')
  if (pages.length && pages[pages.length - 1].headerType !== 0x04) problems.push('last page is not marked as the end of the stream')
  pages.forEach((page, i) => {
    if (page.sequence !== i) problems.push('page ' + i + ' has sequence number ' + page.sequence)
    if (page.serial !== pages[0].serial) problems.push('page ' + i + ' has a different serial number')
    if (page.segments > 255) problems.push('page ' + i + ' has more than 255 segments')
  })

  return { pages, packets, audio: packets.slice(2), head, tags, problems }
}

// Valid Zepp framing - [4-byte big-endian length][4 other bytes][payload] - for a
// recording of exactly `totalBytes` bytes (at least 9). Payloads are random but start
// with 0x48, the TOC byte of the real recordings.
export function framedRecording(totalBytes) {
  if (totalBytes < 9) throw new Error('a framed recording needs at least 9 bytes')
  const parts = []
  let remaining = totalBytes
  for (let index = 0; remaining > 0; index++) {
    let payload = Math.min(40 + (index % 7) * 5, remaining - 8)
    const leftover = remaining - 8 - payload
    if (leftover > 0 && leftover < 9) payload += leftover // never leave a tail too small for a packet
    const header = Buffer.alloc(8)
    header.writeUInt32BE(payload, 0)
    header.writeUInt32BE(index, 4)
    const body = crypto.randomBytes(payload)
    body[0] = 0x48
    parts.push(header, body)
    remaining -= 8 + payload
  }
  return Buffer.concat(parts)
}

// The complete packets of a Zepp recording; a partial last packet is ignored.
export function zeppPackets(buffer) {
  const packets = []
  for (let pos = 0; buffer.length - pos >= 8; ) {
    const length = buffer.readUInt32BE(pos)
    if (length === 0 || pos + 8 + length > buffer.length) break
    packets.push(buffer.subarray(pos + 8, pos + 8 + length))
    pos += 8 + length
  }
  return packets
}

export function samePackets(a, b) {
  return a.length === b.length && a.every((packet, i) => Buffer.compare(packet, b[i]) === 0)
}

// Does this base64 `audio` field hold a clean Ogg/Opus file carrying exactly the packets of `zeppFile`?
export function sameAudio(audioBase64, zeppFile) {
  const ogg = readOgg(Buffer.from(audioBase64, 'base64'))
  return ogg.problems.length === 0 && samePackets(ogg.audio, zeppPackets(zeppFile))
}
