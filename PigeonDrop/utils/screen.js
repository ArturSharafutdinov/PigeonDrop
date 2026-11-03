// Keeps the display awake for a bounded time. By default Zepp OS puts the screen to sleep after ~10 s and
// then closes the Mini Program, which would end a screen that has to stay up: a long recording, an upload
// still in progress, or pairing, which waits for a person to use their phone. Three things can sleep the
// screen - the page timeout, lowering the wrist, and resting a palm on it - so all three are suspended, and
// releaseScreen() undoes them. Every hold has a duration, so nothing can keep the screen on (and drain the
// battery) forever; holding again replaces the hold that was active.

import {
  setPageBrightTime,
  resetPageBrightTime,
  pauseDropWristScreenOff,
  resetDropWristScreenOff,
  pausePalmScreenOff,
  resetPalmScreenOff,
} from '@zos/display'

// Extra time on the page-brightness timeout beyond a hold: the recording stopwatch ticks once a second,
// so an auto-stop can land a moment after the recording limit.
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
