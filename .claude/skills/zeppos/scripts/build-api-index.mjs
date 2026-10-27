#!/usr/bin/env node
// Builds a compact index of the Zepp OS device API from the type declarations in @zeppos/device-types
// (dist/index.d.ts): one line per export with its signature, the English description, the API_LEVEL it appeared
// in and the line of the declaration, so that the full declaration can be read with `sed -n 'A,Bp'`.
//
//   node build-api-index.mjs <path to index.d.ts> [output file]    (output defaults to stdout)
//
// No dependencies: the declarations are regular enough to read line by line. This is a reading aid, not a
// TypeScript parser; whatever it cannot make sense of stays in the .d.ts itself.

import { readFileSync, writeFileSync } from 'node:fs'
import { basename } from 'node:path'

const [input, output] = process.argv.slice(2)
if (!input) {
  console.error('usage: build-api-index.mjs <index.d.ts> [output.md]')
  process.exit(2)
}

const lines = readFileSync(input, 'utf8').split('\n')

// ---- reading one JSDoc block ----------------------------------------------------------------------

// Returns { en, version, deprecated } for the comment ending at lines[end] (`*/`), or null.
function readDoc(end) {
  if (!/\*\/\s*$/.test(lines[end])) return null
  let start = end
  while (start >= 0 && !/\/\*\*/.test(lines[start])) start--
  if (start < 0) return null
  const body = lines.slice(start, end + 1).map((l) => l.replace(/^\s*\/?\*+\/?\s?/, '').replace(/\*\/\s*$/, '').trimEnd())
  const doc = { en: '', version: '', deprecated: false }
  let tag = ''
  for (const line of body) {
    const m = line.match(/^@(\w+)\s*(.*)$/)
    if (m) {
      tag = m[1]
      if (tag === 'en') doc.en = m[2]
      else if (tag === 'version') doc.version = m[2].trim()
      else if (tag === 'deprecated') doc.deprecated = true
    } else if (tag === 'en' && line) {
      doc.en += ' ' + line
    }
  }
  doc.en = doc.en.replace(/\s+/g, ' ').trim()
  return doc
}

// The comment directly above line i (blank lines in between are allowed), or null.
function docAbove(i) {
  let j = i - 1
  while (j >= 0 && lines[j].trim() === '') j--
  return j >= 0 ? readDoc(j) : null
}

// ---- declarations -----------------------------------------------------------------------------

// A declaration may span several lines; join until the parentheses balance.
function readSignature(i, kind) {
  let text = lines[i].trim()
  let depth = (text.match(/\(/g) || []).length - (text.match(/\)/g) || []).length
  let j = i
  while (depth > 0 && j + 1 < lines.length) {
    j++
    const t = lines[j].trim()
    text += ' ' + t
    depth += (t.match(/\(/g) || []).length - (t.match(/\)/g) || []).length
  }
  text = text.replace(/\s+/g, ' ').replace(/\s*\{$/, '')
  if (kind === 'function' || kind === 'method') text = text.replace(/;$/, '')
  return { text, end: j }
}

const modules = [] // { name, from, to, entries: [] }
const globals = { name: 'Globals (available without import)', entries: [] }
let current = null
const namespaceStart = new Map() // name -> line of `namespace name {` in the current scope

function finishModule(i) {
  if (current) current.to = i + 1
  current = null
  namespaceStart.clear()
}

for (let i = 0; i < lines.length; i++) {
  const line = lines[i]

  const mod = line.match(/^declare module '([^']+)' \{/)
  if (mod) {
    current = { name: mod[1], from: i + 1, to: i + 1, entries: [], doc: docAbove(i) }
    modules.push(current)
    continue
  }
  // A module ends at a closing brace in column 0 followed by another `declare` (or the end of the file): the
  // declarations of @zos/ui continue after a column-0 brace that does not end it.
  if (current && /^\}/.test(line)) {
    let next = i + 1
    while (next < lines.length && lines[next].trim() === '') next++
    if (next >= lines.length || /^(declare |\/\*\*)/.test(lines[next])) finishModule(i)
    continue
  }

  const scope = current || globals
  const indent = current ? '  ' : ''

  // namespace X { ... }: holds the Option/Result types of the function X declared right after it.
  const ns = line.match(new RegExp(`^${indent}(?:declare )?namespace (\\w+) \\{`))
  if (ns) {
    namespaceStart.set(ns[1], i + 1)
    continue
  }

  const decl = line.match(new RegExp(`^${indent}(?:declare )?(function|const|class|interface|enum)\\s+(\\w+)`))
  if (!decl) continue
  const [, kind, name] = decl
  const doc = docAbove(i) || { en: '', version: '', deprecated: false }
  const entry = { kind, name, line: i + 1, doc, types: namespaceStart.get(name) || 0, members: [] }

  if (kind === 'function' || kind === 'const') {
    const sig = readSignature(i, kind)
    entry.text = sig.text.replace(/^declare /, '').replace(new RegExp(`^${kind} `), '')
    i = sig.end
  } else if (kind === 'class' || kind === 'interface') {
    entry.text = line.trim().replace(/^declare /, '').replace(/\s*\{$/, '')
    // members: lines at one more level of indentation that look like methods or properties
    const memberIndent = indent + '  '
    let j = i + 1
    for (; j < lines.length; j++) {
      if (new RegExp(`^${indent}\\}`).test(lines[j])) break
      const m = lines[j].match(new RegExp(`^${memberIndent}(static\\s+)?(\\w+)\\??[(:]`))
      if (!m) continue
      const memberDoc = docAbove(j) || { en: '', version: '', deprecated: false }
      const sig = readSignature(j, 'method')
      entry.members.push({ line: j + 1, text: sig.text, doc: memberDoc })
      j = sig.end
    }
    i = j
  } else {
    entry.text = line.trim().replace(/^declare /, '').replace(/\s*\{$/, '')
  }
  scope.entries.push(entry)
}

// ---- output -----------------------------------------------------------------------------------

const out = []
const meta = (() => {
  try {
    return JSON.parse(readFileSync(input.replace(/dist\/[^/]+$/, 'package.json'), 'utf8'))
  } catch {
    return {}
  }
})()

const note = (d) => [d.en && ` — ${d.en}`, d.version && ` [API_LEVEL ${d.version}]`, d.deprecated && ' [deprecated]'].filter(Boolean).join('')

out.push(`# Zepp OS device API index${meta.name ? ` (${meta.name} ${meta.version})` : ''}`)
out.push('')
out.push(`Generated from \`${basename(input)}\`${meta.name ? ` of ${meta.name} ${meta.version}` : ''} by \`build-api-index.mjs\`; do not edit by hand. \`L<n>\` is a line of that file: read a declaration with \`sed -n '<from>,<to>p' <file>\` (the path is printed by \`setup.sh\`).`)
out.push('For a function \`f\`, "types L<n>" is the \`namespace f\` just above it, which holds its \`Option\` (parameters) and \`Result\` types.')
out.push('')
out.push('**Not exhaustive.** The declarations lag behind the firmware and the documentation: check `docs-map.md` and the documentation itself when a module or function is missing here (for example `@zos/media` and `@zos/crypto` are documented but not declared in 4.0.0).')
out.push('')

function render(scope, heading) {
  out.push(`## ${heading}`)
  if (scope.doc && scope.doc.en) out.push(scope.doc.en)
  out.push('')
  for (const e of scope.entries) {
    const types = e.types ? ` · types L${e.types}` : ''
    if (e.kind === 'class' || e.kind === 'interface') {
      out.push(`- \`${e.text}\`${note(e.doc)} · L${e.line}`)
      for (const m of e.members) out.push(`  - \`${m.text}\`${note(m.doc)} · L${m.line}`)
    } else {
      out.push(`- \`${e.text}\`${note(e.doc)} · L${e.line}${types}`)
    }
  }
  out.push('')
}

out.push('## Modules')
out.push('')
for (const m of modules) out.push(`- \`${m.name}\` · L${m.from}-${m.to} · ${m.entries.length} exports${m.doc && m.doc.en ? ` — ${m.doc.en}` : ''}`)
out.push('')
for (const m of modules) render(m, `\`${m.name}\``)
render(globals, globals.name)

const text = out.join('\n')
if (output) writeFileSync(output, text)
else process.stdout.write(text)
