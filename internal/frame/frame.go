// Package frame writes the VP8 key-frame header and assembles the frame
// its partitions belong to, as RFC 6386 chapter 9 defines it.
//
// A key frame holds an uncompressed 10-byte block and then two or more
// boolean-coded partitions. The uncompressed block carries the frame tag,
// the start code, and the picture size. The first partition carries the
// header fields and the per-macroblock prediction records. The remaining
// partitions carry coefficient tokens. WP-1 writes exactly one token
// partition.
package frame

import (
	"errors"
	"fmt"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/token"
)

// MaxDimension is the largest width or height a VP8 frame can carry. The
// picture size fields hold 14 bits each (RFC 6386 section 9.1).
const MaxDimension = 1<<14 - 1

// ErrTooLarge reports a picture that does not fit the 14-bit size fields.
var ErrTooLarge = errors.New("tqwebp: image is larger than 16383 pixels on a side")

// Header holds every frame-level field WP-1 writes. Segmentation stays
// off, the loop filter stays at the level given, and the frame keeps the
// default coefficient probabilities.
type Header struct {
	// Width and Height are the visible picture size in pixels.
	Width, Height int
	// FilterSimple selects the simple loop filter over the normal one.
	FilterSimple bool
	// FilterLevel is the loop filter strength, 0 to 63. WP-1 writes 0:
	// the encoder does not model the filter, and level 0 keeps the
	// decoder's output equal to the encoder's own reconstruction, which
	// is the exact-match gate of specification section 11.2.
	FilterLevel int
	// FilterSharpness is the filter sharpness, 0 to 7.
	FilterSharpness int
	// QuantIndex is the base quantizer index, 0 to 127.
	QuantIndex int
	// SkipProb is the probability that a macroblock is not skipped, on a
	// scale of 256. It must be 1 or more.
	SkipProb uint8
}

// WriteHeader writes the header fields of the first partition, in the
// order RFC 6386 sections 9.2 to 9.11 fix. The caller then writes the
// per-macroblock prediction records into the same encoder.
func WriteHeader(enc *boolenc.Encoder, h Header) {
	// Section 9.2: colour space and clamping type. Both stay at 0.
	enc.WriteFlag(false)
	enc.WriteFlag(false)

	// Section 9.3: segmentation stays off.
	enc.WriteFlag(false)

	// Section 9.4: loop filter.
	enc.WriteFlag(h.FilterSimple)
	enc.WriteLiteral(uint32(h.FilterLevel), 6)
	enc.WriteLiteral(uint32(h.FilterSharpness), 3)
	enc.WriteFlag(false) // no per-reference or per-mode filter deltas

	// Section 9.5: one token partition, so the count exponent is 0.
	enc.WriteLiteral(0, 2)

	// Section 9.6: base quantizer index, then five optional deltas that
	// WP-1 leaves at zero.
	enc.WriteLiteral(uint32(h.QuantIndex), 7)
	for i := 0; i < 5; i++ {
		enc.WriteFlag(false)
	}

	// Section 9.7: a key frame refreshes the entropy probabilities.
	enc.WriteFlag(true)

	// Section 9.8: coefficient probability updates. WP-1 keeps the
	// defaults, so every gate says "no update".
	token.WriteProbUpdates(enc)

	// Section 9.10: the skip flag is in use, with its probability.
	enc.WriteFlag(true)
	enc.WriteLiteral(uint32(h.SkipProb), 8)
}

// Assemble returns a complete VP8 key frame: the uncompressed 10-byte
// block, then the first partition, then the single token partition.
func Assemble(width, height int, firstPartition, tokens []byte) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("tqwebp: image is %dx%d pixels", width, height)
	}
	if width > MaxDimension || height > MaxDimension {
		return nil, ErrTooLarge
	}
	if len(firstPartition) >= 1<<19 {
		return nil, fmt.Errorf("tqwebp: first partition is %d bytes, over the 19-bit limit", len(firstPartition))
	}

	out := make([]byte, 0, 10+len(firstPartition)+len(tokens))

	// Frame tag, RFC 6386 section 9.1: key frame flag 0, version 0, show
	// frame 1, then the 19-bit first partition length.
	size := uint32(len(firstPartition))
	const showFrame = 1 << 4
	out = append(out,
		byte(showFrame|(size&7)<<5),
		byte(size>>3),
		byte(size>>11),
	)

	// Start code and picture size. Both scale fields stay at 0.
	out = append(out, 0x9d, 0x01, 0x2a)
	out = append(out,
		byte(width), byte(width>>8),
		byte(height), byte(height>>8),
	)

	out = append(out, firstPartition...)
	out = append(out, tokens...)
	return out, nil
}
