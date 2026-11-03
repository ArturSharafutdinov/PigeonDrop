export const log = []                       // ordered record of every platform call
export const texts = []                     // every status text shown
export let displayResult = 0                // flip to non-zero to simulate a failing display call
export const setDisplayResult = (v) => { displayResult = v }
export const widget = { TEXT: 'TEXT', BUTTON: 'BUTTON' }, align = { CENTER_H: 1, CENTER_V: 1 }, prop = { MORE: 'MORE', VISIBLE: 'VISIBLE' }, text_style = { WRAP: 1 }
export const px = (n) => n
export const KEY_SHORTCUT = 'S', KEY_EVENT_CLICK = 'C', KEY_EVENT_LONG_PRESS = 'L'
export const id = { RECORDER: 'REC', PLAYER: 'PLY' }, codec = { OPUS: 'OPUS' }
export const created = []                   // the options of every widget created
export const widgets = []                   // the same widgets, with whether each is visible now
export const createWidget = (type, opts) => {
  created.push({ type, ...opts })
  const w = { type, opts, visible: true, setProperty: (p, v) => { if (p === 'VISIBLE') w.visible = v; else texts.push(v.text) } }
  widgets.push(w)
  return w
}
export const onKey = () => {}
export const push = (o) => log.push('push ' + o.url + ' ' + (o.params || ''))
export const create = (what) => ({ setFormat: () => log.push('recorder.setFormat'), start: () => log.push('recorder.start'), stop: () => log.push('recorder.stop') })
export const setPageBrightTime = (o) => { log.push('setPageBrightTime ' + o.brightTime); return displayResult }
export const resetPageBrightTime = () => { log.push('resetPageBrightTime'); return 0 }
export const pauseDropWristScreenOff = (o) => { log.push('pauseDropWristScreenOff ' + o.duration); return displayResult }
export const resetDropWristScreenOff = () => { log.push('resetDropWristScreenOff'); return 0 }
export const pausePalmScreenOff = (o) => { log.push('pausePalmScreenOff ' + o.duration); return displayResult }
export const resetPalmScreenOff = () => { log.push('resetPalmScreenOff'); return 0 }
export const newRecordingPath = () => 'data://voice_messages/voice_1.opus'
export let discardWorks = true
export const setDiscardWorks = (v) => { discardWorks = v }
export const discardRecording = (name) => { log.push('discard ' + name); return discardWorks }
export const listRecordings = () => [{ fileName: 'voice_1.opus', filePath: 'x' }]
export const pruneRecordings = () => log.push('pruneRecordings')
export const retryPendingUploads = () => log.push('retryPendingUploads')
export let pendingCount = 1
export const setPending = (n) => { pendingCount = n }
export const pendingRecordings = () => Array.from({ length: pendingCount }, (_, i) => ({ fileName: 'voice_' + (i + 1) + '.opus' }))
export const setActiveRecording = (n) => log.push('setActive ' + n)
export let listener = null
export const onUploadState = (cb) => { listener = cb; log.push('onUploadState ' + (cb ? 'set' : 'null')) }
export const MAX_RECORDING_SECONDS = 5
export let paired = true
export const setPaired = (v) => { paired = v }
export const isPaired = () => paired
export const playback = []                  // every playAudio() call: { file, callbacks } (not in `log`, which some tests compare exactly)
export const detached = []                  // the callbacks every detachPlayback() call was given
export const playAudio = (file, callbacks) => { playback.push({ file, callbacks }) }
export const detachPlayback = (callbacks) => { detached.push(callbacks) }
