// Server-registration QR screen: generates (or reuses) this watch's account
// code, shows it as a centered QR code pointing at the web setup page, and
// registers an account for it on the server. Reached from the "Настройка
// сервера" button on page/settings.js.

import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { getOrCreateCode } from '../utils/account'
import { WEB_APP_BASE_URL } from '../config'

const REQUEST_TIMEOUT = 15000

Page({
  state: {
    statusText: null, // caption shown below the QR code
  },

  build() {
    const code = getOrCreateCode()

    // Centered QR, sized so its background quiet zone stays within the
    // round display's safe radius (~230px from the 240,240 center) - see
    // ARCHITECTURE.md §4 for the clipping math this follows.
    createWidget(widget.QRCODE, {
      content: WEB_APP_BASE_URL + '/setup?code=' + code,
      x: px(150),
      y: px(150),
      w: px(180),
      h: px(180),
      // bg_* isn't optional in practice - leaving it unset made the widget's
      // background render far larger than the QR itself and bleed past the
      // bezel. Keeping the quiet zone centered right along with the code.
      bg_x: px(135),
      bg_y: px(135),
      bg_w: px(210),
      bg_h: px(210),
      bg_radius: px(12),
    })

    // Caption below the QR, not above it - full width like other status
    // texts in this app (see page/index.js), since plain TEXT widgets don't
    // draw a clippable box the way BUTTON does.
    this.state.statusText = createWidget(widget.TEXT, {
      x: px(0),
      y: px(355),
      w: px(480),
      h: px(110),
      text: 'Код: ' + code + '\nОтсканируйте QR',
      text_size: px(20),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })

    // The QR is shown immediately regardless of whether registration
    // succeeds - the web setup page itself also upserts the account on load
    // (see server/public/setup.html), so a missed BLE round trip here isn't
    // fatal, just less immediate.
    getApp()
      ._options.globalData.messageBuilder.request({ type: 'registerAccount', code: code }, { timeout: REQUEST_TIMEOUT })
      .then((data) => {
        if (!data.ok) throw new Error(data.error || 'unknown error')
        this.updateStatus('Код: ' + code + '\nОтсканируйте QR\nи настройте сервер')
      })
      .catch((err) => {
        console.log('registerAccount failed:', err)
        this.updateStatus('Код: ' + code + '\nОтсканируйте QR\n(регистрация не\nподтверждена)')
      })
  },

  updateStatus(text) {
    this.state.statusText.setProperty(prop.MORE, { text })
  },
})
