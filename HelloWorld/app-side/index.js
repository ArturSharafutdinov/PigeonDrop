AppSideService({
  onInit() {
    const inbox = transferFile.getInbox()

    inbox.on('NEWFILE', () => {
      const fileObject = inbox.getNextFile()

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
