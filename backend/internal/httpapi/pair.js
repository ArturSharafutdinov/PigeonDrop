// The page where a person signs in, pairs a watch, and sees their recordings. It talks to the /v1 API with
// the session cookie and nothing else; it is plain script on purpose (no build step) and will be replaced by
// the real front end. Everything from the server or a watch is put on the page as text, never as markup.
(function () {
  'use strict'

  // The texts are in pair.en.json and pair.ru.json; the server hands them to the page as window.PAIR_TEXTS (pair.texts.js).
  var TEXTS = window.PAIR_TEXTS
  var relay = window.PairRelay // checking and sealing for the Personal server tab (pair.relay.js)
  var LANG_KEY = 'pigeondrop.lang'
  var lang = 'en'
  var chosen = false // the person picked a language with the EN / RU buttons (kept in this browser)

  // The text for a key in the page's language. {name} placeholders are filled from vars; one with no value stays as it is.
  function tr(key, vars) {
    var text = TEXTS[lang][key]
    if (text === undefined) text = TEXTS.en[key]
    if (text === undefined) return key
    return text.replace(/\{(\w+)\}/g, function (all, name) { return vars && Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : all })
  }

  function hasText(key) { return Object.prototype.hasOwnProperty.call(TEXTS.en, key) }

  function savedLanguage() {
    try {
      var l = localStorage.getItem(LANG_KEY)
      return l === 'en' || l === 'ru' ? l : null
    } catch (e) {
      return null // storage blocked: the page still works, it just does not remember
    }
  }

  // Fills every element that has a text key (see pair.html).
  function applyTexts() {
    document.documentElement.lang = lang
    Array.prototype.forEach.call(document.querySelectorAll('[data-i18n]'), function (node) { node.textContent = tr(node.getAttribute('data-i18n')) })
    Array.prototype.forEach.call(document.querySelectorAll('[data-i18n-label]'), function (node) { node.setAttribute('aria-label', tr(node.getAttribute('data-i18n-label'))) })
    $('lang-en').setAttribute('aria-pressed', String(lang === 'en'))
    $('lang-ru').setAttribute('aria-pressed', String(lang === 'ru'))
    showPersonalTexts()
  }

  // Switches the page's language. refresh: redraw what was loaded from the server (the lists, the Telegram line), which
  // is drawn with text. A person's own choice is remembered; the browser's or the account's language is not.
  function setLanguage(next, byPerson, refresh) {
    lang = next
    if (byPerson) {
      chosen = true
      try { localStorage.setItem(LANG_KEY, next) } catch (e) { /* not remembered */ }
    }
    applyTexts()
    if (refresh) {
      notice('')
      if (!$('user').hidden) { loadDevices(); loadTelegram(); loadStorage(); loadMessages(true) }
    }
  }

  var $ = function (id) { return document.getElementById(id) }
  // boxes: the transcript block of each message on the page, by message id; waiting: the ids still being recognised.
  var state = { cursor: null, pendingCode: null, boxes: {}, waiting: {}, loaded: 0, poll: null, tgLinkedAt: null, tgBefore: null, tgUntil: null, tgPoll: null }
  var POLL_MS = 3000
  var TELEGRAM_LINK_MS = 10 * 60 * 1000

  function el(tag, props, children) {
    var node = document.createElement(tag)
    Object.keys(props || {}).forEach(function (k) {
      if (k === 'text') node.textContent = props[k]
      else if (k === 'class') node.className = props[k]
      else node.setAttribute(k, props[k])
    })
    ;(children || []).forEach(function (c) { node.appendChild(c) })
    return node
  }

  function notice(text, kind) {
    var n = $('notice')
    n.textContent = text || ''
    n.className = kind || ''
    n.hidden = !text
  }

  function explain(res) {
    var code = res.data && res.data.code
    var known = code && code !== 'default' && code !== 'retry_in' && hasText('err.' + code)
    var base = known ? tr('err.' + code) : tr('err.default', { status: res.status })
    var wait = res.retryAfter
    return wait && code === 'too_many_attempts' ? base + tr('err.retry_in', { seconds: wait }) : base
  }

  // Calls the API. Resolves with { status, data, retryAfter }; a 401 sends the person back to signing in.
  function api(method, path, body) {
    var init = { method: method, headers: {}, credentials: 'same-origin' }
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json'
      init.body = JSON.stringify(body)
    }
    return fetch(path, init).then(function (r) {
      var retry = parseInt(r.headers.get('Retry-After'), 10)
      var res = { status: r.status, data: null, retryAfter: isNaN(retry) ? null : retry }
      if (r.status === 204) return res
      return r.json().then(function (d) { res.data = d; return res }, function () { return res })
    }).then(function (res) {
      if (res.status === 401) showGuest()
      return res
    })
  }

  function busy(form, on) {
    Array.prototype.forEach.call(form.querySelectorAll('button'), function (b) { b.disabled = on })
  }

  // ---- signed out -------------------------------------------------------------------------------

  // The row of tabs and the panel it shows. view: 'login' | 'register' (not signed in), 'account' (signed in) or 'personal'
  // (either). A person who is not signed in sees three tabs, a signed-in one two.
  var shown = { signedIn: false, view: 'login' }

  function showView(view) {
    shown.view = view
    $('tabs').hidden = false
    $('tab-login').hidden = shown.signedIn
    $('tab-register').hidden = shown.signedIn
    $('tab-account').hidden = !shown.signedIn
    ;['login', 'register', 'account', 'personal'].forEach(function (name) { $('tab-' + name).setAttribute('aria-pressed', String(name === view)) })
    $('guest').hidden = shown.signedIn || view === 'personal'
    $('user').hidden = !shown.signedIn || view === 'personal'
    $('personal').hidden = view !== 'personal'
    $('login-form').hidden = view !== 'login'
    $('register-form').hidden = view !== 'register'
  }

  function showGuest() {
    shown.signedIn = false
    showView(shown.view === 'personal' || shown.view === 'register' ? shown.view : 'login')
    state.waiting = {}
    state.boxes = {}
    clearTimeout(state.poll)
    state.poll = null
    endTelegramWait()
    $('telegram').hidden = true
  }

  function submit(form, send, then) {
    form.addEventListener('submit', function (ev) {
      ev.preventDefault()
      notice('')
      busy(form, true)
      send(new FormData(form)).then(function (res) {
        busy(form, false)
        if (res.status >= 200 && res.status < 300) then(res)
        else notice(explain(res), 'error')
      }, function () {
        busy(form, false)
        notice(tr('no_connection'), 'error')
      })
    })
  }

  // ---- signed in --------------------------------------------------------------------------------

  function showUser(user) {
    // Until the person picks a language here, the page speaks the one their account is set to.
    if (!chosen && (user.language === 'en' || user.language === 'ru') && user.language !== lang) setLanguage(user.language, false, false)
    shown.signedIn = true
    showView(shown.view === 'personal' ? 'personal' : 'account')
    $('who').textContent = user.email
    loadDevices()
    loadTelegram()
    loadMessages(true)
    loadStorage()
    var code = new URLSearchParams(location.search).get('code')
    if (code) lookup(code)
  }

  function when(iso) {
    return iso ? new Date(iso).toLocaleString(lang === 'ru' ? 'ru-RU' : 'en-GB') : '—'
  }

  function loadDevices() {
    api('GET', '/v1/devices').then(function (res) {
      if (res.status !== 200) return
      var list = $('device-list')
      list.textContent = ''
      res.data.devices.forEach(function (d) {
        var revoke = el('button', { type: 'button', class: 'link danger', text: tr('revoke') })
        revoke.addEventListener('click', function () {
          if (!confirm(tr('revoke_confirm', { name: d.name }))) return
          api('DELETE', '/v1/devices/' + d.id).then(function (r) {
            if (r.status === 204) { notice(tr('revoked'), 'ok'); loadDevices() }
            else notice(explain(r), 'error')
          })
        })
        list.appendChild(el('li', {}, [
          el('div', { class: 'title', text: d.name }),
          el('div', { class: 'meta', text: tr('device_meta', { paired: when(d.createdAt), seen: d.lastSeenAt ? when(d.lastSeenAt) : tr('never') }) }),
          el('div', { class: 'actions' }, [revoke]),
        ]))
      })
      $('device-empty').hidden = res.data.devices.length > 0
    })
  }

  function duration(ms) {
    if (ms === null || ms === undefined) return tr('length_unknown')
    var s = Math.round(ms / 1000)
    return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0')
  }

  // 1 MB here is 1 MiB, the unit the limit is set in, so that the limit reads as a round number.
  function size(bytes) {
    if (bytes < 1048576) return Math.round(bytes / 1024) + ' ' + tr('unit_kb')
    var mb = bytes / 1048576
    var text = String(mb >= 10 ? Math.round(mb) : Math.round(mb * 10) / 10)
    return (lang === 'ru' ? text.replace('.', ',') : text) + ' ' + tr('unit_mb')
  }

  function loadStorage() {
    api('GET', '/v1/storage').then(function (res) {
      if (res.status !== 200) return
      var percent = Math.floor(res.data.usedBytes * 100 / res.data.limitBytes)
      $('storage-line').textContent = tr('storage_line', { used: size(res.data.usedBytes), limit: size(res.data.limitBytes), percent: percent })
      $('storage-warning').hidden = res.data.state === 'ok'
    })
  }

  // ---- the text of a recording ------------------------------------------------------------------

  // The text for why a recording is not transcribed, by the error code the server gives.
  function skippedText(code) {
    return hasText('skip.' + code) && code !== 'default' ? tr('skip.' + code) : tr('skip.default')
  }

  // Shows what the server says about a recording's text in its box: waiting, the text, or what went wrong (with a
  // way to try again when that helps). Nothing from the server is ever put in as markup.
  function paintTranscript(id, transcript) {
    var box = state.boxes[id]
    if (!box) return
    box.textContent = ''
    box.hidden = !transcript
    if (transcript && (transcript.state === 'pending' || transcript.state === 'running')) state.waiting[id] = true
    else delete state.waiting[id]
    if (!transcript) return

    var kind = transcript.state
    if (kind === 'pending') box.appendChild(el('p', { class: 'hint', text: tr('transcript_pending') }))
    else if (kind === 'running') box.appendChild(el('p', { class: 'hint', text: tr('transcript_running') }))
    else if (kind === 'done') {
      if (transcript.text) box.appendChild(el('p', { class: 'transcript-text', text: transcript.text }))
      else box.appendChild(el('p', { class: 'hint', text: tr('transcript_empty') }))
    } else if (kind === 'failed') {
      var again = el('button', { type: 'button', class: 'link', text: tr('retry') })
      again.addEventListener('click', function () {
        again.disabled = true
        api('POST', '/v1/messages/' + id + '/transcribe').then(function (res) {
          if (res.status === 202) { paintTranscript(id, res.data); schedulePoll() }
          else { again.disabled = false; notice(explain(res), 'error'); if (res.status === 409) loadMessages(true) }
        }, function () { again.disabled = false; notice(tr('no_connection'), 'error') })
      })
      box.appendChild(el('p', { class: 'hint' }, [document.createTextNode(tr('transcript_failed')), again]))
    } else {
      box.appendChild(el('p', { class: 'hint', text: skippedText(transcript.errorCode) }))
    }
  }

  // Looks again every few seconds while any recording on the page is still waiting for its text, and stops when
  // none is, or the person leaves, or the tab is hidden (it looks at once when the tab is shown again).
  function waitingForText() { return Object.keys(state.waiting).length > 0 }

  function schedulePoll() {
    if (state.poll || document.hidden || !waitingForText()) return
    state.poll = setTimeout(pollNow, POLL_MS)
  }

  function pollNow() {
    state.poll = null
    if (document.hidden || !waitingForText()) return
    api('GET', '/v1/messages?limit=' + Math.min(100, Math.max(20, state.loaded))).then(function (res) {
      if (res.status === 200) res.data.messages.forEach(function (m) { if (state.waiting[m.id]) paintTranscript(m.id, m.transcript) })
      schedulePoll()
    }, schedulePoll)
  }

  document.addEventListener('visibilitychange', function () {
    if (document.hidden) return
    clearTimeout(state.poll)
    state.poll = null
    if (waitingForText()) pollNow()
    if (state.tgUntil !== null) pollTelegram()
  })

  function loadMessages(fresh) {
    if (fresh) { state.cursor = null; state.boxes = {}; state.waiting = {}; state.loaded = 0; $('message-list').textContent = '' }
    var q = '?limit=20' + (state.cursor ? '&before=' + encodeURIComponent(state.cursor) : '')
    api('GET', '/v1/messages' + q).then(function (res) {
      if (res.status !== 200) return
      var list = $('message-list')
      res.data.messages.forEach(function (m) {
        var remove = el('button', { type: 'button', class: 'link danger', text: tr('delete') })
        remove.addEventListener('click', function () {
          if (!confirm(tr('delete_confirm'))) return
          api('DELETE', '/v1/messages/' + m.id).then(function (r) {
            if (r.status === 204) { loadMessages(true); loadStorage() }
            else notice(explain(r), 'error')
          })
        })
        var actions = el('div', { class: 'actions' }, [
          el('a', { href: '/v1/messages/' + m.id + '/audio?format=original', download: '', text: tr('download_original') }),
          remove,
        ])
        var children = [
          el('div', { class: 'title', text: when(m.receivedAt) }),
          el('div', { class: 'meta', text: m.deviceName + ' · ' + duration(m.durationMs) }),
        ]
        if (m.hasWav) children.push(el('audio', { controls: '', preload: 'none', src: '/v1/messages/' + m.id + '/audio' }))
        else children.push(el('div', { class: 'meta', text: tr('no_playback') }))
        var box = el('div', { class: 'transcript' })
        state.boxes[m.id] = box
        children.push(box)
        children.push(actions)
        list.appendChild(el('li', {}, children))
        paintTranscript(m.id, m.transcript)
      })
      state.loaded += res.data.messages.length
      schedulePoll()
      $('transcript-note').hidden = !Object.keys(state.boxes).some(function (id) { return !state.boxes[id].hidden })
      state.cursor = res.data.nextCursor
      $('more').hidden = !state.cursor
      $('message-empty').hidden = list.children.length > 0
      $('delete-all').hidden = list.children.length === 0
      $('download-all').hidden = list.children.length === 0
    })
  }

  // ---- Telegram ---------------------------------------------------------------------------------

  // Shows whether the account is linked to a chat. The section stays hidden when this server has no bot.
  function paintTelegram(status) {
    $('telegram').hidden = !status.available
    var link = status.link
    state.tgLinkedAt = link ? link.linkedAt : null
    if (!status.available) return
    $('telegram-link').textContent = link ? tr('telegram_link_other') : tr('telegram_link')
    $('telegram-unlink').hidden = !link
    $('telegram-blocked').hidden = !(link && link.blocked)
    $('telegram-state').textContent = link
      ? tr('telegram_linked_to', { who: link.username ? '@' + link.username : link.firstName || tr('telegram_private_chat'), when: when(link.linkedAt) })
      : tr('telegram_not_linked')
  }

  function loadTelegram() {
    return api('GET', '/v1/telegram').then(function (res) {
      if (res.status === 200) paintTelegram(res.data)
      return res
    })
  }

  // Waits for the person to press Start in Telegram: looks every few seconds, for as long as the link lives, and
  // only while the tab is shown.
  function endTelegramWait() {
    clearTimeout(state.tgPoll)
    state.tgPoll = null
    state.tgUntil = null
    $('telegram-wait').hidden = true
    $('telegram-open').setAttribute('href', '#')
  }

  function scheduleTelegramPoll() {
    clearTimeout(state.tgPoll)
    state.tgPoll = null
    if (state.tgUntil === null || document.hidden) return
    state.tgPoll = setTimeout(pollTelegram, POLL_MS)
  }

  function pollTelegram() {
    state.tgPoll = null
    if (state.tgUntil === null) return
    if (Date.now() > state.tgUntil) { endTelegramWait(); return }
    loadTelegram().then(function (res) {
      if (state.tgUntil === null) return
      if (res.status === 200 && res.data.link && res.data.link.linkedAt !== state.tgBefore) {
        endTelegramWait()
        notice(tr('telegram_now_linked'), 'ok')
        return
      }
      scheduleTelegramPoll()
    }, scheduleTelegramPoll)
  }

  // ---- pairing ----------------------------------------------------------------------------------

  function lookup(code) {
    notice('')
    return api('GET', '/v1/device/pairing/' + encodeURIComponent(code)).then(function (res) {
      if (res.status !== 200) {
        if (res.status !== 401) notice(explain(res), 'error')
        return
      }
      state.pendingCode = res.data.userCode
      $('pairing-client').textContent = res.data.clientName ? '«' + res.data.clientName + '»' : ''
      $('pairing-code').textContent = res.data.userCode
      $('approve-form').querySelector('[name=name]').value = res.data.clientName || ''
      $('lookup-form').hidden = true
      $('approve-form').hidden = false
    })
  }

  function endPairing() {
    state.pendingCode = null
    $('approve-form').hidden = true
    $('lookup-form').hidden = false
    $('lookup-form').reset()
    history.replaceState(null, '', location.pathname) // the code is spent; do not keep it in the address bar
  }

  // ---- personal server --------------------------------------------------------------------------
  // The person gives the address and the access token of a server of their own; they are sealed in this browser with the
  // key that is in the address of this page after the "#" (the QR code on the watch put it there; a browser never sends
  // that part to any server) and the sealed envelope is handed to the watch through the server, which cannot read it.
  // Nothing typed here is stored: not in the browser's storage, not in the address bar, not logged.

  // What the address carries is read once, before anything on the page can change it.
  var personal = { code: null, secret: null, problem: null, errors: { address: null, token: null }, sent: false }

  function readPersonalAddress() {
    var code = new URLSearchParams(location.search).get('code')
    personal.code = relay.canonicalUserCode(code) === null ? null : code
    personal.secret = relay.keyFromFragment(location.hash)
    personal.problem = !relay.canEncrypt() ? 'crypto' : personal.code === null || personal.secret === null ? 'key' : null
  }

  function setFieldError(id, text) {
    var input = $(id)
    var error = $(id + '-error')
    error.textContent = text
    error.hidden = !text
    if (text) input.setAttribute('aria-invalid', 'true')
    else input.removeAttribute('aria-invalid')
  }

  function showPersonalErrors() {
    setFieldError('personal-address', personal.errors.address ? tr('address_err.' + personal.errors.address) : '')
    setFieldError('personal-token', personal.errors.token ? tr('token_err.' + personal.errors.token) : '')
  }

  function setTokenVisible(visible) {
    $('personal-token').type = visible ? 'text' : 'password'
    $('personal-show').setAttribute('aria-pressed', String(visible))
    $('personal-show').textContent = visible ? tr('personal_hide') : tr('personal_show')
  }

  // The words that depend on the page's language and are not filled in by applyTexts.
  function showPersonalTexts() {
    setTokenVisible($('personal-token').type === 'text')
    showPersonalErrors()
    var blocked = $('personal-blocked')
    blocked.hidden = personal.problem === null || personal.sent
    if (personal.problem === 'crypto') blocked.textContent = tr('personal_blocked_crypto')
    else if (personal.problem === 'key') blocked.textContent = tr('personal_blocked_key')
    $('personal-form').hidden = personal.problem !== null || personal.sent
    $('personal-done').hidden = !personal.sent
  }

  // The envelope was accepted: what was typed is cleared from the page, the key and the code are spent, and the person is
  // shown the address to compare with the watch's screen (the only thing that stays, as text, until they leave).
  function personalSent(host) {
    $('personal-form').reset()
    setTokenVisible(false)
    personal.errors = { address: null, token: null }
    if (personal.secret) personal.secret.fill(0)
    personal.secret = null
    personal.sent = true
    history.replaceState(null, '', location.pathname) // the code is spent; do not keep it or the key in the address bar
    $('personal-host').textContent = host
    showPersonalTexts()
    $('personal-done').focus()
  }

  readPersonalAddress()

  // ---- start ------------------------------------------------------------------------------------

  var saved = savedLanguage()
  chosen = saved !== null
  lang = saved || (/^ru\b/i.test(navigator.language || '') ? 'ru' : 'en')
  applyTexts()
  $('lang-en').addEventListener('click', function () { setLanguage('en', true, true) })
  $('lang-ru').addEventListener('click', function () { setLanguage('ru', true, true) })

  ;['login', 'register', 'account', 'personal'].forEach(function (name) {
    $('tab-' + name).addEventListener('click', function () { notice(''); showView(name) })
  })

  $('personal-show').addEventListener('click', function () { setTokenVisible($('personal-token').type === 'password') })
  ;['address', 'token'].forEach(function (name) {
    $('personal-' + name).addEventListener('input', function () { personal.errors[name] = null; showPersonalErrors() })
  })
  $('personal-form').addEventListener('submit', function (ev) {
    ev.preventDefault()
    notice('')
    var form = $('personal-form')
    var address = relay.normalizeServerUrl($('personal-address').value)
    var token = $('personal-token').value.trim()
    var tokenReason = relay.tokenProblem(token)
    personal.errors = { address: address.ok ? null : address.reason, token: tokenReason }
    showPersonalErrors()
    if (!address.ok) { $('personal-address').focus(); return }
    if (tokenReason) { $('personal-token').focus(); return }
    busy(form, true)
    relay.seal({ secret: personal.secret, userCode: personal.code, url: address.url, token: token }).then(function (envelope) {
      return api('POST', '/v1/device/relay', { userCode: personal.code, envelope: envelope }).then(function (res) {
        busy(form, false)
        if (res.status === 204) personalSent(address.host)
        else if (res.status === 404) notice(tr('personal_err_code'), 'error')
        else notice(explain(res), 'error')
      }, function () { busy(form, false); notice(tr('no_connection'), 'error') })
    }, function () { busy(form, false); notice(tr('personal_err_crypto'), 'error') })
  })

  submit($('login-form'), function (f) {
    return api('POST', '/v1/auth/login', { email: f.get('email'), password: f.get('password') })
  }, function (res) { $('login-form').reset(); showUser(res.data) })

  submit($('register-form'), function (f) {
    return api('POST', '/v1/auth/register', { inviteCode: f.get('inviteCode'), email: f.get('email'), password: f.get('password'), language: lang })
  }, function (res) { $('register-form').reset(); notice(tr('account_created'), 'ok'); showUser(res.data) })

  submit($('lookup-form'), function (f) {
    return lookup(String(f.get('code')).trim()).then(function () { return { status: 200 } })
  }, function () {})

  submit($('approve-form'), function (f) {
    return api('POST', '/v1/device/approve', { userCode: state.pendingCode, name: f.get('name') })
  }, function () {
    notice(tr('pair_done'), 'ok')
    endPairing()
    loadDevices()
  })
  $('approve-cancel').addEventListener('click', function () { notice(''); endPairing() })

  $('logout').addEventListener('click', function () {
    api('POST', '/v1/auth/logout').then(function () { notice(tr('signed_out'), 'ok'); showGuest() })
  })
  $('more').addEventListener('click', function () { loadMessages(false) })

  $('telegram-link').addEventListener('click', function () {
    var button = $('telegram-link')
    notice('')
    button.disabled = true
    api('POST', '/v1/telegram/link').then(function (res) {
      button.disabled = false
      if (res.status !== 200) { if (res.status !== 401) notice(explain(res), 'error'); return }
      var url = null
      try { url = new URL(res.data.url) } catch (e) { url = null }
      if (!url || url.hostname !== 't.me') { notice(tr('telegram_bad_link'), 'error'); return }
      $('telegram-open').setAttribute('href', url.href)
      $('telegram-wait').hidden = false
      state.tgBefore = state.tgLinkedAt
      state.tgUntil = Date.now() + TELEGRAM_LINK_MS
      scheduleTelegramPoll()
    }, function () { button.disabled = false; notice(tr('no_connection'), 'error') })
  })

  $('telegram-unlink').addEventListener('click', function () {
    if (!confirm(tr('telegram_unlink_confirm'))) return
    api('DELETE', '/v1/telegram').then(function (res) {
      if (res.status === 204) { endTelegramWait(); notice(tr('telegram_unlinked'), 'ok'); loadTelegram() }
      else if (res.status !== 401) notice(explain(res), 'error')
    })
  })

  $('delete-all').addEventListener('click', function () {
    if (!confirm(tr('delete_all_confirm'))) return
    api('DELETE', '/v1/messages').then(function (r) {
      if (r.status === 200) {
        notice(tr('deleted_count', { count: r.data.deleted }), 'ok')
        loadMessages(true)
        loadStorage()
      } else if (r.status !== 401) {
        notice(explain(r), 'error')
      }
    })
  })

  api('GET', '/v1/me').then(function (res) {
    if (res.status === 200) showUser(res.data)
    else if (res.status !== 401) notice(tr('server_unreachable'), 'error')
    // A pairing link opened while signed out: say what is going on.
    else if (new URLSearchParams(location.search).get('code')) notice(tr('sign_in_to_pair'))
  }, function () { notice(tr('no_connection'), 'error') })
})()
