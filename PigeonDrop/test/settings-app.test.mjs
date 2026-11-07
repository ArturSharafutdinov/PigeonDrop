// setting/problems.js: what the Settings App tells the person about the address and token they typed.

import { generate, fileUrl } from './helpers/load.mjs'

let failures = 0
const ok = (cond, msg) => { console.log((cond ? '  ok   ' : '  FAIL ') + msg); if (!cond) failures++ }
const SERVER_URL = generate('utils/serverUrl.js', {})
const { addressProblem, tokenProblem, isReady } = await import(fileUrl(generate('setting/problems.js', { '../utils/serverUrl': SERVER_URL })))
const { normalizeServerUrl } = await import(fileUrl(SERVER_URL))

console.log('1. the address')
ok(addressProblem('') === null && addressProblem('   ') === null && addressProblem(undefined) === null, 'nobody is told off before they have typed')
ok(addressProblem('https://my.server.example') === null && addressProblem(' https://my.server.example:8443/drop/ ') === null, 'a good address (trimmed) has no problem')
ok(/https:\/\//.test(addressProblem('http://my.server.example')) && /https:\/\//.test(addressProblem('my.server.example')), 'http and a missing scheme: asks for https://')
ok(/user name or password/.test(addressProblem('https://u:p@my.server.example')), 'a login in the address')
ok(/\?/.test(addressProblem('https://my.server.example?x=1')) && /#/.test(addressProblem('https://my.server.example#x')), 'a query and a fragment')
ok(/port/.test(addressProblem('https://my.server.example:99999')), 'a port out of range')
ok(/spaces/.test(addressProblem('https://my server.example')), 'a space')
{ // every reason the address rules can give has a sentence: nothing falls through to the generic one
  const cases = ['http://a.example', 'a.example', 'https://', 'https://a.example:0', 'https://a.example/../x', 'https://a.example/%41', 'https://a.example?x', 'https://a.example#x', 'https://u@a.example', 'https://a b.example', 'https://' + 'a'.repeat(300) + '.example', 'https://-a.example']
  ok(cases.every((c) => !normalizeServerUrl(c).ok && addressProblem(c) !== null && addressProblem(c) !== 'This address cannot be used.'), 'each kind of bad address gets its own sentence') }

console.log('2. the token')
ok(tokenProblem('') === null && tokenProblem('  ') === null && tokenProblem(undefined) === null, 'empty: nothing to say yet')
ok(tokenProblem('pdt_0123456789abcdef') === null && tokenProblem(' tok\n') === null, 'a good token (pasted with blanks around it) is fine')
ok(tokenProblem('to ken') !== null && tokenProblem('a'.repeat(257)) !== null, 'a space inside, or 257 characters: refused')

console.log('3. when the form says "done"')
ok(isReady('https://my.server.example', 'tok') === true, 'both fields good')
ok(isReady('https://my.server.example', '') === false && isReady('', 'tok') === false && isReady('http://x.example', 'tok') === false && isReady('https://my.server.example', 'to ken') === false, 'anything missing or bad: not ready')

process.exit(failures ? 1 : 0)
