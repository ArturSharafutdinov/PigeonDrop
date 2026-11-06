// Connection screen: where this watch sends its recordings, and how to change that. Reached from "Connection" on
// page/settings.js.
//
//   Pair via QR code   the watch is paired with a PigeonDrop account (page/pair.js).
//   Personal Server    take the address and token the person entered in the Settings App in the Zepp app, from the
//                      phone (app-side/serverConfig.js), and ask the person to confirm them (page/server.js).
//   Back to PigeonDrop shown only while the watch is set up for a server of the person's own (page/server.js).
//
// This page is a menu that leaves by replacing itself (replace, not push), so that the page the person returns to
// after pairing or confirming is built afresh and says what is true now; the Zepp OS device app has no hook that
// would refresh a page that was only covered by another one.

import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { replace } from '@zos/router'
import { isPaired, isCustomServer, serverLabel } from '../utils/device'
import { normalizeServerUrl, isUsableToken } from '../utils/serverUrl'
import { t } from '../i18n'

const REQUEST_TIMEOUT = 15000

Page({
  state: {
    statusText: null, // where the watch sends now, or what is happening
    reading: false, // the settings are being fetched from the phone
    destroyed: false, // set in onDestroy, so a late reply touches nothing
  },

  build() {
    this.state.statusText = createWidget(widget.TEXT, {
      x: px(0),
      y: px(15),
      w: px(480),
      h: px(125),
      text: this.destinationText(),
      text_size: px(20),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })

    // The same three places as on page/settings.js, which keep every corner inside the round display's safe radius.
    this.button(150, t('connection.qr'), () => replace({ url: 'page/pair' }))
    this.button(235, t('connection.personal'), () => this.onTake())
    if (isCustomServer()) this.button(320, t('connection.reset'), () => replace({ url: 'page/server', params: { mode: 'reset' } }))
  },

  onDestroy() {
    this.state.destroyed = true
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

  // Where the recordings go now, in the words of the screen.
  destinationText() {
    if (!isCustomServer()) return t(isPaired() ? 'connection.default_paired' : 'connection.default_unpaired')
    const host = serverLabel() || '?'
    return t(isPaired() ? 'connection.custom' : 'connection.custom_no_token', { host })
  },

  setStatus(text) {
    this.state.statusText.setProperty(prop.MORE, { text })
  },

  // "Personal Server": fetch what the person entered on the phone and, if it is usable, ask them to confirm it here.
  onTake() {
    if (this.state.reading) return
    this.state.reading = true
    this.setStatus(t('connection.reading'))
    const { messageBuilder } = getApp()._options.globalData
    messageBuilder
      .request({ type: 'getServerConfig' }, { timeout: REQUEST_TIMEOUT })
      .then(
        (reply) => reply,
        () => ({ noPhone: true }),
      )
      .then((reply) => {
        this.state.reading = false
        if (this.state.destroyed) return
        this.onConfig(reply)
      })
  },

  onConfig(reply) {
    if (!reply || reply.noPhone) {
      this.setStatus(t('connection.no_phone'))
      return
    }
    if (reply.ok === true && reply.configured === false) {
      this.setStatus(t('connection.not_set'))
      return
    }
    if (reply.ok === true && reply.configured === true) {
      // The phone has checked these, but what comes over Bluetooth is not taken on trust: only an address in its
      // normal form and a usable token go on to the confirmation.
      const checked = normalizeServerUrl(reply.serverUrl)
      if (checked.ok && checked.url === reply.serverUrl && isUsableToken(reply.token)) {
        replace({ url: 'page/server', params: { mode: 'personal', serverUrl: checked.url, token: reply.token } })
        return
      }
    }
    this.setStatus(t('connection.bad_settings'))
  },
})
