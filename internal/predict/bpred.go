package predict

// BMode names one VP8 4x4 luma predictor. The numeric values intentionally
// match the VP8 reference implementation's B_*_PRED values. They are also the
// indices used by the contextual key-frame probability table.
type BMode uint8

// The ten predictors available to a B_PRED macroblock. Keep this order stable:
// it is part of the VP8 bitstream model, not an encoder search preference.
const (
	BDC BMode = iota
	BTM
	BVE
	BHE
	BRD
	BVR
	BLD
	BVL
	BHD
	BHU
	NumBModes
)

// String returns the VP8 reference name of the 4x4 mode.
func (m BMode) String() string {
	switch m {
	case BDC:
		return "B_DC_PRED"
	case BTM:
		return "B_TM_PRED"
	case BVE:
		return "B_VE_PRED"
	case BHE:
		return "B_HE_PRED"
	case BRD:
		return "B_RD_PRED"
	case BVR:
		return "B_VR_PRED"
	case BLD:
		return "B_LD_PRED"
	case BVL:
		return "B_VL_PRED"
	case BHD:
		return "B_HD_PRED"
	case BHU:
		return "B_HU_PRED"
	default:
		return "invalid"
	}
}

// BNeighbors holds every reconstructed sample a 4x4 predictor may read.
// Top[0:4] is the row directly above the block. Top[4:8] is the top-right
// extension required by the diagonal modes. Left is the column directly to
// the left, and Corner is the sample diagonally above-left.
type BNeighbors struct {
	Top    [8]uint8
	Left   [4]uint8
	Corner uint8
}

// Predict4 writes a 4x4 B_PRED predictor to dst. The caller must provide four
// rows of at least four samples and a stride of at least four.
func Predict4(dst []uint8, stride int, mode BMode, nb *BNeighbors) {
	switch mode {
	case BDC:
		predict4DC(dst, stride, nb)
	case BTM:
		predict4TM(dst, stride, nb)
	case BVE:
		predict4VE(dst, stride, nb)
	case BHE:
		predict4HE(dst, stride, nb)
	case BRD:
		predict4RD(dst, stride, nb)
	case BVR:
		predict4VR(dst, stride, nb)
	case BLD:
		predict4LD(dst, stride, nb)
	case BVL:
		predict4VL(dst, stride, nb)
	case BHD:
		predict4HD(dst, stride, nb)
	case BHU:
		predict4HU(dst, stride, nb)
	default:
		panic("tqwebp/predict: unknown 4x4 mode")
	}
}

// BNeighborsForBlock gathers the decoder-equivalent samples for one of the
// sixteen luma subblocks in a B_PRED macroblock. plane is a reconstructed,
// macroblock-padded luma plane; block is the raster index 0 through 15.
//
// The four samples beyond a macroblock's right edge are special. Every
// right-edge subblock reuses the samples captured above the macroblock: the
// next macroblock's top row when it exists, the last top sample repeated at
// the frame's right edge, or MissingTop throughout the top macroblock row.
// This is the workspace behavior a VP8 decoder uses even for subblocks 7, 11,
// and 15, whose immediate top row lies inside the current macroblock.
func BNeighborsForBlock(plane []uint8, stride, mbw, mbx, mby, block int) BNeighbors {
	if mbw <= 0 || mbx < 0 || mbx >= mbw || mby < 0 || block < 0 || block >= 16 {
		panic("tqwebp/predict: invalid 4x4 block location")
	}
	if stride < mbw*16 || stride <= 0 || len(plane)%stride != 0 {
		panic("tqwebp/predict: invalid reconstructed luma plane")
	}

	bx, by := block%4, block/4
	x0, y0 := mbx*16+bx*4, mby*16+by*4
	if y0+4 > len(plane)/stride {
		panic("tqwebp/predict: 4x4 block outside reconstructed luma plane")
	}

	var nb BNeighbors
	if y0 == 0 {
		fill4(nb.Top[:4], MissingTop)
	} else {
		copy(nb.Top[:4], plane[(y0-1)*stride+x0:(y0-1)*stride+x0+4])
	}

	switch {
	case bx < 3 && y0 == 0:
		fill4(nb.Top[4:], MissingTop)
	case bx < 3:
		copy(nb.Top[4:], plane[(y0-1)*stride+x0+4:(y0-1)*stride+x0+8])
	case mby == 0:
		fill4(nb.Top[4:], MissingTop)
	case mbx == mbw-1:
		topRight := plane[(mby*16-1)*stride+mbx*16+15]
		fill4(nb.Top[4:], topRight)
	default:
		topRight := (mby*16-1)*stride + (mbx+1)*16
		copy(nb.Top[4:], plane[topRight:topRight+4])
	}

	if x0 == 0 {
		fill4(nb.Left[:], MissingLeft)
	} else {
		for y := 0; y < 4; y++ {
			nb.Left[y] = plane[(y0+y)*stride+x0-1]
		}
	}

	switch {
	case y0 == 0:
		nb.Corner = MissingTop
	case x0 == 0:
		nb.Corner = MissingLeft
	default:
		nb.Corner = plane[(y0-1)*stride+x0-1]
	}
	return nb
}

func predict4DC(dst []uint8, stride int, nb *BNeighbors) {
	sum := uint16(4)
	for i := 0; i < 4; i++ {
		sum += uint16(nb.Top[i]) + uint16(nb.Left[i])
	}
	v := uint8(sum / 8)
	for y := 0; y < 4; y++ {
		fill4(dst[y*stride:y*stride+4], v)
	}
}

func predict4TM(dst []uint8, stride int, nb *BNeighbors) {
	corner := int(nb.Corner)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			dst[y*stride+x] = clip8(int(nb.Left[y]) + int(nb.Top[x]) - corner)
		}
	}
}

func predict4VE(dst []uint8, stride int, nb *BNeighbors) {
	row := [4]uint8{
		avg3(nb.Corner, nb.Top[0], nb.Top[1]),
		avg3(nb.Top[0], nb.Top[1], nb.Top[2]),
		avg3(nb.Top[1], nb.Top[2], nb.Top[3]),
		avg3(nb.Top[2], nb.Top[3], nb.Top[4]),
	}
	for y := 0; y < 4; y++ {
		copy(dst[y*stride:y*stride+4], row[:])
	}
}

func predict4HE(dst []uint8, stride int, nb *BNeighbors) {
	rows := [4]uint8{
		avg3(nb.Corner, nb.Left[0], nb.Left[1]),
		avg3(nb.Left[0], nb.Left[1], nb.Left[2]),
		avg3(nb.Left[1], nb.Left[2], nb.Left[3]),
		avg3(nb.Left[2], nb.Left[3], nb.Left[3]),
	}
	for y, v := range rows {
		fill4(dst[y*stride:y*stride+4], v)
	}
}

func predict4RD(dst []uint8, stride int, nb *BNeighbors) {
	p, q, r, s := nb.Left[0], nb.Left[1], nb.Left[2], nb.Left[3]
	a, b, c, d, e := nb.Corner, nb.Top[0], nb.Top[1], nb.Top[2], nb.Top[3]
	put4(dst, stride, [16]uint8{
		avg3(p, a, b), avg3(a, b, c), avg3(b, c, d), avg3(c, d, e),
		avg3(q, p, a), avg3(p, a, b), avg3(a, b, c), avg3(b, c, d),
		avg3(r, q, p), avg3(q, p, a), avg3(p, a, b), avg3(a, b, c),
		avg3(s, r, q), avg3(r, q, p), avg3(q, p, a), avg3(p, a, b),
	})
}

func predict4VR(dst []uint8, stride int, nb *BNeighbors) {
	p, q, r := nb.Left[0], nb.Left[1], nb.Left[2]
	a, b, c, d, e := nb.Corner, nb.Top[0], nb.Top[1], nb.Top[2], nb.Top[3]
	put4(dst, stride, [16]uint8{
		avg2(a, b), avg2(b, c), avg2(c, d), avg2(d, e),
		avg3(p, a, b), avg3(a, b, c), avg3(b, c, d), avg3(c, d, e),
		avg3(q, p, a), avg2(a, b), avg2(b, c), avg2(c, d),
		avg3(r, q, p), avg3(p, a, b), avg3(a, b, c), avg3(b, c, d),
	})
}

func predict4LD(dst []uint8, stride int, nb *BNeighbors) {
	t := nb.Top
	q := [7]uint8{
		avg3(t[0], t[1], t[2]), avg3(t[1], t[2], t[3]),
		avg3(t[2], t[3], t[4]), avg3(t[3], t[4], t[5]),
		avg3(t[4], t[5], t[6]), avg3(t[5], t[6], t[7]),
		avg3(t[6], t[7], t[7]),
	}
	put4(dst, stride, [16]uint8{
		q[0], q[1], q[2], q[3],
		q[1], q[2], q[3], q[4],
		q[2], q[3], q[4], q[5],
		q[3], q[4], q[5], q[6],
	})
}

func predict4VL(dst []uint8, stride int, nb *BNeighbors) {
	t := nb.Top
	h := [4]uint8{avg2(t[0], t[1]), avg2(t[1], t[2]), avg2(t[2], t[3]), avg2(t[3], t[4])}
	q := [6]uint8{
		avg3(t[0], t[1], t[2]), avg3(t[1], t[2], t[3]),
		avg3(t[2], t[3], t[4]), avg3(t[3], t[4], t[5]),
		avg3(t[4], t[5], t[6]), avg3(t[5], t[6], t[7]),
	}
	put4(dst, stride, [16]uint8{
		h[0], h[1], h[2], h[3],
		q[0], q[1], q[2], q[3],
		h[1], h[2], h[3], q[4],
		q[1], q[2], q[3], q[5],
	})
}

func predict4HD(dst []uint8, stride int, nb *BNeighbors) {
	p, q, r, s := nb.Left[0], nb.Left[1], nb.Left[2], nb.Left[3]
	a, b, c, d := nb.Corner, nb.Top[0], nb.Top[1], nb.Top[2]
	put4(dst, stride, [16]uint8{
		avg2(p, a), avg3(p, a, b), avg3(a, b, c), avg3(b, c, d),
		avg2(q, p), avg3(q, p, a), avg2(p, a), avg3(p, a, b),
		avg2(r, q), avg3(r, q, p), avg2(q, p), avg3(q, p, a),
		avg2(s, r), avg3(s, r, q), avg2(r, q), avg3(r, q, p),
	})
}

func predict4HU(dst []uint8, stride int, nb *BNeighbors) {
	p, q, r, s := nb.Left[0], nb.Left[1], nb.Left[2], nb.Left[3]
	put4(dst, stride, [16]uint8{
		avg2(p, q), avg3(p, q, r), avg2(q, r), avg3(q, r, s),
		avg2(q, r), avg3(q, r, s), avg2(r, s), avg3(r, s, s),
		avg2(r, s), avg3(r, s, s), s, s,
		s, s, s, s,
	})
}

func avg2(a, b uint8) uint8 {
	return uint8((uint16(a) + uint16(b) + 1) >> 1)
}

func avg3(a, b, c uint8) uint8 {
	return uint8((uint16(a) + 2*uint16(b) + uint16(c) + 2) >> 2)
}

func clip8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func fill4(dst []uint8, v uint8) {
	dst[0], dst[1], dst[2], dst[3] = v, v, v, v
}

func put4(dst []uint8, stride int, values [16]uint8) {
	for y := 0; y < 4; y++ {
		copy(dst[y*stride:y*stride+4], values[y*4:y*4+4])
	}
}
