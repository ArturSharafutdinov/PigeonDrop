// Audio playback shared by every page that plays a recording.
//
// The hardware allows one live Player at a time: a second create(id.PLAYER) throws
// "cannot read property 'addEventListener' of undefined", and the API has no way to
// release or remove anything. Every page is built into its own bundle, so a module-level
// variable would be a separate copy per page and could not be shared; the one Player is
// therefore kept in the app's global data (the same place the BLE connection lives, see
// app.js), created on the first playback and reused for the rest of the app's life.
//
// A Player cannot have its listeners removed either, so they are added once, here, and
// forward to the callbacks of the page that started the current playback.

import { create, id } from '@zos/media'

const NO_CALLBACKS = {}

function sharedAudio() {
  const globals = getApp()._options.globalData

  if (!globals.audio) {
    const player = create(id.PLAYER)
    const audio = { player, callbacks: NO_CALLBACKS }

    player.addEventListener(player.event.PREPARE, (result) => {
      if (result) {
        player.start()
      } else if (audio.callbacks.onError) {
        audio.callbacks.onError()
      }
    })

    player.addEventListener(player.event.COMPLETE, () => {
      player.stop()
      if (audio.callbacks.onComplete) audio.callbacks.onComplete()
    })

    globals.audio = audio
  }

  return globals.audio
}

// Points the shared Player at a file and starts the prepare/play flow. `callbacks` belong to
// the calling page and replace those of whichever page played before:
//   onError()    the file could not be prepared (unreadable or in an unsupported format)
//   onComplete() playback reached the end
// Pass the same object to detachPlayback() when the page goes away.
export function playAudio(filePath, callbacks) {
  const audio = sharedAudio()
  audio.callbacks = callbacks || NO_CALLBACKS

  const player = audio.player
  player.setSource(player.source.FILE, { file: filePath })
  player.prepare()
}

// Stops forwarding player events to a page that is being destroyed, so that a late event
// cannot touch widgets that no longer exist. Does nothing if another page has taken over,
// or if nothing has been played yet. Playback itself is left alone.
export function detachPlayback(callbacks) {
  const audio = getApp()._options.globalData.audio
  if (audio && callbacks && audio.callbacks === callbacks) audio.callbacks = NO_CALLBACKS
}
