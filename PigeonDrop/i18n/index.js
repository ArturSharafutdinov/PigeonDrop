// The texts of the watch app in the language of the watch: Russian for a watch set to Russian, English for all others.
//
// Plain JavaScript tables rather than Zepp OS's .po files (@zos/i18n getText): the texts are broken into lines by hand for
// the round display, and how the .po compiler treats a line break is not documented and cannot be tried without a watch.
// Tables also let the tests read both languages (test/i18n.test.mjs).

import { getLanguage } from '@zos/settings'
import en from './en'
import ru from './ru'

const RUSSIAN = 4 // what getLanguage() returns for ru-RU (the "Multilingual Mapping" in the Zepp OS documentation)

// The text for `key` in the watch's language. {name} placeholders are filled from `values`; one with no value is left
// as it is. A key the Russian table lacks falls back to English, and an unknown key is returned as it is, so a missing
// text shows up as its name instead of as a blank.
export function t(key, values) {
  let text = getLanguage() === RUSSIAN ? ru[key] : undefined
  if (text === undefined) text = en[key]
  if (text === undefined) return key
  if (!values) return text
  return text.replace(/\{(\w+)\}/g, (placeholder, name) => (name in values ? String(values[name]) : placeholder))
}
