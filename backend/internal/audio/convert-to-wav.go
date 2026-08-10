package audio

import (
	"bytes"
	"fmt"
	"os"
)

// OpusToWAV converts the raw bytes of a watch .opus recording into a complete
// .wav file.
//
// It takes and returns bytes rather than file paths on purpose: the upload
// handler already holds the recording in memory, and once recordings are
// encrypted at rest the plaintext never reaches disk at all. A path-based core
// would have to be unpicked twice.
func OpusToWAV(opusData []byte) ([]byte, error) {
	packets, err := splitPackets(opusData)
	if err != nil {
		return nil, fmt.Errorf("framing: %w", err)
	}

	pcm, err := decodePackets(packets)
	if err != nil {
		return nil, fmt.Errorf("decoding: %w", err)
	}

	buf := bytes.NewBuffer(make([]byte, 0, WAVHeaderSize+len(pcm)*bytesPerFrame))
	if err := writeWAV(buf, pcm, SampleRate, Channels); err != nil {
		return nil, fmt.Errorf("encoding wav: %w", err)
	}

	return buf.Bytes(), nil
}

// ConvertToWav reads a watch .opus recording from inputPath and writes the
// converted .wav to outputPath. It mirrors the CLI behaviour of the original
// Node implementation (server/convert-to-wav.js).
func ConvertToWav(inputPath, outputPath string) error {
	// %w wraps the underlying error rather than discarding it, so the message
	// keeps the real cause ("no such file", "permission denied") and callers
	// can still match it with errors.Is(err, os.ErrNotExist).
	opusData, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", inputPath, err)
	}

	wavData, err := OpusToWAV(opusData)
	if err != nil {
		return fmt.Errorf("converting %s: %w", inputPath, err)
	}

	if err := os.WriteFile(outputPath, wavData, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outputPath, err)
	}

	return nil
}
