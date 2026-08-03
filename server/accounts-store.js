// Minimal JSON-file-backed store for user accounts, used by the server URL
// configuration flow (watch generates a code -> account created here -> web
// page lets the user pick a serverUrl -> watch syncs it back). No database,
// no locking - a single local JSON file is enough for this prototype (see
// CLAUDE.md: server/ is explicitly not production infrastructure).

const fs = require('fs')
const path = require('path')
const { log } = require('./logger')

const DATA_DIR = path.join(__dirname, 'data')
const ACCOUNTS_FILE = path.join(DATA_DIR, 'accounts.json')

function readAll() {
  if (!fs.existsSync(ACCOUNTS_FILE)) return {}

  try {
    return JSON.parse(fs.readFileSync(ACCOUNTS_FILE, 'utf8'))
  } catch (err) {
    // A corrupt file shouldn't take down the whole server - treat it as empty.
    log('accounts-store: failed to parse accounts.json -', err.message)
    return {}
  }
}

function writeAll(accounts) {
  if (!fs.existsSync(DATA_DIR)) fs.mkdirSync(DATA_DIR)
  fs.writeFileSync(ACCOUNTS_FILE, JSON.stringify(accounts, null, 2))
}

function getAccount(code) {
  return readAll()[code] || null
}

// Creates a new (empty) account for `code` if none exists yet, otherwise
// returns the existing one unchanged. Called both from the watch (right
// after generating a code) and from the web setup page (as a defensive
// upsert, in case the watch's own registration request never reached the
// server) - either caller landing first should produce the same result.
function upsertAccount(code) {
  const accounts = readAll()

  if (!accounts[code]) {
    const now = new Date().toISOString()
    accounts[code] = { code, customServerUrl: null, createdAt: now, updatedAt: now }
    writeAll(accounts)
  }

  return accounts[code]
}

function updateServerUrl(code, customServerUrl) {
  const accounts = readAll()
  if (!accounts[code]) return null

  accounts[code].customServerUrl = customServerUrl
  accounts[code].updatedAt = new Date().toISOString()
  writeAll(accounts)

  return accounts[code]
}

// Shapes an internal account record for API responses, resolving
// customServerUrl into the actual serverUrl the caller should use - the
// watch never needs to know what "default" means, the server decides.
function toPublicShape(account, defaultServerUrl) {
  return {
    code: account.code,
    customServerUrl: account.customServerUrl,
    serverUrl: account.customServerUrl || defaultServerUrl,
    createdAt: account.createdAt,
    updatedAt: account.updatedAt,
  }
}

module.exports = { getAccount, upsertAccount, updateServerUrl, toPublicShape }
