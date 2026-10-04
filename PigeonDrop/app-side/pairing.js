// Phone half of pairing the watch with a user's account (the watch half is page/pair.js).
//
// The watch cannot reach the server by itself: it asks the phone (Side Service) over BLE, and the phone calls
// the server. Pairing follows RFC 8628: startPairing() gets a device code (the watch's secret) and a short user
// code with the address to show as a QR code; pollPairing() asks whether the person has approved it yet and, once,
// hands over the device token. Both return a Promise of the reply the watch receives. A reply is always
// { ok: true, ... } or { ok: false, code, error }, and a transport failure is { ok: false, error } with no code.
// Endpoint details are in API_SPEC.md.

function post(url, body) {
  return fetch({
    url: url,
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }).then((res) => (typeof res.body === 'string' ? JSON.parse(res.body) : res.body))
}

// The server's own error shape is { ok: false, code, error }; anything else is not an answer we understand.
function failure(data) {
  if (data && data.ok === false) {
    return { ok: false, code: typeof data.code === 'string' ? data.code : undefined, error: String(data.error || 'rejected') }
  }
  return { ok: false, error: 'unexpected answer from the server' }
}

function transportFailure(err) {
  return { ok: false, error: 'server: ' + String((err && err.message) || err) }
}

// message: { clientName }
export function startPairing(message, baseUrl) {
  const clientName = message && typeof message.clientName === 'string' ? message.clientName.slice(0, 64) : undefined
  return post(baseUrl + '/v1/device/code', clientName ? { clientName: clientName } : {}).then((data) => {
    if (data && typeof data.deviceCode === 'string' && typeof data.userCode === 'string' && typeof data.verificationUriComplete === 'string') {
      return {
        ok: true,
        deviceCode: data.deviceCode,
        userCode: data.userCode,
        verificationUriComplete: data.verificationUriComplete,
        expiresIn: data.expiresIn,
        interval: data.interval,
      }
    }
    return failure(data)
  }, transportFailure)
}

// message: { deviceCode }
export function pollPairing(message, baseUrl) {
  if (!message || typeof message.deviceCode !== 'string' || message.deviceCode.length === 0) {
    return Promise.resolve({ ok: false, error: 'bad request' })
  }
  return post(baseUrl + '/v1/device/token', { deviceCode: message.deviceCode }).then((data) => {
    if (data && data.status === 'pending') {
      return { ok: true, status: 'pending', interval: data.interval }
    }
    if (data && data.status === 'approved' && typeof data.deviceToken === 'string' && data.deviceToken.length > 0) {
      return { ok: true, status: 'approved', deviceToken: data.deviceToken, deviceId: data.deviceId }
    }
    return failure(data)
  }, transportFailure)
}
