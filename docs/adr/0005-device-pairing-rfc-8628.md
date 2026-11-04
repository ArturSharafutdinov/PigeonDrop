# 0005. Watches are paired with RFC 8628 and carry a revocable device token

Status: accepted, 2026-10-03.

## Context

Originally a watch named itself with a "code": an identifier, not a secret, and the server accepted recordings from anyone. With accounts, a watch has to prove whose it is. A watch has no keyboard, a tiny screen and no browser; the person has a phone with both.

## Decision

Pairing follows **RFC 8628, the OAuth 2.0 device authorization grant**. The watch (through the phone's Side Service) asks the server for a pair of codes and shows a **QR code** for `<server>/pair?code=...` plus a short user code. The person opens the page on the phone, signs in, **compares the code with the watch's screen** and approves. The watch polls and, on approval, receives a **device token once**. Every upload carries it as a bearer token.

- The device code is a secret (43 characters); the user code is 8 consonants out of 20 (no vowels, no 0/O or 1/I confusion), 34 bits, safe because it lives 10 minutes and wrong guesses are limited.
- Only SHA-256 hashes of tokens and codes are stored. The token is shown once; if the answer is lost the pairing starts over and the stray device can be revoked.
- A device is **revoked** from the web page; its next upload gets `401`, the watch forgets the token and the screen says so, and the recordings stay queued for the next pairing.
- Rate limits apply at every step: issuing codes per address, polling faster than every 3 seconds, wrong codes per user.

## Consequences

- Nothing on the watch needs a password; an unpaired watch simply uploads nothing and says why.
- Showing the code large and asking for a comparison is what defends against a swapped link.
- A "bring your own server" mode was dropped with the old per-watch server address; if it returns it will be an account setting, not a watch setting.
- Sign-in is by invitation, e-mail and password (Argon2id) with cookie sessions; no third-party identity provider, which keeps a young dependency out of the trust path.
