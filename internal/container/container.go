// Package container writes the RIFF wrapper of a simple lossy WebP file,
// as the WebP container specification defines it.
//
// The layout is short:
//
//	"RIFF" u32(fileSize) "WEBP" "VP8 " u32(payloadSize) payload [pad]
//
// The RIFF size field counts every byte after itself. A chunk of odd size
// takes one zero pad byte, and the pad byte counts towards the RIFF size.
// Alpha needs the extended layout with a VP8X chunk and an ALPH chunk,
// which work package WP-5 adds.
package container

import (
	"encoding/binary"
	"fmt"
	"io"
)

// MaxPayload is the largest VP8 payload the container can carry. The RIFF
// size field holds 32 bits, and the header before the payload takes 12 of
// them.
const MaxPayload = 1<<32 - 1 - 12

// WriteSimpleLossy writes payload, a VP8 key frame, to w inside a RIFF
// WebP container.
func WriteSimpleLossy(w io.Writer, payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("tqwebp: VP8 payload of %d bytes does not fit a RIFF container", len(payload))
	}
	pad := len(payload) & 1
	var header [20]byte
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(4+8+len(payload)+pad))
	copy(header[8:12], "WEBP")
	copy(header[12:16], "VP8 ")
	binary.LittleEndian.PutUint32(header[16:20], uint32(len(payload)))

	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if pad == 1 {
		if _, err := w.Write([]byte{0}); err != nil {
			return err
		}
	}
	return nil
}

// Size returns the exact number of bytes WriteSimpleLossy writes for a
// payload of the given length.
func Size(payloadLen int) int {
	return 12 + 8 + payloadLen + payloadLen&1
}
