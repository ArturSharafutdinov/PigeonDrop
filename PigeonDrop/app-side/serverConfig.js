// "Personal Server" (page/connection.js): the phone's half of taking over what the person entered in the Settings
// App (setting/index.js) in the Zepp app. The Settings App keeps the address and the token in `settingsStorage`; the
// watch asks for them with `getServerConfig` when the person presses the button, and keeps its own copy (utils/device.js)
// once they have confirmed it on the watch's screen. The phone does not push anything and keeps nothing else.
//
// Reply, always an object: { ok: true, configured: false }              nothing has been entered (both fields empty)
//                          { ok: true, configured: true, serverUrl, token }   both usable; serverUrl in its normal form
//                          { ok: false, code: 'invalid_url' | 'invalid_token', error }   something is entered but not usable
// The watch checks the reply again before it believes it (page/connection.js).

import { normalizeServerUrl, isUsableToken } from '../utils/serverUrl'

// `settings` is the Side Service's global for the Settings App's storage. A phone where it is missing or throws has
// nothing entered, as far as the watch can tell.
function read(key) {
  try {
    const value = settings.settingsStorage.getItem(key)
    return typeof value === 'string' ? value.trim() : ''
  } catch (e) {
    return ''
  }
}

export function getServerConfig() {
  const address = read('serverUrl')
  const token = read('serverToken')
  if (address === '' && token === '') return { ok: true, configured: false }

  const checked = normalizeServerUrl(address)
  if (!checked.ok) return { ok: false, code: 'invalid_url', error: 'address: ' + checked.reason }
  if (!isUsableToken(token)) return { ok: false, code: 'invalid_token', error: 'token is missing or not usable' }
  return { ok: true, configured: true, serverUrl: checked.url, token }
}
