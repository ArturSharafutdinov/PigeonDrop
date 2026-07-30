// Watch-side app entry point. Fires before any page is shown / after the
// last page closes. Owns the MessageBuilder connection (see shared/message.js)
// so any page can reach it via getApp()._options.globalData.messageBuilder -
// the connection should only be created/torn down once per app lifetime, not
// per page.

import { MessageBuilder } from './shared/message'
import { getPackageInfo } from '@zos/app'
import * as ble from '@zos/ble'

App({
  globalData: {
    messageBuilder: null,
  },

  onCreate(options) {
    console.log('app on create invoke')

    const { appId } = getPackageInfo()
    const messageBuilder = new MessageBuilder({ appId, appDevicePort: 20, appSidePort: 0, ble })

    this.globalData.messageBuilder = messageBuilder
    messageBuilder.connect()

    // PROTOTYPE diagnostic only: the Side Service's own console.log isn't
    // visible through the Zepp App's JS App Log (that screen only shows the
    // watch's logs), so it reports back over the same connection instead -
    // this way the result of the experiment shows up in a log we can
    // actually read.
    messageBuilder.on('call', (fullPayload) => {
      // payload always arrives as a raw Buffer - buf2Json decodes it back
      // into the object the Side Service actually sent.
      const data = messageBuilder.buf2Json(fullPayload.payload)
      console.log('phone confirmed:', JSON.stringify(data))
    })
  },

  onDestroy(options) {
    console.log('app on destroy invoke')
    // Required by MessageBuilder - skipping this leaks the BLE connection.
    this.globalData.messageBuilder && this.globalData.messageBuilder.disConnect()
  },
})
