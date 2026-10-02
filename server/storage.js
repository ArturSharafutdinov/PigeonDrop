// Base directory for all persistent server state (the data/ and uploads/
// subfolders). Defaults to this project's own folder - set STORAGE_DIR to
// keep the state somewhere else (e.g. a mounted persistent volume), so
// accounts-store.js, uploads-store.js, and server.js's uploads/ all end up
// there.
const STORAGE_DIR = process.env.STORAGE_DIR || __dirname

module.exports = { STORAGE_DIR }
