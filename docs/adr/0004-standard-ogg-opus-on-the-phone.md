# 0004. The phone repackages Zepp's Opus into a standard Ogg/Opus file

Status: accepted, 2026-10-02 (after a spike in August).

## Context

The watch's recorder writes its own framing: `[8-byte header][Opus packet]` back to back, where the first four header bytes are the payload length (big endian) and the other four are not understood. Zepp's own guide describes a 12-byte prefix, which does not parse on real recordings. The payloads themselves are standard: SILK wideband, mono, 20 ms per packet. Ordinary players and `ffmpeg` cannot open this.

Other consumers want a standard file: a self-hosted server a user may bring, and Telegram, which plays a voice message only from Ogg/Opus. If the watch sent the private format every consumer would need its own parser.

## Decision

The **phone's Side Service wraps the packets, unchanged and without re-encoding, into an Ogg/Opus file** (RFC 3533 and 7845; `OpusHead`, `OpusTags`, 50 packets per page, pre-skip 0) before posting. A recording cut off in the middle of a packet is trimmed to its whole packets. The server's reader (`internal/audio`) still reads both formats.

The packing happens after all chunks arrived, on the phone: the watch has neither room nor need for it.

## Consequences

- Lossless and cheap: the output carries exactly the input's packets.
- Verified against an independent Ogg reader written from the RFCs (it shares no code with the packer or the server) and against `ffmpeg`'s strict decoder; Telegram plays the result as a voice message with its duration (checked on the real Telegram).
- **Not measured:** the time and memory of the packing on a real phone for a 60 s recording (a 2 s one was), and Android (developed against an iPhone).
- The 60 s limit on a recording is partly this: about 210 KB as base64 in the Side Service's memory.
