// Package cost prices VP8 key-frame syntax elements exactly, in integer
// arithmetic. It is work package WP-2 slice 3: the rate half of the
// rate-distortion machinery a later slice builds on top of it.
//
// Nothing in the production encode path calls this package yet. The
// existing writers keep deciding and emitting exactly what they always
// did, so every encoded file stays byte for byte what earlier releases
// wrote; this package only observes their output shape. A later slice
// wires these primitives into a reconstructed-neighbour search, which is
// why every function here prices one self-contained syntax element and
// none of them mutate state.
//
// # Units
//
// A Cost counts 1/256 of a bit: 256 units per bit, so fractional rates
// accumulate without floating point. Probabilities are Q8 as everywhere
// else in VP8 -- an 8-bit value p means "the decision is false with
// probability p/256". The exact ideal code length of one boolean
// decision is -log2(p/256) for the false branch and -log2((256-p)/256)
// for the true branch; both are precomputed for every codable
// probability and rounded to the nearest unit. Rounding error never
// exceeds half a unit, or 1/512 bit, per decision.
//
// # Determinism
//
// Every operation is integer arithmetic over fixed tables. There is no
// floating point, no map, and no shared mutable state, so results are
// identical on every platform at every GOMAXPROCS.
package cost

// Cost is an entropy-code rate in units of 1/256 bit.
type Cost int64

const (
	// Unit is how many cost units one bit carries.
	Unit Cost = 256
	// PlainBit is the exact cost of one even-odds decision, and of one
	// bit of an L(n) literal field.
	PlainBit = Unit
)

// zeroCostQ8 prices the false branch of one boolean decision. Index p is
// the Q8 probability that the decision is false; entry p holds
// round(-log2(p/256) * 256), the exact ideal code length in 1/256-bit
// units. Entry 0 is unused: probability 0 cannot be coded (boolenc
// refuses it). Entry p doubles as the price of the true branch at
// probability 256-p, because -log2((256-p)/256) = -log2(p'/256) with
// p' = 256-p.
//
// The values were generated once by evaluating round(-log2(p/256)*256)
// per entry; TestBitCostExhaustiveAgainstLogReference recomputes all 510
// entries from the logarithm independently and fails on any drift, so
// accidental regeneration cannot move them unnoticed.
var zeroCostQ8 = [256]uint16{
	0,
	2048, 1792, 1642, 1536, 1454, 1386, 1329, 1280, 1236, 1198, 1162, 1130, 1101, 1073, 1048, 1024,
	1002, 980, 961, 942, 924, 906, 890, 874, 859, 845, 831, 817, 804, 792, 780, 768,
	757, 746, 735, 724, 714, 705, 695, 686, 676, 668, 659, 650, 642, 634, 626, 618,
	611, 603, 596, 589, 582, 575, 568, 561, 555, 548, 542, 536, 530, 524, 518, 512,
	506, 501, 495, 490, 484, 479, 474, 468, 463, 458, 453, 449, 444, 439, 434, 430,
	425, 420, 416, 412, 407, 403, 399, 394, 390, 386, 382, 378, 374, 370, 366, 362,
	358, 355, 351, 347, 343, 340, 336, 333, 329, 326, 322, 319, 315, 312, 309, 305,
	302, 299, 296, 292, 289, 286, 283, 280, 277, 274, 271, 268, 265, 262, 259, 256,
	253, 250, 247, 245, 242, 239, 236, 234, 231, 228, 226, 223, 220, 218, 215, 212,
	210, 207, 205, 202, 200, 197, 195, 193, 190, 188, 185, 183, 181, 178, 176, 174,
	171, 169, 167, 164, 162, 160, 158, 156, 153, 151, 149, 147, 145, 143, 140, 138,
	136, 134, 132, 130, 128, 126, 124, 122, 120, 118, 116, 114, 112, 110, 108, 106,
	104, 102, 101, 99, 97, 95, 93, 91, 89, 87, 86, 84, 82, 80, 78, 77,
	75, 73, 71, 70, 68, 66, 64, 63, 61, 59, 58, 56, 54, 53, 51, 49,
	48, 46, 44, 43, 41, 40, 38, 36, 35, 33, 32, 30, 28, 27, 25, 24,
	22, 21, 19, 18, 16, 15, 13, 12, 10, 9, 7, 6, 4, 3, 1,
}

// BitCostZero returns the exact price of writing the false branch at Q8
// probability p, the probability that the decision is false being p/256.
// It panics when p is 0, mirroring boolenc's own refusal to code that.
func BitCostZero(p uint8) Cost {
	if p == 0 {
		panic("tqwebp/cost: probability 0 is not codable")
	}
	return Cost(zeroCostQ8[p])
}

// BitCostOne returns the exact price of writing the true branch at Q8
// probability p, that is at false-probability p/256. It panics when p is
// 0, mirroring boolenc.
func BitCostOne(p uint8) Cost {
	if p == 0 {
		panic("tqwebp/cost: probability 0 is not codable")
	}
	return Cost(zeroCostQ8[256-int(p)])
}

// BitCost returns the exact price of writing bit at Q8 probability p.
func BitCost(p uint8, bit bool) Cost {
	if bit {
		return BitCostOne(p)
	}
	return BitCostZero(p)
}

// Literal returns the price of an L(n) field: n even-odds bits, most
// significant first. It panics when n is negative.
func Literal(n int) Cost {
	if n < 0 {
		panic("tqwebp/cost: negative literal width")
	}
	return Cost(n) * Unit
}

// SizeFieldBits is the width, in plain bits, of the frame tag's
// first-partition size field, RFC 6386 section 9.1. The field is
// fixed-width, so it prices the same whatever the partition contains.
const SizeFieldBits = 24

// SizeField returns the constant price of signalling the first
// partition's byte length.
func SizeField() Cost { return Literal(SizeFieldBits) }

// PartitionZeroBytes converts a partition-zero rate total into the
// whole-byte count that granularity actually buys: the frame tag signals
// the first partition's length in bytes, so header-side decisions pay
// off only when they cross a whole-byte boundary. The convention is
// round-up -- c units hold c/(8*Unit) bits, and partial bytes count.
//
// Arithmetic coding adds a small data-dependent redundancy to any ideal
// total, and boolenc's finish flush pads by up to three bytes beyond the
// last live byte, so the estimate sits within a few bytes of the real
// partition length rather than matching it exactly;
// TestPartitionZeroBytesTracksRealStreams pins that bound empirically.
func PartitionZeroBytes(c Cost) int {
	return int((c + 8*Unit - 1) / (8 * Unit))
}
