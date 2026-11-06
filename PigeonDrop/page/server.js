// Confirmation screen for changing where this watch sends its recordings. Opened by page/connection.js with
// { mode, serverUrl, token } as its parameter:
//
//   mode 'personal'  the person asks for the server and token they entered on the phone: shows the host the
//                    recordings would go to and how many are waiting, and saves them only after "Use this server"
//   mode 'reset'     the person asks to go back to PigeonDrop: the watch forgets the server and its token (it then
//                    has to be paired again), after a confirmation that says how many recordings are waiting
//
// The host is shown in big letters because reading it is the whole point of the screen: everything the person records
// from now on, and what is already waiting, goes there. Recordings that are waiting stay queued and go to the new
// server (utils/uploadQueue.js sends to whichever server the watch is set up for when it sends).

import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { back, replace } from '@zos/router'
import { saveCustomServer, clearDevice } from '../utils/device'
import { normalizeServerUrl, isUsableToken } from '../utils/serverUrl'
import { clearStorageState } from '../utils/storageState'
import { pendingRecordings, retryPendingUploads } from '../utils/uploadQueue'
import { t } from '../i18n'

const DONE_SHOWN_MS = 2500 // how long "Done" stays up before going back

// The parameter, if it is one this screen may act on; null otherwise (then the screen leaves at once).
function readParams(params) {
  let p = null
  try {
    p = typeof params === 'string' ? JSON.parse(params) : params
  } catch (e) {
    return null
  }
  if (!p || typeof p !== 'object') return null
  if (p.mode === 'reset') return { mode: 'reset' }
  if (p.mode === 'personal') {
    const checked = normalizeServerUrl(p.serverUrl)
    if (checked.ok && checked.url === p.serverUrl && isUsableToken(p.token)) {
      return { mode: 'personal', serverUrl: checked.url, host: checked.host, token: p.token }
    }
  }
  return null
}

Page({
  state: {
    request: null, // what was asked: { mode, serverUrl, host, token }, or null
    statusText: null,
    timer: null, // the one pending setTimeout (leaving the screen), or null
    busy: false, // a choice was made: the buttons do nothing more
    destroyed: false,
  },

  onInit(params) {
    this.state.request = readParams(params)
  },

  build() {
    const request = this.state.request
    if (!request) {
      // Nothing this screen may act on: do not keep the person here.
      this.state.timer = setTimeout(() => replace({ url: 'page/connection' }), 0)
      return
    }
    const pending = pendingRecordings().length
    const text =
      request.mode === 'personal'
        ? t('server.confirm', { host: request.host, pending })
        : t('server.reset_confirm', { pending })

    this.state.statusText = createWidget(widget.TEXT, {
      x: px(0),
      y: px(15),
      w: px(480),
      h: px(205),
      text,
      text_size: px(request.mode === 'personal' && request.host.length > 22 ? 20 : 24),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })
    this.button(235, request.mode === 'personal' ? t('server.use') : t('server.go_back'), () => this.onConfirm())
    this.button(320, t('main.cancel'), () => this.onCancel())
  },

  onDestroy() {
    this.state.destroyed = true
    if (this.state.timer !== null) {
      clearTimeout(this.state.timer)
      this.state.timer = null
    }
  },

  button(y, text, onClick) {
    createWidget(widget.BUTTON, {
      x: px(100),
      y: px(y),
      w: px(280),
      h: px(65),
      radius: px(16),
      normal_color: 0x2f2f2f,
      press_color: 0x4a4a4a,
      text,
      text_size: px(22),
      click_func: onClick,
    })
  },

  setStatus(text) {
    this.state.statusText.setProperty(prop.MORE, { text })
  },

  onCancel() {
    if (this.state.busy) return
    this.state.busy = true
    replace({ url: 'page/connection' })
  },

  onConfirm() {
    const request = this.state.request
    if (this.state.busy || !request) return
    this.state.busy = true
    try {
      if (request.mode === 'personal') saveCustomServer({ serverUrl: request.serverUrl, token: request.token })
      else clearDevice()
      // What the old server last said about the storage was about another account or server.
      clearStorageState()
    } catch (e) {
      console.log('page/server: could not save:', e)
      this.state.busy = false
      this.setStatus(t('server.failed'))
      return
    }
    this.setStatus(request.mode === 'personal' ? t('server.done', { host: request.host }) : t('server.done_reset'))
    // Recordings that were waiting go to the server the watch is set up for now.
    retryPendingUploads()
    this.state.timer = setTimeout(() => {
      this.state.timer = null
      if (!this.state.destroyed) back()
    }, DONE_SHOWN_MS)
  },
})
