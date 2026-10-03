// Regenerates the audio fixtures used by the tests in this package. Run rarely, from the repository root:
//
//   node backend/internal/audio/testdata/generate.mjs
//
// The recordings are synthetic (a sweeping tone), not a real voice. They are encoded as SILK wideband,
// mono, 20 ms packets - exactly what a Zepp OS watch records - with libopus through opusscript, so the
// expected WAV is libopus's own decoding of the same packets:
//
//   tone.zepp  the recording in Zepp's private framing: [4-byte big-endian length][4 other bytes][packet]
//   tone.ogg   the same packets wrapped as Ogg/Opus by PigeonDrop/utils/oggOpus.js (what the phone sends)
//   tone.wav   libopus's decoding at 16 kHz, 16-bit mono (what the server must produce)
//
// Needs `npm install` in server/ (for opusscript). Once the Node prototype is removed, point it at another
// copy of opusscript.

import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { fileURLToPath, pathToFileURL } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const root = path.resolve(here, '..', '..', '..', '..')
const OpusScript = createRequire(path.join(root, 'server', 'package.json'))('opusscript')
const { zeppToOggOpus } = await import(pathToFileURL(path.join(root, 'PigeonDrop', 'utils', 'oggOpus.js')).href)

const SECONDS = 2
const FRAME = 320 // 20 ms at 16 kHz
const encoder = new OpusScript(16000, 1, OpusScript.Application.VOIP)
const decoder = new OpusScript(16000, 1, OpusScript.Application.VOIP)

const framed = []
const pcm = []
for (let f = 0; f < SECONDS * 50; f++) {
  const samples = Buffer.alloc(FRAME * 2)
  for (let i = 0; i < FRAME; i++) {
    const t = (f * FRAME + i) / 16000
    const tone = 6000 * Math.sin(2 * Math.PI * (200 + 150 * t) * t) + 1500 * Math.sin(2 * Math.PI * 1300 * t)
    samples.writeInt16LE(Math.round(tone), i * 2)
  }
  const packet = Buffer.from(encoder.encode(samples, FRAME))
  const header = Buffer.alloc(8)
  header.writeUInt32BE(packet.length, 0)
  header.writeUInt32BE(f, 4)
  framed.push(header, packet)
  pcm.push(Buffer.from(decoder.decode(packet)))
}

const zepp = Buffer.concat(framed)
const ogg = Buffer.from(zeppToOggOpus(new Uint8Array(zepp)).ogg)

const data = Buffer.concat(pcm)
const wav = Buffer.alloc(44)
wav.write('RIFF', 0, 'ascii'); wav.writeUInt32LE(36 + data.length, 4); wav.write('WAVE', 8, 'ascii')
wav.write('fmt ', 12, 'ascii'); wav.writeUInt32LE(16, 16); wav.writeUInt16LE(1, 20); wav.writeUInt16LE(1, 22)
wav.writeUInt32LE(16000, 24); wav.writeUInt32LE(32000, 28); wav.writeUInt16LE(2, 32); wav.writeUInt16LE(16, 34)
wav.write('data', 36, 'ascii'); wav.writeUInt32LE(data.length, 40)

fs.writeFileSync(path.join(here, 'tone.zepp'), zepp)
fs.writeFileSync(path.join(here, 'tone.ogg'), ogg)
fs.writeFileSync(path.join(here, 'tone.wav'), Buffer.concat([wav, data]))
console.log(`tone.zepp ${zepp.length} B, tone.ogg ${ogg.length} B, tone.wav ${44 + data.length} B (${SECONDS} s, ${SECONDS * 50} packets)`)
