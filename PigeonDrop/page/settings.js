// Server-configuration screen: entry point for registering this watch with
// an account on the server (config.js's SERVER_URL / WEB_APP_BASE_URL, QR
// code shown on page/qrcode.js) and pulling down whichever serverUrl has
// been configured for that account (see utils/account.js and
// app-side/index.js). Reached from the "Настройки" button on the main page.

import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { push } from '@zos/router'
import { getCodeIfExists, saveServerUrl } from '../utils/account'
import { listRecordings } from '../utils/recordings'
import { clearAllRecordings } from '../utils/uploadQueue'

const REQUEST_TIMEOUT = 15000
const CLEAR_CONFIRM_MS = 5000 // how long "press again" stays armed before it falls back
const CLEAR_LABEL = 'Очистить память'

Page({
  state: {
    statusText: null, // on-screen status/instructions widget
    clearButton: null, // the "Очистить память" button, whose label changes while it waits for confirmation
    clearTimer: null, // setTimeout id that disarms the confirmation, or null when not armed
  },

  build() {
    // Status/instructions strip at the top. Spans the full width like the
    // main page's statusText (see page/index.js) - only BUTTON widgets need
    // the round-bezel safe-zone treatment here, plain TEXT doesn't draw a
    // clippable box.
    this.state.statusText = createWidget(widget.TEXT, {
      x: px(0),
      y: px(10),
      w: px(480),
      h: px(60),
      text: this.idleText(),
      text_size: px(20),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })

    // Buttons pulled up close under the status text - this page no longer
    // hosts the QR code itself (see page/qrcode.js), so there's no reason to
    // leave a gap for it. Corners stay well within the ~230px round-bezel safe
    // radius (see ARCHITECTURE.md §4): furthest corner is ~166px from center for
    // the first button, ~152px for the second, ~198px for the third.
    createWidget(widget.BUTTON, {
      x: px(100),
      y: px(150),
      w: px(280),
      h: px(65),
      radius: px(16),
      normal_color: 0x2f2f2f,
      press_color: 0x4a4a4a,
      text: 'Настройка сервера',
      text_size: px(22),
      click_func: () => push({ url: 'page/qrcode' }),
    })

    createWidget(widget.BUTTON, {
      x: px(100),
      y: px(230),
      w: px(280),
      h: px(70),
      radius: px(16),
      normal_color: 0x2f2f2f,
      press_color: 0x4a4a4a,
      text: 'Синхронизация',
      text_size: px(24),
      click_func: () => this.onSync(),
    })

    // Deletes every recording and resets the send queue. Asks for a second
    // press first, since there is no undo.
    this.state.clearButton = createWidget(widget.BUTTON, {
      x: px(100),
      y: px(315),
      w: px(280),
      h: px(65),
      radius: px(16),
      normal_color: 0x2f2f2f,
      press_color: 0x4a4a4a,
      text: CLEAR_LABEL,
      text_size: px(22),
      click_func: () => this.onClear(),
    })
  },

  // Only the timer is dropped here - the widgets are already being torn down.
  onDestroy() {
    if (this.state.clearTimer !== null) {
      clearTimeout(this.state.clearTimer)
      this.state.clearTimer = null
    }
  },

  idleText() {
    return 'Настройка сервера\nдля загрузки записей'
  },

  updateStatus(text) {
    this.state.statusText.setProperty(prop.MORE, { text })
  },

  // "Очистить память": the first press only asks for confirmation (and shows
  // what would be deleted); a second press within CLEAR_CONFIRM_MS deletes every
  // recording and empties the send queue (utils/uploadQueue.js#clearAllRecordings).
  onClear() {
    if (this.state.clearTimer === null) {
      this.updateStatus('Удалить все записи: ' + listRecordings().length + '\nНажмите ещё раз')
      this.state.clearButton.setProperty(prop.MORE, { text: 'Нажмите ещё раз' })
      this.state.clearTimer = setTimeout(() => {
        this.cancelClearConfirm()
        this.updateStatus(this.idleText())
      }, CLEAR_CONFIRM_MS)
      return
    }

    this.cancelClearConfirm()
    const result = clearAllRecordings()
    this.updateStatus(result.busy ? 'Идёт отправка.\nПодождите и повторите' : 'Память очищена\nУдалено записей: ' + result.removed)
  },

  // Back to the normal label and forget any pending confirmation.
  cancelClearConfirm() {
    if (this.state.clearTimer !== null) {
      clearTimeout(this.state.clearTimer)
      this.state.clearTimer = null
    }
    if (this.state.clearButton) this.state.clearButton.setProperty(prop.MORE, { text: CLEAR_LABEL })
  },

  // getApp() is a Zepp OS global (no import needed - see the identical
  // pattern in page/index.js), giving access to the single MessageBuilder
  // connection app.js sets up for the whole app's lifetime.
  getMessageBuilder() {
    return getApp()._options.globalData.messageBuilder
  },

  // Pulls the account's currently configured serverUrl from the server and
  // persists it locally - from this point on, recordings upload there
  // instead of config.js's bootstrap SERVER_URL (see app-side/index.js).
  onSync() {
    const code = getCodeIfExists()

    if (!code) {
      this.updateStatus('Сначала нажмите\n«Настройка сервера»')
      return
    }

    this.updateStatus('Синхронизация...')

    this.getMessageBuilder()
      .request({ type: 'sync', code: code }, { timeout: REQUEST_TIMEOUT })
      .then((data) => {
        if (!data.ok) throw new Error(data.error || 'unknown error')
        saveServerUrl(data.serverUrl)
        this.updateStatus('Сервер настроен:\n' + data.serverUrl)
      })
      .catch((err) => {
        console.log('sync failed:', err)
        this.updateStatus('Ошибка синхронизации\n' + String(err))
      })
  },
})
