package audio

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/tphakala/go-opus/opus"
)

// Decoder turns one Opus packet into 16-bit PCM and returns how many samples it wrote.
// It is an interface so that another decoder can replace the pure-Go one without touching callers.
type Decoder interface {
	Decode(packet []byte, pcm []int16) (int, error)
}

func newOpusDecoder() (Decoder, error) {
	return opus.NewDecoder(SampleRate, Channels)
}

// maxFrameSamples is the longest frame Opus can produce (120 ms) at SampleRate.
const maxFrameSamples = SampleRate * 120 / 1000

// ToWAV decodes a recording in either supported container into a 16 kHz, 16-bit, mono WAV file.
func ToWAV(data []byte) ([]byte, error) {
	return toWAV(data, newOpusDecoder)
}

func toWAV(data []byte, newDecoder func() (Decoder, error)) ([]byte, error) {
	packets, _, err := Packets(data)
	if err != nil {
		return nil, err
	}
	if len(packets) > MaxPackets {
		return nil, fmt.Errorf("%w: %d packets (limit %d)", ErrTooLong, len(packets), MaxPackets)
	}

	decoder, err := newDecoder()
	if err != nil {
		return nil, fmt.Errorf("create decoder: %w", err)
	}

	frame := make([]int16, maxFrameSamples)
	pcm := make([]byte, 0, len(packets)*SampleRate/50*2) // 20 ms per packet, 2 bytes per sample
	for i, packet := range packets {
		n, err := decoder.Decode(packet, frame)
		if err != nil {
			return nil, fmt.Errorf("decode packet %d: %w", i, err)
		}
		if n < 0 || n > len(frame) {
			return nil, errors.New("decoder reported an impossible sample count")
		}
		for _, sample := range frame[:n] {
			pcm = binary.LittleEndian.AppendUint16(pcm, uint16(sample))
		}
	}
	return wavFile(pcm), nil
}

// PCMToWAV wraps 16 kHz, 16-bit, mono little-endian samples in a WAV header, the form the speech recognition
// service takes.
func PCMToWAV(pcm []byte) []byte { return wavFile(pcm) }

// wavFile prepends the 44-byte WAV header for 16 kHz, 16-bit, mono PCM.
func wavFile(pcm []byte) []byte {
	const bytesPerSample = 2
	header := make([]byte, 0, 44+len(pcm))
	header = append(header, "RIFF"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(36+len(pcm)))
	header = append(header, "WAVEfmt "...)
	header = binary.LittleEndian.AppendUint32(header, 16) // size of the fmt chunk
	header = binary.LittleEndian.AppendUint16(header, 1)  // PCM
	header = binary.LittleEndian.AppendUint16(header, Channels)
	header = binary.LittleEndian.AppendUint32(header, SampleRate)
	header = binary.LittleEndian.AppendUint32(header, SampleRate*Channels*bytesPerSample) // bytes per second
	header = binary.LittleEndian.AppendUint16(header, Channels*bytesPerSample)            // block align
	header = binary.LittleEndian.AppendUint16(header, 8*bytesPerSample)                   // bits per sample
	header = append(header, "data"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(len(pcm)))
	return append(header, pcm...)
}
