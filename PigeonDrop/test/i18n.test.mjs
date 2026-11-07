// The texts of the watch in English and Russian (i18n/): the two tables agree, t() picks the language of the watch,
// and the screens say the right words in English and in Russian. (The other tests run the screens in English.)

import fs from 'node:fs'
import * as settingsStub from './stubs/settings.mjs'
import * as mainStubs from './stubs/main-page.mjs'
import * as settingsPageStubs from './stubs/settings-page.mjs'
import * as pairStubs from './stubs/pair-page.mjs'
import { generate, fileUrl, stubPath, i18nModule } from './helpers/load.mjs'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const { RUSSIAN, ENGLISH, GERMAN, setLanguage } = settingsStub
const I18N = i18nModule()
const { t } = await import(fileUrl(I18N))
const en = (await import(fileUrl(generate('i18n/en.js', {})))).default
const ru = (await import(fileUrl(generate('i18n/ru.js', {})))).default
const placeholders = (text) => [...text.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(',')
const CYRILLIC = /[А-Яа-яЁё]/

console.log('1. the two tables say the same things')
{ const enKeys = Object.keys(en).sort(), ruKeys = Object.keys(ru).sort()
  ok(enKeys.join() === ruKeys.join(), 'the same keys in both (' + enKeys.length + ')' + (enKeys.join() === ruKeys.join() ? '' : ' -> only in en: ' + enKeys.filter((k) => !(k in ru)) + ', only in ru: ' + ruKeys.filter((k) => !(k in en))))
  ok(enKeys.every((k) => placeholders(en[k]) === placeholders(ru[k])), 'every text has the same {placeholders} in both languages')
  ok(enKeys.every((k) => en[k].trim() !== '' && ru[k].trim() !== ''), 'no text is empty')
  ok(!enKeys.some((k) => CYRILLIC.test(en[k])), 'the English table has no Cyrillic letters')
  ok(ruKeys.every((k) => CYRILLIC.test(ru[k]) || placeholders(ru[k]) !== ''), 'the Russian table is Russian (no English text left behind by mistake)')
  // The status labels are hand-broken for the round display; English letters are narrower than Cyrillic, so an English line
  // longer than the longest Russian one would be a line that was not fitted. Long running texts (.text) wrap by themselves.
  const lines = (table) => Object.keys(table).filter((k) => !k.endsWith('.text')).flatMap((k) => table[k].split('\n').map((l) => l.replace(/\{\w+\}/g, '12')))
  const longestRu = Math.max(...lines(ru).map((l) => l.length)), longestEn = Math.max(...lines(en).map((l) => l.length))
  ok(longestEn <= longestRu, 'no English line is longer than the longest Russian one (' + longestEn + ' <= ' + longestRu + ')') }

console.log('2. t() follows the language of the watch')
setLanguage(RUSSIAN); ok(t('main.cancel') === 'Отмена', 'Russian watch -> Russian')
setLanguage(ENGLISH); ok(t('main.cancel') === 'Cancel', 'English watch -> English')
setLanguage(GERMAN); ok(t('main.cancel') === 'Cancel', 'any other language -> English')
setLanguage(ENGLISH)
ok(t('main.queued', { pending: 3 }) === 'Queued: 3', 'a placeholder is filled')
ok(t('main.queued', { pending: 0 }) === 'Queued: 0', '...also by zero')
ok(t('main.queued', {}) === 'Queued: {pending}' && t('main.queued') === 'Queued: {pending}', 'one with no value is left as it is, not turned into "undefined"')
ok(t('no.such.key') === 'no.such.key', 'an unknown key shows as its name, not as a blank')
ok(t('main.sending', { index: 1, count: 2, chunk: 3, total: 4 }) === 'Sending 1 of 2\npart 3 of 4', 'several placeholders')
ok(t('main.queued', { pending: '{pending}' }) === 'Queued: {pending}', 'a value that looks like a placeholder is not expanded again')

console.log('3. the main page in English')
{
  const S = stubPath('main-page.mjs')
  let opts
  globalThis.Page = (o) => { opts = o }
  globalThis.setInterval = () => 7; globalThis.clearInterval = () => {}; globalThis.setTimeout = () => 1; globalThis.clearTimeout = () => {}
  const link = generate('utils/linkProblem.js', { '../i18n': I18N })
  const screen = generate('utils/screen.js', { '@zos/display': S })
  await import(fileUrl(generate('page/index.js', {
    '@zos/ui': S, '@zos/utils': S, '@zos/interaction': S, '@zos/media': S, '@zos/router': S, '../utils/screen': screen, '../utils/player': S,
    '../utils/recordings': S, '../utils/uploadQueue': S, '../utils/device': S, '../utils/linkProblem': link, '../i18n': I18N, '../config': S,
  })))
  setLanguage(ENGLISH)
  const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) })
  mainStubs.texts.length = 0; mainStubs.created.length = 0
  page.build()
  const labels = mainStubs.created.filter((w) => w.type === 'BUTTON').map((w) => w.text)
  ok(labels.join() === 'Settings,Recordings,Cancel', 'the buttons -> ' + labels.join())
  const last = () => mainStubs.texts[mainStubs.texts.length - 1]
  const state = (s) => { page.showUploadState(s); return last() }
  ok(page.idleText() === 'Click to record\nLong press to play\nthe last one', 'idle text')
  ok(state({ state: 'connecting', pending: 2 }) === 'Connecting to phone...\nQueued: 2', 'connecting')
  ok(state({ state: 'sending', index: 2, count: 3, chunk: 4, total: 11 }) === 'Sending 2 of 3\npart 4 of 11', 'sending')
  ok(state({ state: 'done', pending: 2 }) === 'Queued: 2', 'done with a queue left')
  ok(state({ state: 'done', pending: 0 }) === 'Sent\n\n' + page.idleText(), 'done')
  ok(state({ state: 'error', kind: 'phone', message: 'x', pending: 3 }) === 'Phone is not reachable\nRestart the\nZepp app\nQueued: 3', 'the phone does not answer')
  ok(state({ state: 'error', kind: 'server', message: 'server: no answer in 30 s', pending: 1 }) === 'Server is unreachable\nCheck the internet\non the phone\n(no answer in 30 s)\nQueued: 1', 'the phone cannot reach the server')
  ok(state({ state: 'error', message: 'cannot open file', pending: 1 }) === 'Could not send\ncannot open file\nQueued: 1', 'any other failure shows its reason')
  ok(state({ state: 'unpaired', pending: 2, revoked: false }) === 'Watch is not paired\nSettings →\nConnection\nQueued: 2', 'not paired')
  ok(state({ state: 'unpaired', pending: 2, revoked: true }) === 'Pairing was revoked\nSettings →\nConnection\nQueued: 2', 'pairing revoked')
  setLanguage(RUSSIAN)
  page.build()
  const ruLabels = mainStubs.created.filter((w) => w.type === 'BUTTON').slice(-3).map((w) => w.text)
  ok(ruLabels.join() === 'Настройки,Список записей,Отмена', 'the buttons in Russian -> ' + ruLabels.join())
  ok(page.idleText() === 'Клик — записать\nДолгое нажатие —\nвоспроизвести последнюю', 'idle text in Russian')
  ok(state({ state: 'sending', index: 2, count: 3, chunk: 4, total: 11 }) === 'Отправка 2 из 3\nчасть 4 из 11', 'sending in Russian')
  ok(state({ state: 'error', kind: 'phone', message: 'x', pending: 3 }) === 'Нет связи с телефоном\nПерезапустите\nприложение Zepp\nВ очереди: 3', 'the phone does not answer, in Russian')
  setLanguage(GERMAN)
  mainStubs.texts.length = 0; mainStubs.created.length = 0; page.build()
  ok(state({ state: 'connecting', pending: 1 }) === 'Connecting to phone...\nQueued: 1', 'a watch in German gets English')
  ok(!mainStubs.texts.some((x) => CYRILLIC.test(x)) && !mainStubs.created.some((w) => CYRILLIC.test(w.text || '')), 'nothing on the screen is Russian') }

console.log('4. settings and pairing in English')
{
  const S = stubPath('settings-page.mjs')
  let opts
  globalThis.Page = (o) => { opts = o }
  await import(fileUrl(generate('page/settings.js', { '@zos/ui': S, '@zos/utils': S, '@zos/router': S, '../utils/device': S, '../utils/recordings': S, '../utils/uploadQueue': S, '../i18n': I18N })))
  setLanguage(ENGLISH)
  const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) })
  settingsPageStubs.widgets.length = 0
  page.build()
  const labels = settingsPageStubs.widgets.filter((w) => w.type === 'BUTTON').map((w) => w.opts.text)
  ok(labels.join() === 'Connection,Check connection,Clear memory', 'the buttons -> ' + labels.join())
  ok(page.checkText({ noPhone: true }) === 'Phone does not answer\nRestart the\nZepp app', 'check: the phone does not answer')
  ok(page.checkText({ ok: true, paired: true }) === 'All good:\nserver is reachable,\nwatch is paired', 'check: all good')
  ok(page.checkText({ ok: true, paired: false }) === 'Server is reachable,\nwatch is not paired', 'check: not paired')
  ok(page.checkText({ stage: 'server', error: 'server: timeout' }) === 'Server is unreachable\n(timeout)', 'check: server down')
  ok(page.checkText({ stage: 'token' }) === 'Server is reachable,\nbut the pairing\nwas revoked', 'check: pairing revoked')
  ok(page.checkText({ stage: 'other' }) === 'Server error\n(no answer)', 'check: no reason given') }
{
  const S = stubPath('pair-page.mjs')
  let opts
  globalThis.Page = (o) => { opts = o }
  const link = generate('utils/linkProblem.js', { '../i18n': I18N })
  await import(fileUrl(generate('page/pair.js', { '@zos/ui': S, '@zos/utils': S, '@zos/router': S, '@zos/device': S, '../utils/device': S, '../utils/storageState': S, '../utils/uploadQueue': S, '../utils/screen': S, '../utils/linkProblem': link, '../i18n': I18N })))
  setLanguage(ENGLISH)
  globalThis.getApp = () => ({ _options: { globalData: { messageBuilder: { request: () => new Promise(() => {}) } } } })   // a phone that never answers
  const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) })
  pairStubs.widgets.length = 0
  page.build()
  ok(pairStubs.widgets[0].text === 'Pair the watch' && pairStubs.widgets[1].text === 'Connecting to phone\nand server...', 'the title and the first status')
  ok(page.failureText({ code: 'too_many_attempts' }) === 'Too many attempts.\nPlease wait', 'too many attempts')
  ok(page.failureText({ noPhone: true }).startsWith('Phone is not reachable'), 'the phone does not answer')
  ok(page.failureText({ noServer: true }).startsWith('Server is unreachable'), 'the phone cannot reach the server')
  ok(page.failureText({}) === 'Could not get\na pairing code', 'anything else') }

console.log('5. the storage warning in English')
{
  const S = stubPath('storage-page.mjs')
  let opts
  globalThis.Page = (o) => { opts = o }
  await import(fileUrl(generate('page/storage.js', { '@zos/ui': S, '@zos/utils': S, '@zos/router': S, '@zos/sensor': S, '../utils/storageState': S, '../utils/screen': S, '../i18n': I18N })))
  setLanguage(ENGLISH)
  const page = Object.assign(Object.create(opts), { state: structuredClone({ ...opts.state }) })
  page.onInit(JSON.stringify({ state: 'nearly_full' }))
  ok(page.state.warning.title === 'Storage\nis almost full' && /^Clear it on the website/.test(page.state.warning.text), 'nearly full')
  page.onInit(JSON.stringify({ state: 'full' }))
  ok(page.state.warning.title === 'Storage\nis full' && /oldest recordings are being deleted/.test(page.state.warning.text), 'full')
  for (const bad of ['constructor', '__proto__', 'toString', 'ok', 5, null]) {
    page.onInit(JSON.stringify({ state: bad }))
    ok(page.state.warning === null || page.state.warning.title !== undefined && ['Storage\nis full', 'Storage\nis almost full'].includes(page.state.warning.title), 'an unknown state ' + JSON.stringify(bad) + ' is not a warning of its own') } }

console.log('5b. the connection and confirmation screens, in English and in Russian')
{
  const S = stubPath('connection-pages.mjs')
  const stubs = await import(fileUrl(S))
  const serverUrl = generate('utils/serverUrl.js', {})
  let opts
  globalThis.Page = (o) => { opts = o }
  globalThis.setTimeout = () => 1; globalThis.clearTimeout = () => {}
  await import(fileUrl(generate('page/connection.js', { '@zos/ui': S, '@zos/utils': S, '@zos/router': S, '../utils/device': S, '../utils/serverUrl': serverUrl, '../i18n': I18N })))
  const connection = opts
  await import(fileUrl(generate('page/server.js', { '@zos/ui': S, '@zos/utils': S, '@zos/router': S, '../utils/device': S, '../utils/serverUrl': serverUrl, '../utils/storageState': S, '../utils/uploadQueue': S, '../i18n': I18N })))
  const server = opts
  const make = (o) => Object.assign(Object.create(o), { state: structuredClone({ ...o.state }) })
  const texts = (page, params) => { stubs.widgets.length = 0; if (params) page.onInit(JSON.stringify(params)); page.build(); return stubs.widgets.map((w) => w.type === 'BUTTON' ? w.opts.text : w.text) }
  Object.assign(stubs.state, { paired: true, custom: true, label: 'own.example:8443' }); stubs.setPending(2)
  setLanguage(ENGLISH)
  ok(texts(make(connection)).join('|') === 'Recordings go to\nyour Personal Server:\nown.example:8443|Pair via QR code|Personal Server|Back to PigeonDrop', 'the connection screen in English')
  ok(texts(make(server), { mode: 'personal', serverUrl: 'https://own.example:8443', token: 't' }).join('|') === 'Send recordings to\nown.example:8443\nQueued: 2|Use this server|Cancel', 'the confirmation in English')
  setLanguage(RUSSIAN)
  ok(texts(make(connection)).join('|') === 'Записи идут на\nваш сервер:\nown.example:8443|По QR-коду|Свой сервер|Вернуть PigeonDrop', 'the connection screen in Russian')
  ok(texts(make(server), { mode: 'personal', serverUrl: 'https://own.example:8443', token: 't' }).join('|') === 'Отправлять записи на\nown.example:8443\nВ очереди: 2|Использовать|Отмена', 'the confirmation in Russian')
  ok(texts(make(server), { mode: 'reset' }).join('|') === 'Вернуться на PigeonDrop?\nВ очереди: 2\nПривяжите часы снова,\nчтобы отправить их|Вернуть|Отмена', 'going back to PigeonDrop, in Russian')
  setLanguage(ENGLISH)
  stubs.state.custom = false; stubs.state.label = null }

console.log('6. the source has no text of its own left')
{ // Every string a person reads comes from the tables: no Cyrillic letters in the code of the pages and utilities outside comments.
  const files = ['page/index.js', 'page/list.js', 'page/pair.js', 'page/settings.js', 'page/storage.js', 'page/connection.js', 'page/server.js', 'utils/linkProblem.js', 'utils/uploadQueue.js']
  const root = new URL('..', import.meta.url).pathname
  for (const f of files) {
    const code = fs.readFileSync(root + f, 'utf8').split('\n').filter((l) => !/^\s*(\/\/|\*|\/\*)/.test(l)).map((l) => l.replace(/\s\/\/.*$/, ''))
    ok(!code.some((l) => CYRILLIC.test(l)), f + ' has no Cyrillic string left') } }

console.log('\n' + (failures ? failures + ' FAILURE(S)' : 'ALL CHECKS PASS'))
process.exit(failures ? 1 : 0)
