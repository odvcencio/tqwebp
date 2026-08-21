// Package encoder holds the VP8 key-frame encoding pipeline: colour
// conversion, prediction, transforms, quantization, reconstruction, and
// serialization. The module root wraps it in the public interface.
//
// The package sits behind internal/ for one reason: the repository's own
// gate harness needs the encoder's reconstruction, and the exact-match
// gate would otherwise force that hook into the supported interface.
package encoder

import (
	"image"
	"io"

	"m31labs.dev/turboquant/blockdsp"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/container"
	"m31labs.dev/tqwebp/internal/frame"
	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/quantize"
	"m31labs.dev/tqwebp/internal/token"
	"m31labs.dev/tqwebp/internal/yuv"
)

// Block layout inside one macroblock record. The order matches the order
// a decoder reads the blocks in: the Walsh-Hadamard block first, then the
// sixteen luma blocks in raster order, then the four blue-difference
// blocks, then the four red-difference blocks.
const (
	blockY2   = 0
	blockLuma = 1
	blockU    = 17
	blockV    = 21
	numBlocks = 25
)

// quantizer rounding bias, as a fraction of the quantizer step. Pure
// truncation, which is what a plain dead-zone quantizer does, throws away
// almost a whole step of accuracy on every coefficient. A bias of three
// eighths rounds most coefficients to the nearer level and still pulls
// small ones to zero, which is the trade libwebp makes as well.
const (
	biasNumerator   = 3
	biasDenominator = 8
)

// Method 1 is the first effort tier that spends work on 4x4 luma search.
// The constants below are intentionally conservative until the exact
// fixed-point bit-cost and lambda model lands in the next WP-2 slice.
const (
	bPredMinMethod                 = 1
	bPredMinImprovementNumerator   = 1
	bPredMinImprovementDenominator = 8
	bPredPenaltyDivisor            = 4
	bPredTrialPenaltyMultiplier    = 32
	bPredMinCoefficientReduction   = 8
)

// macroblock holds everything the serializer needs about one macroblock.
// The analysis pass fills it; the serialization pass reads it. Two passes
// exist because the skip probability in the frame header depends on how
// many macroblocks turn out to be skippable.
type macroblock struct {
	// useBPred selects sixteen 4x4 luma predictors instead of yMode's one
	// 16x16 predictor. B_PRED macroblocks carry their own luma DC
	// coefficients and therefore never carry a Y2 block.
	useBPred bool
	yMode    predict.Mode
	uvMode   predict.Mode
	skip     bool
	// levels holds quantized coefficient levels in scan order.
	levels [numBlocks][16]int16
	// nz marks the blocks that carry at least one coefficient. The token
	// writer feeds these flags into the neighbour contexts.
	nz [numBlocks]bool
}

// encoder holds one frame's state.
type encoder struct {
	cfg Config
	src *yuv.Planes
	rec *yuv.Planes
	q   quantize.Quantizer
	mbw int
	mbh int
	mbs []macroblock

	// bPredModes is allocated only when at least one macroblock actually
	// selects B_PRED. Keeping the sixteen modes out of macroblock preserves
	// the whole-block-only path's macroblock-slice allocation size.
	bPredModes [][16]predict.BMode

	// filterLevel is the loop filter strength the frame header signals.
	// It stays at 0 in this release: the encoder does not model the
	// filter, and level 0 keeps the decoder's picture equal to the
	// encoder's own reconstruction, which the exact-match test needs.
	filterLevel int

	// Scratch buffers, one macroblock wide, reused across the frame.
	predY [16 * 16]uint8
	bestY [16 * 16]uint8
	predU [8 * 8]uint8
	predV [8 * 8]uint8
	bestU [8 * 8]uint8
	bestV [8 * 8]uint8
	nbY   neighborBuf
	nbU   neighborBuf
	nbV   neighborBuf
}

// neighborBuf owns the sample arrays one Neighbors value points into. The
// encoder keeps one buffer per plane, because a chroma mode search holds
// two neighbour sets at once.
type neighborBuf struct {
	top  [16]uint8
	left [16]uint8
	nb   predict.Neighbors
}

func newEncoder(src *yuv.Planes, cfg Config) *encoder {
	return &encoder{
		cfg: cfg,
		src: src,
		rec: yuv.NewPlanes(src.Width, src.Height),
		q:   quantize.New(quantize.IndexForQuality(cfg.Quality)),
		mbw: src.MBW,
		mbh: src.MBH,
		mbs: make([]macroblock, src.MBW*src.MBH),
	}
}

// run analyses and reconstructs every macroblock, in the raster order a
// decoder uses. Each macroblock predicts from the reconstruction of its
// neighbours, so the order is not free.
func (e *encoder) run() {
	for mby := 0; mby < e.mbh; mby++ {
		for mbx := 0; mbx < e.mbw; mbx++ {
			e.encodeMacroblock(mbx, mby)
		}
	}
}

// encodeMacroblock chooses the prediction modes, codes the residual, and
// writes the reconstruction back into the recon planes.
func (e *encoder) encodeMacroblock(mbx, mby int) {
	mbIndex := mby*e.mbw + mbx
	mb := &e.mbs[mbIndex]

	if mb.useBPred {
		e.chooseChromaMode(mbx, mby, mb)
		e.codeBPredLuma(mbx, mby, mb, &e.bPredModes[mbIndex], false)
	} else {
		wholePredictionSSE := e.chooseLumaMode(mbx, mby, mb)
		e.chooseChromaMode(mbx, mby, mb)
		e.codeLuma(mbx, mby, mb)
		if e.cfg.Method >= bPredMinMethod && e.shouldTrialBPred(wholePredictionSSE) {
			e.tryBPredLuma(mbx, mby, mbIndex, mb)
		}
	}
	e.codeChroma(mbx, mby, mb)

	mb.skip = true
	for i := 0; i < numBlocks; i++ {
		if mb.nz[i] {
			mb.skip = false
			break
		}
	}
}

// setBPredModes records one complete B_PRED decision while preserving a nil
// mode plane for frames that stay on the whole-block path.
func (e *encoder) setBPredModes(mbIndex int, modes [16]predict.BMode) {
	if e.bPredModes == nil {
		e.bPredModes = make([][16]predict.BMode, len(e.mbs))
	}
	e.mbs[mbIndex].useBPred = true
	e.bPredModes[mbIndex] = modes
}

// chooseLumaMode picks the whole-block luma mode with the smallest sum of
// squared errors against the source, and leaves its predictor in bestY.
func (e *encoder) chooseLumaMode(mbx, mby int, mb *macroblock) int32 {
	nb := e.neighbors(&e.nbY, e.rec.Y, e.rec.YStride, mbx*16, mby*16, 16, mbx > 0, mby > 0)
	src := e.src.Y[(mby*16)*e.src.YStride+mbx*16:]

	best := int32(-1)
	for m := predict.Mode(0); m < predict.NumModes; m++ {
		predict.Predict(e.predY[:], 16, 16, m, nb)
		sse := blockdsp.SSE16x16(src, e.src.YStride, e.predY[:], 16)
		if best < 0 || sse < best {
			best = sse
			mb.yMode = m
			copy(e.bestY[:], e.predY[:])
		}
	}
	return best
}

// chooseChromaMode picks one mode for both chroma planes, because the
// bitstream carries one chroma mode per macroblock. The score is the sum
// of squared errors over both planes.
func (e *encoder) chooseChromaMode(mbx, mby int, mb *macroblock) {
	nbU := e.neighbors(&e.nbU, e.rec.U, e.rec.CStride, mbx*8, mby*8, 8, mbx > 0, mby > 0)
	nbV := e.neighbors(&e.nbV, e.rec.V, e.rec.CStride, mbx*8, mby*8, 8, mbx > 0, mby > 0)

	srcU := e.src.U[(mby*8)*e.src.CStride+mbx*8:]
	srcV := e.src.V[(mby*8)*e.src.CStride+mbx*8:]

	best := int32(-1)
	for m := predict.Mode(0); m < predict.NumModes; m++ {
		predict.Predict(e.predU[:], 8, 8, m, nbU)
		predict.Predict(e.predV[:], 8, 8, m, nbV)
		sse := sse8x8(srcU, e.src.CStride, e.predU[:], 8) + sse8x8(srcV, e.src.CStride, e.predV[:], 8)
		if best < 0 || sse < best {
			best = sse
			mb.uvMode = m
			copy(e.bestU[:], e.predU[:])
			copy(e.bestV[:], e.predV[:])
		}
	}
}

// codeLuma transforms, quantizes, and reconstructs the sixteen luma
// blocks. Their direct-current values travel together through the
// Walsh-Hadamard block, as RFC 6386 section 14.2 requires for a
// macroblock with a whole-block luma mode.
func (e *encoder) codeLuma(mbx, mby int, mb *macroblock) {
	srcBase := (mby * 16) * e.src.YStride
	var coeffs [16][16]int16
	for b := 0; b < 16; b++ {
		bx, by := (b%4)*4, (b/4)*4
		var residual [16]int16
		for y := 0; y < 4; y++ {
			srcRow := e.src.Y[srcBase+(by+y)*e.src.YStride+mbx*16+bx:]
			predRow := e.bestY[(by+y)*16+bx:]
			for x := 0; x < 4; x++ {
				residual[y*4+x] = int16(srcRow[x]) - int16(predRow[x])
			}
		}
		coeffs[b] = blockdsp.FDCT4x4(&residual)
	}

	// The Walsh-Hadamard block collects the sixteen direct-current
	// values, in the same raster order the subblocks sit in.
	var dc [16]int16
	for b := 0; b < 16; b++ {
		dc[b] = coeffs[b][0]
	}
	y2 := blockdsp.FWHT4x4(&dc)
	y2Levels := quantizeBlock(&y2, e.q.Y2)
	mb.levels[blockY2] = toScanOrder(&y2Levels)
	mb.nz[blockY2] = anyNonZero(&y2Levels, 0)

	y2Dequant := blockdsp.DequantizeBlock(&y2Levels, e.q.Y2.DC, e.q.Y2.AC)
	reconDC := blockdsp.IWHT4x4(&y2Dequant)

	for b := 0; b < 16; b++ {
		levels := quantizeBlock(&coeffs[b], e.q.Y1)
		// Position 0 belongs to the Walsh-Hadamard block, so this block
		// never codes it.
		levels[0] = 0
		mb.levels[blockLuma+b] = toScanOrder(&levels)
		mb.nz[blockLuma+b] = anyNonZero(&levels, 1)

		dequant := blockdsp.DequantizeBlock(&levels, e.q.Y1.DC, e.q.Y1.AC)
		dequant[0] = reconDC[b]
		residual := blockdsp.IDCT4x4(&dequant)

		bx, by := (b%4)*4, (b/4)*4
		e.reconstruct(e.rec.Y, e.rec.YStride, mbx*16+bx, mby*16+by, e.bestY[:], 16, bx, by, &residual)
	}
}

// tryBPredLuma compares one searched B_PRED candidate with the already-coded
// whole-block candidate. A rejection restores both the macroblock record and
// its exact reconstruction, and therefore leaves no B_PRED allocation or
// syntax footprint behind.
func (e *encoder) tryBPredLuma(mbx, mby, mbIndex int, mb *macroblock) {
	wholeDistortion := e.lumaMacroblockSSE(mbx, mby)
	penalty := e.bPredPenalty()
	if int64(wholeDistortion) <= penalty {
		return
	}

	wholeMB := *mb
	var wholeRecon [16 * 16]uint8
	copyLuma16(wholeRecon[:], 16, 0, 0, e.rec.Y, e.rec.YStride, mbx*16, mby*16)

	candidate := wholeMB
	var modes [16]predict.BMode
	e.codeBPredLuma(mbx, mby, &candidate, &modes, true)
	bPredDistortion := e.lumaMacroblockSSE(mbx, mby)
	wholeCoefficients := lumaCoefficientCount(&wholeMB)
	bPredCoefficients := lumaCoefficientCount(&candidate)
	if !admitBPred(wholeDistortion, bPredDistortion, penalty, wholeCoefficients, bPredCoefficients) {
		*mb = wholeMB
		copyLuma16(e.rec.Y, e.rec.YStride, mbx*16, mby*16, wholeRecon[:], 16, 0, 0)
		return
	}

	*mb = candidate
	e.setBPredModes(mbIndex, modes)
}

// admitBPred applies the provisional Method-1 admission policy. It requires
// a quantizer-scaled absolute distortion win, a one-eighth relative win, and
// enough retired coefficients to conservatively pay for sixteen submodes.
// Slice 3's exact fixed-point bit cost and lambda replace this proxy.
func admitBPred(wholeDistortion, bPredDistortion int32, penalty int64, wholeCoefficients, bPredCoefficients int) bool {
	improvement := int64(wholeDistortion) - int64(bPredDistortion)
	minRatioImprovement := int64(wholeDistortion) * bPredMinImprovementNumerator / bPredMinImprovementDenominator
	return improvement > penalty &&
		improvement > minRatioImprovement &&
		bPredCoefficients+bPredMinCoefficientReduction <= wholeCoefficients
}

// lumaCoefficientCount counts the quantized Y2 and Y1 levels that are not
// zero. It is a deliberately coarse rate proxy, not a claim about exact bits.
func lumaCoefficientCount(mb *macroblock) int {
	count := 0
	for block := blockY2; block < blockLuma+16; block++ {
		for _, level := range mb.levels[block] {
			if level != 0 {
				count++
			}
		}
	}
	return count
}

// shouldTrialBPred prunes smooth macroblocks before the ten-mode subblock
// search. Prediction error is only a detail signal here; final admission uses
// decoder-equivalent reconstructed distortion.
func (e *encoder) shouldTrialBPred(wholePredictionSSE int32) bool {
	return int64(wholePredictionSSE) > e.bPredPenalty()*bPredTrialPenaltyMultiplier
}

// bPredPenalty is a deterministic quantizer-scaled proxy for the extra mode
// and coefficient syntax of B_PRED. It is deliberately conservative. The
// exact boolean/token cost and lambda replace this proxy in the next slice.
func (e *encoder) bPredPenalty() int64 {
	step := int64(e.q.Y1.AC)
	penalty := step * step / bPredPenaltyDivisor
	if penalty < 1 {
		return 1
	}
	return penalty
}

// lumaMacroblockSSE measures the picture a decoder reconstructs, including
// the padded samples VP8 uses at odd image boundaries.
func (e *encoder) lumaMacroblockSSE(mbx, mby int) int32 {
	x0, y0 := mbx*16, mby*16
	return blockdsp.SSE16x16(
		e.src.Y[y0*e.src.YStride+x0:], e.src.YStride,
		e.rec.Y[y0*e.rec.YStride+x0:], e.rec.YStride,
	)
}

// copyLuma16 copies one padded 16x16 luma macroblock between planes.
func copyLuma16(dst []uint8, dstStride, dstX, dstY int, src []uint8, srcStride, srcX, srcY int) {
	for y := 0; y < 16; y++ {
		copy(dst[(dstY+y)*dstStride+dstX:(dstY+y)*dstStride+dstX+16], src[(srcY+y)*srcStride+srcX:(srcY+y)*srcStride+srcX+16])
	}
}

// codeBPredLuma predicts, transforms, quantizes, and reconstructs the sixteen
// luma blocks of a B_PRED macroblock in decoder raster order. Each predictor
// sees the quantized reconstruction of every block above and to its left. When
// search is true, it chooses the smallest prediction SSE with stable BMode
// ordering as the tie-break. A B_PRED block carries its own direct-current
// coefficient, so this path omits Y2 and codes from scan position zero.
func (e *encoder) codeBPredLuma(mbx, mby int, mb *macroblock, modes *[16]predict.BMode, search bool) {
	mb.levels[blockY2] = [16]int16{}
	mb.nz[blockY2] = false

	var pred, bestPred [16]uint8
	for b := 0; b < 16; b++ {
		bx, by := (b%4)*4, (b/4)*4
		x0, y0 := mbx*16+bx, mby*16+by

		nb := predict.BNeighborsForBlock(e.rec.Y, e.rec.YStride, e.mbw, mbx, mby, b)
		src := e.src.Y[y0*e.src.YStride+x0:]
		if search {
			bestSSE := int32(-1)
			for mode := predict.BMode(0); mode < predict.NumBModes; mode++ {
				predict.Predict4(pred[:], 4, mode, &nb)
				sse := blockdsp.SSE4x4(src, e.src.YStride, pred[:], 4)
				if bestSSE < 0 || sse < bestSSE {
					bestSSE = sse
					modes[b] = mode
					bestPred = pred
				}
			}
			pred = bestPred
		} else {
			predict.Predict4(pred[:], 4, modes[b], &nb)
		}

		var residual [16]int16
		for y := 0; y < 4; y++ {
			srcRow := src[y*e.src.YStride:]
			predRow := pred[y*4 : y*4+4]
			for x := 0; x < 4; x++ {
				residual[y*4+x] = int16(srcRow[x]) - int16(predRow[x])
			}
		}

		coeff := blockdsp.FDCT4x4(&residual)
		levels := quantizeBlock(&coeff, e.q.Y1)
		mb.levels[blockLuma+b] = toScanOrder(&levels)
		mb.nz[blockLuma+b] = anyNonZero(&levels, 0)

		dequant := blockdsp.DequantizeBlock(&levels, e.q.Y1.DC, e.q.Y1.AC)
		residualOut := blockdsp.IDCT4x4(&dequant)
		e.reconstruct(e.rec.Y, e.rec.YStride, x0, y0, pred[:], 4, 0, 0, &residualOut)
	}
}

// codeChroma transforms, quantizes, and reconstructs the four blocks of
// each chroma plane. Chroma blocks carry their own direct-current value.
func (e *encoder) codeChroma(mbx, mby int, mb *macroblock) {
	e.codePlane8(e.src.U, e.rec.U, e.bestU[:], mbx, mby, mb, blockU)
	e.codePlane8(e.src.V, e.rec.V, e.bestV[:], mbx, mby, mb, blockV)
}

func (e *encoder) codePlane8(src, rec []uint8, pred []uint8, mbx, mby int, mb *macroblock, base int) {
	stride := e.src.CStride
	srcBase := (mby * 8) * stride
	for b := 0; b < 4; b++ {
		bx, by := (b%2)*4, (b/2)*4
		var residual [16]int16
		for y := 0; y < 4; y++ {
			srcRow := src[srcBase+(by+y)*stride+mbx*8+bx:]
			predRow := pred[(by+y)*8+bx:]
			for x := 0; x < 4; x++ {
				residual[y*4+x] = int16(srcRow[x]) - int16(predRow[x])
			}
		}
		coeff := blockdsp.FDCT4x4(&residual)
		levels := quantizeBlock(&coeff, e.q.UV)
		mb.levels[base+b] = toScanOrder(&levels)
		mb.nz[base+b] = anyNonZero(&levels, 0)

		dequant := blockdsp.DequantizeBlock(&levels, e.q.UV.DC, e.q.UV.AC)
		residualOut := blockdsp.IDCT4x4(&dequant)
		e.reconstruct(rec, stride, mbx*8+bx, mby*8+by, pred, 8, bx, by, &residualOut)
	}
}

// reconstruct adds a 4x4 residual to its predictor and stores the clamped
// result in the reconstruction plane. The decoder does exactly this, so
// the two pictures stay equal.
func (e *encoder) reconstruct(plane []uint8, stride, x0, y0 int, pred []uint8, predStride, px, py int, residual *[16]int16) {
	for y := 0; y < 4; y++ {
		dst := plane[(y0+y)*stride+x0:]
		predRow := pred[(py+y)*predStride+px:]
		for x := 0; x < 4; x++ {
			dst[x] = clamp8(int32(predRow[x]) + int32(residual[y*4+x]))
		}
	}
}

// neighbors gathers the reconstructed samples around a block, with the
// values RFC 6386 section 12.2 puts outside the frame: 127 above the top
// row, 129 left of the first column. The corner follows the decoder's own
// workspace rule: a macroblock in the top row reads 127 there, whatever
// its column is.
func (e *encoder) neighbors(buf *neighborBuf, plane []uint8, stride, x0, y0, size int, hasLeft, hasTop bool) *predict.Neighbors {
	top := buf.top[:size]
	left := buf.left[:size]

	if hasTop {
		copy(top, plane[(y0-1)*stride+x0:(y0-1)*stride+x0+size])
	} else {
		for i := range top {
			top[i] = predict.MissingTop
		}
	}
	if hasLeft {
		for i := 0; i < size; i++ {
			left[i] = plane[(y0+i)*stride+x0-1]
		}
	} else {
		for i := range left {
			left[i] = predict.MissingLeft
		}
	}

	corner := predict.MissingTop
	switch {
	case !hasTop:
		corner = predict.MissingTop
	case !hasLeft:
		corner = predict.MissingLeft
	default:
		corner = plane[(y0-1)*stride+x0-1]
	}

	buf.nb = predict.Neighbors{
		Top:     top,
		Left:    left,
		Corner:  corner,
		HasTop:  hasTop,
		HasLeft: hasLeft,
	}
	return &buf.nb
}

// quantizeBlock applies the rounding bias and then the dead-zone
// quantizer of blockdsp. It also clamps every level into the range the
// token tree can carry.
func quantizeBlock(coeff *[16]int16, f quantize.Factors) [16]int16 {
	biased := *coeff
	dcBias := int32(f.DC) * biasNumerator / biasDenominator
	acBias := int32(f.AC) * biasNumerator / biasDenominator
	for i := range biased {
		bias := acBias
		if i == 0 {
			bias = dcBias
		}
		v := int32(biased[i])
		switch {
		case v > 0:
			v += bias
		case v < 0:
			v -= bias
		}
		biased[i] = int16(v)
	}

	levels := blockdsp.QuantizeBlock(&biased, f.DC, f.AC)
	for i, v := range levels {
		if v > token.MaxLevel {
			levels[i] = token.MaxLevel
		} else if v < -token.MaxLevel {
			levels[i] = -token.MaxLevel
		}
	}
	return levels
}

// toScanOrder rewrites a raster-order block into the zigzag scan order
// the token layer codes in.
func toScanOrder(raster *[16]int16) [16]int16 {
	var scan [16]int16
	for i, pos := range blockdsp.ZigZag {
		scan[i] = raster[pos]
	}
	return scan
}

// anyNonZero reports whether a raster-order block carries a coefficient
// at or after the first coded scan position.
func anyNonZero(raster *[16]int16, first int) bool {
	for i := first; i < 16; i++ {
		if raster[blockdsp.ZigZag[i]] != 0 {
			return true
		}
	}
	return false
}

// sse8x8 sums the squared errors of an 8x8 block. blockdsp exports the
// 4x4 and 16x16 sizes, so the 8x8 chroma score adds four 4x4 scores.
func sse8x8(a []uint8, aStride int, b []uint8, bStride int) int32 {
	var sum int32
	for i := 0; i < 4; i++ {
		ax, ay := (i%2)*4, (i/2)*4
		sum += blockdsp.SSE4x4(a[ay*aStride+ax:], aStride, b[ay*bStride+ax:], bStride)
	}
	return sum
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

// reconstruction returns the encoder's own picture, in the exact shape
// golang.org/x/image/vp8 returns from a decode of the same file. With
// loop filter level 0 the two must be equal, byte for byte.
func (e *encoder) reconstruction() *image.YCbCr {
	return &image.YCbCr{
		Y:              e.rec.Y,
		Cb:             e.rec.U,
		Cr:             e.rec.V,
		YStride:        e.rec.YStride,
		CStride:        e.rec.CStride,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, e.rec.Width, e.rec.Height),
	}
}

// writeFile serializes the analysed frame and writes the container.
func (e *encoder) writeFile(w io.Writer) error {
	payload, err := e.frameBytes()
	if err != nil {
		return err
	}
	return container.WriteSimpleLossy(w, payload)
}

// frameBytes serializes the analysed frame into a VP8 key frame.
func (e *encoder) frameBytes() ([]byte, error) {
	skipProb := e.skipProbability()

	first := boolenc.New(1024 + len(e.mbs)*2)
	frame.WriteHeader(first, frame.Header{
		Width:           e.src.Width,
		Height:          e.src.Height,
		FilterSimple:    true,
		FilterLevel:     e.filterLevel,
		FilterSharpness: 0,
		QuantIndex:      int(e.q.Index),
		SkipProb:        skipProb,
	})
	// Whole-block-only frames do not need the 4x4 context workspace, so they
	// incur no additional per-frame context allocation.
	var aboveModes [][4]predict.BMode
	if e.bPredModes != nil {
		aboveModes = make([][4]predict.BMode, e.mbw)
	}
	for mby := 0; mby < e.mbh; mby++ {
		var leftModes [4]predict.BMode
		for mbx := 0; mbx < e.mbw; mbx++ {
			mb := &e.mbs[mby*e.mbw+mbx]
			mbIndex := mby*e.mbw + mbx
			writeSkip(first, skipProb, mb.skip)
			if mb.useBPred {
				writeBPredLumaMode(first, &e.bPredModes[mbIndex], &aboveModes[mbx], &leftModes)
			} else {
				writeLumaMode(first, mb.yMode)
				if aboveModes != nil {
					contextMode := bModeForLumaMode(mb.yMode)
					for i := 0; i < 4; i++ {
						aboveModes[mbx][i] = contextMode
						leftModes[i] = contextMode
					}
				}
			}
			writeChromaMode(first, mb.uvMode)
		}
	}

	tokens := e.writeTokens()
	return frame.Assemble(e.src.Width, e.src.Height, first.Finish(), tokens)
}

// skipProbability returns the probability that a macroblock carries
// coefficients, on a scale of 256. RFC 6386 codes the skip flag against
// it, so the value comes from the real skip count.
func (e *encoder) skipProbability() uint8 {
	coded := 0
	for i := range e.mbs {
		if !e.mbs[i].skip {
			coded++
		}
	}
	total := len(e.mbs)
	p := (2*255*coded + total) / (2 * total)
	if p < 1 {
		p = 1
	}
	if p > 255 {
		p = 255
	}
	return uint8(p)
}
