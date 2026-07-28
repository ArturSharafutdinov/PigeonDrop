import { readdirSync, mkdirSync, rmSync } from '@zos/fs'
import { MAX_RECORDINGS } from '../config'

export const RECORDINGS_DIR = 'voice_messages'
const FILE_PREFIX = 'voice_'
const FILE_SUFFIX = '.opus'

function pad(n) {
  return n < 10 ? '0' + n : '' + n
}

export function ensureRecordingsDir() {
  try {
    mkdirSync({ path: RECORDINGS_DIR })
  } catch (e) {
    // directory already exists, nothing to do
  }
}

export function newRecordingPath() {
  ensureRecordingsDir()
  const fileName = FILE_PREFIX + Date.now() + FILE_SUFFIX
  return 'data://' + RECORDINGS_DIR + '/' + fileName
}

export function formatTimestamp(timestamp) {
  const date = new Date(timestamp)

  return (
    pad(date.getDate()) +
    '.' +
    pad(date.getMonth() + 1) +
    '.' +
    date.getFullYear() +
    ' ' +
    pad(date.getHours()) +
    ':' +
    pad(date.getMinutes()) +
    ':' +
    pad(date.getSeconds())
  )
}

export function listRecordings() {
  ensureRecordingsDir()
  const files = readdirSync({ path: RECORDINGS_DIR }) || []

  return files
    .filter(function (name) {
      return name.indexOf(FILE_PREFIX) === 0 && name.indexOf(FILE_SUFFIX) === name.length - FILE_SUFFIX.length
    })
    .map(function (name) {
      const timestamp = parseInt(name.slice(FILE_PREFIX.length, -FILE_SUFFIX.length), 10)

      return {
        fileName: name,
        filePath: 'data://' + RECORDINGS_DIR + '/' + name,
        timestamp: timestamp,
        label: formatTimestamp(timestamp),
      }
    })
    .filter(function (item) {
      return !isNaN(item.timestamp)
    })
    .sort(function (a, b) {
      return b.timestamp - a.timestamp
    })
}

export function pruneOldRecordings() {
  const recordings = listRecordings()
  const excess = recordings.slice(MAX_RECORDINGS)

  excess.forEach(function (item) {
    rmSync({ path: RECORDINGS_DIR + '/' + item.fileName })
  })
}
