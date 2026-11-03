// What the screen says when something on the way from the watch to the server does not answer, and what
// the person can do about it.
//
// Two different breakages look the same from the watch (a request that gets no reply), so they are told
// apart where they can be seen (utils/uploadQueue.js on the watch, app-side/*.js on the phone) and named here:
//
//   'phone'  - the Zepp app on the phone did not answer the watch: it is not running, it is stuck, or the
//              Bluetooth link is down. Restarting the Zepp app is what helps.
//   'server' - the phone answered, but it could not reach the server: no internet on the phone, or the
//              server (or the proxy in front of it) is down.
//
// Each text has lines short enough for the round display at the size used by the status labels.

import { t } from '../i18n'

export const PHONE = 'phone'
export const SERVER = 'server'

// The text for a kind of problem, or null for anything else (the caller then shows its own reason).
export function problemText(kind) {
  if (kind === PHONE) return t('link.phone')
  if (kind === SERVER) return t('link.server')
  return null
}
