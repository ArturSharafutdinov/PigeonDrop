// utils/recordings.js: discardRecording(), what "Отмена" uses to throw a recording away.

import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import * as fsStub from './stubs/fs.mjs'
import { generate, fileUrl, stubPath } from './helpers/load.mjs'

const ok = (c, m) => { console.log((c ? 'ok   ' : 'FAIL ') + m); if (!c) process.exitCode = 1 }
const root = fs.mkdtempSync(path.join(os.tmpdir(), 'recordings-'))
fsStub.ctl.root = root
fs.mkdirSync(path.join(root, 'voice_messages'))
const dir = (f) => path.join(root, 'voice_messages', f)
const names = () => fs.readdirSync(path.join(root, 'voice_messages')).sort().join()
const R = await import(fileUrl(generate('utils/recordings.js', { '@zos/fs': stubPath('fs.mjs') })))

fs.writeFileSync(dir('voice_1.opus'), 'a'); fs.writeFileSync(dir('voice_2.opus'), 'b')
ok(R.discardRecording('voice_1.opus') === true && names() === 'voice_2.opus', 'deletes that file and only that one -> ' + names())
ok(R.discardRecording('voice_1.opus') === true && names() === 'voice_2.opus', 'a file that is not there (the recorder never made it) counts as gone, nothing else is touched')

fsStub.ctl.failRm = () => 'return'
ok(R.discardRecording('voice_2.opus') === false && names() === 'voice_2.opus', 'a delete that answers with an error and leaves the file: false, judged from the folder')
fsStub.ctl.failRm = () => 'throw'
ok(R.discardRecording('voice_2.opus') === false && names() === 'voice_2.opus', 'a delete that throws: false, no exception')
fsStub.ctl.failRm = null
fsStub.ctl.failList = true
ok(R.discardRecording('voice_2.opus') === false, 'a folder that cannot be listed afterwards: the file cannot be shown gone, false, no exception')
fsStub.ctl.failList = false
fs.rmSync(dir('voice_2.opus'), { force: true })
ok(R.discardRecording('voice_2.opus') === true, 'gone is gone')

fs.rmSync(path.join(root, 'voice_messages'), { recursive: true })
ok(R.discardRecording('voice_9.opus') === true, 'no recordings folder at all: nothing to remove, true, and nothing is created')
ok(!fs.existsSync(path.join(root, 'voice_messages')), '...nothing created')
fs.rmSync(root, { recursive: true, force: true })
if (!process.exitCode) console.log('ALL CHECKS PASS')
