// The watch's identity towards the server: the device token it received when it was paired with a user's
// account (page/pair.js). The token is what makes the server accept this watch's recordings and file them under
// its owner; it is sent with every upload and never shown. Stored in one small JSON file in the app's private
// storage, the same @zos/fs pattern utils/recordings.js and utils/uploadQueue.js use.
//
// A watch may instead be set up for a server the person runs themselves ("Personal Server"): then the same file also
// holds that server's address, beside the token that server gave. The address and the token are always written
// together, because a token is only good for the server that issued it. Without an address in the file the watch
// uses the PigeonDrop server this build was made for. A watch with an address never falls back to that server on its
// own, whatever goes wrong with the address or the token (see getDeviceToken, dropCustomToken): where the recordings
// go changes only when the person changes it.

import { readFileSync, writeFileSync, rmSync } from '@zos/fs'
import { normalizeServerUrl, isUsableToken } from './serverUrl'

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

function hasOwn(object, key) {
  return Object.prototype.hasOwnProperty.call(object, key)
}

// The address stored in a device record, if it is one the app would have written; null if the record has none or a damaged one.
function storedServerUrl(device) {
  if (!hasOwn(device, 'serverUrl') || typeof device.serverUrl !== 'string') return null
  const checked = normalizeServerUrl(device.serverUrl)
  return checked.ok && checked.url === device.serverUrl ? checked.url : null
}

// True when this watch is set up for a server of the person's own (even if its address was later damaged).
export function isCustomServer() {
  return hasOwn(readDevice(), 'serverUrl')
}

// The address of the person's own server, or null: the watch uses the PigeonDrop server, or its address is damaged.
export function getServerUrl() {
  return storedServerUrl(readDevice())
}

// What the screens show as the destination: host[:port] of the person's own server, or null.
export function serverLabel() {
  const url = getServerUrl()
  return url === null ? null : normalizeServerUrl(url).host
}

// The device token, or null if this watch has not been paired (or its pairing was revoked and cleared).
// A watch set up for its own server whose address is damaged has no token either: it has nowhere to send, and sending
// to the PigeonDrop server instead would be the worst answer.
export function getDeviceToken() {
  const device = readDevice()
  if (hasOwn(device, 'serverUrl') && storedServerUrl(device) === null) return null
  const token = device.token
  return typeof token === 'string' && token.length > 0 ? token : null
}

export function isPaired() {
  return getDeviceToken() !== null
}

// Keeps what the server gave on a successful pairing. Replaces any earlier pairing: pairing again always
// ends with exactly one token. It also ends a setup for the person's own server: the record has no address afterwards.
export function saveDevice({ token, deviceId }) {
  writeFileSync({ path: DEVICE_FILE, data: JSON.stringify({ token, deviceId }), options: { encoding: 'utf8' } })
}

// Sets the watch up for the person's own server. Throws on an address or token the app would not accept, before
// anything is written. Replaces any earlier setup, in one write, so the address and the token never disagree.
export function saveCustomServer({ serverUrl, token }) {
  const checked = normalizeServerUrl(serverUrl)
  if (!checked.ok) throw new Error('bad server address')
  if (!isUsableToken(token)) throw new Error('bad token')
  writeFileSync({ path: DEVICE_FILE, data: JSON.stringify({ token, serverUrl: checked.url }), options: { encoding: 'utf8' } })
}

// The person's own server refused the token: forget the token and keep the address. The watch stays set up for that
// server and sends nothing until a new token is given; the recordings wait. Does nothing for a watch that is not set up
// for its own server (the PigeonDrop pairing is ended by clearDevice).
export function dropCustomToken() {
  const url = getServerUrl()
  if (url === null) return
  writeFileSync({ path: DEVICE_FILE, data: JSON.stringify({ serverUrl: url }), options: { encoding: 'utf8' } })
}

// Forgets the pairing or the setup for the person's own server: afterwards the watch is not paired and uses the
// PigeonDrop server again. Used when that server says the token no longer works (the device was revoked), and
// when the person goes back to PigeonDrop.
export function clearDevice() {
  rmSync({ path: DEVICE_FILE })
}
