// Base directory for all persistent server state (the data/ and uploads/
// subfolders). Defaults to this project's own folder for local dev - set
// STORAGE_DIR when deploying somewhere with a mounted persistent volume at
// a different path (e.g. Railway), so accounts-store.js, uploads-store.js,
// and server.js's uploads/ all end up on that volume instead of the
// container's ephemeral filesystem.
const STORAGE_DIR = process.env.STORAGE_DIR || __dirname

module.exports = { STORAGE_DIR }
