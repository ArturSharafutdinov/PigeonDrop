// The part of the "Personal server" tab that is not about the page: checking what was typed, reading the key from the
// address, and sealing the settings for the watch. No page, no network: plain functions of their input, so that they can
// be tried on their own (pair.relay.test.mjs) against the same test vector and the same table of addresses as the watch
// (PigeonDrop/test/fixtures). The watch's half is PigeonDrop/PigeonDrop/utils/relayCrypto.js; the format of the envelope
// is in docs/architecture.md.
//
// What is typed on the page is never kept: nothing here writes to storage, to the address bar or to the console, and
// seal() hands back only the encrypted envelope.
(function (root) {
  'use strict'

  var VERSION = 1
  var MAX_ADDRESS = 200
  var MAX_TOKEN = 256
  var CONTROL = /[\s\u0000-\u001f\u007f-\u009f]/
  var ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_'

  // ---- the address ------------------------------------------------------------------------------
  // The rules of normalizeServerUrl in PigeonDrop/utils/serverUrl.js, written again because the page is plain script
  // (no build step, nothing shared). test/fixtures/server-url-cases.json is what keeps the two from drifting apart.

  function bad(reason) {
    return { ok: false, reason: reason }
  }

  function isIPv4(host) {
    var parts = host.split('.')
    if (parts.length !== 4) return false
    for (var i = 0; i < 4; i++) {
      if (!/^(0|[1-9][0-9]{0,2})$/.test(parts[i]) || Number(parts[i]) > 255) return false
    }
    return true
  }

  function isHostName(host) {
    if (host.length === 0 || host.length > 253) return false
    var labels = host.split('.')
    for (var i = 0; i < labels.length; i++) {
      if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(labels[i])) return false
    }
    return !/^[0-9]+$/.test(labels[labels.length - 1]) // "1.2.3" and "10.0.0.256" are broken IP addresses, not names
  }

  // Returns { ok: true, url, host } or { ok: false, reason }; the reasons are codes the page has a text for.
  function normalizeServerUrl(input) {
    if (typeof input !== 'string') return bad('not_text')
    var text = input.trim()
    if (text.length === 0) return bad('empty')
    if (text.length > MAX_ADDRESS) return bad('too_long')
    if (CONTROL.test(text)) return bad('bad_characters')

    var scheme = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/(.*)$/.exec(text)
    if (!scheme) return bad('no_scheme')
    if (scheme[1].toLowerCase() !== 'https') return bad('not_https')
    var rest = scheme[2]
    if (rest.indexOf('#') !== -1) return bad('has_fragment')
    if (rest.indexOf('?') !== -1) return bad('has_query')

    var slash = rest.indexOf('/')
    var authority = slash === -1 ? rest : rest.slice(0, slash)
    var path = slash === -1 ? '' : rest.slice(slash)
    if (authority.indexOf('@') !== -1) return bad('has_credentials')

    var parts = /^([^:]*)(:([^:]*))?$/.exec(authority)
    if (!parts) return bad('bad_host')
    var host = parts[1].toLowerCase()
    if (!isIPv4(host) && !isHostName(host)) return bad('bad_host')

    var port = ''
    if (parts[2] !== undefined) {
      var digits = parts[3]
      if (!/^(0|[1-9][0-9]{0,4})$/.test(digits) || Number(digits) < 1 || Number(digits) > 65535) return bad('bad_port')
      if (digits !== '443') port = ':' + digits
    }

    if (path !== '') {
      if (path.indexOf('//') !== -1) return bad('bad_path')
      path = path.replace(/\/+$/, '')
      if (!/^(\/[A-Za-z0-9._~-]+)*$/.test(path)) return bad('bad_path')
      var segments = path.split('/')
      for (var i = 0; i < segments.length; i++) {
        if (segments[i] === '.' || segments[i] === '..') return bad('bad_path')
      }
    }
    return { ok: true, url: 'https://' + host + port + path, host: host + port }
  }

  // ---- the token --------------------------------------------------------------------------------

  // Why a token cannot be used, as a code the page has a text for; null when it can. The caller trims first (a token
  // pasted with a line break at its end is the usual case); the rules are those of isUsableToken on the watch.
  function tokenProblem(token) {
    if (typeof token !== 'string' || token.length === 0) return 'empty'
    if (token.length > MAX_TOKEN) return 'too_long'
    if (CONTROL.test(token)) return 'bad_characters'
    return null
  }

  // ---- bytes ------------------------------------------------------------------------------------

  function base64urlEncode(bytes) {
    var out = ''
    for (var i = 0; i < bytes.length; i += 3) {
      var n = (bytes[i] << 16) | ((i + 1 < bytes.length ? bytes[i + 1] : 0) << 8) | (i + 2 < bytes.length ? bytes[i + 2] : 0)
      out += ALPHABET.charAt((n >> 18) & 63) + ALPHABET.charAt((n >> 12) & 63)
      if (i + 1 < bytes.length) out += ALPHABET.charAt((n >> 6) & 63)
      if (i + 2 < bytes.length) out += ALPHABET.charAt(n & 63)
    }
    return out
  }

  // Strict, like the watch's: only the alphabet, no padding, no spare bits set. null for anything else.
  function base64urlDecode(text) {
    if (typeof text !== 'string' || text.length % 4 === 1) return null
    var values = []
    for (var i = 0; i < text.length; i++) {
      var v = ALPHABET.indexOf(text.charAt(i))
      if (v < 0) return null
      values.push(v)
    }
    var spare = text.length % 4 === 2 ? 0x0f : text.length % 4 === 3 ? 0x03 : 0
    if (spare !== 0 && (values[values.length - 1] & spare) !== 0) return null
    var out = new Uint8Array(Math.floor((text.length * 3) / 4))
    var at = 0
    for (var j = 0; j < values.length; j += 4) {
      var n = (values[j] << 18) | ((values[j + 1] || 0) << 12) | ((values[j + 2] || 0) << 6) | (values[j + 3] || 0)
      if (at < out.length) out[at++] = (n >> 16) & 255
      if (at < out.length) out[at++] = (n >> 8) & 255
      if (at < out.length) out[at++] = n & 255
    }
    return out
  }

  function concat(parts) {
    var total = 0
    parts.forEach(function (p) { total += p.length })
    var out = new Uint8Array(total)
    var at = 0
    parts.forEach(function (p) { out.set(p, at); at += p.length })
    return out
  }

  // ---- what the address bar carries -------------------------------------------------------------

  // The watch's secret from the part of the address after the "#": "#k=" and 22 characters (16 bytes, base64url). The
  // browser never sends that part to any server. null when it is missing or is not exactly that.
  function keyFromFragment(hash) {
    if (typeof hash !== 'string') return null
    var found = null
    hash.replace(/^#/, '').split('&').forEach(function (pair) {
      if (pair.slice(0, 2) === 'k=') found = found === null ? pair.slice(2) : ''
    })
    if (found === null || found.length !== 22) return null
    return base64urlDecode(found)
  }

  // The pairing's short code the way the watch binds the envelope to it: no dash, capitals. null if it is not a code.
  function canonicalUserCode(code) {
    if (typeof code !== 'string') return null
    var canonical = code.replace(/-/g, '').toUpperCase()
    return /^[A-Z0-9]{1,32}$/.test(canonical) ? canonical : null
  }

  // Whether this browser can encrypt (WebCrypto is only there on a secure connection).
  function canEncrypt() {
    return !!(root.crypto && root.crypto.subtle && typeof root.crypto.getRandomValues === 'function')
  }

  // ---- sealing ----------------------------------------------------------------------------------

  function hmac(subtle, keyBytes, data) {
    return subtle.importKey('raw', keyBytes, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']).then(function (key) {
      return subtle.sign('HMAC', key, data)
    }).then(function (sig) { return new Uint8Array(sig) })
  }

  // Seals the address and the token for the watch that made `secret`, for the pairing with this user code.
  //   options.secret    Uint8Array(16), from keyFromFragment
  //   options.userCode  the code as it is on the page (canonicalUserCode is applied)
  //   options.url       the normalized address (normalizeServerUrl(...).url)
  //   options.token     the token, trimmed and checked (tokenProblem)
  //   options.first     16 bytes for the first block: random in use, fixed only by tests
  // Resolves with { v: 1, ct, tag }. Rejects if the browser cannot do it.
  function seal(options) {
    var subtle = root.crypto && root.crypto.subtle
    if (!subtle) return Promise.reject(new Error('no WebCrypto'))
    var enc = new TextEncoder()
    var code = canonicalUserCode(options.userCode)
    if (code === null || !options.secret || options.secret.length !== 16) return Promise.reject(new Error('bad input'))
    var first = options.first || root.crypto.getRandomValues(new Uint8Array(16))
    var plain = concat([first, enc.encode(JSON.stringify({ url: options.url, token: options.token }))])

    return Promise.all([
      hmac(subtle, options.secret, enc.encode('pigeondrop/relay/v1/enc')),
      hmac(subtle, options.secret, enc.encode('pigeondrop/relay/v1/mac')),
    ]).then(function (keys) {
      var kEnc = keys[0].slice(0, 16)
      var kMac = keys[1]
      return subtle.importKey('raw', kEnc, { name: 'AES-CBC' }, false, ['encrypt']).then(function (key) {
        // The PKCS#7 padding is added by the browser. The IV is zero on purpose: the first block is random.
        return subtle.encrypt({ name: 'AES-CBC', iv: new Uint8Array(16) }, key, plain)
      }).then(function (cipher) {
        var ct = new Uint8Array(cipher)
        var input = concat([enc.encode('v1'), new Uint8Array(1), enc.encode(code), new Uint8Array(1), ct])
        return hmac(subtle, kMac, input).then(function (tag) {
          return { v: VERSION, ct: base64urlEncode(ct), tag: base64urlEncode(tag) }
        })
      })
    })
  }

  root.PairRelay = {
    VERSION: VERSION,
    normalizeServerUrl: normalizeServerUrl,
    tokenProblem: tokenProblem,
    keyFromFragment: keyFromFragment,
    canonicalUserCode: canonicalUserCode,
    canEncrypt: canEncrypt,
    seal: seal,
    base64urlEncode: base64urlEncode,
    base64urlDecode: base64urlDecode,
  }
})(typeof window !== 'undefined' ? window : globalThis)
