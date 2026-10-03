// Stands in for the platform and the app's own modules while page/pair.js is under test.
export const widgets = []
export const widget = { TEXT: 'TEXT', QRCODE: 'QRCODE', BUTTON: 'BUTTON' }, align = { CENTER_H: 1, CENTER_V: 1 }, prop = { MORE: 'MORE' }, text_style = { WRAP: 1 }
export const px = (n) => n
export const createWidget = (type, opts) => {
  const w = { type, opts, text: opts.text, setProperty(_p, v) { if ('text' in v) w.text = v.text } }
  widgets.push(w)
  return w
}
export const calls = []
export const back = () => { calls.push('back') }
export const info = { value: { deviceName: 'Amazfit Test' }, throws: false }
export const getDeviceInfo = () => { if (info.throws) throw new Error('no device info'); return info.value }
export const saved = []
export const saveDevice = (d) => { saved.push(d); calls.push('saveDevice') }
export const retryPendingUploads = () => { calls.push('retryPendingUploads') }
export const holds = []
export const holdScreen = (ms) => { holds.push(ms); calls.push('holdScreen') }
export const releaseScreen = () => { calls.push('releaseScreen') }
