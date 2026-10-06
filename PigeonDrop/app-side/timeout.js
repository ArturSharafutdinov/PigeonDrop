// A deadline for a promise. fetch() in the Side Service has none of its own: with the phone's network half
// dead a request can stay pending for minutes, and the watch - which only sees "no reply" - could not tell
// that from a Side Service that has died. So every call to the server gets a deadline shorter than the
// watch's own timeout, and the phone answers "cannot reach the server" in time (see app-side/uploads.js and
// app-side/pairing.js). The request itself cannot be cancelled and may still finish later; its result is
// ignored (the server de-duplicates uploads, so a recording that arrives after all is harmless).

export function withTimeout(promise, ms) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('no answer in ' + Math.round(ms / 1000) + ' s')), ms)
    promise.then(
      (value) => {
        clearTimeout(timer)
        resolve(value)
      },
      (err) => {
        clearTimeout(timer)
        reject(err)
      },
    )
  })
}
