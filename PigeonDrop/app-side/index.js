// Side Service: the phone-side counterpart of the watch app. It runs inside
// the Zepp App itself (not on the watch), has no UI, and is what actually
// receives the recordings the watch sends over BLE and forwards them to the
// server.

import { MessageBuilder } from '../shared/message-side'
import { SERVER_URL } from '../config'
import { handleUploadChunk } from './uploads'
import { startPairing, pollPairing } from './pairing'
import { checkServer } from './serverCheck'

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
    //   uploadChunk - one numbered piece of a recording (see ./uploads.js).
    //                 Recordings used to be sent as ONE fire-and-forget
    //                 call(); that overran BLE for anything beyond a few
    //                 seconds of audio, so the reply to each chunk is now
    //                 the flow control, and the reply to the last chunk
    //                 carries the server's result.
    //   pairStart   - page/pair.js asks for the codes of a new pairing (see ./pairing.js).
    //   pairPoll    - page/pair.js asks whether the pairing was approved yet.
    //   checkServer - the settings page's "Проверить связь": walks the way to the server (./serverCheck.js).
    //   ping        - the watch asks whether anybody is here before it starts an upload; any reply will do
    //                 (utils/uploadQueue.js), so that a phone that does not answer is found in seconds.
    messageBuilder.on('request', ({ request, response }) => {
      const payload = messageBuilder.buf2Json(request.payload)
      const { type } = payload

      const reply = (promise) =>
        promise.then((data) => response({ data })).catch((err) => {
          console.log('request failed:', type, err)
          response({ data: { ok: false, error: String(err) } })
        })

      if (type === 'uploadChunk') {
        reply(handleUploadChunk(payload, SERVER_URL))
      } else if (type === 'pairStart') {
        reply(startPairing(payload, SERVER_URL))
      } else if (type === 'pairPoll') {
        reply(pollPairing(payload, SERVER_URL))
      } else if (type === 'checkServer') {
        reply(checkServer(payload, SERVER_URL))
      } else if (type === 'ping') {
        reply(Promise.resolve({ ok: true }))
      } else {
        reply(Promise.reject('unknown request type: ' + type))
      }
    })
  },

  onRun() {},

  onDestroy() {},
})
