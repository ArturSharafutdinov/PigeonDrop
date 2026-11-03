// The warning that the account's storage on the server is nearly full, or full (so that the oldest recordings
// are being deleted). The server cannot reach the watch by itself, so the warning comes with the answer to an
// upload (utils/uploadQueue.js) and the main page opens this page for it (page/index.js).
//
// It is a page of its own, not a label on the main page: a label shared the screen with the status text and
// the buttons and printed over them. This page takes the screen for a few seconds, buzzes once at the start so
// that it is noticed on the wrist, and goes back to the main page by itself.

import { createWidget, widget, align, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { back } from '@zos/router'
import { Vibrator, VIBRATOR_SCENE_DURATION_LONG } from '@zos/sensor'
import { getStorageState } from '../utils/storageState'
import { holdScreen } from '../utils/screen'
import { t } from '../i18n'

const SHOWN_MS = 10 * 1000
const TITLE_COLOR = 0xffb74d

// The text keys (i18n/en.js) of the warning for each state the server can report.
const WARNING_KEYS = { nearly_full: 'storage.nearly_full', full: 'storage.full' }

function textsFor(state) {
  if (!Object.prototype.hasOwnProperty.call(WARNING_KEYS, state)) return null
  const key = WARNING_KEYS[state]
  return { title: t(key + '.title'), text: t(key + '.text') }
}

// The page is opened with { state } as its parameter. If that is missing or unreadable, what the upload queue
// last remembered is used; if there is nothing to warn about, the page does not stay.
function warningFor(params) {
  let state = null
  try {
    state = JSON.parse(params).state
  } catch (e) {
    // no usable parameter
  }
  return textsFor(state) || textsFor(getStorageState())
}

Page({
  state: {
    timer: null, // the one pending setTimeout (leaving the page), or null
    vibrator: null, // the Vibrator that was started, so it can be stopped if the page goes early
    leaving: false, // set once back() has been called, so it is called once
    warning: null, // what to show: { title, text }, or null when there is nothing to say
  },

  onInit(params) {
    this.state.warning = warningFor(params)
  },

  build() {
    const warning = this.state.warning
    if (!warning) {
      // Nothing to say after all: do not hold the person up.
      this.state.timer = setTimeout(() => this.leave(), 0)
      return
    }

    // Both texts stay inside the round display's safe zone (every corner of each box is within ~215 px of the
    // centre), like everything else in this app.
    createWidget(widget.TEXT, {
      x: px(90),
      y: px(90),
      w: px(300),
      h: px(96),
      text: warning.title,
      text_size: px(36),
      color: TITLE_COLOR,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })
    createWidget(widget.TEXT, {
      x: px(70),
      y: px(200),
      w: px(340),
      h: px(150),
      text: warning.text,
      text_size: px(24),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })

    // The screen stays on for the whole time the page is up, and the hold ends by itself shortly after.
    holdScreen(SHOWN_MS)
    this.buzz()
    this.state.timer = setTimeout(() => this.leave(), SHOWN_MS)
  },

  // One long buzz (VIBRATOR_SCENE_DURATION_LONG, 1 s). A watch that cannot vibrate must not lose the warning.
  buzz() {
    try {
      const vibrator = new Vibrator()
      vibrator.setMode({ mode: VIBRATOR_SCENE_DURATION_LONG })
      vibrator.start()
      this.state.vibrator = vibrator
    } catch (e) {
      console.log('page/storage: vibration failed:', e)
    }
  },

  // Back to the page that opened this one: the main page.
  leave() {
    this.state.timer = null
    if (this.state.leaving) return
    this.state.leaving = true
    try {
      back()
    } catch (e) {
      console.log('page/storage: back failed:', e)
    }
  },

  onDestroy() {
    if (this.state.timer !== null) {
      clearTimeout(this.state.timer)
      this.state.timer = null
    }
    if (this.state.vibrator) {
      try {
        this.state.vibrator.stop()
      } catch (e) {
        console.log('page/storage: could not stop the vibration:', e)
      }
      this.state.vibrator = null
    }
  },
})
