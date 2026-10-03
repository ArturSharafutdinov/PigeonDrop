// Package audio turns what the watch sends into something playable.
//
// A recording is a sequence of Opus packets (SILK wideband, mono, 20 ms each) that arrives
// in one of two containers:
//
//   - Ogg/Opus (RFC 3533, RFC 7845): what the current app sends. The phone wraps the watch's
//     packets into a standard file without re-encoding them.
//   - Zepp's own framing: what a watch stores, [4-byte big-endian length][4 other bytes][packet]
//     repeated. Still accepted so that older clients keep working.
//
// Both yield the same packets, which are decoded to a 16 kHz, 16-bit, mono WAV.
package audio

import (
	"bytes"
	"errors"
	"fmt"
)

// Format names a container.
type Format string

const (
	FormatOggOpus Format = "ogg-opus"
	FormatZepp    Format = "zepp"
)

const (
	// SampleRate of the decoded audio. The watch records at 16 kHz.
	SampleRate = 16000
	// Channels of the decoded audio: always mono.
	Channels = 1

	// MaxPackets bounds how much a single recording can expand when decoded: 30 000 packets are
	// 10 minutes, about 19 MB of WAV. The product limit is 60 s; without a bound, a few megabytes
	// of one-byte packets would decode into gigabytes of silence.
	MaxPackets = 30000
)

// ErrTooLong is returned for a recording with more than MaxPackets packets.
var ErrTooLong = errors.New("recording is too long")

// Detect says which container data looks like, from its first bytes.
func Detect(data []byte) Format {
	if bytes.HasPrefix(data, []byte("OggS")) {
		return FormatOggOpus
	}
	return FormatZepp
}

// Packets extracts the Opus packets from a recording in either container. Input is untrusted:
// every length is checked, and anything that does not parse cleanly is an error. The returned
// packets alias data.
func Packets(data []byte) ([][]byte, Format, error) {
	format := Detect(data)

	var packets [][]byte
	var err error
	switch format {
	case FormatOggOpus:
		packets, err = oggPackets(data)
	default:
		packets = zeppPackets(data)
		if len(packets) == 0 {
			err = errors.New("no audio packets in the recording")
		}
	}
	if err != nil {
		return nil, format, fmt.Errorf("%s: %w", format, err)
	}
	return packets, format, nil
}
