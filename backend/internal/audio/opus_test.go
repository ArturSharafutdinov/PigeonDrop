package audio

import (
	"os"
	"testing"
)

// Known-good values for testdata/test.opus, established by parsing the same
// file with the original Node implementation (pigeondrop/convert-to-wav.js).
const (
	fixturePackets = 236
	fixtureBytes   = 12492

	// Every packet in a watch recording is 20ms wideband mono SILK, which
	// makes its first payload byte (Opus's TOC byte) 0x48 without exception.
	// A different value means we sliced at the wrong offset.
	fixtureTOC = 0x48
)

func TestSplitPackets(t *testing.T) {
	data, err := os.ReadFile("testdata/test.opus")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if len(data) != fixtureBytes {
		t.Fatalf("fixture is %d bytes, expected %d - wrong file?", len(data), fixtureBytes)
	}

	packets, err := splitPackets(data)
	if err != nil {
		t.Fatalf("splitPackets: %v", err)
	}

	if len(packets) != fixturePackets {
		t.Errorf("packet count = %d, want %d", len(packets), fixturePackets)
	}

	// The self-verifying property: headers plus payloads must account for
	// every byte in the file, with nothing left over.
	total := 0
	for _, p := range packets {
		total += packetHeaderSize + len(p)
	}
	if total != len(data) {
		t.Errorf("packets span %d bytes but the file is %d - framing is off", total, len(data))
	}

	for i, p := range packets {
		if len(p) == 0 {
			t.Fatalf("packet %d is empty", i)
		}
		if p[0] != fixtureTOC {
			t.Errorf("packet %d TOC = %#x, want %#x", i, p[0], fixtureTOC)
		}
	}
}

// Malformed input must produce an error rather than a partial result - a
// framing error means every byte after it is misaligned, so there is nothing
// worth salvaging.
func TestSplitPacketsRejectsMalformed(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "empty file",
			data: []byte{},
		},
		{
			name: "header cut short",
			data: []byte{0x00, 0x00, 0x00},
		},
		{
			name: "zero length",
			data: []byte{0x00, 0x00, 0x00, 0x00, 0xde, 0xad, 0xbe, 0xef},
		},
		{
			name: "length beyond sanity bound",
			data: []byte{0x00, 0x0f, 0x00, 0x00, 0xde, 0xad, 0xbe, 0xef, 0x48},
		},
		{
			name: "payload overruns end of file",
			data: []byte{0x00, 0x00, 0x00, 0x64, 0xde, 0xad, 0xbe, 0xef, 0x48, 0x00},
		},
		{
			name: "trailing partial packet",
			data: []byte{0x00, 0x00, 0x00, 0x02, 0xde, 0xad, 0xbe, 0xef, 0x48, 0x1e, 0x00, 0x00},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := splitPackets(tt.data); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}
