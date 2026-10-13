export const widgets = []
export const widget = { TEXT: 'TEXT', BUTTON: 'BUTTON' }, align = { CENTER_H: 1, CENTER_V: 1 }, prop = { MORE: 'MORE' }, text_style = { WRAP: 1 }
export const px = (n) => n
export const createWidget = (type, opts) => { const w = { type, opts, text: opts.text, setProperty(_p, v) { if ('text' in v) w.text = v.text } }; widgets.push(w); return w }
export const pushed = []
export const push = (o) => { pushed.push(o) }
export let paired = false
export const setPaired = (v) => { paired = v }
export const isPaired = () => paired
export const getDeviceToken = () => (paired ? 'tok' : null)
export let recordingCount = 3
export const setRecordingCount = (n) => { recordingCount = n }
export const listRecordings = () => Array.from({ length: recordingCount })
export const calls = []
export let clearResult = { removed: 3, busy: false }
export const setClearResult = (r) => { clearResult = r }
export const clearAllRecordings = () => { calls.push('clearAllRecordings'); return clearResult }
