// node --test scripts/build-api-index.test.mjs
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// A small file in the shape of @zeppos/device-types: documented modules, a function with its namespace, a
// multi-line signature, a class with methods, a module whose body continues after a column-0 brace (as @zos/ui
// does), and a global.
const fixture = `
/**
  * @en File System
  * @zh x
  */
declare module '@zos/fs' {

  /**
   * @en Read-only flag
   * @zh x
   */
  const O_RDONLY: number
  namespace openSync {
    interface Option {
      path: string
    }
    type Result = number
  }
  /**
   * @en Open a file
   *   on two lines
   * @zh x
   * @version 2.0
   */
  function openSync(option: openSync.Option): openSync.Result
  /**
   * @en Many parameters
   * @zh x
   */
  function compare(
    a: number,
    b: number,
  ): number
  /**
   * @en A sensor
   * @zh x
   * @version 2.1
   */
  class Step {
    /**
     * @en Current value
     * @zh x
     */
    getCurrent(): number
    static EVENT: number
  }
}

declare namespace App {
  interface Option {}
}
/**
 * @en Register the app
 * @zh x
 */
declare function App(option: App.Option): void

/**
  * @en Widgets
  * @zh x
  */
declare module '@zos/ui' {
  namespace Inner {
    interface X {}
  }
}

namespace Tail {
    interface Y {}
}
  /** @en Make a widget
   * @zh x */
  const createWidget: number
}

declare module '@zos/user' {
  function getProfile(): number
}
`

function build(text) {
  const dir = mkdtempSync(join(tmpdir(), 'api-index-'))
  const file = join(dir, 'index.d.ts')
  writeFileSync(file, text)
  return execFileSync('node', [new URL('./build-api-index.mjs', import.meta.url).pathname, file], { encoding: 'utf8' })
}

test('lists modules with their ranges and the descriptions of the modules', () => {
  const out = build(fixture)
  assert.match(out, /- `@zos\/fs` · L\d+-\d+ · 4 exports — File System/)
  assert.match(out, /- `@zos\/user` · L\d+-\d+ · 1 exports/)
})

test('a function carries its signature, description, API_LEVEL and the namespace with its types', () => {
  const out = build(fixture)
  assert.match(out, /- `openSync\(option: openSync\.Option\): openSync\.Result` — Open a file on two lines \[API_LEVEL 2\.0\] · L\d+ · types L\d+/)
  assert.match(out, /- `O_RDONLY: number` — Read-only flag · L\d+/)
})

test('a signature over several lines is joined into one', () => {
  assert.match(build(fixture), /- `compare\( a: number, b: number, \): number`/)
})

test('a class lists its methods', () => {
  const out = build(fixture)
  assert.match(out, /- `class Step` — A sensor \[API_LEVEL 2\.1\] · L\d+\n {2}- `getCurrent\(\): number` — Current value/)
  assert.match(out, / {2}- `static EVENT: number`/)
})

test('a module continues after a closing brace in column 0 that is not followed by another declaration', () => {
  const out = build(fixture)
  assert.match(out, /## `@zos\/ui`[\s\S]*- `createWidget: number` — Make a widget/)
})

test('globals are listed apart from the modules', () => {
  const out = build(fixture)
  assert.match(out, /## Globals \(available without import\)[\s\S]*- `App\(option: App\.Option\): void` — Register the app/)
})
