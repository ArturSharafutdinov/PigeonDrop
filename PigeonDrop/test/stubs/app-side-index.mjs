// Stands in for everything app-side/index.js imports except the choice of server (utils/serverUrl.js), which is real.
export const SERVER_URL = 'https://default.example'
export const log = []      // which part was called, and with which server address
export const handlers = {} // what the Side Service registered with the message channel
export class MessageBuilder {
  listen() {}
  on(name, fn) { handlers[name] = fn }
  buf2Json(x) { return x }
}
const call = (fn) => (payload, url) => { log.push({ fn, url }); return Promise.resolve({ ok: true, fn }) }
export const handleUploadChunk = call('uploadChunk')
export const startPairing = call('pairStart')
export const pollPairing = call('pairPoll')
export const checkServer = call('checkServer')
export const getServerConfig = () => { log.push({ fn: 'getServerConfig' }); return { ok: true, configured: false } }
