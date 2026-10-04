// Stands in for the platform and the app's own modules while page/storage.js is under test.
export const widgets = []
export const widget = { TEXT: 'TEXT' }, align = { CENTER_H: 1, CENTER_V: 1 }, text_style = { WRAP: 1 }
export const px = (n) => n
export const createWidget = (type, opts) => { const w = { type, ...opts }; widgets.push(w); return w }
export const calls = []
export const nav = { backThrows: false }
export const back = () => { calls.push('back'); if (nav.backThrows) throw new Error('no page to go back to') }
export const VIBRATOR_SCENE_DURATION_LONG = 1000
export const vib = { instances: [], failOn: null }    // failOn: 'new' | 'setMode' | 'start' | 'stop'
export class Vibrator {
  constructor() { if (vib.failOn === 'new') throw new Error('no vibrator'); this.log = []; vib.instances.push(this) }
  setMode(o) { if (vib.failOn === 'setMode') throw new Error('bad mode'); this.log.push('setMode ' + o.mode) }
  start() { if (vib.failOn === 'start') throw new Error('cannot vibrate'); this.log.push('start') }
  stop() { if (vib.failOn === 'stop') throw new Error('cannot stop'); this.log.push('stop') }
}
export const store = { state: null }
export const getStorageState = () => store.state
export const holds = []
export const holdScreen = (ms) => { holds.push(ms); calls.push('holdScreen') }
