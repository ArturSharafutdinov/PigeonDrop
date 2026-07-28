// Side Service: the phone-side counterpart of the watch app. It runs inside
// the Zepp App itself (not on the watch), has no UI, and is what actually
// receives the recordings the watch sends over BLE.
//
// `transferFile` is a global provided by the Side Service runtime - unlike
// the watch side (which imports TransferFile from '@zos/ble/TransferFile'),
// that module path doesn't resolve here, so no import is needed or possible.

AppSideService({
  onInit() {
    const inbox = transferFile.getInbox()

    // Fires whenever the watch starts sending us a new file.
    inbox.on('NEWFILE', () => {
      const fileObject = inbox.getNextFile()

      // Fires again once that specific transfer finishes (or fails).
      fileObject.on('change', (event) => {
        if (event.data.readyState === 'transferred') {
          console.log('voice message received:', fileObject.fileName, fileObject.filePath)
        } else if (event.data.readyState === 'error') {
          console.log('voice message transfer error')
        }
      })
    })
  },

  onRun() {},

  onDestroy() {},
})
