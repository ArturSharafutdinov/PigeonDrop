// Loads the app's real source files for tests, with their imports redirected.
//
// The app is written for the Zepp OS runtime: it imports '@zos/*' modules that
// exist only on the watch, and its own modules without file extensions. Rather
// than keep a second copy of the code for tests, generate() reads the real file,
// rewrites each import to the stub (or generated sibling) given in `rewrites`,
// and writes the result to a temp folder. Every import must be mapped: a new
// import in the source makes the tests fail loudly instead of silently running
// against the wrong thing.

import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const OUT_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'pigeondrop-test-'))
process.on('exit', () => fs.rmSync(OUT_DIR, { recursive: true, force: true }))

let counter = 0

export const stubPath = (name) => path.join(ROOT, 'test', 'stubs', name)

// The real i18n module (i18n/index.js with both text tables) on top of the stubbed @zos/settings
// (test/stubs/settings.mjs), which says the watch is set to Russian until a test changes it.
export function i18nModule() {
  return generate('i18n/index.js', {
    '@zos/settings': stubPath('settings.mjs'),
    './en': generate('i18n/en.js', {}),
    './ru': generate('i18n/ru.js', {}),
  })
}
export const fileUrl = (file) => pathToFileURL(file).href

// `relPath` is relative to PigeonDrop/. `rewrites` maps an import specifier to
// the absolute path of the file that should be imported instead. Returns the
// path of the generated file.
export function generate(relPath, rewrites) {
  const source = fs.readFileSync(path.join(ROOT, relPath), 'utf8')

  // Matches `import ... from '...'` on one line and the `} from '...'` that closes a multi-line import.
  const code = source.replace(/^((?:import\b[^\n]*?|\s*\})\s*from\s+)(['"])([^'"\n]+)\2/gm, (match, head, quote, specifier) => {
    const target = rewrites[specifier]
    if (!target) throw new Error(relPath + ' imports ' + JSON.stringify(specifier) + ', which this test does not map')
    return head + quote + fileUrl(target) + quote
  })

  const file = path.join(OUT_DIR, ++counter + '-' + path.basename(relPath, '.js') + '.mjs')
  fs.writeFileSync(file, code)
  return file
}
