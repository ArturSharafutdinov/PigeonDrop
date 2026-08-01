// Manages the watch's local account state: the unique code used to register
// with the server (see config.js's SERVER_URL / WEB_APP_BASE_URL) and the
// serverUrl resolved from a "Синхронизация" sync. Stored as a single flat
// JSON file, following the same @zos/fs pattern utils/recordings.js already
// uses for storing state on the watch's private /data directory.

import { readFileSync, writeFileSync } from '@zos/fs'

const ACCOUNT_FILE = 'account.json'

function readAccount() {
  const raw = readFileSync({ path: ACCOUNT_FILE, options: { encoding: 'utf8' } })
  if (!raw) return {}

  try {
    return JSON.parse(raw)
  } catch (e) {
    // Corrupt/partial file shouldn't crash the app - just start fresh.
    console.log('utils/account: failed to parse account.json, resetting')
    return {}
  }
}

function writeAccount(account) {
  writeFileSync({ path: ACCOUNT_FILE, data: JSON.stringify(account), options: { encoding: 'utf8' } })
}

// Returns the existing device code, generating and persisting a new one on
// first use. No crypto needed - there's no authentication yet, this is just
// an identifier the server uses to key the account record (see
// server/accounts-store.js).
export function getOrCreateCode() {
  const account = readAccount()

  if (account.code) return account.code

  const code = Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8)
  writeAccount({ ...account, code })
  return code
}

// Like getOrCreateCode, but never generates one - used by "Синхронизация",
// which should fail with a clear message rather than silently minting a
// fresh (unregistered) code that the server has never heard of.
export function getCodeIfExists() {
  return readAccount().code || null
}

// The server URL to actually use for uploads - null until the first
// successful sync, in which case app-side/index.js falls back to config.js's
// SERVER_URL bootstrap value.
export function getServerUrl() {
  return readAccount().serverUrl || null
}

export function saveServerUrl(serverUrl) {
  const account = readAccount()
  writeAccount({ ...account, serverUrl })
}
