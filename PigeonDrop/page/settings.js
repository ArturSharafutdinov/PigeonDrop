// Settings screen: pairing this watch with an account (page/pair.js, which leaves the device
// token in utils/device.js) and clearing the watch's memory of recordings. Reached from the
// "Настройки" button on the main page.

import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { push } from '@zos/router'
import { isPaired } from '../utils/device'
import { listRecordings } from '../utils/recordings'
import { clearAllRecordings } from '../utils/uploadQueue'

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
      text: 'Привязать часы',
      text_size: px(24),
      click_func: () => push({ url: 'page/pair' }),
    })

    // Deletes every recording and resets the send queue. Asks for a second
    // press first, since there is no undo.
    this.state.clearButton = createWidget(widget.BUTTON, {
      x: px(100),
      y: px(235),
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
    return isPaired() ? 'Часы привязаны\nк аккаунту' : 'Часы не привязаны\nк аккаунту'
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
})
