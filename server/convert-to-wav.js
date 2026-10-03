// Converts a recording from the watch into a normal .wav file that any player
// can open. Two input formats are accepted and told apart by their first bytes:
//
//   Ogg/Opus  ("OggS") - what the app sends now: the phone wraps Zepp's packets
//             into a standard Ogg/Opus file (PigeonDrop/utils/oggOpus.js).
//   Zepp's own packet format - what a watch recording looks like before that
//             wrapping, still accepted so that older clients keep working.
//             Not a standard container: ffmpeg/afplay can't open it directly.
//
// Both carry the same Opus packets, so both end up in the same decoding loop.
//
// Zepp's format: each packet is [8-byte header][payload], repeated back to back,
// where the header's first 4 bytes are the payload length, big-endian (the
// remaining 4 are unidentified and skipped). Sample rate 16000 Hz, mono,
// 16-bit PCM. Payloads are SILK wideband, 20 ms per packet.
//
// NOTE: Zepp's own docs (docs.zepp.com/docs/guides/faq/opus-to-mp3) describe
// this as [4-byte length][8-byte header] - a 12-byte prefix. That is wrong:
// parsing real recordings that way overruns the file on the second packet.
// The 8-byte total below is what actually works, verified across recordings.
//
// Used both as a CLI (`node convert-to-wav.js <input.opus> [output.wav]`) and
// as a module (server.js calls opus2wav directly after saving an upload).

const OpusScript = require('opusscript')
const fs = require('fs')
const wav = require('wav')
const path = require('path')

const RATE = 16000
const CHANNELS = 1

function isOggOpus(data) {
  return data.length >= 4 && data.toString('latin1', 0, 4) === 'OggS'
}

// Ogg's CRC-32: polynomial 0x04C11DB7, no reflection, init 0, no final xor.
const CRC_TABLE = (() => {
  const table = new Uint32Array(256)
  for (let i = 0; i < 256; i++) {
    let r = (i << 24) >>> 0
    for (let k = 0; k < 8; k++) r = (r & 0x80000000) !== 0 ? ((r << 1) ^ 0x04c11db7) >>> 0 : (r << 1) >>> 0
    table[i] = r
  }
  return table
})()

function oggCrc(page) {
  let crc = 0
  for (let i = 0; i < page.length; i++) crc = ((crc << 8) ^ CRC_TABLE[((crc >>> 24) ^ page[i]) & 0xff]) >>> 0
  return crc
}

// Reads a mono Ogg/Opus stream (RFC 3533, RFC 7845) and returns its audio
// packets, without the OpusHead and OpusTags headers. The input comes from a
// client, so everything is bounds- and checksum-checked and anything unexpected
// throws. Packets are rebuilt from lacing values, so one that continues onto the
// next page is handled too, although our own packer never produces one.
function oggOpusPackets(data) {
  const packets = []
  let pending = []
  let packetIndex = 0
  let pos = 0

  while (pos < data.length) {
    if (data.length - pos < 27 || data.toString('latin1', pos, pos + 4) !== 'OggS') {
      throw new Error('not an Ogg page at byte ' + pos)
    }
    const segmentCount = data[pos + 26]
    if (data.length - pos < 27 + segmentCount) throw new Error('truncated Ogg page at byte ' + pos)

    let bodyLength = 0
    for (let i = 0; i < segmentCount; i++) bodyLength += data[pos + 27 + i]
    const pageLength = 27 + segmentCount + bodyLength
    if (pos + pageLength > data.length) throw new Error('truncated Ogg page at byte ' + pos)

    const page = Buffer.from(data.subarray(pos, pos + pageLength))
    const stored = page.readUInt32LE(22)
    page.writeUInt32LE(0, 22)
    if (oggCrc(page) !== stored) throw new Error('Ogg page checksum mismatch at byte ' + pos)

    let bodyPos = 27 + segmentCount
    for (let i = 0; i < segmentCount; i++) {
      const lace = page[27 + i]
      pending.push(page.subarray(bodyPos, bodyPos + lace))
      bodyPos += lace
      if (lace < 255) {
        const packet = Buffer.concat(pending)
        pending = []
        if (packetIndex === 0) {
          if (packet.toString('latin1', 0, 8) !== 'OpusHead') throw new Error('first Ogg packet is not OpusHead')
          if (packet[9] !== 1) throw new Error('only mono Opus is supported, got ' + packet[9] + ' channels')
        } else if (packetIndex > 1) {
          packets.push(packet)
        }
        packetIndex++
      }
    }
    pos += pageLength
  }

  if (pending.length > 0) throw new Error('Ogg stream ends inside a packet')
  if (packets.length === 0) throw new Error('no audio packets in the Ogg stream')
  return packets
}

// Zepp's own framing. A truncated final packet ends the scan instead of being
// decoded as garbage.
function zeppPackets(data) {
  const packets = []
  for (let pos = 0; data.length - pos >= 8; ) {
    const len = data.readUInt32BE(pos)
    if (len === 0 || pos + 8 + len > data.length) break
    packets.push(data.subarray(pos + 8, pos + 8 + len))
    pos += 8 + len
  }
  return packets
}

// Returns a Promise that resolves once the .wav file has been written to
// disk (rejects if it fails). Deliberately does NOT use wav.FileWriter -
// FileWriter pipes into a separate fs.createWriteStream to create the file,
// then independently re-opens that same path in 'r+' mode afterwards to
// patch the header with the final byte length. Those are two unsynchronized
// async operations: if the header re-open runs before the piped write has
// actually created the file on disk (very likely for small payloads, where
// everything happens within a few event-loop ticks), the re-open fails with
// ENOENT - intermittently, depending on scheduling. Confirmed by reading
// node_modules/wav's source, not assumed.
//
// Instead: use the plain wav.Writer (no file I/O of its own), buffer its
// output ourselves, patch the corrected header into that buffer once the
// stream is done, and write the whole thing to disk in a single
// synchronous call - no pipe, no second open, no race.
function opus2wav(input, output) {
  const encoder = new OpusScript(RATE, CHANNELS, OpusScript.Application.AUDIO)
  const data = fs.readFileSync(input)
  const packets = isOggOpus(data) ? oggOpusPackets(data) : zeppPackets(data)
  const pcm = Buffer.concat(packets.map((packet) => encoder.decode(packet)))

  return new Promise((resolve, reject) => {
    const writer = new wav.Writer({ channels: CHANNELS, sampleRate: RATE, bitDepth: 16 })
    const chunks = []
    let headerBuffer = null
    let streamEnded = false
    let settled = false

    // 'header' (the corrected, final-length header) and 'end' (all data
    // chunks pushed, including the original placeholder header) aren't
    // guaranteed to fire in a particular order - wait for both before
    // writing anything.
    function finalize() {
      if (settled || !streamEnded || !headerBuffer) return
      settled = true

      try {
        const body = Buffer.concat(chunks)
        headerBuffer.copy(body, 0)
        fs.writeFileSync(output, body)
        resolve(output)
      } catch (err) {
        reject(err)
      }
    }

    writer.on('data', (chunk) => chunks.push(chunk))
    writer.on('header', (header) => {
      headerBuffer = header
      finalize()
    })
    writer.on('end', () => {
      streamEnded = true
      finalize()
    })
    writer.on('error', reject)

    writer.write(pcm)
    writer.end()
  })
}

module.exports = { opus2wav, isOggOpus }

if (require.main === module) {
  const input = process.argv[2]
  const output = process.argv[3] || input.replace(/\.opus$/, '.wav')

  if (!input) {
    console.error('Usage: node convert-to-wav.js <input.opus> [output.wav]')
    process.exit(1)
  }

  opus2wav(input, output)
    .then(() => console.log('wrote', path.resolve(output)))
    .catch((err) => {
      console.error('conversion failed:', err.message)
      process.exit(1)
    })
}
