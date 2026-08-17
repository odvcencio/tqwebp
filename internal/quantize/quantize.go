// Package quantize maps the public quality knob onto a VP8 quantizer
// index, and the index onto the dequantization factors the bitstream
// implies.
//
// The factor tables are normative. RFC 6386 section 14.1 prints them, and
// every decoder holds the same two 128-entry tables. The encoder never
// writes factors into the stream; it writes the index, and the decoder
// looks the factors up. The encoder must therefore use exactly the
// factors the decoder will derive, or the two pictures drift apart.
package quantize

// Index is a VP8 base quantizer index. It runs from 0, the finest step,
// to 127, the coarsest.
type Index int

// Factors holds the direct-current and alternating-current
// dequantization factors of one coefficient plane.
type Factors struct {
	DC int16
	AC int16
}

// Quantizer holds every factor one frame needs at a given index.
type Quantizer struct {
	// Index is the base index the frame header carries.
	Index Index
	// Y1 holds the luma factors. In a 16x16 predicted macroblock only the
	// alternating-current factor applies, because the luma
	// direct-current coefficients travel through the Y2 block.
	Y1 Factors
	// Y2 holds the factors of the Walsh-Hadamard block that carries the
	// sixteen luma direct-current values.
	Y2 Factors
	// UV holds the chroma factors.
	UV Factors
}

// New returns the factors RFC 6386 section 9.6 derives from a base index,
// with every optional delta at zero. Values outside 0 to 127 clamp.
//
// The three special cases below are normative and easy to get wrong:
// the Y2 direct-current factor doubles, the Y2 alternating-current factor
// scales by 155/100 and never falls below 8, and the chroma
// direct-current index clamps at 117 instead of 127.
func New(index Index) Quantizer {
	q := clampIndex(index)
	// The 155/100 scale runs in 32 bits on purpose. The largest table
	// entry is 284, and 284*155 overflows a signed 16-bit multiply.
	y2ac := int16(int32(TableAC[q]) * 155 / 100)
	if y2ac < 8 {
		y2ac = 8
	}
	return Quantizer{
		Index: q,
		Y1: Factors{
			DC: int16(TableDC[q]),
			AC: int16(TableAC[q]),
		},
		Y2: Factors{
			DC: int16(TableDC[q]) * 2,
			AC: y2ac,
		},
		UV: Factors{
			DC: int16(TableDC[min(int(q), 117)]),
			AC: int16(TableAC[q]),
		},
	}
}

func clampIndex(q Index) Index {
	if q < 0 {
		return 0
	}
	if q > 127 {
		return 127
	}
	return q
}

// TableDC and TableAC are the dequantization tables of RFC 6386 section
// 14.1. Test T9 pins them against factors recovered from decoded frames.
var (
	TableDC = [128]uint16{
		4, 5, 6, 7, 8, 9, 10, 10,
		11, 12, 13, 14, 15, 16, 17, 17,
		18, 19, 20, 20, 21, 21, 22, 22,
		23, 23, 24, 25, 25, 26, 27, 28,
		29, 30, 31, 32, 33, 34, 35, 36,
		37, 37, 38, 39, 40, 41, 42, 43,
		44, 45, 46, 46, 47, 48, 49, 50,
		51, 52, 53, 54, 55, 56, 57, 58,
		59, 60, 61, 62, 63, 64, 65, 66,
		67, 68, 69, 70, 71, 72, 73, 74,
		75, 76, 76, 77, 78, 79, 80, 81,
		82, 83, 84, 85, 86, 87, 88, 89,
		91, 93, 95, 96, 98, 100, 101, 102,
		104, 106, 108, 110, 112, 114, 116, 118,
		122, 124, 126, 128, 130, 132, 134, 136,
		138, 140, 143, 145, 148, 151, 154, 157,
	}
	TableAC = [128]uint16{
		4, 5, 6, 7, 8, 9, 10, 11,
		12, 13, 14, 15, 16, 17, 18, 19,
		20, 21, 22, 23, 24, 25, 26, 27,
		28, 29, 30, 31, 32, 33, 34, 35,
		36, 37, 38, 39, 40, 41, 42, 43,
		44, 45, 46, 47, 48, 49, 50, 51,
		52, 53, 54, 55, 56, 57, 58, 60,
		62, 64, 66, 68, 70, 72, 74, 76,
		78, 80, 82, 84, 86, 88, 90, 92,
		94, 96, 98, 100, 102, 104, 106, 108,
		110, 112, 114, 116, 119, 122, 125, 128,
		131, 134, 137, 140, 143, 146, 149, 152,
		155, 158, 161, 164, 167, 170, 173, 177,
		181, 185, 189, 193, 197, 201, 205, 209,
		213, 217, 221, 225, 229, 234, 239, 245,
		249, 254, 259, 264, 269, 274, 279, 284,
	}
)
