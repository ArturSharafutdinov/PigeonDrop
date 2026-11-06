// Pairing screen: connects this watch to a person's account. Reached from "Pair via QR code" on
// page/connection.js. (Pairing ends any setup for a server of the person's own: see saveDevice in utils/device.js.)
//
// It follows RFC 8628 (the device authorization grant), with the phone's Side Service making the
// network calls (app-side/pairing.js): the server gives this watch a short code and an address; the
// watch shows them as a QR code and as text; the person opens the address on their phone, signs in,
// checks the code against this screen and approves; meanwhile the watch asks every few seconds
// whether that happened, and on the answer that it did, receives the device token (utils/device.js)
// that every later upload carries. See docs/api.md and backend/README.md.
//
// The screen is held on while it waits, which has a limit (the code's lifetime), and a code that
// runs out is not renewed behind the person's back: the screen says so and is opened again.

import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { back } from '@zos/router'
import { getDeviceInfo } from '@zos/device'
import { saveDevice } from '../utils/device'
import { clearStorageState } from '../utils/storageState'
import { retryPendingUploads } from '../utils/uploadQueue'
import { holdScreen, releaseScreen } from '../utils/screen'
import { problemText, PHONE, SERVER } from '../utils/linkProblem'
import { t } from '../i18n'

const REQUEST_TIMEOUT = 15000
const DEFAULT_INTERVAL_S = 5
const MIN_INTERVAL_S = 3 // the server refuses polls faster than this
const START_RETRIES = 5 // attempts to get a code when the phone or server is unreachable
const START_RETRY_DELAY_MS = 4000
const HOLD_MS = 11 * 60 * 1000 // a little over the lifetime of a code (10 minutes)
const DONE_SHOWN_MS = 2500 // how long "paired" stays up before going back to settings

Page({
  state: {
    statusText: null, // caption below the QR code
    headerText: null, // line above it
    qr: null, // the QR widget, created once the server has given a code
    deviceCode: null, // the secret this watch polls with
    expiresAt: 0, // Date.now() after which the code is dead
    timer: null, // the one pending setTimeout (next poll, retry, or leaving the screen), or null
    destroyed: false, // set in onDestroy, so a late reply from the phone touches nothing
    failures: 0, // consecutive failed attempts to get a code
  },

  build() {
    this.state.headerText = createWidget(widget.TEXT, {
      x: px(0),
      y: px(40),
      w: px(480),
      h: px(70),
      text: t('pair.title'),
      text_size: px(26),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })

    // Below the QR code, full width like the other status texts in this app (plain TEXT widgets
    // draw no clippable box, unlike BUTTON).
    this.state.statusText = createWidget(widget.TEXT, {
      x: px(0),
      y: px(355),
      w: px(480),
      h: px(110),
      text: t('pair.connecting'),
      text_size: px(24),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })

    holdScreen(HOLD_MS)
    this.requestCode()
  },

  onDestroy() {
    this.state.destroyed = true
    this.clearTimer()
    releaseScreen()
  },

  getMessageBuilder() {
    return getApp()._options.globalData.messageBuilder
  },

  setStatus(text) {
    this.state.statusText.setProperty(prop.MORE, { text })
  },

  clearTimer() {
    if (this.state.timer !== null) {
      clearTimeout(this.state.timer)
      this.state.timer = null
    }
  },

  later(fn, ms) {
    this.clearTimer()
    this.state.timer = setTimeout(() => {
      this.state.timer = null
      fn()
    }, ms)
  },

  // What the watch calls itself: shown to the person approving, and offered as the device's name.
  clientName() {
    try {
      const info = getDeviceInfo()
      return (info && info.deviceName) || 'Amazfit'
    } catch (e) {
      return 'Amazfit'
    }
  },

  // Asks the server (through the phone) for the codes of a new pairing.
  requestCode() {
    this.getMessageBuilder()
      .request({ type: 'pairStart', clientName: this.clientName() }, { timeout: REQUEST_TIMEOUT })
      .then(
        (reply) => this.onCode(reply),
        (err) => this.onCode({ ok: false, noPhone: true, error: String((err && err.message) || err) }),
      )
  },

  // What to tell the person when no code could be had: the phone not answering and the phone not reaching the
  // server need different things from them (utils/linkProblem.js); anything else the server said has its own words.
  failureText(reply) {
    if (reply && reply.code === 'too_many_attempts') return t('pair.too_many')
    if (reply && reply.noPhone) return problemText(PHONE)
    if (reply && reply.noServer) return problemText(SERVER)
    return t('pair.no_code')
  },

  onCode(reply) {
    if (this.state.destroyed) return

    if (!reply || reply.ok !== true) {
      console.log('pairStart failed:', reply && reply.error)
      this.state.failures++
      if (this.state.failures > START_RETRIES) {
        // It gives up, and says why in the words of the last failure, so the person knows what to fix.
        this.setStatus(this.failureText(reply))
        releaseScreen()
        return
      }
      this.setStatus(this.failureText(reply))
      this.later(() => this.requestCode(), START_RETRY_DELAY_MS)
      return
    }

    this.state.failures = 0
    this.state.deviceCode = reply.deviceCode
    this.state.expiresAt = Date.now() + (reply.expiresIn > 0 ? reply.expiresIn * 1000 : HOLD_MS)

    // Centered QR, sized so its background quiet zone stays within the round display's safe
    // radius (~230px from the 240,240 center) - see docs/architecture.md (round display) for the clipping math.
    this.state.qr = createWidget(widget.QRCODE, {
      content: reply.verificationUriComplete,
      x: px(150),
      y: px(150),
      w: px(180),
      h: px(180),
      // bg_* isn't optional in practice - leaving it unset made the widget's background render far
      // larger than the QR itself and bleed past the bezel. Keeping the quiet zone centered along
      // with the code.
      bg_x: px(135),
      bg_y: px(135),
      bg_w: px(210),
      bg_h: px(210),
      bg_radius: px(12),
    })
    this.setStatus(t('pair.scan', { code: reply.userCode }))
    this.later(() => this.poll(), this.interval(reply.interval))
  },

  interval(seconds) {
    return Math.max(seconds > 0 ? seconds : DEFAULT_INTERVAL_S, MIN_INTERVAL_S) * 1000
  },

  // Asks whether the person has approved yet.
  poll() {
    if (this.state.destroyed) return
    if (Date.now() >= this.state.expiresAt) {
      this.expired()
      return
    }

    this.getMessageBuilder()
      .request({ type: 'pairPoll', deviceCode: this.state.deviceCode }, { timeout: REQUEST_TIMEOUT })
      .then(
        (reply) => this.onPoll(reply),
        (err) => this.onPoll({ ok: false, error: String((err && err.message) || err) }),
      )
  },

  onPoll(reply) {
    if (this.state.destroyed) return

    if (reply && reply.ok === true && reply.status === 'approved' && typeof reply.deviceToken === 'string' && reply.deviceToken.length > 0) {
      this.paired(reply)
      return
    }
    if (reply && reply.ok === true) {
      this.later(() => this.poll(), this.interval(reply.interval)) // still waiting
      return
    }
    if (reply && (reply.code === 'expired_token' || reply.code === 'invalid_device_code')) {
      this.expired()
      return
    }
    // The phone or the server could not be reached (or asked us to slow down): keep trying until the code runs out.
    console.log('pairPoll failed:', reply && (reply.code || reply.error))
    this.later(() => this.poll(), DEFAULT_INTERVAL_S * 1000)
  },

  paired(reply) {
    saveDevice({ token: reply.deviceToken, deviceId: reply.deviceId })
    // What the server last said about the storage was about the account this watch was paired with before, which may
    // be a different one; the first upload for the new account says it afresh.
    try {
      clearStorageState()
    } catch (e) {
      console.log('could not forget the old storage warning:', e)
    }
    this.setStatus(t('pair.done'))
    releaseScreen()
    // Recordings made before pairing have been waiting; send them now.
    retryPendingUploads()
    this.later(() => {
      try {
        back()
      } catch (e) {
        console.log('back failed:', e)
      }
    }, DONE_SHOWN_MS)
  },

  expired() {
    this.clearTimer()
    this.setStatus(t('pair.expired'))
    releaseScreen()
  },
})
