package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Ogg's CRC-32: polynomial 0x04C11DB7, not reflected, initial value 0, no final xor.
var crcTable = func() (table [256]uint32) {
	for i := range table {
		r := uint32(i) << 24
		for range 8 {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		table[i] = r
	}
	return table
}()

// pageCRC is the checksum of a whole page with its own checksum field (bytes 22-25) counted as zero.
func pageCRC(page []byte) uint32 {
	var crc uint32
	for i, b := range page {
		if i >= 22 && i < 26 {
			b = 0
		}
		crc = crc<<8 ^ crcTable[byte(crc>>24)^b]
	}
	return crc
}

// oggPackets reads a mono Ogg/Opus stream and returns its audio packets, without the OpusHead
// and OpusTags header packets. A packet is rebuilt from lacing values, so one that continues
// onto the next page is handled too, although the phone's packer never produces one.
func oggPackets(data []byte) ([][]byte, error) {
	var (
		packets [][]byte
		carry   []byte
		index   int // number of complete packets seen so far, header packets included
	)

	for pos := 0; pos < len(data); {
		if len(data)-pos < 27 || !bytes.Equal(data[pos:pos+4], []byte("OggS")) {
			return nil, fmt.Errorf("not an Ogg page at byte %d", pos)
		}
		segments := int(data[pos+26])
		if len(data)-pos < 27+segments {
			return nil, fmt.Errorf("truncated Ogg page at byte %d", pos)
		}
		bodyLen := 0
		for i := range segments {
			bodyLen += int(data[pos+27+i])
		}
		pageLen := 27 + segments + bodyLen
		if pos+pageLen > len(data) {
			return nil, fmt.Errorf("truncated Ogg page at byte %d", pos)
		}

		page := data[pos : pos+pageLen]
		if pageCRC(page) != binary.LittleEndian.Uint32(page[22:26]) {
			return nil, fmt.Errorf("Ogg page checksum mismatch at byte %d", pos)
		}

		body := page[27+segments:]
		for i := range segments {
			lace := int(page[27+i])
			carry = append(carry, body[:lace]...)
			body = body[lace:]
			if lace == 255 {
				continue // the packet goes on in the next segment
			}

			packet := carry
			carry = nil
			switch index {
			case 0:
				if err := checkOpusHead(packet); err != nil {
					return nil, err
				}
			case 1:
				// OpusTags: nothing in it matters for decoding.
			default:
				packets = append(packets, packet)
			}
			index++
		}
		pos += pageLen
	}

	if len(carry) > 0 {
		return nil, errors.New("stream ends inside a packet")
	}
	if len(packets) == 0 {
		return nil, errors.New("no audio packets in the Ogg stream")
	}
	return packets, nil
}

func checkOpusHead(packet []byte) error {
	if len(packet) < 19 || !bytes.Equal(packet[:8], []byte("OpusHead")) {
		return errors.New("first Ogg packet is not an OpusHead")
	}
	if packet[8]>>4 != 0 {
		return fmt.Errorf("unsupported OpusHead version %d", packet[8])
	}
	if packet[9] != 1 {
		return fmt.Errorf("only mono Opus is supported, got %d channels", packet[9])
	}
	return nil
}
