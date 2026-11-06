// The address of a server the person runs themselves ("Personal Server").
//
// One place decides what counts as a usable address, and the watch, the phone's Side Service and the Settings App
// all use it, so that an address one of them accepts is never refused by another. The rules are strict on purpose:
// recordings and the access token go to this address, so only a plain https address is taken, with no login in it, no
// query and no fragment. Written by hand: the `URL` class is not known to exist on the watch or in the Side Service.
//
// normalizeServerUrl() returns { ok: true, url, host } or { ok: false, reason }.
//   url    the address to keep, as https://host[:port][/path], no trailing slash; "/v1/messages" is added to it
//   host   host[:port], what the screens show (the person confirms where the recordings will go by reading it)
//   reason a short code, not a text: 'not_text' 'empty' 'too_long' 'bad_characters' 'no_scheme' 'not_https'
//          'has_query' 'has_fragment' 'has_credentials' 'bad_host' 'bad_port' 'bad_path'
// The cases are in test/fixtures/server-url-cases.json; the web page that hands an address to the watch must agree with them.

const MAX_LENGTH = 200

function fail(reason) {
  return { ok: false, reason }
}

// 4 numbers, each 0-255 with no leading zero (some systems read "010" as octal).
function isIPv4(host) {
  const parts = host.split('.')
  if (parts.length !== 4) return false
  for (let i = 0; i < 4; i++) {
    if (!/^(0|[1-9][0-9]{0,2})$/.test(parts[i]) || Number(parts[i]) > 255) return false
  }
  return true
}

function isHostName(host) {
  if (host.length === 0 || host.length > 253) return false
  const labels = host.split('.')
  for (let i = 0; i < labels.length; i++) {
    if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(labels[i])) return false
  }
  // A name whose last part is all digits is a malformed IP address ("1.2.3", "10.0.0.256"), not a name.
  return !/^[0-9]+$/.test(labels[labels.length - 1])
}

export function normalizeServerUrl(input) {
  if (typeof input !== 'string') return fail('not_text')
  const text = input.trim()
  if (text.length === 0) return fail('empty')
  if (text.length > MAX_LENGTH) return fail('too_long')
  if (/[\s\u0000-\u001f\u007f-\u009f]/.test(text)) return fail('bad_characters')

  const scheme = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/(.*)$/.exec(text)
  if (!scheme) return fail('no_scheme')
  if (scheme[1].toLowerCase() !== 'https') return fail('not_https')
  const rest = scheme[2]
  if (rest.indexOf('#') !== -1) return fail('has_fragment')
  if (rest.indexOf('?') !== -1) return fail('has_query')

  const slash = rest.indexOf('/')
  const authority = slash === -1 ? rest : rest.slice(0, slash)
  let path = slash === -1 ? '' : rest.slice(slash)
  if (authority.indexOf('@') !== -1) return fail('has_credentials')

  const parts = /^([^:]*)(:([^:]*))?$/.exec(authority)
  if (!parts) return fail('bad_host') // a second colon: an IPv6 address, which is not taken
  const host = parts[1].toLowerCase()
  if (!isIPv4(host) && !isHostName(host)) return fail('bad_host')

  let port = ''
  if (parts[2] !== undefined) {
    const digits = parts[3]
    if (!/^(0|[1-9][0-9]{0,4})$/.test(digits) || Number(digits) < 1 || Number(digits) > 65535) return fail('bad_port')
    if (digits !== '443') port = ':' + digits
  }

  if (path !== '') {
    if (path.indexOf('//') !== -1) return fail('bad_path')
    path = path.replace(/\/+$/, '')
    if (!/^(\/[A-Za-z0-9._~-]+)*$/.test(path)) return fail('bad_path')
    const segments = path.split('/')
    for (let i = 0; i < segments.length; i++) {
      if (segments[i] === '.' || segments[i] === '..') return fail('bad_path')
    }
  }

  return { ok: true, url: 'https://' + host + port + path, host: host + port }
}

// Which server a request from the watch is for. The watch adds `serverUrl` to the message only when the person has a
// server of their own; without the field the request is for the PigeonDrop server this build was made for.
// A `serverUrl` that cannot be used must never turn into the default server: that would send the person's recordings
// somewhere they did not choose. It is an error, and the recordings stay on the watch.
// Returns { ok: true, url, custom } or { ok: false, error }.
export function resolveServer(message, defaultUrl) {
  if (message && typeof message === 'object' && Object.prototype.hasOwnProperty.call(message, 'serverUrl')) {
    const checked = normalizeServerUrl(message.serverUrl)
    if (!checked.ok) return { ok: false, error: 'bad server address' }
    return { ok: true, url: checked.url, custom: true }
  }
  return { ok: true, url: defaultUrl, custom: false }
}
