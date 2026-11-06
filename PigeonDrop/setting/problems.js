// What the Settings App (setting/index.js) says about the address and token the person typed. Kept apart from the
// screen because the screen is built from the Zepp app's own widgets and cannot be tried outside it; these are plain
// functions of the text, tested in test/settings-app.test.mjs. The rules are the ones the watch applies, from
// utils/serverUrl.js, so what is accepted here is accepted there.
//
// Each function returns a sentence for the person, or null when there is nothing to say (including when the field is empty:
// nobody is told off before they have typed).

import { normalizeServerUrl, isUsableToken } from '../utils/serverUrl'

const ADDRESS_PROBLEMS = {
  not_text: 'Enter the address of your server.',
  empty: 'Enter the address of your server.',
  too_long: 'The address is too long (200 characters at most).',
  bad_characters: 'The address must not contain spaces.',
  no_scheme: 'The address must start with https://',
  not_https: 'Only https:// addresses are accepted.',
  has_query: 'Leave out everything after a ? in the address.',
  has_fragment: 'Leave out everything after a # in the address.',
  has_credentials: 'Do not put a user name or password in the address.',
  bad_host: 'The part after https:// is not a valid server name.',
  bad_port: 'The port must be a number from 1 to 65535.',
  bad_path: 'The path may only contain letters, digits and . _ ~ - between slashes.',
}

export function addressProblem(text) {
  if (typeof text !== 'string' || text.trim() === '') return null
  const checked = normalizeServerUrl(text)
  return checked.ok ? null : ADDRESS_PROBLEMS[checked.reason] || 'This address cannot be used.'
}

export function tokenProblem(text) {
  if (typeof text !== 'string' || text.trim() === '') return null
  return isUsableToken(text.trim()) ? null : 'The token must be 1 to 256 characters without spaces.'
}

// What the form shows under the fields once both are valid: where to go next.
export function isReady(address, token) {
  return typeof address === 'string' && typeof token === 'string' && address.trim() !== '' && token.trim() !== '' && addressProblem(address) === null && tokenProblem(token) === null
}
