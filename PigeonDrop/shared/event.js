// Copyright (c) 2021-present Zepp Health. All Rights Reserved.
// Licensed under the Apache License, Version 2.0; see ./LICENSE and ../../THIRD_PARTY_NOTICES.md.
// Vendored unmodified from https://github.com/zepp-health/zeppos-samples (application/2.0/todo-list/shared/event.js).

export class EventBus {
  constructor() {
    this.map = new Map()
  }

  on(type, cb) {
    if (this.map.has(type)) {
      this.map.get(type).push(cb)
    } else {
      this.map.set(type, [cb])
    }
  }

  off(type, cb) {
    if (type) {
      if (cb) {
        const cbs = this.map.get(type)

        if (!cbs) return
        const index = cbs.findIndex((i) => i === cb)

        if (index >= 0) {
          cbs.splice(index, 1)
        }
      } else {
        this.map.delete(type)
      }
    } else {
      this.map.clear()
    }
  }

  emit(type, ...args) {
    for (let cb of this.map.get(type) ? this.map.get(type) : []) {
      cb && cb(...args)
    }
  }

  count(type) {
    return this.map.get(type) ? this.map.get(type).length : 0
  }
}
