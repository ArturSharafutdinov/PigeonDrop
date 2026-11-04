## What and why

<!-- What does this change, and what problem does it solve? Link the issue if there is one. -->

## How it was verified

<!-- Commands you ran, what you looked at. For the watch app say whether you tried it on a real watch. -->

## Not verified

<!-- Anything you could not check, and anything the reviewer should look at first. -->

## Checklist

- [ ] The tests pass (`make check` in `backend/`, `npm test` in `PigeonDrop/`, the `stt` tests if `stt/` changed)
- [ ] New behaviour has a test; a bug fix has a test that fails without the fix
- [ ] Code, comments, docs and commit messages are in English; texts shown to people are in the language tables (`PigeonDrop/i18n/`, `pair.*.json`, `backend/internal/telegram/texts.go`)
- [ ] Generated code is regenerated (`make generate`), not edited by hand
