// Package audio converts the .opus recordings produced by the watch into
// playable .wav files.
//
// The watch's .opus files are NOT standard Ogg/Opus containers - they use a
// custom Zepp OS framing that no ordinary player or ffmpeg can open. See
// splitPackets for the layout.
package audio

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/opus"
)

// The watch records 16 kHz mono - fixed by its recorder, not configurable.
// Every packet is 20ms of SILK wideband audio (TOC byte 0x48).
const (
	SampleRate = 16000
	Channels   = 1

	// maxSamplesPerPacket is Opus's largest legal frame (120ms), which is the
	// most a single DecodeToInt16 call can produce. The watch only ever emits
	// 20ms frames, but sizing the scratch buffer to the format's maximum
	// means a firmware change can't overflow it.
	maxSamplesPerPacket = SampleRate / 1000 * 120

	// samplesPerPacket is what the watch actually produces, used only to
	// pre-size the output slice.
	samplesPerPacket = SampleRate / 1000 * 20
)

// packetHeaderSize is the prefix the watch writes before each Opus payload.
// The first 4 bytes are the payload length, big-endian; the remaining 4 vary
// per packet and have never been identified (they are not a checksum,
// timestamp, or sequence number in any pattern we could find). We skip them.
//
// NOTE: Zepp's own docs describe a 4-byte length PLUS an 8-byte header - a
// 12-byte prefix. That is wrong. Parsing real recordings that way overruns
// the file on the second packet; 8 bytes total is what actually works,
// verified across every recording we have.
const packetHeaderSize = 8

// maxPacketSize is a sanity bound, not a format limit. A real Opus packet can
// be larger, but the watch's 20ms wideband mono packets run 16-70 bytes in
// practice. A length wildly beyond that means we have lost framing alignment
// and are reading audio bytes as if they were a length - better to fail
// loudly than to hand back garbage that decodes into noise.
const maxPacketSize = 4096

// splitPackets splits a raw watch .opus file into its individual Opus packets.
//
// The file has no header of its own; it is just packets laid end to end:
//
//	┌──────────────┬──────────────┬────────────────────────┐
//	│ length (4B)  │ unknown (4B) │ payload (length bytes) │
//	│  big-endian  │   skipped    │    one Opus packet     │
//	└──────────────┴──────────────┴────────────────────────┘
//
// Because every packet declares its own length, a correctly-parsed file lands
// exactly on its own end - headers plus payloads sum to the file size with
// nothing left over. That property is what makes this step self-verifying,
// and it is what the tests assert.
//
// The returned packets alias data: they are subslices, not copies, so no
// payload bytes are duplicated. data must therefore stay alive and unmodified
// for as long as the returned packets are in use.
func splitPackets(data []byte) ([][]byte, error) {
	var packets [][]byte

	for pos := 0; pos < len(data); {
		// A full header has to be present before we can read a length from
		// it. binary.BigEndian.Uint32 panics on a short slice rather than
		// returning an error, so this check is load-bearing, not defensive.
		if pos+packetHeaderSize > len(data) {
			return nil, fmt.Errorf(
				"truncated header for packet %d at offset %d: %d bytes remain, need %d",
				len(packets), pos, len(data)-pos, packetHeaderSize)
		}

		payloadLen := int(binary.BigEndian.Uint32(data[pos:]))

		if payloadLen == 0 || payloadLen > maxPacketSize {
			return nil, fmt.Errorf(
				"implausible length %d for packet %d at offset %d: framing is misaligned",
				payloadLen, len(packets), pos)
		}

		start := pos + packetHeaderSize
		end := start + payloadLen

		if end > len(data) {
			return nil, fmt.Errorf(
				"packet %d at offset %d claims %d bytes but only %d remain",
				len(packets), pos, payloadLen, len(data)-start)
		}

		packets = append(packets, data[start:end])

		// end is already pos + header + payload - the next packet starts
		// exactly where this one stopped.
		pos = end
	}

	if len(packets) == 0 {
		return nil, fmt.Errorf("no packets found: file is empty or not in the watch's opus format")
	}

	return packets, nil
}

// decodePackets decodes Opus packets into a single run of 16-bit PCM samples.
//
// One decoder is created for the whole file rather than per packet: Opus is a
// stateful codec, and each frame's decoding depends on the state left by the
// previous one. Decoding packets with independent decoders would produce
// audible artefacts at every frame boundary.
func decodePackets(packets [][]byte) ([]int16, error) {
	dec, err := opus.NewDecoderWithOutput(SampleRate, Channels)
	if err != nil {
		return nil, fmt.Errorf("creating decoder for %d Hz / %d channel(s): %w", SampleRate, Channels, err)
	}

	// One scratch buffer, reused across every packet. DecodeToInt16 overwrites
	// the front of it and returns how many samples are actually valid, so only
	// out[:n] may ever be read - the rest is stale data from earlier packets.
	out := make([]int16, maxSamplesPerPacket)

	// Exact capacity for a well-formed watch recording: one allocation rather
	// than a dozen doubling reallocations.
	pcm := make([]int16, 0, len(packets)*samplesPerPacket)

	for i, pkt := range packets {
		if len(pkt) == 0 {
			return nil, fmt.Errorf("packet %d of %d is empty", i, len(packets))
		}

		n, err := dec.DecodeToInt16(pkt, out)
		if err != nil {
			return nil, fmt.Errorf("decoding packet %d of %d (%d bytes, TOC %#x): %w",
				i, len(packets), len(pkt), pkt[0], err)
		}

		// out[:n]... copies the values. Appending `out` itself would store an
		// alias of the shared scratch buffer, and every element would end up
		// holding the final packet's samples.
		pcm = append(pcm, out[:n]...)
	}

	return pcm, nil
}
