// Side Service: the phone-side counterpart of the watch app. It runs inside
// the Zepp App itself (not on the watch), has no UI, and is what actually
// receives the recordings the watch sends over BLE and forwards them to the
// server.

import { MessageBuilder } from '../shared/message-side'
import { SERVER_URL } from '../config'
import { handleUploadChunk } from './uploads'
import { startPairing, pollPairing } from './pairing'
import { checkServer } from './serverCheck'
import { resolveServer } from '../utils/serverUrl'

const messageBuilder = new MessageBuilder()

AppSideService({
  onInit() {
    // Nothing to do once listening: Side Service console output is never visible
    // on the phone, so the only way to report anything is a reply to the watch.
    messageBuilder.listen(() => {})

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
    //   checkServer - the settings page's "Check connection": walks the way to the server (./serverCheck.js).
    //                 uploadChunk and checkServer may carry the watch's `serverUrl` (a server of the person's own).
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

      // Recordings and the connection check go to the server the watch names: its own `serverUrl` when the person set
      // the watch up for a server of their own, the PigeonDrop server of this build otherwise. An address that cannot
      // be used is an error and never the default: see resolveServer. Pairing is always with the PigeonDrop server.
      if (type === 'uploadChunk') {
        const target = resolveServer(payload, SERVER_URL)
        reply(target.ok ? handleUploadChunk(payload, target.url) : Promise.resolve({ ok: false, error: target.error }))
      } else if (type === 'pairStart') {
        reply(startPairing(payload, SERVER_URL))
      } else if (type === 'pairPoll') {
        reply(pollPairing(payload, SERVER_URL))
      } else if (type === 'checkServer') {
        const target = resolveServer(payload, SERVER_URL)
        reply(target.ok ? checkServer(payload, target.url) : Promise.resolve({ ok: false, stage: 'server', error: target.error }))
      } else if (type === 'ping') {
        reply(Promise.resolve({ ok: true }))
      } else {
        reply(Promise.reject('unknown request type: ' + type))
      }
    })
  },

  // Lifecycle hooks of the Side Service. There is nothing to start or tear down here:
  // every request is self-contained and the partial uploads in app-side/uploads.js
  // are dropped by their own time-to-live.
  onRun() {},

  onDestroy() {},
})
