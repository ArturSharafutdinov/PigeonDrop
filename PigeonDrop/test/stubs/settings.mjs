// A stand-in for @zos/settings. The tests of the screens run in Russian unless a test says otherwise (they check
// the Russian texts); test/i18n.test.mjs switches to English with setLanguage().
export const RUSSIAN = 4, ENGLISH = 2, GERMAN = 7   // values of getLanguage(), from the Multilingual Mapping of the Zepp OS docs
let language = RUSSIAN
export const setLanguage = (code) => { language = code }
export const getLanguage = () => language
