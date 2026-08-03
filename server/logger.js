// Minimal file logger - appends timestamped lines to server.log alongside
// printing to the console, so there's a persistent record to look back at
// after something goes wrong (the server runs unattended much of the time).
// No rotation, no levels, no external dependency - matches this prototype's
// "no dependencies" ethos (see CLAUDE.md).

const fs = require('fs')
const path = require('path')

const LOG_FILE = path.join(__dirname, 'server.log')

function formatArg(arg) {
  if (typeof arg === 'string') return arg
  if (arg instanceof Error) return arg.stack || arg.message
  try {
    return JSON.stringify(arg)
  } catch (e) {
    return String(arg)
  }
}

function log(...args) {
  const line = `[${new Date().toISOString()}] ${args.map(formatArg).join(' ')}`
  console.log(line)

  try {
    fs.appendFileSync(LOG_FILE, line + '\n')
  } catch (err) {
    // A failing log write should never take down the server - just say so
    // on the console, once, and move on.
    console.error('logger: failed to write to server.log -', err.message)
  }
}

module.exports = { log }
