// What the screen says when the phone does not answer or cannot reach the server (utils/linkProblem.js).

import { generate, fileUrl } from './helpers/load.mjs'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const L = await import(fileUrl(generate('utils/linkProblem.js', {})))

console.log('1. each problem has its own words and its own advice')
const phone = L.problemText(L.PHONE), server = L.problemText(L.SERVER)
ok(typeof phone === 'string' && typeof server === 'string' && phone !== server, 'two different texts')
ok(/Перезапустите/.test(phone) && /Zepp/.test(phone), 'the phone not answering: restart the Zepp app -> ' + JSON.stringify(phone))
ok(/интернет/.test(server) && !/Zepp/.test(server), 'the phone not reaching the server: check the internet, nothing about Zepp -> ' + JSON.stringify(server))
ok(/телефон/.test(phone.split('\n')[0]) && /сервер/.test(server.split('\n')[0]), 'the first line names what is not reachable')

console.log('2. they fit the status label on the round display')
// The status labels are 28 px text on a 480 px wide label; the longest line the app already shows is "Не удалось отправить" (20 characters).
for (const [name, text] of [['phone', phone], ['server', server]]) {
  const lines = text.split('\n')
  ok(lines.length <= 3, name + ': at most 3 lines, the queue line is added below -> ' + lines.length)
  ok(lines.every((l) => l.length <= 22), name + ': no line longer than 22 characters -> ' + Math.max(...lines.map((l) => l.length))) }

console.log('3. anything else has no text, so the caller shows its own reason')
for (const kind of [undefined, null, '', 'other', 'constructor', '__proto__', 'toString', 5]) {
  ok(L.problemText(kind) === null, 'kind ' + JSON.stringify(kind) + ' -> null') }

console.log('\n' + (failures ? failures + ' FAILURE(S)' : 'ALL CHECKS PASS'))
process.exit(failures ? 1 : 0)
