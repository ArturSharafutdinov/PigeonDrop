// Settings screen: pairing this watch with an account (page/pair.js, which leaves the device
// token in utils/device.js) and clearing the watch's memory of recordings. Reached from the
// Settings button on the main page.

import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { push } from '@zos/router'
import { isPaired, getDeviceToken } from '../utils/device'
import { listRecordings } from '../utils/recordings'
import { clearAllRecordings } from '../utils/uploadQueue'
import { t } from '../i18n'

const CLEAR_CONFIRM_MS = 5000 // how long "press again" stays armed before it falls back
// Longer than the phone's own two steps of 10 s each (app-side/serverCheck.js), so its answer arrives in time.
const CHECK_TIMEOUT_MS = 25000

Page({
  state: {
    statusText: null, // on-screen status/instructions widget
    clearButton: null, // the "Clear memory" button, whose label changes while it waits for confirmation
    checkButton: null, // the "Check connection" button
    checking: false, // a check is under way
    destroyed: false, // set in onDestroy, so a late reply touches nothing
    clearTimer: null, // setTimeout id that disarms the confirmation, or null when not armed
  },

  build() {
    // Status/instructions strip at the top. Spans the full width like the
    // main page's statusText (see page/index.js) - only BUTTON widgets need
    // the round-bezel safe-zone treatment here, plain TEXT doesn't draw a
    // clippable box.
    this.state.statusText = createWidget(widget.TEXT, {
      x: px(0),
      y: px(15),
      w: px(480),
      h: px(125),
      text: this.idleText(),
      text_size: px(20),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })

    // Corners stay well within the ~230px round-bezel safe radius (see ARCHITECTURE.md §4):
    // furthest corner is ~166px from center for the first button, ~152px for the second.
    createWidget(widget.BUTTON, {
      x: px(100),
      y: px(150),
      w: px(280),
      h: px(65),
      radius: px(16),
      normal_color: 0x2f2f2f,
      press_color: 0x4a4a4a,
      text: t('settings.pair'),
      text_size: px(24),
      click_func: () => push({ url: 'page/pair' }),
    })

    // Walks the way to the server through the phone and says where it breaks.
    this.state.checkButton = createWidget(widget.BUTTON, {
      x: px(100),
      y: px(235),
      w: px(280),
      h: px(65),
      radius: px(16),
      normal_color: 0x2f2f2f,
      press_color: 0x4a4a4a,
      text: t('settings.check'),
      text_size: px(22),
      click_func: () => this.onCheck(),
    })

    // Deletes every recording and resets the send queue. Asks for a second
    // press first, since there is no undo.
    this.state.clearButton = createWidget(widget.BUTTON, {
      x: px(100),
      y: px(320),
      w: px(280),
      h: px(65),
      radius: px(16),
      normal_color: 0x2f2f2f,
      press_color: 0x4a4a4a,
      text: t('settings.clear'),
      text_size: px(22),
      click_func: () => this.onClear(),
    })
  },

  // Only the timer is dropped here - the widgets are already being torn down.
  onDestroy() {
    this.state.destroyed = true
    if (this.state.clearTimer !== null) {
      clearTimeout(this.state.clearTimer)
      this.state.clearTimer = null
    }
  },

  idleText() {
    return t(isPaired() ? 'settings.paired' : 'settings.not_paired')
  },

  updateStatus(text) {
    this.state.statusText.setProperty(prop.MORE, { text })
  },

  // "Проверить связь": the phone checks the server and the device token (app-side/serverCheck.js);
  // the status line says in a few words where it went wrong, or that all is well.
  onCheck() {
    if (this.state.checking) return
    this.state.checking = true
    this.updateStatus(t('settings.checking'))
    const { messageBuilder } = getApp()._options.globalData
    messageBuilder
      .request({ type: 'checkServer', deviceToken: getDeviceToken() || '' }, { timeout: CHECK_TIMEOUT_MS })
      .then(
        (reply) => reply,
        () => ({ noPhone: true }),
      )
      .then((reply) => {
        this.state.checking = false
        if (this.state.destroyed) return
        this.updateStatus(this.checkText(reply))
      })
  },

  // The words for a check's result. `error` is the technical reason, cut to fit a line.
  checkText(reply) {
    const why = String((reply && reply.error) || '').replace(/^server: /, '').slice(0, 30)
    if (!reply || reply.noPhone) return t('settings.check_phone')
    if (reply.ok === true) return t(reply.paired === false ? 'settings.check_unpaired' : 'settings.check_ok')
    if (reply.stage === 'server') return t('settings.check_server_down', { why })
    if (reply.stage === 'token') return t('settings.check_revoked')
    return t('settings.check_server_error', { why: why || t('settings.no_answer') })
  },

  // "Очистить память": the first press only asks for confirmation (and shows
  // what would be deleted); a second press within CLEAR_CONFIRM_MS deletes every
  // recording and empties the send queue (utils/uploadQueue.js#clearAllRecordings).
  onClear() {
    if (this.state.clearTimer === null) {
      this.updateStatus(t('settings.confirm_clear', { count: listRecordings().length }))
      this.state.clearButton.setProperty(prop.MORE, { text: t('settings.press_again') })
      this.state.clearTimer = setTimeout(() => {
        this.cancelClearConfirm()
        this.updateStatus(this.idleText())
      }, CLEAR_CONFIRM_MS)
      return
    }

    this.cancelClearConfirm()
    const result = clearAllRecordings()
    this.updateStatus(result.busy ? t('settings.busy') : t('settings.cleared', { removed: result.removed }))
  },

  // Back to the normal label and forget any pending confirmation.
  cancelClearConfirm() {
    if (this.state.clearTimer !== null) {
      clearTimeout(this.state.clearTimer)
      this.state.clearTimer = null
    }
    if (this.state.clearButton) this.state.clearButton.setProperty(prop.MORE, { text: t('settings.clear') })
  },
})
