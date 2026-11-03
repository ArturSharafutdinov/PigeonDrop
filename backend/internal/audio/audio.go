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
	"time"
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

	// MaxPackets bounds how many packets a recording can have: 30 000 packets of the watch's 20 ms are 10 minutes.
	// It does not bound how much they decode to, because a packet may hold up to 120 ms: 30 000 of those are an
	// hour, 115 MB of WAV, for each of the recordings decoded at the same time. MaxDuration does.
	MaxPackets = 30000
	// MaxDuration is the longest recording that is decoded, whatever its packets claim and whatever the decoder
	// makes of them: 10 minutes are about 19 MB of WAV. The product limit is 60 s.
	MaxDuration = 10 * time.Minute

	// maxSamples is MaxDuration in samples.
	maxSamples = int(MaxDuration/time.Second) * SampleRate
)

// ErrTooLong is returned for a recording with more than MaxPackets packets or more than MaxDuration of audio.
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
