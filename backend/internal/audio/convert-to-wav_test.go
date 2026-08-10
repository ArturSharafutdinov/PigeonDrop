package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Reference values for testdata/test.opus, produced by the original Node
// implementation (server/convert-to-wav.js, which wraps libopus via
// opusscript). testdata/test.wav is that exact output, kept as a golden file.
//
// 236 packets x 20ms x 16kHz = 75520 samples = 4.72s.
const (
	goldenSamples  = 236 * samplesPerPacket // 75520
	goldenWAVBytes = WAVHeaderSize + goldenSamples*bytesPerFrame

	// goldenRMS and goldenPeak characterise the reference waveform. pion/opus
	// is an independent implementation of RFC 6716 rather than a libopus
	// binding, so its output is perceptually equivalent but not bit-identical;
	// these are compared with a tolerance, never for exact equality.
	goldenRMS  = 376.4
	goldenPeak = 6757
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}

	return data
}

// pcmFromWAV returns the sample data of a 16-bit PCM WAV, skipping the header.
func pcmFromWAV(t *testing.T, wav []byte) []int16 {
	t.Helper()

	if len(wav) < WAVHeaderSize {
		t.Fatalf("wav is %d bytes, shorter than a %d-byte header", len(wav), WAVHeaderSize)
	}

	body := wav[WAVHeaderSize:]
	pcm := make([]int16, len(body)/bytesPerFrame)
	if err := binary.Read(bytes.NewReader(body), binary.LittleEndian, pcm); err != nil {
		t.Fatalf("reading pcm: %v", err)
	}

	return pcm
}

func rms(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}

	var sum float64
	for _, s := range pcm {
		sum += float64(s) * float64(s)
	}

	return math.Sqrt(sum / float64(len(pcm)))
}

func peak(pcm []int16) int {
	var max int
	for _, s := range pcm {
		v := int(s)
		if v < 0 {
			v = -v
		}
		if v > max {
			max = v
		}
	}

	return max
}

// The end-to-end test: a real watch recording must convert to a WAV that
// matches the Node reference in structure, length, and loudness.
func TestOpusToWAV(t *testing.T) {
	got, err := OpusToWAV(readFixture(t, "test.opus"))
	if err != nil {
		t.Fatalf("OpusToWAV: %v", err)
	}

	golden := readFixture(t, "test.wav")

	if len(got) != goldenWAVBytes {
		t.Errorf("output is %d bytes, want %d", len(got), goldenWAVBytes)
	}

	// The header is fully determined by the sample count and format, so it
	// must match the reference byte for byte - any difference is a real bug,
	// not decoder variance.
	if !bytes.Equal(got[:WAVHeaderSize], golden[:WAVHeaderSize]) {
		t.Errorf("wav header mismatch:\n got % x\nwant % x",
			got[:WAVHeaderSize], golden[:WAVHeaderSize])
	}

	gotPCM, goldenPCM := pcmFromWAV(t, got), pcmFromWAV(t, golden)

	if len(gotPCM) != len(goldenPCM) {
		t.Fatalf("decoded %d samples, reference has %d", len(gotPCM), len(goldenPCM))
	}

	// Loudness must land close to the reference. A decoder that silently
	// produced silence, noise, or half the audio would fail here even though
	// the sample count matched.
	gotRMS, wantRMS := rms(gotPCM), rms(goldenPCM)
	if relDiff(gotRMS, wantRMS) > 0.05 {
		t.Errorf("RMS = %.1f, reference %.1f (>5%% apart)", gotRMS, wantRMS)
	}
}

func TestDecodePackets(t *testing.T) {
	packets, err := splitPackets(readFixture(t, "test.opus"))
	if err != nil {
		t.Fatalf("splitPackets: %v", err)
	}

	pcm, err := decodePackets(packets)
	if err != nil {
		t.Fatalf("decodePackets: %v", err)
	}

	if len(pcm) != goldenSamples {
		t.Errorf("decoded %d samples, want %d", len(pcm), goldenSamples)
	}

	if d := float64(len(pcm)) / SampleRate; math.Abs(d-4.72) > 0.01 {
		t.Errorf("duration = %.3fs, want 4.720s", d)
	}

	if got := rms(pcm); relDiff(got, goldenRMS) > 0.05 {
		t.Errorf("RMS = %.1f, want ~%.1f", got, goldenRMS)
	}

	if got := peak(pcm); relDiff(float64(got), goldenPeak) > 0.10 {
		t.Errorf("peak = %d, want ~%d", got, goldenPeak)
	}
}

// The header is pure arithmetic over the sample count, so it can be checked
// against known-good bytes without decoding anything.
func TestWriteWAVHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := writeWAV(&buf, make([]int16, goldenSamples), SampleRate, Channels); err != nil {
		t.Fatalf("writeWAV: %v", err)
	}

	golden := readFixture(t, "test.wav")

	if !bytes.Equal(buf.Bytes()[:WAVHeaderSize], golden[:WAVHeaderSize]) {
		t.Errorf("header mismatch:\n got % x\nwant % x",
			buf.Bytes()[:WAVHeaderSize], golden[:WAVHeaderSize])
	}

	if buf.Len() != goldenWAVBytes {
		t.Errorf("total size = %d, want %d", buf.Len(), goldenWAVBytes)
	}
}

func TestWriteWAVRejectsBadFormat(t *testing.T) {
	tests := []struct {
		name       string
		sampleRate int
		channels   int
	}{
		{"zero sample rate", 0, 1},
		{"negative sample rate", -16000, 1},
		{"zero channels", SampleRate, 0},
		{"negative channels", SampleRate, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := writeWAV(&bytes.Buffer{}, []int16{0}, tt.sampleRate, tt.channels); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestConvertToWav(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.wav")

	if err := ConvertToWav(filepath.Join("testdata", "test.opus"), out); err != nil {
		t.Fatalf("ConvertToWav: %v", err)
	}

	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	if len(written) != goldenWAVBytes {
		t.Errorf("wrote %d bytes, want %d", len(written), goldenWAVBytes)
	}
}

func TestConvertToWavMissingInput(t *testing.T) {
	err := ConvertToWav(filepath.Join("testdata", "does-not-exist.opus"), filepath.Join(t.TempDir(), "out.wav"))
	if err == nil {
		t.Fatal("expected an error for a missing input file")
	}

	// The wrapped cause must survive, so callers can distinguish "not found"
	// (a 404) from "disk broken" (a 500) once this runs behind HTTP.
	if !os.IsNotExist(unwrapAll(err)) {
		t.Errorf("error should wrap os.ErrNotExist, got %v", err)
	}
}

func TestOpusToWAVRejectsGarbage(t *testing.T) {
	if _, err := OpusToWAV([]byte("this is not an opus file at all")); err == nil {
		t.Error("expected an error for non-opus input")
	}
}

func relDiff(got, want float64) float64 {
	if want == 0 {
		return math.Abs(got)
	}

	return math.Abs(got-want) / math.Abs(want)
}

func unwrapAll(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}
