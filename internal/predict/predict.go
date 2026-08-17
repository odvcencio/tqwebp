// Package predict builds VP8 intra predictors for whole 16x16 luma blocks
// and whole 8x8 chroma blocks, as RFC 6386 chapter 12 defines them.
//
// The package mirrors the decoder exactly. A VP8 decoder predicts from the
// reconstructed samples above and to the left of the block, never from the
// original picture, and it never applies the loop filter before it
// predicts inside one frame. The encoder must read the same samples, or
// its picture drifts away from the decoder's.
//
// Work package WP-1 codes every macroblock with a 16x16 luma mode, so the
// ten 4x4 sub-modes of B_PRED do not appear here. They arrive with WP-2.
package predict

// Mode names one intra prediction mode. The values follow RFC 6386
// section 11.2, so DC is 0, V is 1, H is 2, and TM is 3.
type Mode uint8

// The four modes a 16x16 luma block or an 8x8 chroma block can use.
const (
	// DC fills the block with the average of the row above and the column
	// to the left.
	DC Mode = iota
	// V copies the row above down the block.
	V
	// H copies the column to the left across the block.
	H
	// TM adds the gradient of the row above to the column to the left.
	TM
	// NumModes counts the modes above. Mode search walks 0 to NumModes-1.
	NumModes
)

// String returns the RFC 6386 name of the mode.
func (m Mode) String() string {
	switch m {
	case DC:
		return "DC_PRED"
	case V:
		return "V_PRED"
	case H:
		return "H_PRED"
	case TM:
		return "TM_PRED"
	}
	return "invalid"
}

// Defaults for samples outside the frame, from RFC 6386 section 12.2 and
// from the workspace setup of golang.org/x/image/vp8. A macroblock in the
// top row reads 127 above itself. A macroblock in the left column reads
// 129 to its left.
const (
	// MissingTop is the sample value above the top macroblock row.
	MissingTop uint8 = 127
	// MissingLeft is the sample value left of the leftmost macroblock.
	MissingLeft uint8 = 129
)

// Neighbors holds the reconstructed samples around one block. Top and
// Left must both hold Size samples. Callers fill them with MissingTop and
// MissingLeft where the frame ends, and set HasTop and HasLeft to false
// there, because only the DC mode changes shape at the frame edge.
type Neighbors struct {
	Top     []uint8
	Left    []uint8
	Corner  uint8
	HasTop  bool
	HasLeft bool
}

// Predict writes the size by size predictor for mode into dst, which must
// hold size rows of stride samples.
func Predict(dst []uint8, stride int, size int, mode Mode, nb *Neighbors) {
	switch mode {
	case DC:
		predictDC(dst, stride, size, nb)
	case V:
		predictV(dst, stride, size, nb)
	case H:
		predictH(dst, stride, size, nb)
	case TM:
		predictTM(dst, stride, size, nb)
	default:
		panic("tqwebp/predict: unknown mode")
	}
}

// predictDC fills the block with one average. The average covers the row
// above and the column to the left when both exist, one of them when only
// one exists, and neither when the block sits in the top left corner, in
// which case the predictor is the constant 128.
func predictDC(dst []uint8, stride, size int, nb *Neighbors) {
	var sum, count int32
	if nb.HasTop {
		for _, v := range nb.Top[:size] {
			sum += int32(v)
		}
		count += int32(size)
	}
	if nb.HasLeft {
		for _, v := range nb.Left[:size] {
			sum += int32(v)
		}
		count += int32(size)
	}

	var avg uint8 = 0x80
	if count > 0 {
		avg = uint8((sum + count/2) / count)
	}
	fill(dst, stride, size, avg)
}

func predictV(dst []uint8, stride, size int, nb *Neighbors) {
	for y := 0; y < size; y++ {
		copy(dst[y*stride:y*stride+size], nb.Top[:size])
	}
}

func predictH(dst []uint8, stride, size int, nb *Neighbors) {
	for y := 0; y < size; y++ {
		fillRow(dst[y*stride:y*stride+size], nb.Left[y])
	}
}

func predictTM(dst []uint8, stride, size int, nb *Neighbors) {
	corner := int32(nb.Corner)
	for y := 0; y < size; y++ {
		base := int32(nb.Left[y]) - corner
		row := dst[y*stride : y*stride+size]
		for x := 0; x < size; x++ {
			row[x] = clamp8(base + int32(nb.Top[x]))
		}
	}
}

func fill(dst []uint8, stride, size int, v uint8) {
	for y := 0; y < size; y++ {
		fillRow(dst[y*stride:y*stride+size], v)
	}
}

func fillRow(row []uint8, v uint8) {
	for i := range row {
		row[i] = v
	}
}

func clamp8(v int32) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
