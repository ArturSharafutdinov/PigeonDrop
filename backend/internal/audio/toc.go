package audio

// packetSamples is how much audio an Opus packet holds, in samples at SampleRate, read from its table-of-contents
// byte (RFC 6716, section 3.1) and, for the arbitrary-frame-count form, the frame-count byte after it. Packets are
// untrusted: a packet is 1 byte and can still stand for up to 120 ms, six times what the watch's 20 ms packets do, so
// the number of packets does not bound how much a recording decodes to. ok is false for a packet whose length cannot
// be told (empty, or the form that needs a second byte and has none); the decoder deals with those.
func packetSamples(packet []byte) (samples int, ok bool) {
	if len(packet) == 0 {
		return 0, false
	}
	toc := packet[0]

	// The length of one frame, in units of 2.5 ms.
	var unit int
	switch config := toc >> 3; {
	case config < 12: // SILK only: 10, 20, 40 or 60 ms in each of three bandwidths
		unit = [...]int{4, 8, 16, 24}[config%4]
	case config < 16: // hybrid: 10 or 20 ms
		unit = [...]int{4, 8}[config%2]
	default: // CELT only: 2.5, 5, 10 or 20 ms
		unit = [...]int{1, 2, 4, 8}[config%4]
	}

	var frames int
	switch toc & 3 {
	case 0:
		frames = 1
	case 1, 2:
		frames = 2
	default:
		if len(packet) < 2 {
			return 0, false
		}
		frames = int(packet[1] & 0x3f)
	}
	return unit * frames * (SampleRate / 400), true
}
