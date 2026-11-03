// A stand-in for @zos/media, for utils/player.js. Like the hardware, it refuses a second Player.
export const id = { PLAYER: 'PLY' }
export const log = []                       // ordered record of every call on the Player
export const created = []                   // every Player that was created

export const create = () => {
  if (created.length > 0) throw new TypeError("cannot read property 'addEventListener' of undefined")
  const listeners = {}
  const player = {
    event: { PREPARE: 'PREPARE', COMPLETE: 'COMPLETE' },
    source: { FILE: 'FILE' },
    addEventListener: (event, fn) => { (listeners[event] = listeners[event] || []).push(fn); log.push('listen ' + event) },
    setSource: (kind, options) => log.push('setSource ' + options.file),
    prepare: () => log.push('prepare'),
    start: () => log.push('start'),
    stop: () => log.push('stop'),
    emit: (event, result) => (listeners[event] || []).forEach((fn) => fn(result)),   // what the firmware does
  }
  created.push(player)
  return player
}
