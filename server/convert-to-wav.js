// Converts a .opus file recorded by the watch (Zepp OS's own packet format -
// NOT a standard Ogg/Opus container, so ffmpeg/afplay can't open it directly)
// into a normal .wav file that any player can open.
//
// Format: each packet is [8-byte header][payload], repeated back to back,
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
  const out = []

  for (let pos = 0; pos < data.byteLength; ) {
    const len = data.readUInt32BE(pos)
    const payload = data.subarray(pos + 8, pos + 8 + len)
    out.push(encoder.decode(payload))
    pos += 8 + len
  }

  const pcm = Buffer.concat(out)

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

module.exports = { opus2wav }

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
