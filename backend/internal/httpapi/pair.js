// The page where a person signs in, pairs a watch, and sees their recordings. It talks to the /v1 API with
// the session cookie and nothing else; it is plain script on purpose (no build step) and will be replaced by
// the real front end. Everything from the server or a watch is put on the page as text, never as markup.
(function () {
  'use strict'

  var MESSAGES = {
    invalid_credentials: 'Неверная почта или пароль.',
    too_many_attempts: 'Слишком много попыток. Подождите немного и повторите.',
    invalid_invite: 'Код приглашения недействителен, просрочен или уже использован.',
    email_taken: 'Эта почта уже зарегистрирована.',
    invalid_email: 'Это не похоже на адрес почты.',
    weak_password: 'Пароль слишком короткий: нужно не меньше 12 символов.',
    invalid_user_code: 'Такого кода нет, или он устарел. Откройте экран привязки на часах заново.',
    forbidden_origin: 'Запрос отклонён: он пришёл с другого сайта.',
    invalid_request: 'Проверьте введённые данные.',
    not_found: 'Не найдено: возможно, это уже удалили.',
    conflict: 'Эту запись сейчас нельзя расшифровать заново: список мог устареть.',
    unavailable: 'Telegram сейчас недоступен. Попробуйте позже.',
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
    var base = MESSAGES[res.data && res.data.code] || 'Не получилось (ошибка ' + res.status + ').'
    var wait = res.retryAfter
    return wait && res.data && res.data.code === 'too_many_attempts' ? base + ' Через ' + wait + ' с.' : base
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

  function showGuest() {
    $('user').hidden = true
    $('guest').hidden = false
    state.waiting = {}
    state.boxes = {}
    clearTimeout(state.poll)
    state.poll = null
    endTelegramWait()
    $('telegram').hidden = true
  }

  function showTab(which) {
    var login = which === 'login'
    $('login-form').hidden = !login
    $('register-form').hidden = login
    $('tab-login').setAttribute('aria-pressed', String(login))
    $('tab-register').setAttribute('aria-pressed', String(!login))
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
        notice('Нет связи с сервером.', 'error')
      })
    })
  }

  // ---- signed in --------------------------------------------------------------------------------

  function showUser(user) {
    $('guest').hidden = true
    $('user').hidden = false
    $('who').textContent = user.email
    loadDevices()
    loadTelegram()
    loadMessages(true)
    loadStorage()
    var code = new URLSearchParams(location.search).get('code')
    if (code) lookup(code)
  }

  function when(iso) {
    return iso ? new Date(iso).toLocaleString('ru-RU') : '—'
  }

  function loadDevices() {
    api('GET', '/v1/devices').then(function (res) {
      if (res.status !== 200) return
      var list = $('device-list')
      list.textContent = ''
      res.data.devices.forEach(function (d) {
        var revoke = el('button', { type: 'button', class: 'link danger', text: 'Отозвать' })
        revoke.addEventListener('click', function () {
          if (!confirm('Отозвать «' + d.name + '»? Эти часы перестанут отправлять записи.')) return
          api('DELETE', '/v1/devices/' + d.id).then(function (r) {
            if (r.status === 204) { notice('Устройство отозвано.', 'ok'); loadDevices() }
            else notice(explain(r), 'error')
          })
        })
        list.appendChild(el('li', {}, [
          el('div', { class: 'title', text: d.name }),
          el('div', { class: 'meta', text: 'Привязано: ' + when(d.createdAt) + ' · Последний раз на связи: ' + (d.lastSeenAt ? when(d.lastSeenAt) : 'ещё не было') }),
          el('div', { class: 'actions' }, [revoke]),
        ]))
      })
      $('device-empty').hidden = res.data.devices.length > 0
    })
  }

  function duration(ms) {
    if (ms === null || ms === undefined) return 'длина неизвестна'
    var s = Math.round(ms / 1000)
    return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0')
  }

  // 1 МБ here is 1 MiB, the unit the limit is set in, so that the limit reads as a round number.
  function size(bytes) {
    if (bytes < 1048576) return Math.round(bytes / 1024) + ' КБ'
    var mb = bytes / 1048576
    return String(mb >= 10 ? Math.round(mb) : Math.round(mb * 10) / 10).replace('.', ',') + ' МБ'
  }

  function loadStorage() {
    api('GET', '/v1/storage').then(function (res) {
      if (res.status !== 200) return
      var percent = Math.floor(res.data.usedBytes * 100 / res.data.limitBytes)
      $('storage-line').textContent = 'Занято ' + size(res.data.usedBytes) + ' из ' + size(res.data.limitBytes) + ' (' + percent + '%).'
      $('storage-warning').hidden = res.data.state === 'ok'
    })
  }

  // ---- the text of a recording ------------------------------------------------------------------

  var SKIPPED = {
    not_decodable: 'Запись не удалось прочитать, расшифровать её нельзя.',
    too_long: 'Запись слишком длинная для расшифровки.',
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
    if (kind === 'pending') box.appendChild(el('p', { class: 'hint', text: 'Ждёт расшифровки…' }))
    else if (kind === 'running') box.appendChild(el('p', { class: 'hint', text: 'Расшифровывается…' }))
    else if (kind === 'done') {
      if (transcript.text) box.appendChild(el('p', { class: 'transcript-text', text: transcript.text }))
      else box.appendChild(el('p', { class: 'hint', text: 'Речь не обнаружена.' }))
    } else if (kind === 'failed') {
      var again = el('button', { type: 'button', class: 'link', text: 'Повторить' })
      again.addEventListener('click', function () {
        again.disabled = true
        api('POST', '/v1/messages/' + id + '/transcribe').then(function (res) {
          if (res.status === 202) { paintTranscript(id, res.data); schedulePoll() }
          else { again.disabled = false; notice(explain(res), 'error'); if (res.status === 409) loadMessages(true) }
        }, function () { again.disabled = false; notice('Нет связи с сервером.', 'error') })
      })
      box.appendChild(el('p', { class: 'hint' }, [document.createTextNode('Не удалось расшифровать. '), again]))
    } else {
      box.appendChild(el('p', { class: 'hint', text: SKIPPED[transcript.errorCode] || 'Эту запись не расшифровывают.' }))
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
        var remove = el('button', { type: 'button', class: 'link danger', text: 'Удалить' })
        remove.addEventListener('click', function () {
          if (!confirm('Удалить эту запись насовсем?')) return
          api('DELETE', '/v1/messages/' + m.id).then(function (r) {
            if (r.status === 204) { loadMessages(true); loadStorage() }
            else notice(explain(r), 'error')
          })
        })
        var actions = el('div', { class: 'actions' }, [
          el('a', { href: '/v1/messages/' + m.id + '/audio?format=original', download: '', text: 'Скачать оригинал' }),
          remove,
        ])
        var children = [
          el('div', { class: 'title', text: when(m.receivedAt) }),
          el('div', { class: 'meta', text: m.deviceName + ' · ' + duration(m.durationMs) }),
        ]
        if (m.hasWav) children.push(el('audio', { controls: '', preload: 'none', src: '/v1/messages/' + m.id + '/audio' }))
        else children.push(el('div', { class: 'meta', text: 'Не удалось преобразовать для прослушивания; оригинал можно скачать.' }))
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
    $('telegram-link').textContent = link ? 'Привязать другой чат' : 'Привязать Telegram'
    $('telegram-unlink').hidden = !link
    $('telegram-blocked').hidden = !(link && link.blocked)
    $('telegram-state').textContent = link
      ? 'Записи приходят в Telegram: ' + (link.username ? '@' + link.username : link.firstName || 'личный чат') + ' (привязано ' + when(link.linkedAt) + ').'
      : 'Не привязан. Привяжите чат, и новые записи с часов будут приходить в него голосовым сообщением с расшифровкой.'
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
        notice('Telegram привязан.', 'ok')
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

  // ---- start ------------------------------------------------------------------------------------

  $('tab-login').addEventListener('click', function () { showTab('login') })
  $('tab-register').addEventListener('click', function () { showTab('register') })

  submit($('login-form'), function (f) {
    return api('POST', '/v1/auth/login', { email: f.get('email'), password: f.get('password') })
  }, function (res) { $('login-form').reset(); showUser(res.data) })

  submit($('register-form'), function (f) {
    return api('POST', '/v1/auth/register', { inviteCode: f.get('inviteCode'), email: f.get('email'), password: f.get('password'), language: 'ru' })
  }, function (res) { $('register-form').reset(); notice('Аккаунт создан.', 'ok'); showUser(res.data) })

  submit($('lookup-form'), function (f) {
    return lookup(String(f.get('code')).trim()).then(function () { return { status: 200 } })
  }, function () {})

  submit($('approve-form'), function (f) {
    return api('POST', '/v1/device/approve', { userCode: state.pendingCode, name: f.get('name') })
  }, function () {
    notice('Готово. Часы получат доступ в течение нескольких секунд.', 'ok')
    endPairing()
    loadDevices()
  })
  $('approve-cancel').addEventListener('click', function () { notice(''); endPairing() })

  $('logout').addEventListener('click', function () {
    api('POST', '/v1/auth/logout').then(function () { notice('Вы вышли.', 'ok'); showGuest() })
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
      if (!url || url.hostname !== 't.me') { notice('Сервер вернул не ту ссылку, которую ждали. Не открывайте её.', 'error'); return }
      $('telegram-open').setAttribute('href', url.href)
      $('telegram-wait').hidden = false
      state.tgBefore = state.tgLinkedAt
      state.tgUntil = Date.now() + TELEGRAM_LINK_MS
      scheduleTelegramPoll()
    }, function () { button.disabled = false; notice('Нет связи с сервером.', 'error') })
  })

  $('telegram-unlink').addEventListener('click', function () {
    if (!confirm('Перестать присылать записи в Telegram?')) return
    api('DELETE', '/v1/telegram').then(function (res) {
      if (res.status === 204) { endTelegramWait(); notice('Telegram отключён.', 'ok'); loadTelegram() }
      else if (res.status !== 401) notice(explain(res), 'error')
    })
  })

  $('delete-all').addEventListener('click', function () {
    if (!confirm('Удалить все записи насовсем? Это нельзя отменить.')) return
    api('DELETE', '/v1/messages').then(function (r) {
      if (r.status === 200) {
        notice('Удалено записей: ' + r.data.deleted + '.', 'ok')
        loadMessages(true)
        loadStorage()
      } else if (r.status !== 401) {
        notice(explain(r), 'error')
      }
    })
  })

  api('GET', '/v1/me').then(function (res) {
    if (res.status === 200) showUser(res.data)
    else if (res.status !== 401) notice('Не удалось связаться с сервером.', 'error')
    // A pairing link opened while signed out: say what is going on.
    else if (new URLSearchParams(location.search).get('code')) notice('Войдите, чтобы привязать часы.')
  }, function () { notice('Нет связи с сервером.', 'error') })
})()
