// The watch's identity towards the server: the device token it received when it was paired with a user's
// account (page/pair.js). The token is what makes the server accept this watch's recordings and file them under
// its owner; it is sent with every upload and never shown. Stored in one small JSON file in the app's private
// storage, the same @zos/fs pattern utils/recordings.js and utils/uploadQueue.js use.

import { readFileSync, writeFileSync, rmSync } from '@zos/fs'

const DEVICE_FILE = 'device.json'

function readDevice() {
  const raw = readFileSync({ path: DEVICE_FILE, options: { encoding: 'utf8' } })
  if (!raw) return {}

  try {
    const parsed = JSON.parse(raw)
    return parsed && typeof parsed === 'object' ? parsed : {}
  } catch (e) {
    // A corrupt or half-written file means "not paired", not a crash: pairing again repairs it.
    console.log('utils/device: failed to parse device.json, treating the watch as not paired')
    return {}
  }
}

// The device token, or null if this watch has not been paired (or its pairing was revoked and cleared).
export function getDeviceToken() {
  const token = readDevice().token
  return typeof token === 'string' && token.length > 0 ? token : null
}

export function isPaired() {
  return getDeviceToken() !== null
}

// Keeps what the server gave on a successful pairing. Replaces any earlier pairing: pairing again always
// ends with exactly one token.
export function saveDevice({ token, deviceId }) {
  writeFileSync({ path: DEVICE_FILE, data: JSON.stringify({ token, deviceId }), options: { encoding: 'utf8' } })
}

// Forgets the pairing: used when the server says the token no longer works (the device was revoked).
export function clearDevice() {
  rmSync({ path: DEVICE_FILE })
}
