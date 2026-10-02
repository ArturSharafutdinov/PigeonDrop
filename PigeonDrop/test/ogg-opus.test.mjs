// utils/oggOpus.js (wraps a Zepp recording into a standard Ogg/Opus file) and
// app-side/base64.js. Everything is checked against the independent reference reader in
// helpers/ogg.mjs, not against the packer itself.

import crypto from 'node:crypto'
import { generate, fileUrl } from './helpers/load.mjs'
import { oggCrc32, readOgg, samePackets, zeppPackets } from './helpers/ogg.mjs'

const packer = await import(fileUrl(generate('utils/oggOpus.js', {})))
const b64 = await import(fileUrl(generate('app-side/base64.js', {})))

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }

// A Zepp recording whose packets have the given payload sizes.
function zepp(sizes) {
  return Buffer.concat(sizes.flatMap((size, i) => {
    const header = Buffer.alloc(8); header.writeUInt32BE(size, 0); header.writeUInt32BE(i, 4)
    return [header, crypto.randomBytes(size)]
  }))
}
const pack = (buffer) => packer.zeppToOggOpus(new Uint8Array(buffer))

console.log('1. round trip: every packet comes out byte for byte, in a file that is valid Ogg/Opus')
for (const [label, sizes] of [
  ['one packet', [60]],
  ['49 packets (one page, not full)', Array(49).fill(60)],
  ['50 packets (exactly one full page)', Array(50).fill(60)],
  ['51 packets (a second page with one packet)', Array(51).fill(60)],
  ['lacing boundaries 1, 254, 255, 256, 509, 510, 511', [1, 254, 255, 256, 509, 510, 511]],
  ['a 60 s recording, 3000 packets', Array.from({ length: 3000 }, (_, i) => 40 + (i % 9) * 6)],
]) {
  const input = zepp(sizes); const out = pack(input); const ogg = readOgg(out.ogg)
  ok(ogg.problems.length === 0 && samePackets(ogg.audio, zeppPackets(input)) && out.packets === sizes.length && out.droppedBytes === 0, label + ' -> ' + ogg.pages.length + ' pages, ' + (ogg.problems[0] || 'clean')) }

console.log('2. page layout')
{ const out = pack(zepp(Array(120).fill(50))); const ogg = readOgg(out.ogg)
  ok(ogg.pages.length === 2 + 3, '120 packets -> OpusHead page, OpusTags page and 3 audio pages (50 + 50 + 20) -> ' + ogg.pages.length)
  ok(ogg.pages[0].headerType === 0x02 && ogg.pages[ogg.pages.length - 1].headerType === 0x04 && ogg.pages.slice(1, -1).every((p) => p.headerType === 0), 'only the first page is marked as the start and only the last as the end')
  ok(ogg.pages[0].granule === 0n && ogg.pages[1].granule === 0n, 'the two header pages have granule position 0')
  ok(ogg.pages.slice(2).map((p) => Number(p.granule)).join() === [50 * 960, 100 * 960, 120 * 960].join(), 'audio pages count 20 ms (960 samples at 48 kHz) per packet -> ' + ogg.pages.slice(2).map((p) => p.granule).join()) }
{ const big = zepp(Array(120).fill(1275)); const ogg = readOgg(pack(big).ogg)   // 1275 = 5 x 255 needs 6 lacing values each
  ok(ogg.problems.length === 0 && ogg.pages.every((p) => p.segments <= 255) && samePackets(ogg.audio, zeppPackets(big)), 'maximum-size packets never overflow a page\'s 255 segments (' + ogg.pages.length + ' pages)')
  const granules = ogg.pages.slice(2).map((p) => Number(p.granule))
  ok(granules.every((g, i) => i === 0 || g > granules[i - 1]) && granules[granules.length - 1] === 120 * 960, 'and granule positions still rise to the total') }

console.log('3. truncated and broken input')
{ const whole = zepp(Array(10).fill(60)); const cut = whole.subarray(0, whole.length - 20)
  const out = pack(cut)
  ok(out.packets === 9 && out.droppedBytes === 68 - 20, 'a recording cut mid-packet keeps the 9 complete packets and reports the dropped tail (' + out.droppedBytes + ' bytes)')
  ok(readOgg(out.ogg).problems.length === 0, 'and the result is a clean file') }
{ const input = Buffer.concat([zepp([60, 60, 60]), Buffer.alloc(8), zepp([60, 60])])    // a zero length prefix in the middle
  ok(pack(input).packets === 3, 'a zero length prefix ends the scan - what follows cannot be trusted') }
for (const [label, bytes] of [['empty input', Buffer.alloc(0)], ['fewer bytes than one header', Buffer.alloc(7, 1)], ['a header with no payload after it', zepp([60]).subarray(0, 8)], ['a length that runs past the end', (() => { const b = zepp([60]); b.writeUInt32BE(61, 0); return b })()]]) {
  let message = ''; try { pack(bytes) } catch (err) { message = err.message }
  ok(/no audio packets/.test(message), label + ' -> throws "' + message + '"') }

console.log('4. headers')
{ const ogg = readOgg(pack(zepp([60, 60])).ogg); const h = ogg.head
  ok(h[8] === 1 && h[9] === 1 && h.readUInt16LE(10) === 0 && h.readUInt32LE(12) === 16000 && h.readUInt16LE(16) === 0 && h[18] === 0, 'OpusHead: version 1, mono, pre-skip 0, 16000 Hz, gain 0, mapping family 0')
  ok(ogg.tags.readUInt32LE(8) === 'pigeondrop'.length && ogg.tags.readUInt32LE(12 + 10) === 0, 'OpusTags: vendor string and no user comments') }
{ const input = zepp(Array.from({ length: 200 }, (_, i) => 30 + (i % 11)))
  ok(Buffer.compare(Buffer.from(pack(input).ogg), Buffer.from(pack(input).ogg)) === 0, 'packing is deterministic (same input, same bytes)') }

console.log('5. the checksum')
{ let same = true
  for (let n = 0; n < 40; n++) { const bytes = crypto.randomBytes(n * 13); if (packer.oggCrc(bytes) !== oggCrc32(bytes)) same = false }
  ok(same, 'the table-driven CRC in the packer equals the bit-by-bit reference for 40 random inputs')
  ok(oggCrc32(Buffer.from('123456789')) === 0x89a1897f, 'the reference passes a known-answer test (CRC of "123456789" with poly 0x04C11DB7, init 0, no xor is 0x89a1897f): 0x' + oggCrc32(Buffer.from('123456789')).toString(16)) }

console.log('6. speed (Node, not the phone): a 60 s recording')
{ const input = new Uint8Array(zepp(Array.from({ length: 3000 }, (_, i) => 40 + (i % 9) * 6)))
  const t0 = Date.now(); packer.zeppToOggOpus(input); const ms = Date.now() - t0
  ok(ms < 2000, 'wrapped in ' + ms + ' ms (' + input.length + ' bytes in)') }

console.log('7. base64 helpers: with Buffer, and the fallback for runtimes without it')
{ const real = globalThis.Buffer
  const cases = [0, 1, 2, 3, 4, 5, 6, 7, 31, 32, 33, 1000, 15360, 15361].map((n) => crypto.randomBytes(n))
  const run = (label) => {
    const all = cases.every((bytes) => {
      const text = b64.bytesToBase64(new Uint8Array(bytes))
      const back = b64.base64ToBytes(text)
      return text === bytes.toString('base64') && Buffer.compare(Buffer.from(back), bytes) === 0
    })
    ok(all, label + ': encode matches the standard and decode round-trips for lengths 0..15361')
  }
  run('with Buffer')
  // Buffer is looked up at call time, so hiding it here is what a runtime without it looks like.
  const realFrom = real.from
  globalThis.Buffer = undefined
  try {
    const bytes = new Uint8Array([0, 1, 2, 250, 251, 252, 253, 254, 255])
    const text = b64.bytesToBase64(bytes)
    const back = b64.base64ToBytes(text)
    ok(text === realFrom.call(real, bytes).toString('base64') && back.length === bytes.length && back.every((v, i) => v === bytes[i]), 'without Buffer: the plain-JS path works and agrees -> ' + text)
    ok(b64.base64ToBytes('AAEC\nA/8=').length === 5 && b64.base64ToBytes('AAEC\nA/8=').join() === '0,1,2,3,255', 'without Buffer: whitespace and padding are skipped')
  } finally { globalThis.Buffer = real }
  ok(b64.base64ToBytes('').length === 0 && b64.bytesToBase64(new Uint8Array(0)) === '', 'empty input both ways') }

console.log('\n' + (failures ? failures + ' FAILURE(S)' : 'ALL CHECKS PASS'))
process.exit(failures ? 1 : 0)
