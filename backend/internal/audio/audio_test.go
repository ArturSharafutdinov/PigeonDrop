package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// --- helpers that build Ogg files, written from the specs and independent of the code under test ---

// refCRC is the Ogg checksum computed bit by bit.
func refCRC(b []byte) uint32 {
	var crc uint32
	for _, v := range b {
		crc ^= uint32(v) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func oggPage(seq uint32, headerType byte, continued bool, packets ...[]byte) []byte {
	var lacing, body []byte
	for i, p := range packets {
		n := len(p)
		for n >= 255 {
			lacing = append(lacing, 255)
			n -= 255
		}
		// a packet that continues on the next page ends its lacing on 255 with no terminating segment
		if !(continued && i == len(packets)-1 && len(p)%255 == 0 && len(p) > 0) {
			lacing = append(lacing, byte(n))
		}
		body = append(body, p...)
	}
	page := []byte("OggS\x00")
	page = append(page, headerType)
	page = append(page, make([]byte, 8)...) // granule
	page = binary.LittleEndian.AppendUint32(page, 1234)
	page = binary.LittleEndian.AppendUint32(page, seq)
	page = append(page, 0, 0, 0, 0, byte(len(lacing)))
	page = append(page, lacing...)
	page = append(page, body...)
	binary.LittleEndian.PutUint32(page[22:], refCRC(page))
	return page
}

func opusHead(channels byte) []byte {
	head := []byte("OpusHead\x01")
	head = append(head, channels, 0, 0)
	head = binary.LittleEndian.AppendUint32(head, 16000)
	return append(head, 0, 0, 0)
}

func opusTags() []byte { return append([]byte("OpusTags\x04\x00\x00\x00test"), 0, 0, 0, 0) }

func oggFile(channels byte, audio ...[]byte) []byte {
	out := oggPage(0, 0x02, false, opusHead(channels))
	out = append(out, oggPage(1, 0, false, opusTags())...)
	return append(out, oggPage(2, 0x04, false, audio...)...)
}

func filled(n int, v byte) []byte { return bytes.Repeat([]byte{v}, n) }

// --- the real fixtures ---

func TestFixturesDecodeToTheExpectedWAV(t *testing.T) {
	want := fixture(t, "tone.wav")

	for _, name := range []string{"tone.zepp", "tone.ogg"} {
		got, err := ToWAV(fixture(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: WAV differs from libopus's own decoding (got %d bytes, want %d)", name, len(got), len(want))
		}
	}
}

func TestBothContainersYieldTheSamePackets(t *testing.T) {
	zepp, zf, err := Packets(fixture(t, "tone.zepp"))
	if err != nil || zf != FormatZepp {
		t.Fatalf("zepp: %v %v", zf, err)
	}
	ogg, of, err := Packets(fixture(t, "tone.ogg"))
	if err != nil || of != FormatOggOpus {
		t.Fatalf("ogg: %v %v", of, err)
	}
	if len(zepp) != 100 || len(ogg) != 100 {
		t.Fatalf("packet counts: zepp %d, ogg %d, want 100", len(zepp), len(ogg))
	}
	for i := range zepp {
		if !bytes.Equal(zepp[i], ogg[i]) {
			t.Fatalf("packet %d differs between containers", i)
		}
	}
}

func TestWAVHeader(t *testing.T) {
	wav, err := ToWAV(fixture(t, "tone.ogg"))
	if err != nil {
		t.Fatal(err)
	}
	pcm := len(wav) - 44
	if string(wav[0:4]) != "RIFF" || string(wav[8:16]) != "WAVEfmt " || string(wav[36:40]) != "data" {
		t.Fatalf("bad chunk ids: %q %q %q", wav[0:4], wav[8:16], wav[36:40])
	}
	le := binary.LittleEndian
	if le.Uint32(wav[4:]) != uint32(36+pcm) || le.Uint32(wav[40:]) != uint32(pcm) ||
		le.Uint16(wav[20:]) != 1 || le.Uint16(wav[22:]) != 1 || le.Uint32(wav[24:]) != 16000 ||
		le.Uint32(wav[28:]) != 32000 || le.Uint16(wav[32:]) != 2 || le.Uint16(wav[34:]) != 16 {
		t.Fatalf("bad fmt fields: % x", wav[:44])
	}
	if pcm != 2*32000 {
		t.Fatalf("two seconds should be %d bytes of samples, got %d", 2*32000, pcm)
	}
}

// --- Zepp framing ---

func TestZeppTruncatedTailIsTrimmedNotFatal(t *testing.T) {
	whole := fixture(t, "tone.zepp")
	packets, _, err := Packets(whole[:len(whole)-5]) // cut inside the last packet
	if err != nil || len(packets) != 99 {
		t.Fatalf("got %d packets, %v; want 99", len(packets), err)
	}
	wav, err := ToWAV(whole[:len(whole)-5])
	if err != nil || len(wav) != 44+99*640 {
		t.Fatalf("WAV of the trimmed recording: %d bytes, %v", len(wav), err)
	}
}

func TestZeppRejectsRecordingsWithoutAWholePacket(t *testing.T) {
	good := fixture(t, "tone.zepp")
	oversized := append([]byte{}, good[:8+40]...)
	binary.BigEndian.PutUint32(oversized, 0xFFFFFFFF)

	for name, data := range map[string][]byte{
		"empty":                   nil,
		"shorter than a header":   good[:7],
		"header without a packet": good[:8],
		"length past the end":     oversized,
		"zero length":             make([]byte, 64),
	} {
		if _, _, err := Packets(data); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestZeppStopsAtAZeroLengthPacket(t *testing.T) {
	good := fixture(t, "tone.zepp")
	first := 8 + int(binary.BigEndian.Uint32(good))
	broken := append(append(append([]byte{}, good[:first]...), make([]byte, 8)...), good[first:]...)

	packets, _, err := Packets(broken)
	if err != nil || len(packets) != 1 {
		t.Fatalf("got %d packets, %v; want 1 (what follows a zero length cannot be trusted)", len(packets), err)
	}
}

// --- Ogg/Opus ---

func TestOggLacingBoundaries(t *testing.T) {
	var want [][]byte
	for _, n := range []int{1, 254, 255, 256, 509, 510, 511, 1275} {
		want = append(want, filled(n, byte(n)))
	}
	got, format, err := Packets(oggFile(1, want...))
	if err != nil || format != FormatOggOpus || len(got) != len(want) {
		t.Fatalf("got %d packets, %v, %v", len(got), format, err)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("packet %d (%d bytes) did not survive", i, len(want[i]))
		}
	}
}

func TestOggPacketContinuedOnTheNextPage(t *testing.T) {
	big := filled(600, 7) // 255 + 255 + 90: spread over two pages
	file := oggPage(0, 0x02, false, opusHead(1))
	file = append(file, oggPage(1, 0, false, opusTags())...)
	file = append(file, oggPage(2, 0, true, filled(10, 1), big[:510])...)
	file = append(file, oggPage(3, 0x05, false, big[510:])...)

	got, _, err := Packets(file)
	if err != nil || len(got) != 2 || !bytes.Equal(got[1], big) {
		t.Fatalf("got %d packets, %v (second packet intact: %v)", len(got), err, len(got) == 2 && bytes.Equal(got[1], big))
	}
}

func TestOggRejectsDamagedOrUnsupportedStreams(t *testing.T) {
	good := fixture(t, "tone.ogg")
	flip := func(at int) []byte {
		b := append([]byte{}, good...)
		b[at] ^= 0xFF
		return b
	}

	cases := map[string]struct {
		data []byte
		want string
	}{
		"flipped byte in the last page": {flip(len(good) - 10), "checksum"},
		"cut inside a page":             {good[:len(good)-30], "truncated"},
		"cut inside the page header":    {good[:20], "not an Ogg page"},
		"garbage after the last page":   {append(append([]byte{}, good...), 1, 2, 3), "not an Ogg page"},
		"stereo":                        {oggFile(2, filled(60, 1)), "mono"},
		"first packet is not OpusHead":  {append(oggPage(0, 0x02, false, []byte("NotAnOpusHeadAtAllReally")), oggPage(1, 0, false, opusTags())...), "OpusHead"},
		"unsupported OpusHead version":  {append(oggPage(0, 0x02, false, append([]byte("OpusHead\x20\x01\x00\x00"), 0x80, 0x3e, 0, 0, 0, 0, 0)), oggPage(1, 0, false, opusTags())...), "version"},
		"headers only, no audio":        {append(oggPage(0, 0x02, false, opusHead(1)), oggPage(1, 0x04, false, opusTags())...), "no audio"},
		"stream ends inside a packet":   {append(oggPage(0, 0x02, false, opusHead(1)), append(oggPage(1, 0, false, opusTags()), oggPage(2, 0x04, true, filled(255, 1))...)...), "inside a packet"},
	}
	for name, tc := range cases {
		_, _, err := Packets(tc.data)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error mentioning %q", name, err, tc.want)
		}
	}
}

func TestRecordingLongerThanTheLimitIsRefusedBeforeDecoding(t *testing.T) {
	packets := make([][]byte, MaxPackets+1)
	for i := range packets {
		packets[i] = []byte{0x48}
	}
	// Ogg pages hold at most 255 segments, so spread the packets over many pages.
	file := append(oggPage(0, 0x02, false, opusHead(1)), oggPage(1, 0, false, opusTags())...)
	for seq, rest := uint32(2), packets; len(rest) > 0; seq++ {
		n := min(200, len(rest))
		file = append(file, oggPage(seq, 0, false, rest[:n]...)...)
		rest = rest[n:]
	}

	called := false
	_, err := toWAV(file, func() (Decoder, error) { called = true; return nil, errors.New("must not be reached") })
	if !errors.Is(err, ErrTooLong) || called {
		t.Fatalf("got %v (decoder created: %v), want ErrTooLong before any decoding", err, called)
	}
}

// oggOf puts packets in an Ogg/Opus file, however many (pages hold at most 255 segments).
func oggOf(packets [][]byte) []byte {
	file := append(oggPage(0, 0x02, false, opusHead(1)), oggPage(1, 0, false, opusTags())...)
	for seq, rest := uint32(2), packets; len(rest) > 0; seq++ {
		n := min(200, len(rest))
		file = append(file, oggPage(seq, 0, false, rest[:n]...)...)
		rest = rest[n:]
	}
	return file
}

func TestPacketSamplesReadsTheTableOfContents(t *testing.T) {
	cases := []struct {
		name    string
		packet  []byte
		samples int // at 16 kHz
		ok      bool
	}{
		{"SILK wideband 20 ms, one frame (what the watch sends)", []byte{0x48, 1, 2, 3}, 320, true},
		{"SILK narrowband 10 ms", []byte{0x00, 1}, 160, true},
		{"SILK 60 ms", []byte{0x58, 1}, 960, true},
		{"SILK 60 ms, two frames of equal size", []byte{0x59, 1, 2}, 1920, true},
		{"SILK 60 ms, two frames of different size", []byte{0x5a, 1, 1, 2}, 1920, true},
		{"SILK 60 ms, two frames by the count byte", []byte{0x5b, 2}, 1920, true},
		{"SILK 20 ms, forty-eight frames by the count byte", []byte{0x4b, 48 | 0x40}, 48 * 320, true}, // 0x40: padding flag, not part of the count
		{"hybrid 20 ms", []byte{0x68, 1}, 320, true},
		{"CELT 2.5 ms", []byte{0x80, 1}, 40, true},
		{"CELT 20 ms", []byte{0xf8, 1}, 320, true},
		{"empty", nil, 0, false},
		{"count byte missing", []byte{0x5b}, 0, false},
	}
	for _, c := range cases {
		samples, ok := packetSamples(c.packet)
		if samples != c.samples || ok != c.ok {
			t.Errorf("%s: %d samples, ok=%v; want %d, %v", c.name, samples, ok, c.samples, c.ok)
		}
	}
}

// A packet is a few bytes and can stand for 120 ms, so the number of packets is not what limits a recording: 30 000
// of them, within what an upload may be, would decode to an hour of audio, 115 MB of WAV for every recording being
// decoded at the same time.
func TestRecordingThatClaimsMoreAudioThanTheLimitIsRefusedBeforeDecoding(t *testing.T) {
	packets := make([][]byte, MaxPackets)
	for i := range packets {
		packets[i] = []byte{0x5b, 2} // SILK 60 ms twice: 120 ms in two bytes
	}

	called := false
	_, err := toWAV(oggOf(packets), func() (Decoder, error) { called = true; return nil, errors.New("must not be reached") })

	if !errors.Is(err, ErrTooLong) || called {
		t.Fatalf("got %v (decoder created: %v), want ErrTooLong before any decoding", err, called)
	}
	if !strings.Contains(err.Error(), "limit 10m") {
		t.Errorf("the error should name the limit: %v", err)
	}
}

// The header only says what a packet should hold. A decoder can make more of it (a lost packet is concealed with as
// much as the one before it), so the limit is kept on what actually comes out, too.
type longDecoder struct{ samples int }

func (d longDecoder) Decode(_ []byte, pcm []int16) (int, error) { return min(d.samples, len(pcm)), nil }

func TestRecordingThatDecodesToMoreAudioThanTheLimitIsStopped(t *testing.T) {
	packets := make([][]byte, 6000)
	for i := range packets {
		packets[i] = []byte{0x48} // says 20 ms
	}

	_, err := toWAV(oggOf(packets), func() (Decoder, error) { return longDecoder{samples: maxFrameSamples}, nil }) // 120 ms each: 12 minutes

	if !errors.Is(err, ErrTooLong) || !strings.Contains(err.Error(), "decoded") {
		t.Fatalf("got %v, want ErrTooLong from the decoding", err)
	}
}

func TestRecordingJustWithinTheLimitIsDecoded(t *testing.T) {
	packets := make([][]byte, 5000) // 5000 packets of 120 ms: ten minutes
	for i := range packets {
		packets[i] = []byte{0x5b, 2}
	}

	wav, err := toWAV(oggOf(packets), func() (Decoder, error) { return longDecoder{samples: maxFrameSamples}, nil })

	if err != nil {
		t.Fatalf("a recording of exactly the limit was refused: %v", err)
	}
	if want := 44 + maxSamples*2; len(wav) != want {
		t.Errorf("%d bytes, want %d", len(wav), want)
	}
}

// --- decoding failures ---

type failingDecoder struct{}

func (failingDecoder) Decode([]byte, []int16) (int, error) { return 0, errors.New("corrupt packet") }

func TestDecodeFailureNamesThePacket(t *testing.T) {
	_, err := toWAV(fixture(t, "tone.zepp"), func() (Decoder, error) { return failingDecoder{}, nil })
	if err == nil || !strings.Contains(err.Error(), "decode packet 0") {
		t.Fatalf("got %v", err)
	}
}

func TestDecoderCreationFailureIsReported(t *testing.T) {
	_, err := toWAV(fixture(t, "tone.zepp"), func() (Decoder, error) { return nil, errors.New("no codec") })
	if err == nil || !strings.Contains(err.Error(), "create decoder") {
		t.Fatalf("got %v", err)
	}
}

// --- never panic on hostile input ---

func FuzzPackets(f *testing.F) {
	f.Add(fixture(f, "tone.zepp"))
	f.Add(fixture(f, "tone.ogg"))
	f.Add([]byte("OggS"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		packets, _, err := Packets(data)
		if err == nil && len(packets) == 0 {
			t.Fatal("no error but no packets")
		}
	})
}

func FuzzToWAV(f *testing.F) {
	f.Add(fixture(f, "tone.zepp"))
	f.Add(fixture(f, "tone.ogg"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if wav, err := ToWAV(data); err == nil && (len(wav) < 44 || string(wav[:4]) != "RIFF") {
			t.Fatal("success without a WAV")
		}
	})
}

func TestPCMToWAVMakesTheHeaderTheServiceExpects(t *testing.T) {
	pcm := []byte{1, 0, 2, 0, 3, 0}

	wav := PCMToWAV(pcm)

	if len(wav) != 44+len(pcm) || !bytes.Equal(wav[44:], pcm) {
		t.Fatalf("%d bytes", len(wav))
	}
	if string(wav[:4]) != "RIFF" || string(wav[8:16]) != "WAVEfmt " || string(wav[36:40]) != "data" {
		t.Errorf("header %q", wav[:44])
	}
	if got := binary.LittleEndian.Uint32(wav[4:8]); got != uint32(36+len(pcm)) {
		t.Errorf("RIFF size %d", got)
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != uint32(len(pcm)) {
		t.Errorf("data size %d", got)
	}
	if rate := binary.LittleEndian.Uint32(wav[24:28]); rate != 16000 {
		t.Errorf("rate %d", rate)
	}
}
