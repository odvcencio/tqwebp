// Package container writes the RIFF wrapper of a lossy WebP file, as the
// WebP container specification defines it.
//
// # The simple layout
//
// An opaque picture takes the simple layout, which is short:
//
//	"RIFF" u32(fileSize) "WEBP" "VP8 " u32(payloadSize) payload [pad]
//
// The RIFF size field counts every byte after itself. A chunk of odd size
// takes one zero pad byte, and the pad byte counts towards the RIFF size.
//
// # The extended layout
//
// A picture with a translucent pixel takes the extended layout. A VP8X
// chunk states the canvas size and the feature flags, an ALPH chunk
// carries the alpha plane, and the VP8 key frame follows:
//
//	"RIFF" u32(fileSize) "WEBP"
//	"VP8X" u32(10) flags u24(0) u24(width-1) u24(height-1)
//	"ALPH" u32(alphaSize) alpha [pad]
//	"VP8 " u32(payloadSize) payload [pad]
//
// The canvas fields are 24-bit little-endian and hold the size minus
// one, so they carry 1 to 16777216 pixels. The VP8 key frame's own size
// fields hold 14 bits, so this encoder still refuses a picture wider or
// taller than MaxDimension of package frame.
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

// MaxCanvas is the largest canvas size the VP8X 24-bit minus-one fields
// can state.
const MaxCanvas = 1 << 24

// VP8X feature flags, in the bit order of the container specification's
// first VP8X byte.
const (
	FlagAnimation = 1 << 1
	FlagXMP       = 1 << 2
	FlagEXIF      = 1 << 3
	FlagAlpha     = 1 << 4
	FlagICC       = 1 << 5
)

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

// vp8xChunkSize is the byte count of a whole VP8X chunk: the four-byte
// identifier, the four-byte size field, and the ten-byte body.
const vp8xChunkSize = 18

// WriteExtendedLossyAlpha writes payload, a VP8 key frame, together with
// alphaChunk, a complete ALPH chunk body, inside the extended RIFF WebP
// container. Width and height are the visible picture size.
//
// The caller builds alphaChunk with package alpha: the header byte and
// then the filtered plane.
func WriteExtendedLossyAlpha(w io.Writer, alphaChunk, payload []byte, width, height int) error {
	if width <= 0 || height <= 0 || width > MaxCanvas || height > MaxCanvas {
		return fmt.Errorf("tqwebp: canvas of %dx%d pixels does not fit a VP8X chunk", width, height)
	}
	alphaPad := len(alphaChunk) & 1
	payloadPad := len(payload) & 1
	body := int64(4) + vp8xChunkSize +
		int64(8+len(alphaChunk)+alphaPad) +
		int64(8+len(payload)+payloadPad)
	if body > 1<<32-1 {
		return fmt.Errorf("tqwebp: a file of %d bytes does not fit a RIFF container", body)
	}

	var header [12 + vp8xChunkSize + 8]byte
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(body))
	copy(header[8:12], "WEBP")

	copy(header[12:16], "VP8X")
	binary.LittleEndian.PutUint32(header[16:20], 10)
	header[20] = FlagAlpha
	// header[21:24] stay zero: the reserved field of the VP8X body.
	putUint24(header[24:27], uint32(width-1))
	putUint24(header[27:30], uint32(height-1))

	copy(header[30:34], "ALPH")
	binary.LittleEndian.PutUint32(header[34:38], uint32(len(alphaChunk)))

	var between [8]byte
	copy(between[0:4], "VP8 ")
	binary.LittleEndian.PutUint32(between[4:8], uint32(len(payload)))

	writes := [][]byte{header[:], alphaChunk}
	if alphaPad == 1 {
		writes = append(writes, []byte{0})
	}
	writes = append(writes, between[:], payload)
	if payloadPad == 1 {
		writes = append(writes, []byte{0})
	}
	for _, part := range writes {
		if len(part) == 0 {
			continue
		}
		if _, err := w.Write(part); err != nil {
			return err
		}
	}
	return nil
}

// ExtendedSize returns the exact number of bytes WriteExtendedLossyAlpha
// writes for the given chunk lengths.
func ExtendedSize(alphaChunkLen, payloadLen int) int {
	return 12 + vp8xChunkSize +
		8 + alphaChunkLen + alphaChunkLen&1 +
		8 + payloadLen + payloadLen&1
}

// putUint24 writes a 24-bit little-endian field.
func putUint24(dst []byte, v uint32) {
	dst[0] = uint8(v)
	dst[1] = uint8(v >> 8)
	dst[2] = uint8(v >> 16)
}
