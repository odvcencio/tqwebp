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

// SegmentFeature gates one per-segment quantizer or loop-filter delta.
// When Enabled is true, Value carries a signed delta written as an
// absolute magnitude followed by a sign flag.
type SegmentFeature struct {
	// Enabled signals that this segment overrides the base value.
	Enabled bool
	// Value is the signed delta applied to the segment.
	Value int
}

// SegmentProbability gates one segment-map tree probability update.
// When Update is true, Value is the new 8-bit probability.
type SegmentProbability struct {
	// Update signals that the decoder must read a new probability.
	Update bool
	// Value is the replacement probability, an 8-bit literal.
	Value uint8
}

// Segmentation describes the optional per-segment feature map of RFC
// 6386 section 9.3. The zero value keeps segmentation off.
type Segmentation struct {
	// Enabled turns segmentation on.
	Enabled bool
	// UpdateMap signals that the frame refreshes the three tree
	// probabilities used to code the segment id.
	UpdateMap bool
	// UpdateData signals that the frame redefines the per-segment
	// quantizer and loop-filter deltas.
	UpdateData bool
	// Absolute selects absolute magnitudes over signed deltas relative
	// to the previous values. It only matters when UpdateData is on.
	Absolute bool
	// Quantizer holds one gate per segment for quantizer deltas.
	Quantizer [4]SegmentFeature
	// LoopFilter holds one gate per segment for loop-filter deltas.
	LoopFilter [4]SegmentFeature
	// TreeProbs holds the three segment-map tree probabilities.
	TreeProbs [3]SegmentProbability
}

// Header holds every frame-level field WP-1 writes. The loop filter stays
// at the level given, and the frame keeps the default coefficient
// probabilities.
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
	// TokenProbs, when not nil, is the coefficient probability table the
	// token partition codes against. WriteHeader signals exactly the
	// entries that differ from the default table and leaves the rest at
	// their defaults, so the decoder reconstructs this table before it
	// reads any tokens. A nil keeps the default table and writes one
	// "no update" decision per entry, byte for byte as earlier releases.
	TokenProbs *token.Probs
	// Segmentation carries the optional per-segment feature data of
	// section 9.3. The zero value keeps segmentation off, byte for byte
	// as earlier releases.
	Segmentation Segmentation
}

// WriteHeader writes the header fields of the first partition, in the
// order RFC 6386 sections 9.2 to 9.11 fix. The caller then writes the
// per-macroblock prediction records into the same encoder. It panics on
// a Segmentation whose enabled feature values do not fit their fields.
func WriteHeader(enc *boolenc.Encoder, h Header) {
	validateSegmentation(h.Segmentation)

	// Section 9.2: colour space and clamping type. Both stay at 0.
	enc.WriteFlag(false)
	enc.WriteFlag(false)

	// Section 9.3: segmentation.
	enc.WriteFlag(h.Segmentation.Enabled)
	if h.Segmentation.Enabled {
		writeSegmentation(enc, h.Segmentation)
	}

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

	// Section 9.8: coefficient probability updates. A nil TokenProbs
	// keeps the defaults, so every gate says "no update"; a table
	// signals exactly its differences from the defaults.
	token.WriteProbs(enc, h.TokenProbs)

	// Section 9.10: the skip flag is in use, with its probability.
	enc.WriteFlag(true)
	enc.WriteLiteral(uint32(h.SkipProb), 8)
}

// validateSegmentation panics when an enabled feature value would be
// silently truncated by the fixed-width fields of section 9.3: quantizer
// deltas carry 7 magnitude bits, loop-filter deltas 6.
func validateSegmentation(s Segmentation) {
	for i, f := range s.Quantizer {
		if !f.Enabled {
			continue
		}
		if f.Value < -127 || f.Value > 127 {
			panic(fmt.Sprintf("tqwebp/frame: segmentation quantizer delta for segment %d is %d, out of range -127..127", i, f.Value))
		}
	}
	for i, f := range s.LoopFilter {
		if !f.Enabled {
			continue
		}
		if f.Value < -63 || f.Value > 63 {
			panic(fmt.Sprintf("tqwebp/frame: segmentation loop-filter delta for segment %d is %d, out of range -63..63", i, f.Value))
		}
	}
}

// writeSegmentFeature writes one per-segment feature gate and, when the
// gate is on, the signed value as an absolute magnitude of n bits
// followed by a sign flag (RFC 6386 section 9.3).
func writeSegmentFeature(enc *boolenc.Encoder, f SegmentFeature, n int) {
	enc.WriteFlag(f.Enabled)
	if f.Enabled {
		v := f.Value
		negative := v < 0
		if negative {
			v = -v
		}
		enc.WriteLiteral(uint32(v), n)
		enc.WriteFlag(negative)
	}
}

// writeSegmentation writes the body of section 9.3 after the enabled
// flag: feature data first, then the segment-map tree probabilities.
func writeSegmentation(enc *boolenc.Encoder, s Segmentation) {
	enc.WriteFlag(s.UpdateMap)
	enc.WriteFlag(s.UpdateData)
	if s.UpdateData {
		enc.WriteFlag(s.Absolute)
		for _, f := range s.Quantizer {
			writeSegmentFeature(enc, f, 7)
		}
		for _, f := range s.LoopFilter {
			writeSegmentFeature(enc, f, 6)
		}
	}
	if s.UpdateMap {
		for _, p := range s.TreeProbs {
			enc.WriteFlag(p.Update)
			if p.Update {
				enc.WriteLiteral(uint32(p.Value), 8)
			}
		}
	}
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
