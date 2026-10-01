package webpwire

import (
	"encoding/binary"
	"errors"
	"image"
)

var malformed = errors.New("malformed container")
var unsupported = errors.New("unsupported bitstream feature")

func U24(p []byte) uint32   { return uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 }
func Put24(p []byte, n int) { p[0], p[1], p[2] = byte(n), byte(n>>8), byte(n>>16) }

// Probe validates the codec header against the declared payload length without
// requiring that payload to be allocated. It does not validate entropy coding.
func Probe(id string, p []byte, size int64) (image.Point, bool, error) {
	bad := func(k Kind) (image.Point, bool, error) {
		err := malformed
		if k == Unsupported {
			err = unsupported
		}
		return image.Point{}, false, &Fault{Frame: -1, Kind: k, Err: err}
	}
	if id == "VP8L" {
		if size < 5 || len(p) < 5 || p[0] != 0x2f {
			return bad(Invalid)
		}
		v := binary.LittleEndian.Uint32(p[1:5])
		if v>>29 != 0 {
			return bad(Unsupported)
		}
		return image.Pt(int(v&0x3fff)+1, int(v>>14&0x3fff)+1), v&(1<<28) != 0, nil
	}
	if id != "VP8 " || size < 10 || len(p) < 10 || p[0]&1 != 0 || p[0]&0x10 == 0 || p[0]>>1&7 > 3 || string(p[3:6]) != "\x9d\x01\x2a" {
		return bad(Invalid)
	}
	w := int(binary.LittleEndian.Uint16(p[6:8]) & 0x3fff)
	h := int(binary.LittleEndian.Uint16(p[8:10]) & 0x3fff)
	partition := int64(p[0]>>5) | int64(p[1])<<3 | int64(p[2])<<11
	if w == 0 || h == 0 || partition > size-10 {
		return bad(Invalid)
	}
	return image.Pt(w, h), false, nil
}

func ValidateAlpha(p []byte, dim image.Point, writing bool) error {
	if len(p) < 1 {
		return &Fault{Frame: -1, Kind: Invalid, Err: malformed}
	}
	if p[0]&3 > 1 || p[0]>>4&3 > 1 {
		return &Fault{Frame: -1, Kind: Unsupported, Err: unsupported}
	}
	if writing && p[0]&0xc0 != 0 {
		return &Fault{Frame: -1, Kind: Invalid, Err: malformed}
	}
	if p[0]&3 == 0 && uint64(len(p)-1) != uint64(dim.X)*uint64(dim.Y) || p[0]&3 == 1 && len(p) < 2 {
		return &Fault{Frame: -1, Kind: Invalid, Err: malformed}
	}
	return nil
}
