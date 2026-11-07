// Stands in for utils/device.js and config.js while utils/uploadQueue.js is under test.
export const device = { token: 'pdd_test_token_0123456789', cleared: 0, serverUrl: null, custom: false, tokenDropped: 0 }
export const getDeviceToken = () => device.token
export const isPaired = () => device.token !== null
export const clearDevice = () => { device.token = null; device.custom = false; device.serverUrl = null; device.cleared++ }
// A watch set up for the person's own server (utils/device.js): `custom` with an address, or with a damaged one (null).
export const isCustomServer = () => device.custom
export const getServerUrl = () => (device.custom ? device.serverUrl : null)
export const dropCustomToken = () => { if (device.custom) { device.token = null; device.tokenDropped++ } }
export const MAX_RECORDINGS = 20
export const MAX_PENDING_RECORDINGS = 50
