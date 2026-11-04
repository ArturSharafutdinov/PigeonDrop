// Copyright (c) 2021-present Zepp Health. All Rights Reserved.
// Licensed under the Apache License, Version 2.0; see ./LICENSE and ../../THIRD_PARTY_NOTICES.md.
// Vendored unmodified from https://github.com/zepp-health/zeppos-samples (application/2.0/todo-list/shared/defer.js).

export function Deferred() {
  const defer = {}

  defer.promise = new Promise(function (resolve, reject) {
    defer.resolve = resolve
    defer.reject = reject
  })

  return defer
}

export function delay(ms) {
  const defer = Deferred()

  setTimeout(defer.resolve, ms)

  return defer.promise
}

export function timeout(ms, cb) {
  const defer = Deferred()
  ms = ms || 1000

  const wait = setTimeout(() => {
    clearTimeout(wait)

    if (cb) {
      cb && cb(defer.resolve, defer.reject)
    } else {
      defer.reject('Timed out in ' + ms + 'ms.')
    }
  }, ms)

  return defer.promise
}
