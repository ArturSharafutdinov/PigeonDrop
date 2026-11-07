# Personal Server: sending recordings to a server of your own

By default the watch sends its recordings to the PigeonDrop server it was paired with. A **Personal Server** is a server you run yourself that receives the recordings instead. It only receives them: speech recognition, the Telegram bot and the page with your recordings belong to the PigeonDrop service and need an account there.

This page is for two readers: the person who wants to point their watch at their own server, and the author of a server that can receive the recordings (the contract is in [What a server must do](#what-a-server-must-do)).

## Setting a watch up

There are two ways to give the watch the address and the token. Both end the same way: the watch shows the host and asks before it uses it.

**Through the Zepp app.** Nothing passes through the PigeonDrop server.

1. Make a **token** on your server for the watch: one that can only add recordings and can be revoked. Anyone who has the token can add recordings to your server, so do not reuse a token that can do more.
2. In the Zepp app on the phone, open the settings of PigeonDrop and fill in **Server address** (`https://…`, see [Addresses](#addresses)) and **Access token**. The token is shown while you type it. Nothing is sent anywhere from this screen; the values stay in the Zepp app on the phone.
3. On the watch open **Settings → Connection → Personal Server**. The watch reads the values from the phone and shows the host the recordings would go to, and how many are waiting. Press **Use this server** after reading the host.

**Through the pairing page.** For a person who would rather type on the page than in the Zepp app. It needs no account.

1. On the watch open **Settings → Connection → Pair via QR code** and scan the QR code with the phone. The page opens.
2. Choose the **Personal server** tab (next to *Sign in* and *Register*), enter the address and the token (same rules, see [Addresses](#addresses)) and press **Send to watch**. The page shows the address in large type.
3. Within a few seconds the watch shows the host and the number of waiting recordings. Compare the host with the page and press **Use this server**. If it is wrong, press **Cancel** and start again from the QR code.

The tab works only when the page was opened from the watch's QR code, because the code carries the key that protects what you type. A watch that is too old, or one that cannot do the cryptography, shows a plain QR code, and the tab says so; then use the Zepp app.

Recordings that were already waiting on the watch are sent to the new server. To go back, **Settings → Connection → Back to PigeonDrop**; the watch then has to be paired again (**Pair via QR code**), and pairing with a PigeonDrop account also ends a Personal Server setup.

The watch never switches back to the PigeonDrop server by itself. If your server refuses the token (it was revoked), the watch forgets the token, keeps your address, says that the server refused the token, and keeps the recordings until you give it a new one.

## Addresses

Only plain `https://host[:port][/path]` addresses are accepted: no `http://`, no user name or password in the address, no query (`?…`), no fragment (`#…`), no IPv6 address, no percent-encoding. A server may live under a path (`https://example.com/pigeondrop`). The host must be a name or an IPv4 address, and the certificate has to be one the phone trusts. Whether the phone's Zepp app would also accept `http://` or a self-signed certificate has **not been tested**, which is why they are refused. The cases that decide what is accepted are in [`PigeonDrop/test/fixtures/server-url-cases.json`](../PigeonDrop/test/fixtures/server-url-cases.json).

## What a server must do

The watch and the phone's Side Service speak to the server in the way described in [HTTP API](api.md), "Uploading a recording". A Personal Server needs only that part: **no pairing, no `/healthz`**.

1. **`POST {address}/v1/messages`** with `Content-Type: application/json`, the header `Authorization: Bearer <token>` and the body `{ "messageId": "<the file name on the watch>", "audio": "<base64 of a standard Ogg/Opus file>" }`. The format of the file is in [HTTP API](api.md) (mono, 16 kHz, SILK packets as recorded, 50 packets per page).
2. **Check the token first, then the body.** A wrong token gets `401 {"ok": false, "code": "unauthorized"}`. A correct token with a body that is empty or incomplete (for example `{}`) gets `400 {"ok": false, "code": "invalid_request"}` and **nothing is stored**. The *Check connection* button of the watch relies on exactly this.
3. **Success is `200 {"ok": true}`.** Further fields are optional: `id`, `bytesReceived`, `duplicate`, and `storage` (`{ "state": "ok" | "nearly_full" | "full", "usedBytes": …, "limitBytes": … }`, which the watch turns into a warning).
4. **Be idempotent by `messageId` for one token.** The same `messageId` again must not store a second recording and must answer `200`. The watch repeats a recording whenever the confirmation was lost on the way back.
5. **Answer with the codes the watch understands.**

   | Answer | What the watch does |
   |---|---|
   | `200` with `"ok": true` | marks the recording as sent |
   | `401` | forgets the token, keeps the address, keeps the recordings, tells the person |
   | `413` | skips **this** recording and goes on with the next ones |
   | `429` | stops sending and tries again at the next start, connection or recording |
   | `502`, `503`, `504` | counts the server as unreachable and tries again later |
   | anything else (`400` for a good recording, `404`, `500`, …) | **stops the queue**: the recording stays and is tried again at the next start, and everything behind it waits |

   So a recording your server will never accept must be answered `413`, not `400`.
6. **Answer within 30 seconds** (the phone gives up then and the watch repeats the recording, which is why step 4 matters). Do heavy work after answering.
7. The body can be up to about 770 KB (a recording of up to 512 KiB, in base64). A minute of audio is about 140 KiB.

A server that follows this passes the *Check connection* button of the watch. A conformance check as a script is not written yet.

## What is protected, and what is not

**On the pairing page.** The address and the token are encrypted in your browser before anything is sent, with a key that only your watch has: the watch makes it for every attempt and puts it in the QR code after the `#`, and a browser never sends that part of an address to anybody. The PigeonDrop server only passes the encrypted message on: it keeps it in memory (never in the database, the logs or backups) for at most 10 minutes and deletes it when the watch has picked it up. In your browser's developer tools (Network tab) the request contains only encrypted data, and the page keeps nothing of what you type (no storage, no address bar, no console). The format is in [Architecture](architecture.md).

What this cannot protect against: if the server or the page were tampered with, it could serve code that reads what you type before it is encrypted. The code is open, but you cannot see what is actually running on the server. If you do not want to trust the server with this, use the Zepp app instead; then nothing passes through it.

**In the Zepp app.** The address and token you type in the Zepp app are stored by the Zepp app on your phone, in plain text, until the watch asks for them; the watch keeps its own copy in its private storage, as it keeps the PigeonDrop token. They are not sent to the PigeonDrop server.

**In both cases.**

- The watch shows the host and waits for your confirmation before it starts sending anywhere new.
- The recordings travel from the watch to the phone over Bluetooth and from the phone to your server over HTTPS. Nobody at PigeonDrop sees them when they go to your server.

## Not verified

- The pairing page's way has not run on a real watch or phone. In particular: that the watch's cryptography (`@zos/crypto`) gives what its documentation says (the watch tests itself on a known message every time it makes the QR code, and shows the plain code if the test fails), that the keys it generates are random enough to be the secret, that a QR code with the extra key still scans from the round display and that phone scanners keep the part after the `#`, and the new texts on the screens. The page itself was tried in a browser against a server on this computer, and the encrypted result was opened with a separate implementation of the watch's side.
- The page says to enter the settings in the Zepp app instead; that route is itself unverified (the next point).
- The settings screen in the Zepp app, and that the phone's Zepp app hands the values to the watch's Side Service, have been built but **not tried on a real phone or watch**. It is not known where the Zepp app shows the settings of a side-loaded app, nor whether the values survive a restart of the Zepp app.
- The wording on the watch screens has not been looked at on the round display.
- The settings screen is in English only.
