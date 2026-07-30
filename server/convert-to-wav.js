// Converts a .opus file recorded by the watch (Zepp OS's own packet format -
// NOT a standard Ogg/Opus container, so ffmpeg/afplay can't open it directly)
// into a normal .wav file that any player can open.
//
// Format, per Zepp's own docs (docs.zepp.com/docs/guides/faq/opus-to-mp3):
// each packet is [4-byte big-endian length][8-byte header][payload], repeated
// back to back. Sample rate 16000 Hz, mono, 16-bit PCM.
//
// Used both as a CLI (`node convert-to-wav.js <input.opus> [output.wav]`) and
// as a module (server.js calls opus2wav directly after saving an upload).

const OpusScript = require('opusscript')
const fs = require('fs')
const wav = require('wav')
const path = require('path')

const RATE = 16000
const CHANNELS = 1

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
  const writer = new wav.FileWriter(output, {
    channels: CHANNELS,
    sampleRate: RATE,
    bitDepth: 16,
  })
  writer.write(pcm)
  writer.end()

  return output
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
  console.log('wrote', path.resolve(output))
}
