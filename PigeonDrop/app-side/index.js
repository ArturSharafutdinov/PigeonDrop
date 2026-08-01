// Side Service: the phone-side counterpart of the watch app. It runs inside
// the Zepp App itself (not on the watch), has no UI, and is what actually
// receives the recordings the watch sends over BLE.
//
// `transferFile` is a global provided by the Side Service runtime - unlike
// the watch side (which imports TransferFile from '@zos/ble/TransferFile'),
// that module path doesn't resolve here, so no import is needed or possible.

import { MessageBuilder } from '../shared/message-side'
import { SERVER_URL } from '../config'

// TransferFile gets a copy of the recording onto the phone, but its
// FileObject only exposes metadata (fileName/filePath/fileSize) - there's no
// documented way to read the actual bytes back out of it here. MessageBuilder
// sends the recording (already base64-encoded, see page/index.js) directly
// over its own BLE channel instead, so it arrives as a plain JSON payload
// that can be forwarded straight to fetch(). Runs alongside the existing
// TransferFile flow for now, not replacing it.
const messageBuilder = new MessageBuilder()

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

    messageBuilder.listen(() => {
      console.log('messageBuilder listening')
    })

    // 'call' fires for fire-and-forget sends (MessageBuilder#call on the
    // device side). fullPayload.payload arrives as a reassembled Buffer -
    // complete, regardless of how many BLE chunks it took to get here -
    // buf2Json decodes it back into the { fileName, audio, serverUrl }
    // object the watch sent.
    messageBuilder.on('call', (fullPayload) => {
      const { fileName, audio, serverUrl } = messageBuilder.buf2Json(fullPayload.payload)
      console.log('recording received:', fileName, audio.length)

      // serverUrl comes from the watch's last successful sync (see
      // utils/account.js on the device side) - it's null until the user has
      // configured and synced an account at least once, in which case
      // SERVER_URL (the bootstrap/account server from config.js) is used.
      fetch({
        url: (serverUrl || SERVER_URL) + '/messages',
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ fileName, audio }),
      })
        .then((res) => {
          const data = typeof res.body === 'string' ? JSON.parse(res.body) : res.body
          console.log('upload ok:', fileName, JSON.stringify(data))

          // Reports the result back to the watch - see the matching listener
          // in app.js for why (Side Service logs aren't visible anywhere on
          // the phone side, so this is the only way to actually see this
          // worked).
          messageBuilder.call({ uploadOk: true, fileName: fileName, bytesReceived: data.bytesReceived })
        })
        .catch((err) => {
          console.log('upload failed:', fileName, err)
          messageBuilder.call({ uploadOk: false, fileName: fileName, error: String(err) })
        })
    })

    // 'request' is MessageBuilder's other channel - unlike 'call' (fire and
    // forget), a request expects a response() back to the watch, which is
    // exactly what account registration and sync need. Both page/settings.js
    // calls (registerAccount, sync) land here, distinguished by `type`.
    messageBuilder.on('request', ({ request, response }) => {
      const { type, code } = messageBuilder.buf2Json(request.payload)
      console.log('account request:', type, code)

      const respondWithError = (err) => {
        console.log('account request failed:', type, err)
        response({ data: { ok: false, error: String(err) } })
      }

      if (type === 'registerAccount') {
        fetch({
          url: SERVER_URL + '/accounts',
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ code }),
        })
          .then((res) => {
            const data = typeof res.body === 'string' ? JSON.parse(res.body) : res.body
            response({ data })
          })
          .catch(respondWithError)
      } else if (type === 'sync') {
        fetch({ url: SERVER_URL + '/accounts/' + encodeURIComponent(code), method: 'GET' })
          .then((res) => {
            const data = typeof res.body === 'string' ? JSON.parse(res.body) : res.body
            response({ data })
          })
          .catch(respondWithError)
      } else {
        respondWithError('unknown request type: ' + type)
      }
    })
  },

  onRun() {},

  onDestroy() {},
})
