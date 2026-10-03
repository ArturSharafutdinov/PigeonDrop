// Stands in for utils/device.js and config.js while utils/uploadQueue.js is under test.
export const device = { token: 'pdd_test_token_0123456789', cleared: 0 }
export const getDeviceToken = () => device.token
export const isPaired = () => device.token !== null
export const clearDevice = () => { device.token = null; device.cleared++ }
export const MAX_RECORDINGS = 20
export const MAX_PENDING_RECORDINGS = 50
