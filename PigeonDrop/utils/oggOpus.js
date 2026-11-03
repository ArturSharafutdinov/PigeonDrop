// Wraps a Zepp OS recording into a standard Ogg/Opus file WITHOUT re-encoding.
//
// Zepp's recorder emits [8-byte header][Opus packet] repeated, where the first
// 4 header bytes are the payload length (big-endian) and the other 4 are
// ignored (see ARCHITECTURE.md section 4). Every packet is a valid 20 ms mono
// SILK-wideband Opus packet (TOC byte 0x48), so the payloads can be copied
// verbatim into an Ogg stream (RFC 3533 container, RFC 7845 Opus mapping).
//
// Used by the phone's Side Service (app-side/uploads.js), so that every server -
// the SaaS and any user-supplied one - receives a standard file instead of
// Zepp's private framing. Pure ES2015, no platform APIs: only Uint8Array and
// plain integer maths, so it runs unchanged in the Side Service and in Node.
// Checked against an independent Ogg reader written from the RFCs
// (test/helpers/ogg.mjs), and the output decodes under ffmpeg's strict mode.

const ZEPP_HEADER_SIZE = 8
const PACKETS_PER_PAGE = 50 // 50 x 20 ms = 1 s of audio per Ogg page
const SERIAL = 0x50494745 // arbitrary bitstream serial number
const PRE_SKIP = 0 // samples at 48 kHz; 0 keeps output identical to a raw decode
const INPUT_RATE = 16000 // informational only (RFC 7845 section 5.1)
const SAMPLES_PER_PACKET = 960 // 20 ms at the 48 kHz granule clock
const VENDOR = 'pigeondrop'

// Ogg's CRC-32: polynomial 0x04C11DB7, no reflection, init 0, no final xor.
const CRC_TABLE = (function () {
  const table = []
  for (let i = 0; i < 256; i++) {
    let r = (i << 24) >>> 0
    for (let k = 0; k < 8; k++) {
      r = (r & 0x80000000) !== 0 ? ((r << 1) ^ 0x04c11db7) >>> 0 : (r << 1) >>> 0
    }
    table.push(r)
  }
  return table
})()

export function oggCrc(bytes) {
  let crc = 0
  for (let i = 0; i < bytes.length; i++) {
    crc = (((crc << 8) ^ CRC_TABLE[((crc >>> 24) ^ bytes[i]) & 0xff]) >>> 0)
  }
  return crc
}

function writeU16(out, offset, value) {
  out[offset] = value & 0xff
  out[offset + 1] = (value >>> 8) & 0xff
}

function writeU32(out, offset, value) {
  out[offset] = value & 0xff
  out[offset + 1] = (value >>> 8) & 0xff
  out[offset + 2] = (value >>> 16) & 0xff
  out[offset + 3] = (value >>> 24) & 0xff
}

function writeAscii(out, offset, text) {
  for (let i = 0; i < text.length; i++) {
    out[offset + i] = text.charCodeAt(i) & 0xff
  }
}

// Splits Zepp's [len(4, BE)][unknown(4)][payload] stream into packet views (no
// copies). The length prefix is data from the watch's storage, so every bound is
// checked. A recording cut short - the app killed mid-write, the file truncated -
// ends in a partial packet; scanning stops there and everything before it is
// kept, since losing a whole message over its last few milliseconds would be
// worse than trimming them. Returns { packets, droppedBytes }.
export function unframeZepp(bytes) {
  const packets = []
  let offset = 0
  while (bytes.length - offset >= ZEPP_HEADER_SIZE) {
    const length =
      bytes[offset] * 16777216 + bytes[offset + 1] * 65536 + bytes[offset + 2] * 256 + bytes[offset + 3]
    if (length === 0 || offset + ZEPP_HEADER_SIZE + length > bytes.length) break
    packets.push(bytes.subarray(offset + ZEPP_HEADER_SIZE, offset + ZEPP_HEADER_SIZE + length))
    offset += ZEPP_HEADER_SIZE + length
  }
  return { packets: packets, droppedBytes: bytes.length - offset }
}

function buildPage(sequence, granule, headerType, packets) {
  let segmentCount = 0
  let bodyLength = 0
  for (let i = 0; i < packets.length; i++) {
    segmentCount += Math.floor(packets[i].length / 255) + 1
    bodyLength += packets[i].length
  }
  if (segmentCount > 255) {
    throw new Error('too many segments for one Ogg page')
  }

  const page = new Uint8Array(27 + segmentCount + bodyLength)
  writeAscii(page, 0, 'OggS')
  page[4] = 0 // stream structure version
  page[5] = headerType // 0x02 = first page, 0x04 = last page
  writeU32(page, 6, granule % 4294967296) // granule position, 64-bit little-endian
  writeU32(page, 10, Math.floor(granule / 4294967296))
  writeU32(page, 14, SERIAL)
  writeU32(page, 18, sequence)
  // bytes 22..25: CRC, filled in last
  page[26] = segmentCount

  let cursor = 27
  for (let i = 0; i < packets.length; i++) {
    // Lacing: a packet of n bytes is floor(n/255) segments of 255 plus one
    // segment of n % 255 (which is 0 when n is a multiple of 255).
    let remaining = packets[i].length
    while (remaining >= 255) {
      page[cursor++] = 255
      remaining -= 255
    }
    page[cursor++] = remaining
  }
  for (let i = 0; i < packets.length; i++) {
    page.set(packets[i], cursor)
    cursor += packets[i].length
  }

  writeU32(page, 22, oggCrc(page))
  return page
}

function opusHead() {
  const head = new Uint8Array(19)
  writeAscii(head, 0, 'OpusHead')
  head[8] = 1 // version
  head[9] = 1 // channels: mono
  writeU16(head, 10, PRE_SKIP)
  writeU32(head, 12, INPUT_RATE)
  writeU16(head, 16, 0) // output gain
  head[18] = 0 // channel mapping family 0
  return head
}

function opusTags() {
  const tags = new Uint8Array(8 + 4 + VENDOR.length + 4)
  writeAscii(tags, 0, 'OpusTags')
  writeU32(tags, 8, VENDOR.length)
  writeAscii(tags, 12, VENDOR)
  writeU32(tags, 12 + VENDOR.length, 0) // no user comments
  return tags
}

// bytes: Uint8Array holding a Zepp recording.
// Returns { ogg: Uint8Array, packets: number, droppedBytes: number }, where
// droppedBytes is the length of a truncated tail that could not be used.
// Throws if the recording holds no audio packet at all.
export function zeppToOggOpus(bytes) {
  const unframed = unframeZepp(bytes)
  const packets = unframed.packets
  if (packets.length === 0) {
    throw new Error('no audio packets in the recording')
  }
  const pages = [buildPage(0, 0, 0x02, [opusHead()]), buildPage(1, 0, 0x00, [opusTags()])]

  // A page holds at most PACKETS_PER_PAGE packets and at most 255 lacing
  // segments (a packet of n bytes needs floor(n / 255) + 1 of them), so a run of
  // unusually large packets simply ends the page sooner.
  let sequence = 2
  let done = 0
  let start = 0
  while (start < packets.length) {
    let end = start
    let segments = 0
    while (end < packets.length && end - start < PACKETS_PER_PAGE) {
      const needed = Math.floor(packets[end].length / 255) + 1
      if (segments + needed > 255) break
      segments += needed
      end++
    }
    if (end === start) {
      throw new Error('packet too large for an Ogg page')
    }
    done += end - start
    const isLast = end === packets.length
    pages.push(buildPage(sequence++, PRE_SKIP + done * SAMPLES_PER_PACKET, isLast ? 0x04 : 0x00, packets.slice(start, end)))
    start = end
  }

  let total = 0
  for (let i = 0; i < pages.length; i++) total += pages[i].length
  const ogg = new Uint8Array(total)
  let cursor = 0
  for (let i = 0; i < pages.length; i++) {
    ogg.set(pages[i], cursor)
    cursor += pages[i].length
  }
  return { ogg: ogg, packets: packets.length, droppedBytes: unframed.droppedBytes }
}
