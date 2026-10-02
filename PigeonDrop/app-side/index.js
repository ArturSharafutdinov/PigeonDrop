// Side Service: the phone-side counterpart of the watch app. It runs inside
// the Zepp App itself (not on the watch), has no UI, and is what actually
// receives the recordings the watch sends over BLE and forwards them to the
// server.

import { MessageBuilder } from '../shared/message-side'
import { SERVER_URL } from '../config'
import { handleUploadChunk } from './uploads'

// This used to run alongside a parallel @zos/ble/TransferFile receive path
// (global `transferFile`), removed after it was decided its FileObject - only
// exposing metadata, never the actual bytes - never did anything beyond
// silently filling up phone storage.
const messageBuilder = new MessageBuilder()

AppSideService({
  onInit() {
    messageBuilder.listen(() => {
      console.log('messageBuilder listening')
    })

    // 'request' is MessageBuilder's request/response channel: the watch sends a
    // request() and waits for the response() sent from here. Everything the
    // watch asks of the phone goes through it, distinguished by `type`:
    //   uploadChunk     - one numbered piece of a recording (see ./uploads.js).
    //                     Recordings used to be sent as ONE fire-and-forget
    //                     call(); that overran BLE for anything beyond a few
    //                     seconds of audio, so the reply to each chunk is now
    //                     the flow control, and the reply to the last chunk
    //                     carries the server's result.
    //   registerAccount - page/settings.js
    //   sync            - page/settings.js
    messageBuilder.on('request', ({ request, response }) => {
      const payload = messageBuilder.buf2Json(request.payload)
      const { type, code } = payload

      const respondWithError = (err) => {
        console.log('request failed:', type, err)
        response({ data: { ok: false, error: String(err) } })
      }

      if (type === 'uploadChunk') {
        handleUploadChunk(payload, SERVER_URL)
          .then((data) => response({ data }))
          .catch(respondWithError)
        return
      }

      console.log('account request:', type, code)

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
