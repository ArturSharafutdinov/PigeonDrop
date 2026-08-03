// Watch-side app entry point. Fires before any page is shown / after the
// last page closes. Owns the MessageBuilder connection (see shared/message.js)
// so any page can reach it via getApp()._options.globalData.messageBuilder -
// the connection should only be created/torn down once per app lifetime, not
// per page.

import { MessageBuilder } from './shared/message'
import { getPackageInfo } from '@zos/app'
import * as ble from '@zos/ble'
import { markUploaded, retryPendingUploads } from './utils/uploadQueue'

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

    // The Side Service's own console.log isn't visible through the Zepp
    // App's JS App Log (that screen only shows the watch's logs), so it
    // reports upload results back over the same connection instead - this
    // is also how the upload queue (utils/uploadQueue.js) finds out a
    // recording actually made it to the server, since it has no other way
    // to observe that.
    messageBuilder.on('call', (fullPayload) => {
      // payload always arrives as a raw Buffer - buf2Json decodes it back
      // into the object the Side Service actually sent.
      const data = messageBuilder.buf2Json(fullPayload.payload)
      console.log('phone confirmed:', JSON.stringify(data))

      if (data.uploadOk && data.fileName) {
        markUploaded(data.fileName)
      }
    })

    // One of the three retry triggers for the upload queue: BLE reconnecting
    // means the phone is reachable again, so anything still pending gets
    // another shot without waiting for the next recording or app reopen.
    ble.addListener((status) => {
      if (status === true) {
        retryPendingUploads()
      }
    })
  },

  onDestroy(options) {
    console.log('app on destroy invoke')
    // Required by MessageBuilder - skipping this leaks the BLE connection.
    this.globalData.messageBuilder && this.globalData.messageBuilder.disConnect()
    ble.removeListener()
  },
})
