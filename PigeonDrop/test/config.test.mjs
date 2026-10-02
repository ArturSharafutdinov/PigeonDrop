// config.js is gitignored (it holds the real server URL), so the app builds
// against whatever the developer has locally - while CI and fresh clones only
// have config.example.js. Every constant the code imports from the config must
// therefore also exist in config.example.js, or those builds break.

import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const example = await import(pathToFileURL(path.join(ROOT, 'config.example.js')).href)

function sources(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) return ['node_modules', 'test', 'dist', 'assets'].includes(entry.name) ? [] : sources(full)
    return entry.name.endsWith('.js') && entry.name !== 'config.js' && entry.name !== 'config.example.js' ? [full] : []
  })
}

let failures = 0
const used = new Map()
for (const file of sources(ROOT)) {
  const code = fs.readFileSync(file, 'utf8')
  for (const match of code.matchAll(/import\s*\{([^}]*)\}\s*from\s*['"](?:\.\.?\/)+config['"]/g)) {
    for (const name of match[1].split(',').map((n) => n.trim()).filter(Boolean)) used.set(name, path.relative(ROOT, file))
  }
}

console.log('config constants used by the code: ' + [...used.keys()].join(', '))
for (const [name, file] of used) {
  const present = name in example
  console.log((present ? 'ok   ' : 'FAIL ') + name + ' (used in ' + file + ') is exported by config.example.js')
  if (!present) failures++
}
if (used.size === 0) { console.log('FAIL found no config imports at all - the scan is broken'); failures++ }
process.exit(failures ? 1 : 0)
