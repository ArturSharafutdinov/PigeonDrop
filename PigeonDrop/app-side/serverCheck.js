// "Check connection" (page/settings.js): the phone's half. It walks the way a recording takes and says
// where it stops, as a reply { ok, stage, error }:
//   stage 'server' - the server could not be reached, or says it is unwell (GET /healthz also checks its database);
//   stage 'token'  - the server is fine, but it does not accept this watch's device token (no longer paired);
//   stage 'upload' - the upload route answered something unexpected.
// The token is checked with an upload request that has no body: the server looks at the token first and refuses
// the empty request with 400, so nothing is stored. Each call has a deadline shorter than the watch's wait
// (REQUEST_TIMEOUT in page/settings.js), as in app-side/timeout.js.
//
// A watch set up for a server of the person's own (the message carries its `serverUrl`) is checked by the upload
// request alone: such a server is only required to take uploads (docs/api.md), so it may have no /healthz, and a
// check that fails for that would blame a server that works. Without a token there is nothing to check for such a
// watch: it says the token is missing (stage 'token') rather than that all is well.

import { withTimeout } from './timeout'

const STEP_TIMEOUT_MS = 10 * 1000

function reason(err) {
  return String((err && err.message) || err)
}

// message: { deviceToken, serverUrl? }
export function checkServer(message, baseUrl) {
  const deviceToken = message && typeof message.deviceToken === 'string' ? message.deviceToken : ''
  const custom = !!message && Object.prototype.hasOwnProperty.call(message, 'serverUrl')

  if (custom) {
    if (!deviceToken) return Promise.resolve({ ok: false, stage: 'token', error: 'no token' })
    return checkToken(baseUrl, deviceToken)
  }

  return withTimeout(fetch({ url: baseUrl + '/healthz', method: 'GET' }), STEP_TIMEOUT_MS).then(
    (res) => {
      if (res.status !== 200) return { ok: false, stage: 'server', error: 'HTTP ' + res.status }
      if (!deviceToken) return { ok: true, stage: 'server', paired: false }
      return checkToken(baseUrl, deviceToken)
    },
    (err) => ({ ok: false, stage: 'server', error: reason(err) }),
  )
}

function checkToken(baseUrl, deviceToken) {
  return withTimeout(
    fetch({
      url: baseUrl + '/v1/messages',
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + deviceToken },
      body: '{}',
    }),
    STEP_TIMEOUT_MS,
  ).then(
    (r) => {
      if (r.status === 401) return { ok: false, stage: 'token', error: 'HTTP 401' }
      if (r.status === 400) return { ok: true, stage: 'upload', paired: true }
      return { ok: false, stage: 'upload', error: 'HTTP ' + r.status }
    },
    (err) => ({ ok: false, stage: 'upload', error: reason(err) }),
  )
}
