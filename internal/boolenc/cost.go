package boolenc

// VP8 rate costs use eight fractional bits. One full bit therefore costs 256
// Q8 units and one byte costs 2048. These units match libwebp's encoder cost
// tables and keep every rate-distortion comparison integer-only.
const (
	CostScaleQ8 = uint64(256)
	ByteCostQ8  = 8 * CostScaleQ8
)

// entropyCostQ8 is libwebp's canonical fixed-point approximation from
// src/dsp/cost.c. The rarest outcomes are deliberately capped at seven bits;
// this is the encoder cost convention, not a runtime floating-point formula.
// Index p prices a false decision whose probability is p/256. A true decision
// uses index 255-p, exactly as libwebp's VP8BitCost does.
var entropyCostQ8 = [256]uint16{
	1792, 1792, 1792, 1536, 1536, 1408, 1366, 1280, 1280, 1216, 1178, 1152,
	1110, 1076, 1061, 1024, 1024, 992, 968, 951, 939, 911, 896, 878,
	871, 854, 838, 820, 811, 794, 786, 768, 768, 752, 740, 732,
	720, 709, 704, 690, 683, 672, 666, 655, 647, 640, 631, 622,
	615, 607, 598, 592, 586, 576, 572, 564, 559, 555, 547, 541,
	534, 528, 522, 512, 512, 504, 500, 494, 488, 483, 477, 473,
	467, 461, 458, 452, 448, 443, 438, 434, 427, 424, 419, 415,
	410, 406, 403, 399, 394, 390, 384, 384, 377, 374, 370, 366,
	362, 359, 355, 351, 347, 342, 342, 336, 333, 330, 326, 323,
	320, 316, 312, 308, 305, 302, 299, 296, 293, 288, 287, 283,
	280, 277, 274, 272, 268, 266, 262, 256, 256, 256, 251, 248,
	245, 242, 240, 237, 234, 232, 228, 226, 223, 221, 218, 216,
	214, 211, 208, 205, 203, 201, 198, 196, 192, 191, 188, 187,
	183, 181, 179, 176, 175, 171, 171, 168, 165, 163, 160, 159,
	156, 154, 152, 150, 148, 146, 144, 142, 139, 138, 135, 133,
	131, 128, 128, 125, 123, 121, 119, 117, 115, 113, 111, 110,
	107, 105, 103, 102, 100, 98, 96, 94, 92, 91, 89, 86,
	86, 83, 82, 80, 77, 76, 74, 73, 71, 69, 67, 66,
	64, 63, 61, 59, 57, 55, 54, 52, 51, 49, 47, 46,
	44, 43, 41, 40, 38, 36, 35, 33, 32, 30, 29, 27,
	25, 24, 22, 21, 19, 18, 16, 15, 13, 12, 10, 9,
	7, 6, 4, 3,
}

// DecisionCostQ8 returns the fixed-point cost of one boolean decision. prob
// is the probability of false on a scale of 256. Probability zero is not
// codable and panics for the same reason Encoder.WriteBool does.
func DecisionCostQ8(prob uint8, bit bool) uint16 {
	if prob == 0 {
		panic("tqwebp/boolenc: probability 0 is not codable")
	}
	if bit {
		return entropyCostQ8[255-prob]
	}
	return entropyCostQ8[prob]
}

// LiteralCostQ8 returns the cost of a literal whose bits are coded at even
// odds. Its value cannot affect the cost.
func LiteralCostQ8(bits int) uint64 {
	if bits < 0 {
		panic("tqwebp/boolenc: negative literal width")
	}
	if uint64(bits) > ^uint64(0)/CostScaleQ8 {
		panic("tqwebp/boolenc: literal cost overflows uint64")
	}
	return uint64(bits) * CostScaleQ8
}

// BytesCostQ8 converts an exact partition byte count into Q8 rate units.
func BytesCostQ8(bytes uint64) uint64 {
	if bytes > ^uint64(0)/ByteCostQ8 {
		panic("tqwebp/boolenc: byte cost overflows uint64")
	}
	return bytes * ByteCostQ8
}

// CostBytesCeil converts a Q8 entropy estimate to the smallest whole-byte
// count that can hold that many bits. This is an estimate of partition
// pressure; Encoder.ProjectedLen reports the exact serialized length of an
// already-written partition.
func CostBytesCeil(costQ8 uint64) uint64 {
	bytes := costQ8 / ByteCostQ8
	if costQ8%ByteCostQ8 != 0 {
		bytes++
	}
	return bytes
}

// CostWriter prices a sequence of VP8 decisions without producing bytes. Its
// WriteBool method has the arithmetic encoder's signature, so a local syntax
// walker can target either one without coupling the production encoder to an
// interface or paying dynamic-dispatch allocations.
type CostWriter struct {
	costQ8 uint64
}

// WriteBool adds one decision's cost.
func (w *CostWriter) WriteBool(prob uint8, bit bool) {
	w.costQ8 += uint64(DecisionCostQ8(prob, bit))
}

// CostQ8 returns the accumulated cost in 1/256-bit units.
func (w *CostWriter) CostQ8() uint64 { return w.costQ8 }

// Reset clears the accumulated cost for reuse.
func (w *CostWriter) Reset() { w.costQ8 = 0 }
