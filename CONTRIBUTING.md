# Contributing to PigeonDrop

Thanks for looking. PigeonDrop is a small project with one maintainer, so the rules below are short; they are there to keep changes easy to review.

## Language

Code, comments, documentation, commit messages, issues and pull requests are in **English**. Russian is a language of the product, not of the project: it appears only as a second interface language (the watch, the pairing page and the Telegram bot) and in test data that checks Unicode handling. Every text a person reads comes from a language table, never from the code:

| Where | Tables |
|---|---|
| Watch app | `PigeonDrop/i18n/en.js`, `ru.js` |
| Pairing page | `backend/internal/httpapi/pair.en.json`, `pair.ru.json` |
| Telegram bot | `backend/internal/telegram/texts.go` |

To add a language, add a table next to each of these (tests check that the tables agree on their keys and placeholders). The watch picks its language from the system setting; unknown languages get English.

## Set up and check

You need Go 1.26 (the `toolchain` line in `backend/go.mod` pins the patch release), PostgreSQL server binaries for the backend tests, Node.js 22+ for the watch app tests and Docker for the images. Python 3 with nothing installed runs the speech service's tests.

```bash
# Backend (Go)
cd backend
scripts/dev-db.sh up        # a throwaway PostgreSQL on 127.0.0.1:55432, data in backend/.dev
make check                  # gofmt, vet, tests with the race detector, generated code is up to date
make lint                   # golangci-lint (CI runs the same version)
make vuln                   # govulncheck
make generate               # after editing api/openapi.yaml or internal/db/queries/*.sql

# Watch app (JavaScript, Zepp OS 4.0)
cd PigeonDrop
npm test                    # plain node:test on stubs of the Zepp OS modules, nothing to install

# Speech recognition service (Python)
cd stt
python3 -m unittest discover -s tests -t .
```

Do not edit generated code (`backend/internal/api/api.gen.go`, `backend/internal/db/dbgen/`): change `api/openapi.yaml` or the SQL in `internal/db/queries` and run `make generate`. CI fails if it is stale.

The watch app cannot be tried without a watch or the Zepp OS simulator: the tests run on stubs, so a change to screens, Bluetooth, files or memory needs a real run (`zeus dev`, or `zeus preview` for a device). Say in the pull request what you tried and what you could not. `.claude/skills/zeppos/references/gotchas.md` lists where the real devices differ from the documentation.

## Changes

- **Branches and pull requests.** Work on a branch, open a pull request against `main`, keep it to one subject. Nothing is pushed to `main` directly.
- **Commit messages** are short and in the imperative: a subject line that says what was done, and, if needed, a line or two on why.
- **Pull request text** says what changed and why, how you verified it and what you did not verify (the template asks for exactly that).
- **Tests.** New behaviour comes with a test; a bug fix comes with a test that fails without the fix.
- **Comments** explain why, not what. A comment that states a fact about a device or a service should say how you know it.
- **Dependencies** are pinned and updated by Dependabot; a new one needs a reason, and its license has to be compatible with MIT (add it to `THIRD_PARTY_NOTICES.md`).

## Security

Please report vulnerabilities privately, see [SECURITY.md](SECURITY.md).
