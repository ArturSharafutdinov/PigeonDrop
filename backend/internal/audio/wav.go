package audio

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	bitsPerSample = 16
	bytesPerFrame = bitsPerSample / 8

	// WAVHeaderSize is the size of a canonical PCM WAV header: the 12-byte
	// RIFF chunk, the 24-byte fmt chunk, and the 8-byte data chunk descriptor.
	// Everything after it is raw samples.
	WAVHeaderSize = 44

	pcmFormatTag = 1  // uncompressed PCM, as opposed to a compressed codec
	fmtChunkSize = 16 // fixed for PCM; other formats append extra fields
)

// writeWAV writes pcm to w as a complete WAV file: a 44-byte canonical header
// followed by the samples.
//
// Every numeric field here is LITTLE-endian - note the deliberate contrast
// with the watch's packet lengths in opus.go, which are big-endian. Getting
// these backwards produces a file that still opens but reports a nonsense
// duration and plays as static, so it is worth being explicit about.
//
// The four-character tags ("RIFF", "WAVE", "fmt ", "data") are literal ASCII
// bytes rather than numbers, and so have no endianness at all.
func writeWAV(w io.Writer, pcm []int16, sampleRate, channels int) error {
	if channels <= 0 {
		return fmt.Errorf("channels must be positive, got %d", channels)
	}
	if sampleRate <= 0 {
		return fmt.Errorf("sampleRate must be positive, got %d", sampleRate)
	}

	dataSize := len(pcm) * bytesPerFrame
	blockAlign := channels * bytesPerFrame
	byteRate := sampleRate * blockAlign

	// binary.Write picks its byte width from the Go type, so every value must
	// be explicitly sized. Passing a plain int fails with "invalid type int" -
	// int has no fixed width, so the package refuses to guess.
	header := []any{
		[]byte("RIFF"),
		uint32(WAVHeaderSize - 8 + dataSize), // size of everything after this field
		[]byte("WAVE"),
		[]byte("fmt "), // the trailing space is part of the tag
		uint32(fmtChunkSize),
		uint16(pcmFormatTag),
		uint16(channels),
		uint32(sampleRate),
		uint32(byteRate),
		uint16(blockAlign),
		uint16(bitsPerSample),
		[]byte("data"),
		uint32(dataSize),
	}

	for i, field := range header {
		if err := binary.Write(w, binary.LittleEndian, field); err != nil {
			return fmt.Errorf("writing wav header field %d: %w", i, err)
		}
	}

	// binary.Write accepts a slice of fixed-size values directly, so the whole
	// sample run goes out in one call - no per-sample loop needed.
	if err := binary.Write(w, binary.LittleEndian, pcm); err != nil {
		return fmt.Errorf("writing %d samples: %w", len(pcm), err)
	}

	return nil
}
