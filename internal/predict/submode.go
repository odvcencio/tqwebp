package predict

// This file is the scalar foundation of work package WP-2 slice 1: the
// native 4x4 sub-mode values of a B_PRED macroblock, the exact sample
// neighbourhood such a block predicts from, all ten scalar predictors,
// and the key-frame syntax that codes one sub-mode against its above and
// left neighbour contexts.
//
// Every rule here mirrors what a VP8 decoder does, because an encoder
// that predicts differently from the decoder drifts away from it. The
// decoder this repository gates against is golang.org/x/image/vp8, whose
// pred.go and reconstruct.go implement the same rules this file follows;
// the package's own tests decode crafted B_PRED frames through it and
// require pixel equality.
//
// Nothing in the production encoder path selects B_PRED yet. The whole
// file is inert until a later slice starts choosing sub-modes.

// SubMode names one 4x4 luma prediction sub-mode. The numeric values are
// the reference VP8 sub-mode numbers -- the order DC, TM, VE, HE, RD, VR,
// LD, VL, HD, HU in which golang.org/x/image/vp8's predfunc.go, libwebp,
// and the bitstream token tree itself enumerate the ten modes. Tooling,
// dumps, the contextual probability table below, and future mode-search
// code all speak this numbering, so it must never be reordered.
type SubMode uint8

const (
	// BDC fills the block with the average of the four samples above and
	// the four samples to the left. RFC name B_DC_PRED.
	BDC SubMode = iota
	// BTM adds the gradient of the row above to the column to the left,
	// exactly like the whole-block TM mode at 4x4 scale. RFC name
	// B_TM_PRED.
	BTM
	// BVE copies smoothed columns built from the row above, its top-left
	// corner sample included, and its top-right extension. RFC name
	// B_VE_PRED.
	BVE
	// BHE copies smoothed rows built from the column to the left and the
	// corner above it. RFC name B_HE_PRED.
	BHE
	// BRD propagates the corner, row above, and left column down and to
	// the right. RFC name B_RD_PRED.
	BRD
	// BVR interpolates between the row above and the left column around
	// the vertical axis tilted right. RFC name B_VR_PRED.
	BVR
	// BLD propagates the row above down and to the left, off its eight
	// available samples. RFC name B_LD_PRED.
	BLD
	// BVL interpolates between the row above and the top-right extension
	// around the vertical axis tilted left. RFC name B_VL_PRED.
	BVL
	// BHD interpolates between the left column and the row above around
	// the horizontal axis tilted down. RFC name B_HD_PRED.
	BHD
	// BHU propagates the left column up and to the right. RFC name
	// B_HU_PRED.
	BHU
	// NumSubModes counts the sub-modes above. A later slice's mode
	// search walks 0 to NumSubModes-1.
	NumSubModes
)

// String returns the RFC 6386 name of the sub-mode.
func (m SubMode) String() string {
	switch m {
	case BDC:
		return "B_DC_PRED"
	case BTM:
		return "B_TM_PRED"
	case BVE:
		return "B_VE_PRED"
	case BHE:
		return "B_HE_PRED"
	case BLD:
		return "B_LD_PRED"
	case BRD:
		return "B_RD_PRED"
	case BVR:
		return "B_VR_PRED"
	case BVL:
		return "B_VL_PRED"
	case BHD:
		return "B_HD_PRED"
	case BHU:
		return "B_HU_PRED"
	}
	return "invalid"
}

// SubNeighbors holds the twelve reconstructed samples one 4x4 block
// predicts from, RFC 6386 section 12.3.
//
//	Top[0:4] Top[4:8]
//	Corner   block
//	Left
//
// Top[0:4] are the samples directly above the block. Top[4:8] are the
// top-right extension: the four samples that continue the row above the
// block to its right. Left holds the four samples of the column to the
// left, topmost first. Corner is the single sample diagonally above and
// left of the block.
//
// Samples the frame does not provide carry the same fill values the
// decoder uses, so no availability flags are needed: every 4x4 predictor
// consumes all twelve entries unconditionally. See GatherSubNeighbors.
type SubNeighbors struct {
	Top    [8]uint8
	Left   [4]uint8
	Corner uint8
}

// GatherSubNeighbors fills nb with the reconstructed samples around the
// 4x4 block whose top-left sample sits at (x0, y0) of plane, which stores
// rows of stride samples.
//
// Argument paddedWidth is the plane width rounded up to whole macroblocks:
// ceil(frameWidth/16)*16. The reconstruction covers exactly that area, so
// it bounds both the genuine top-right extension and the replication rule
// below.
//
// Border rules, matching the decoder workspace setup of RFC 6386 section
// 12.2 and golang.org/x/image/vp8's prepareYBR:
//
//   - A block on the top macroblock row reads MissingTop (127) for its
//     whole row above, extension included, and for its corner.
//   - A block in the leftmost column reads MissingLeft (129) for its left
//     column, and for its corner when it has a row above.
//   - While the four top-right extension samples stay inside the block's
//     own macroblock they come from the continuation of the row above.
//     The same holds on a macroblock's first sub-block row even when the
//     extension crosses into the next macroblock column: those samples
//     belong to the macroblock row above, which is fully reconstructed.
//   - A rightmost-sub-block-column block (x0 at offset 12 of its
//     macroblock) below its macroblock's first sub-block row would read
//     the macroblock to its right, which a raster walk has not
//     reconstructed yet. The decoder never does that: prepareYBR seeds
//     the extension slots of every lower sub-block row from the
//     macroblock top border. So the extension here is the macroblock's
//     top border extension -- the row above the macroblock, MissingTop on
//     the frame's top macroblock row, replicating that border row's last
//     sample in the frame's rightmost macroblock column.
func GatherSubNeighbors(nb *SubNeighbors, plane []uint8, stride, x0, y0, paddedWidth int) {
	hasTop := y0 > 0
	hasLeft := x0 > 0

	if hasTop {
		row := plane[(y0-1)*stride:]
		copy(nb.Top[0:4], row[x0:x0+4])
		if x0%16 != 12 || y0%16 == 0 {
			// The extension stays inside the reconstructed area of the
			// rows above, or the block sits in its macroblock's first
			// sub-block row, where the crossing extension still belongs
			// to the already reconstructed macroblock row above.
			if x0+8 <= paddedWidth {
				copy(nb.Top[4:8], row[x0+4:x0+8])
			} else {
				last := row[paddedWidth-1]
				nb.Top[4], nb.Top[5], nb.Top[6], nb.Top[7] = last, last, last, last
			}
		} else if mbTop := y0 &^ 15; mbTop == 0 {
			// Lower sub-block row of the top macroblock row: the
			// macroblock top border is all MissingTop.
			for i := 4; i < 8; i++ {
				nb.Top[i] = MissingTop
			}
		} else {
			// Rightmost sub-block column below the macroblock's first
			// row: reuse the macroblock top border extension rather than
			// the unreconstructed macroblock to the right.
			border := plane[(mbTop-1)*stride:]
			if x0+8 <= paddedWidth {
				copy(nb.Top[4:8], border[x0+4:x0+8])
			} else {
				last := border[paddedWidth-1]
				nb.Top[4], nb.Top[5], nb.Top[6], nb.Top[7] = last, last, last, last
			}
		}
	} else {
		for i := range nb.Top {
			nb.Top[i] = MissingTop
		}
	}

	for j := 0; j < 4; j++ {
		if hasLeft {
			nb.Left[j] = plane[(y0+j)*stride+x0-1]
		} else {
			nb.Left[j] = MissingLeft
		}
	}

	switch {
	case !hasTop:
		nb.Corner = MissingTop
	case !hasLeft:
		nb.Corner = MissingLeft
	default:
		nb.Corner = plane[(y0-1)*stride+x0-1]
	}
}

// PredictSub writes the 4x4 predictor for sub-mode m into dst, which must
// hold four rows of stride samples. The neighbourhood must have been
// gathered for the same block position by GatherSubNeighbors.
func PredictSub(dst []uint8, stride int, m SubMode, nb *SubNeighbors) {
	switch m {
	case BDC:
		predictSubDC(dst, stride, nb)
	case BTM:
		predictSubTM(dst, stride, nb)
	case BVE:
		predictSubVE(dst, stride, nb)
	case BHE:
		predictSubHE(dst, stride, nb)
	case BLD:
		predictSubLD(dst, stride, nb)
	case BRD:
		predictSubRD(dst, stride, nb)
	case BVR:
		predictSubVR(dst, stride, nb)
	case BVL:
		predictSubVL(dst, stride, nb)
	case BHD:
		predictSubHD(dst, stride, nb)
	case BHU:
		predictSubHU(dst, stride, nb)
	default:
		panic("tqwebp/predict: unknown sub-mode")
	}
}

// avg2 is the two-sample interpolation filter (a+b+1)/2 the diagonal
// modes use, RFC 6386 section 12.3. Both inputs are sample values, hence
// non-negative, so integer division is exact.
func avg2(a, b int32) uint8 { return uint8((a + b + 1) >> 1) }

// avg3 is the three-sample interpolation filter (a+2b+c+2)/4, centred on
// b, the diagonal modes use.
func avg3(a, b, c int32) uint8 { return uint8((a + 2*b + c + 2) >> 2) }

// predictSubDC averages the four samples above and the four to the left.
// Unlike the whole-block DC mode there are no edge variants: the decoder
// sums all eight samples whatever borders are missing, because the fill
// values 127 and 129 already stand in for them.
func predictSubDC(dst []uint8, stride int, nb *SubNeighbors) {
	sum := int32(4) // rounding term of the /8 average
	for i := 0; i < 4; i++ {
		sum += int32(nb.Top[i]) + int32(nb.Left[i])
	}
	v := uint8(sum >> 3)
	for y := 0; y < 4; y++ {
		fillRow(dst[y*stride:y*stride+4], v)
	}
}

// predictSubTM scales the gradient of the row above onto every left
// sample, from the corner, exactly like the whole-block TM mode.
func predictSubTM(dst []uint8, stride int, nb *SubNeighbors) {
	corner := int32(nb.Corner)
	for y := 0; y < 4; y++ {
		base := int32(nb.Left[y]) - corner
		row := dst[y*stride : y*stride+4]
		for x := 0; x < 4; x++ {
			row[x] = clamp8(base + int32(nb.Top[x]))
		}
	}
}

// predictSubVE builds each output column from a three-tap horizontal
// average of the row above, centred on that column's top sample. The
// first column includes the corner sample, and the last column reaches
// into the top-right extension.
func predictSubVE(dst []uint8, stride int, nb *SubNeighbors) {
	t := &nb.Top
	a, b, c, d, e, f := int32(nb.Corner), int32(t[0]), int32(t[1]), int32(t[2]), int32(t[3]), int32(t[4])
	cols := [4]uint8{
		avg3(a, b, c),
		avg3(b, c, d),
		avg3(c, d, e),
		avg3(d, e, f),
	}
	for y := 0; y < 4; y++ {
		row := dst[y*stride : y*stride+4]
		copy(row[:], cols[:])
	}
}

// predictSubHE builds each output row from a three-tap vertical average
// of the left column, centred on that row's left sample. The top row
// smooths towards the corner; the bottom row repeats its own sample.
func predictSubHE(dst []uint8, stride int, nb *SubNeighbors) {
	l := &nb.Left
	p, q, r, s := int32(l[0]), int32(l[1]), int32(l[2]), int32(l[3])
	rows := [4]uint8{
		avg3(int32(nb.Corner), p, q),
		avg3(r, q, p),
		avg3(s, r, q),
		avg3(s, s, r),
	}
	for y := 0; y < 4; y++ {
		fillRow(dst[y*stride:y*stride+4], rows[y])
	}
}

// predictSubLD propagates the row above, extension included, down and to
// the left along anti-diagonals of three-tap averages. Its last
// anti-diagonal repeats the eighth sample, which keeps the mode defined
// without any sample beyond Top[7].
func predictSubLD(dst []uint8, stride int, nb *SubNeighbors) {
	t := &nb.Top
	a, b, c, d := int32(t[0]), int32(t[1]), int32(t[2]), int32(t[3])
	e, f, g, h := int32(t[4]), int32(t[5]), int32(t[6]), int32(t[7])
	abc, bcd, cde, def := avg3(a, b, c), avg3(b, c, d), avg3(c, d, e), avg3(d, e, f)
	efg, fgh, ghh := avg3(e, f, g), avg3(f, g, h), avg3(g, h, h)

	grid := [4][4]uint8{
		{abc, bcd, cde, def},
		{bcd, cde, def, efg},
		{cde, def, efg, fgh},
		{def, efg, fgh, ghh},
	}
	writeGrid(dst, stride, &grid)
}

// predictSubVL interpolates pairs of above-row samples down the even rows
// and three-tap averages down the odd rows. Like BLD it reads only the
// row above and its extension.
func predictSubVL(dst []uint8, stride int, nb *SubNeighbors) {
	t := &nb.Top
	a, b, c, d := int32(t[0]), int32(t[1]), int32(t[2]), int32(t[3])
	e, f, g, h := int32(t[4]), int32(t[5]), int32(t[6]), int32(t[7])
	ab, bc, cd, de := avg2(a, b), avg2(b, c), avg2(c, d), avg2(d, e)
	abc, bcd, cde, def := avg3(a, b, c), avg3(b, c, d), avg3(c, d, e), avg3(d, e, f)
	efg, fgh := avg3(e, f, g), avg3(f, g, h)

	grid := [4][4]uint8{
		{ab, bc, cd, de},
		{abc, bcd, cde, def},
		{bc, cd, de, efg},
		{bcd, cde, def, fgh},
	}
	writeGrid(dst, stride, &grid)
}

// predictSubRD propagates the corner, the row above, and the left column
// down and to the right along diagonals of three-tap averages.
func predictSubRD(dst []uint8, stride int, nb *SubNeighbors) {
	l := &nb.Left
	s, r, q, p := int32(l[3]), int32(l[2]), int32(l[1]), int32(l[0])
	a := int32(nb.Corner)
	b, c, d, e := int32(nb.Top[0]), int32(nb.Top[1]), int32(nb.Top[2]), int32(nb.Top[3])
	srq, rqp, qpa, pab := avg3(s, r, q), avg3(r, q, p), avg3(q, p, a), avg3(p, a, b)
	abc, bcd, cde := avg3(a, b, c), avg3(b, c, d), avg3(c, d, e)

	grid := [4][4]uint8{
		{pab, abc, bcd, cde},
		{qpa, pab, abc, bcd},
		{rqp, qpa, pab, abc},
		{srq, rqp, qpa, pab},
	}
	writeGrid(dst, stride, &grid)
}

// predictSubVR blends the row above and the left column around the
// vertical axis tilted right: two-sample averages run down the first
// superdiagonal and three-tap averages down the diagonals either side.
func predictSubVR(dst []uint8, stride int, nb *SubNeighbors) {
	l := &nb.Left
	r, q, p := int32(l[2]), int32(l[1]), int32(l[0])
	a := int32(nb.Corner)
	b, c, d, e := int32(nb.Top[0]), int32(nb.Top[1]), int32(nb.Top[2]), int32(nb.Top[3])
	ab, bc, cd, de := avg2(a, b), avg2(b, c), avg2(c, d), avg2(d, e)
	rqp, qpa, pab := avg3(r, q, p), avg3(q, p, a), avg3(p, a, b)
	abc, bcd, cde := avg3(a, b, c), avg3(b, c, d), avg3(c, d, e)

	grid := [4][4]uint8{
		{ab, bc, cd, de},
		{pab, abc, bcd, cde},
		{qpa, ab, bc, cd},
		{rqp, pab, abc, bcd},
	}
	writeGrid(dst, stride, &grid)
}

// predictSubHD blends the left column and the row above around the
// horizontal axis tilted down, mirror-symmetric to BVR about the main
// diagonal.
func predictSubHD(dst []uint8, stride int, nb *SubNeighbors) {
	l := &nb.Left
	s, r, q, p := int32(l[3]), int32(l[2]), int32(l[1]), int32(l[0])
	a := int32(nb.Corner)
	b, c, d := int32(nb.Top[0]), int32(nb.Top[1]), int32(nb.Top[2])
	sr, rq, qp, pa := avg2(s, r), avg2(r, q), avg2(q, p), avg2(p, a)
	srq, rqp, qpa, pab := avg3(s, r, q), avg3(r, q, p), avg3(q, p, a), avg3(p, a, b)
	abc, bcd := avg3(a, b, c), avg3(b, c, d)

	grid := [4][4]uint8{
		{pa, pab, abc, bcd},
		{qp, qpa, pa, pab},
		{rq, rqp, qp, qpa},
		{sr, srq, rq, rqp},
	}
	writeGrid(dst, stride, &grid)
}

// predictSubHU propagates the left column up and to the right along
// rising diagonals, repeating the bottom left sample across the bottom
// rows once the column runs out.
func predictSubHU(dst []uint8, stride int, nb *SubNeighbors) {
	l := &nb.Left
	s, r, q, p := int32(l[3]), int32(l[2]), int32(l[1]), int32(l[0])
	pq, qr, rs := avg2(p, q), avg2(q, r), avg2(r, s)
	pqr, qrs, rss := avg3(p, q, r), avg3(q, r, s), avg3(r, s, s)
	sss := nb.Left[3]

	grid := [4][4]uint8{
		{pq, pqr, qr, qrs},
		{qr, qrs, rs, rss},
		{rs, rss, sss, sss},
		{sss, sss, sss, sss},
	}
	writeGrid(dst, stride, &grid)
}

// writeGrid stores a computed 4x4 predictor into dst.
func writeGrid(dst []uint8, stride int, grid *[4][4]uint8) {
	for y := 0; y < 4; y++ {
		copy(dst[y*stride:y*stride+4], grid[y][:])
	}
}

// KeyFrameSubModeProbs holds the probabilities a key frame codes 4x4
// sub-modes against, RFC 6386 section 11.4 (the key-frame sub-mode
// table). It is extracted verbatim from golang.org/x/image/vp8's pred.go
// -- its predProb table -- so any future re-extraction is a plain text
// diff. The outer index is the above neighbour's SubMode, the middle
// index the left neighbour's, both in this package's native numbering,
// and the inner array holds the nine token-tree probabilities in node
// order.
//
// A key frame starts every macroblock row and column from BDC wherever
// no real sub-block sits above or to the left, because the reference
// decoder seeds its neighbour contexts with B_DC_PRED.
var KeyFrameSubModeProbs = [NumSubModes][NumSubModes][9]uint8{
	{ // native above DC
		{231, 120, 48, 89, 115, 113, 120, 152, 112}, // native left DC
		{152, 179, 64, 126, 170, 118, 46, 70, 95},   // native left TM
		{175, 69, 143, 80, 85, 82, 72, 155, 103},    // native left VE
		{56, 58, 10, 171, 218, 189, 17, 13, 152},    // native left HE
		{114, 26, 17, 163, 44, 195, 21, 10, 173},    // native left RD
		{121, 24, 80, 195, 26, 62, 44, 64, 85},      // native left VR
		{144, 71, 10, 38, 171, 213, 144, 34, 26},    // native left LD
		{170, 46, 55, 19, 136, 160, 33, 206, 71},    // native left VL
		{63, 20, 8, 114, 114, 208, 12, 9, 226},      // native left HD
		{81, 40, 11, 96, 182, 84, 29, 16, 36},       // native left HU
	},
	{ // native above TM
		{134, 183, 89, 137, 98, 101, 106, 165, 148}, // native left DC
		{72, 187, 100, 130, 157, 111, 32, 75, 80},   // native left TM
		{66, 102, 167, 99, 74, 62, 40, 234, 128},    // native left VE
		{41, 53, 9, 178, 241, 141, 26, 8, 107},      // native left HE
		{74, 43, 26, 146, 73, 166, 49, 23, 157},     // native left RD
		{65, 38, 105, 160, 51, 52, 31, 115, 128},    // native left VR
		{104, 79, 12, 27, 217, 255, 87, 17, 7},      // native left LD
		{87, 68, 71, 44, 114, 51, 15, 186, 23},      // native left VL
		{47, 41, 14, 110, 182, 183, 21, 17, 194},    // native left HD
		{66, 45, 25, 102, 197, 189, 23, 18, 22},     // native left HU
	},
	{ // native above VE
		{88, 88, 147, 150, 42, 46, 45, 196, 205}, // native left DC
		{43, 97, 183, 117, 85, 38, 35, 179, 61},  // native left TM
		{39, 53, 200, 87, 26, 21, 43, 232, 171},  // native left VE
		{56, 34, 51, 104, 114, 102, 29, 93, 77},  // native left HE
		{39, 28, 85, 171, 58, 165, 90, 98, 64},   // native left RD
		{34, 22, 116, 206, 23, 34, 43, 166, 73},  // native left VR
		{107, 54, 32, 26, 51, 1, 81, 43, 31},     // native left LD
		{68, 25, 106, 22, 64, 171, 36, 225, 114}, // native left VL
		{34, 19, 21, 102, 132, 188, 16, 76, 124}, // native left HD
		{62, 18, 78, 95, 85, 57, 50, 48, 51},     // native left HU
	},
	{ // native above HE
		{193, 101, 35, 159, 215, 111, 89, 46, 111}, // native left DC
		{60, 148, 31, 172, 219, 228, 21, 18, 111},  // native left TM
		{112, 113, 77, 85, 179, 255, 38, 120, 114}, // native left VE
		{40, 42, 1, 196, 245, 209, 10, 25, 109},    // native left HE
		{88, 43, 29, 140, 166, 213, 37, 43, 154},   // native left RD
		{61, 63, 30, 155, 67, 45, 68, 1, 209},      // native left VR
		{100, 80, 8, 43, 154, 1, 51, 26, 71},       // native left LD
		{142, 78, 78, 16, 255, 128, 34, 197, 171},  // native left VL
		{41, 40, 5, 102, 211, 183, 4, 1, 221},      // native left HD
		{51, 50, 17, 168, 209, 192, 23, 25, 82},    // native left HU
	},
	{ // native above RD
		{138, 31, 36, 171, 27, 166, 38, 44, 229}, // native left DC
		{67, 87, 58, 169, 82, 115, 26, 59, 179},  // native left TM
		{63, 59, 90, 180, 59, 166, 93, 73, 154},  // native left VE
		{40, 40, 21, 116, 143, 209, 34, 39, 175}, // native left HE
		{47, 15, 16, 183, 34, 223, 49, 45, 183},  // native left RD
		{46, 17, 33, 183, 6, 98, 15, 32, 183},    // native left VR
		{57, 46, 22, 24, 128, 1, 54, 17, 37},     // native left LD
		{65, 32, 73, 115, 28, 128, 23, 128, 205}, // native left VL
		{40, 3, 9, 115, 51, 192, 18, 6, 223},     // native left HD
		{87, 37, 9, 115, 59, 77, 64, 21, 47},     // native left HU
	},
	{ // native above VR
		{104, 55, 44, 218, 9, 54, 53, 130, 226},  // native left DC
		{64, 90, 70, 205, 40, 41, 23, 26, 57},    // native left TM
		{54, 57, 112, 184, 5, 41, 38, 166, 213},  // native left VE
		{30, 34, 26, 133, 152, 116, 10, 32, 134}, // native left HE
		{39, 19, 53, 221, 26, 114, 32, 73, 255},  // native left RD
		{31, 9, 65, 234, 2, 15, 1, 118, 73},      // native left VR
		{75, 32, 12, 51, 192, 255, 160, 43, 51},  // native left LD
		{88, 31, 35, 67, 102, 85, 55, 186, 85},   // native left VL
		{56, 21, 23, 111, 59, 205, 45, 37, 192},  // native left HD
		{55, 38, 70, 124, 73, 102, 1, 34, 98},    // native left HU
	},
	{ // native above LD
		{125, 98, 42, 88, 104, 85, 117, 175, 82}, // native left DC
		{95, 84, 53, 89, 128, 100, 113, 101, 45}, // native left TM
		{75, 79, 123, 47, 51, 128, 81, 171, 1},   // native left VE
		{57, 17, 5, 71, 102, 57, 53, 41, 49},     // native left HE
		{38, 33, 13, 121, 57, 73, 26, 1, 85},     // native left RD
		{41, 10, 67, 138, 77, 110, 90, 47, 114},  // native left VR
		{115, 21, 2, 10, 102, 255, 166, 23, 6},   // native left LD
		{101, 29, 16, 10, 85, 128, 101, 196, 26}, // native left VL
		{57, 18, 10, 102, 102, 213, 34, 20, 43},  // native left HD
		{117, 20, 15, 36, 163, 128, 68, 1, 26},   // native left HU
	},
	{ // native above VL
		{102, 61, 71, 37, 34, 53, 31, 243, 192},  // native left DC
		{69, 60, 71, 38, 73, 119, 28, 222, 37},   // native left TM
		{68, 45, 128, 34, 1, 47, 11, 245, 171},   // native left VE
		{62, 17, 19, 70, 146, 85, 55, 62, 70},    // native left HE
		{37, 43, 37, 154, 100, 163, 85, 160, 1},  // native left RD
		{63, 9, 92, 136, 28, 64, 32, 201, 85},    // native left VR
		{75, 15, 9, 9, 64, 255, 184, 119, 16},    // native left LD
		{86, 6, 28, 5, 64, 255, 25, 248, 1},      // native left VL
		{56, 8, 17, 132, 137, 255, 55, 116, 128}, // native left HD
		{58, 15, 20, 82, 135, 57, 26, 121, 40},   // native left HU
	},
	{ // native above HD
		{164, 50, 31, 137, 154, 133, 25, 35, 218}, // native left DC
		{51, 103, 44, 131, 131, 123, 31, 6, 158},  // native left TM
		{86, 40, 64, 135, 148, 224, 45, 183, 128}, // native left VE
		{22, 26, 17, 131, 240, 154, 14, 1, 209},   // native left HE
		{45, 16, 21, 91, 64, 222, 7, 1, 197},      // native left RD
		{56, 21, 39, 155, 60, 138, 23, 102, 213},  // native left VR
		{83, 12, 13, 54, 192, 255, 68, 47, 28},    // native left LD
		{85, 26, 85, 85, 128, 128, 32, 146, 171},  // native left VL
		{18, 11, 7, 63, 144, 171, 4, 4, 246},      // native left HD
		{35, 27, 10, 146, 174, 171, 12, 26, 128},  // native left HU
	},
	{ // native above HU
		{190, 80, 35, 99, 180, 80, 126, 54, 45},     // native left DC
		{85, 126, 47, 87, 176, 51, 41, 20, 32},      // native left TM
		{101, 75, 128, 139, 118, 146, 116, 128, 85}, // native left VE
		{56, 41, 15, 176, 236, 85, 37, 9, 62},       // native left HE
		{71, 30, 17, 119, 118, 255, 17, 18, 138},    // native left RD
		{101, 38, 60, 138, 55, 70, 43, 26, 142},     // native left VR
		{146, 36, 19, 30, 171, 255, 97, 27, 20},     // native left LD
		{138, 45, 61, 62, 219, 1, 81, 188, 64},      // native left VL
		{32, 41, 20, 117, 151, 142, 20, 21, 163},    // native left HD
		{112, 19, 12, 61, 195, 128, 48, 4, 24},      // native left HU
	},
}

// BoolWriter is the subset of a VP8 boolean encoder the sub-mode tree
// walk needs. *boolenc.Encoder satisfies it; the interface keeps this
// package free of an encoder dependency, exactly as the predictor half
// of the package stays free of every dependency.
type BoolWriter interface {
	WriteBool(prob uint8, bit bool)
}

// SubModeTree is the key-frame sub-mode token tree, shaped as the three
// reference decoders walk it (RFC 6386 chapter 11). Even entries belong
// to a false branch, odd entries to a true branch. A non-positive entry
// is a leaf that names its SubMode negated; the B_DC_PRED leaf is
// therefore stored as 0. A positive entry is the array index of the next
// node pair, and the node at index i codes against probability slot i/2.
//
// The first four splits peel off DC, TM, and VE, then fork between the
// {HE, RD, VR} and {LD, VL, HD, HU} groups; the two probability slots of
// the HE group sit below that fork, which is why entries 6 and 7 are the
// node indices 8 and 12 rather than leaf names.
var SubModeTree = [18]int16{
	-int16(BDC), 2,
	-int16(BTM), 4,
	-int16(BVE), 6,
	8, 12,
	-int16(BHE), 10,
	-int16(BRD), -int16(BVR),
	-int16(BLD), 14,
	-int16(BVL), 16,
	-int16(BHD), -int16(BHU),
}

// subModePath is the decision sequence the tree walks to select one
// sub-mode: n decisions, each using probability slot probIdx[i] of the
// contextual row and emitting bit[i].
type subModePath struct {
	n       uint8
	probIdx [9]uint8
	bit     [9]bool
}

// subModePaths derives one path per sub-mode by walking SubModeTree once
// at start-up, so the tree alone decides what WriteSubMode emits.
var subModePaths = buildSubModePaths()

func buildSubModePaths() [NumSubModes]subModePath {
	var paths [NumSubModes]subModePath
	var probIdx [9]uint8
	var bit [9]bool

	var walk func(node, depth int)
	walk = func(node, depth int) {
		for b := 0; b < 2; b++ {
			t := SubModeTree[node+b]
			probIdx[depth] = uint8(node / 2)
			bit[depth] = b == 1
			if t <= 0 {
				m := SubMode(-t)
				paths[m].n = uint8(depth + 1)
				paths[m].probIdx = probIdx
				paths[m].bit = bit
				continue
			}
			walk(int(t), depth+1)
		}
	}
	walk(0, 0)
	return paths
}

// SubModePath reports the boolean decisions WriteSubMode issues for m,
// in coding order: probIdx names the entry of the context probability
// row each decision codes against, bit is the decision itself, and n
// counts them. It exists so the WP-2 rate model can price sub-mode
// choices without writing anything and without a second copy of the
// tree: the tree stays the single source of what the syntax emits.
func SubModePath(m SubMode) (probIdx [9]uint8, bit [9]bool, n int) {
	if m < 0 || m >= NumSubModes {
		panic("tqwebp/predict: unknown sub-mode")
	}
	p := &subModePaths[m]
	return p.probIdx, p.bit, int(p.n)
}

// WriteSubMode writes one 4x4 sub-mode into w, coding against the
// key-frame contextual probabilities selected by the block above
// (aboveCtx) and the block to the left (leftCtx).
//
// The caller updates those two contexts itself while it walks the
// macroblock in raster order, exactly as the decoder does: after each
// block is coded its mode becomes both the left context of the next
// block in its row and the above context of the block one row down in
// the same column.
func WriteSubMode(w BoolWriter, aboveCtx, leftCtx SubMode, m SubMode) {
	if m >= NumSubModes || aboveCtx >= NumSubModes || leftCtx >= NumSubModes {
		panic("tqwebp/predict: unknown sub-mode")
	}
	p := &KeyFrameSubModeProbs[aboveCtx][leftCtx]
	path := &subModePaths[m]
	for i := uint8(0); i < path.n; i++ {
		w.WriteBool(p[path.probIdx[i]], path.bit[i])
	}
}
