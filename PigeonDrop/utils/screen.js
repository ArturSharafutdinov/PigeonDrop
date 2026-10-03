// Keeps the display awake for a bounded time. By default Zepp OS puts the screen to sleep after ~10 s and
// then closes the Mini Program, which would end a screen that has to stay up (pairing waits for a person to
// use their phone). Three things can sleep the screen - the page timeout, lowering the wrist, and resting a
// palm on it - so all three are suspended, and releaseScreen() undoes them. page/index.js does the same for
// recording and uploading with its own bookkeeping; see there for why every hold must have a limit.

import {
  setPageBrightTime,
  resetPageBrightTime,
  pauseDropWristScreenOff,
  resetDropWristScreenOff,
  pausePalmScreenOff,
  resetPalmScreenOff,
} from '@zos/display'

const SLACK_MS = 5 * 1000

// Display calls return 0 on success. A failure does not stop what the caller is doing, but it is logged.
function check(name, result) {
  if (result !== 0) console.log('display call failed:', name, result)
}

export function holdScreen(durationMs) {
  releaseScreen()
  check('setPageBrightTime', setPageBrightTime({ brightTime: durationMs + SLACK_MS }))
  check('pauseDropWristScreenOff', pauseDropWristScreenOff({ duration: durationMs }))
  check('pausePalmScreenOff', pausePalmScreenOff({ duration: durationMs }))
}

export function releaseScreen() {
  resetPageBrightTime()
  resetDropWristScreenOff()
  resetPalmScreenOff()
}
