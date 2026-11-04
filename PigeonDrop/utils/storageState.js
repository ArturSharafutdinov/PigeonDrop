// How full the account's storage on the server was the last time the server said so. Every answer to an upload
// carries it ({ state: 'ok' | 'nearly_full' | 'full', ... }, docs/api.md); the main page shows a warning for the
// last two. The server cannot reach the watch by itself, so this is the only way the person hears about it, and
// the state is kept in a small file so the warning is still there when the app is opened again, until a later
// upload says there is room again. Same @zos/fs pattern as utils/device.js.

import { readFileSync, writeFileSync, rmSync } from '@zos/fs'

const STORAGE_FILE = 'storage.json'
const STATES = ['ok', 'nearly_full', 'full']

// The state in the `storage` object of a server answer, or null if there is none or it is not one this app knows
// (a newer server may add states; an unknown one is better ignored than shown wrongly).
export function storageStateOf(storage) {
  return storage && typeof storage === 'object' && STATES.indexOf(storage.state) !== -1 ? storage.state : null
}

// The last state the server reported, or null when there is nothing to warn about ('ok' is not kept).
export function getStorageState() {
  const raw = readFileSync({ path: STORAGE_FILE, options: { encoding: 'utf8' } })
  if (!raw) return null

  try {
    const state = JSON.parse(raw).state
    return state === 'nearly_full' || state === 'full' ? state : null
  } catch (e) {
    // A corrupt or half-written file means "no warning", not a crash: the next upload writes it again.
    console.log('utils/storageState: failed to parse storage.json, ignoring it')
    return null
  }
}

// Remembers what the server said. 'ok' removes the warning. Writes only when something changed: an upload
// answers every time, and the flash does not need to be rewritten with the same thing.
export function saveStorageState(state) {
  if (STATES.indexOf(state) === -1) return
  const wanted = state === 'ok' ? null : state
  if (getStorageState() === wanted) return

  if (wanted === null) clearStorageState()
  else writeFileSync({ path: STORAGE_FILE, data: JSON.stringify({ state: wanted }), options: { encoding: 'utf8' } })
}

// Forgets the warning: used when the watch is no longer paired, since what the server said was about that
// account and the next one may be a different one.
export function clearStorageState() {
  rmSync({ path: STORAGE_FILE })
}
