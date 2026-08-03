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

// Returns a Promise that only resolves once the .wav file has actually
// finished writing to disk (and rejects if it fails). Two things make this
// less obvious than it looks - both confirmed by reading node_modules/wav's
// source directly, not assumed:
//
// 1. FileWriter's 'finish' event fires once its *writable Transform side*
//    has processed .end() - it does NOT wait for the file it's piped to
//    (`writer.file`, a plain fs.createWriteStream) to actually finish, and it
//    does NOT wait for the header-rewrite step FileWriter does afterwards
//    (a second fs.open/write/close cycle to patch the WAV header with the
//    final byte length). The real "fully written, header patched, file
//    closed" signal is FileWriter's custom 'done' event.
// 2. `writer.pipe(writer.file)` (done internally by FileWriter) does NOT
//    forward errors between the two streams - a `.pipe()`'d error on the
//    destination never reaches the source's 'error' listeners. So an error
//    opening/writing the underlying file only ever surfaces on `writer.file`,
//    not on `writer` itself - both need their own 'error' listener.
//
// Missing either of these is exactly what let a failed file open crash the
// whole server previously: the promise would resolve (or never reject) on
// the wrong/incomplete signal, and the real failure surfaced later as an
// unhandled stream 'error' event with no listener anywhere.
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
    const writer = new wav.FileWriter(output, {
      channels: CHANNELS,
      sampleRate: RATE,
      bitDepth: 16,
    })

    writer.on('error', reject)
    writer.file.on('error', reject)
    writer.on('done', () => resolve(output))

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
