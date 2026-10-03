package audio

import "encoding/binary"

const zeppHeaderSize = 8

// zeppPackets splits Zepp's [4-byte big-endian length][4 other bytes][packet] stream. A recording
// cut short - the app closed mid-write - ends in a partial packet; the scan stops there and keeps
// everything before it, because losing a whole message over its last few milliseconds would be
// worse than trimming them.
func zeppPackets(data []byte) [][]byte {
	var packets [][]byte
	for pos := 0; len(data)-pos >= zeppHeaderSize; {
		length := uint64(binary.BigEndian.Uint32(data[pos:]))
		if length == 0 || length > uint64(len(data)-pos-zeppHeaderSize) {
			break
		}
		start := pos + zeppHeaderSize
		packets = append(packets, data[start:start+int(length)])
		pos = start + int(length)
	}
	return packets
}
