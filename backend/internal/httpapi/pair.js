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
  }

  var $ = function (id) { return document.getElementById(id) }
  var state = { cursor: null, pendingCode: null }

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

  function loadMessages(fresh) {
    if (fresh) { state.cursor = null; $('message-list').textContent = '' }
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
        children.push(actions)
        list.appendChild(el('li', {}, children))
      })
      state.cursor = res.data.nextCursor
      $('more').hidden = !state.cursor
      $('message-empty').hidden = list.children.length > 0
      $('delete-all').hidden = list.children.length === 0
    })
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
