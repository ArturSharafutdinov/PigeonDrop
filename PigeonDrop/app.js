// Watch-side app entry point. Fires before any page is shown / after the
// last page closes. Owns the MessageBuilder connection (see shared/message.js)
// so any page can reach it via getApp()._options.globalData.messageBuilder -
// the connection should only be created/torn down once per app lifetime, not
// per page.

import { MessageBuilder } from './shared/message'
import { getPackageInfo } from '@zos/app'
import * as ble from '@zos/ble'
import { retryPendingUploads } from './utils/uploadQueue'

App({
  globalData: {
    messageBuilder: null,
  },

  onCreate(options) {
    const { appId } = getPackageInfo()
    const messageBuilder = new MessageBuilder({ appId, appDevicePort: 20, appSidePort: 0, ble })

    this.globalData.messageBuilder = messageBuilder
    messageBuilder.connect()

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
    // Required by MessageBuilder - skipping this leaks the BLE connection.
    this.globalData.messageBuilder && this.globalData.messageBuilder.disConnect()
    ble.removeListener()
  },
})
