// Stands in for everything page/connection.js and page/server.js import, except the address rules (utils/serverUrl.js)
// and the texts (i18n), which are real.
export const widgets = []
export const widget = { TEXT: 'TEXT', BUTTON: 'BUTTON' }, align = { CENTER_H: 1, CENTER_V: 1 }, prop = { MORE: 'MORE' }, text_style = { WRAP: 1 }
export const px = (n) => n
export const createWidget = (type, opts) => { const w = { type, opts, text: opts.text, setProperty(_p, v) { if ('text' in v) w.text = v.text } }; widgets.push(w); return w }
export const calls = []   // router and queue calls, in order
export const replace = (o) => calls.push({ fn: 'replace', url: o.url, params: o.params })
export const back = () => calls.push({ fn: 'back' })
export const push = (o) => calls.push({ fn: 'push', url: o.url })

// utils/device.js
export const state = { paired: true, custom: false, label: null, failSave: false }
export const isPaired = () => state.paired
export const isCustomServer = () => state.custom
export const serverLabel = () => state.label
export const saveCustomServer = (o) => { if (state.failSave) throw new Error('disk full'); calls.push({ fn: 'saveCustomServer', ...o }) }
export const clearDevice = () => calls.push({ fn: 'clearDevice' })

// utils/storageState.js and utils/uploadQueue.js
export const clearStorageState = () => calls.push({ fn: 'clearStorageState' })
export let pending = 3
export const setPending = (n) => { pending = n }
export const pendingRecordings = () => Array.from({ length: pending })
export const retryPendingUploads = () => calls.push({ fn: 'retryPendingUploads' })
