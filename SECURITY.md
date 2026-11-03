# Security policy

## Reporting a vulnerability

Please do not open a public issue for a security problem. Report it privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability** (private vulnerability reporting). Include what you found, how to reproduce it, the version or commit, and what an attacker could do with it.

You will get an answer within a week. This is a one-person project, so a fix can take longer; you will be told where it stands, and credited in the release notes unless you prefer not to be.

## Supported versions

Only the latest commit on `main` is supported. There are no maintained release branches yet.

## What is in scope

The code in this repository: the Go server (`backend/`), the pairing page it serves, the speech recognition service (`stt/`), the watch app and its Side Service (`PigeonDrop/`), and the deployment scripts (`.github/`, `tools/`). Issues in a dependency belong to that dependency, but tell me if PigeonDrop uses it in a way that makes it worse.

PigeonDrop is meant to be self-hosted. A public instance, if one exists, is invitation-only and is not a bug bounty target.

## How the project protects what it holds

So that you know what to test, and what to expect:

- **Accounts:** passwords are hashed with Argon2id (OWASP's minimum parameters, a dummy verification for unknown emails so that timing does not reveal them), registration needs an invitation code, sign-in is rate-limited. Sessions, device tokens, invitation codes and Telegram link codes are stored only as SHA-256 hashes; the session cookie is `__Host-`, `HttpOnly`, `Secure` and `SameSite=Lax`.
- **Watches** are paired with an account by the OAuth 2.0 device authorization grant (RFC 8628) and then carry a revocable device token; a revoked token is refused at once.
- **Web:** state-changing requests pass a cross-origin check (CSRF); the pairing page has a strict Content-Security-Policy (no inline code, no third-party hosts) and builds its page from text, never from markup.
- **Data:** every recording belongs to one account; files are stored under server-chosen names; uploads, recordings and archive downloads are size-limited; an account's storage is capped.
- **Speech recognition:** the service refuses requests without its token (compared in constant time), takes one recording at a time and runs in its own container with a memory limit.
- **Supply chain:** the dependencies are pinned and updated by Dependabot, `govulncheck`, `npm audit`, gitleaks and shellcheck run in CI, and the container images are pinned by digest.

Audio recordings and transcripts are personal data. They stay on the server you run, except that a voice message and its transcript are sent to Telegram when you link a Telegram chat.
