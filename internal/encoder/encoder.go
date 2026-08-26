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
	"m31labs.dev/tqwebp/internal/cost"
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

// macroblock holds everything the serializer needs about one macroblock.
// The analysis pass fills it; the serialization pass reads it. Two passes
// exist because the skip probability in the frame header depends on how
// many macroblocks turn out to be skippable.
type macroblock struct {
	yMode  predict.Mode
	uvMode predict.Mode
	skip   bool
	// bpred marks a macroblock whose luma uses the 4x4 sub-mode set
	// instead of a whole-block mode behind the Walsh-Hadamard transform.
	// The forced reference path sets it for every macroblock; production
	// sets it only at Method 5 or above when rd_select.go's rate-distortion
	// search adopts the sixteen-block candidate.
	bpred bool
	// subModes holds the sixteen raster-ordered 4x4 luma decisions of a
	// B_PRED macroblock.
	subModes [16]predict.SubMode
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
	// The RD search quantizes the same coefficient factors millions of times.
	// These frame-local tables replace the runtime integer divisions with
	// exact signed-coefficient lookups.
	qY1 blockQuantizer
	qUV blockQuantizer
	mbw int
	mbh int
	mbs []macroblock

	// filterLevel is the loop filter strength the frame header signals.
	// It stays at 0 in this release: the encoder does not model the
	// filter, and level 0 keeps the decoder's picture equal to the
	// encoder's own reconstruction, which the exact-match test needs.
	filterLevel int

	// rateProbs is the immutable token probability table used by RD and
	// coefficient-refinement pricing. It is read-only throughout a pass; the
	// default value is the standard VP8 coefficient probability table, so
	// behavior matches pricing directly against token.DefaultProbs.
	rateProbs *token.Probs

	// forceBPred makes every macroblock take the B_PRED luma path of
	// work package WP-2 slice 2A, bypassing selection. It exists so
	// tests can drive the coding path directly; production encodes go
	// through the Method 5/6 search in rd_select.go instead, and
	// methods below the boundary never leave the whole-block path.
	forceBPred bool

	// subCtxAbove holds the B_PRED sub-mode contexts the frame header
	// codes against: one four-entry vector per macroblock column, the
	// way the decoder keeps one above record per column. The vector of
	// macroblock row mby, column mbx reads what the macroblock directly
	// above it left there.
	subCtxAbove [][4]predict.SubMode

	// modeLambda prices whole-block versus B_PRED decisions. The separate
	// trellisLambda prices nearby coefficient levels; conflating the much
	// larger trellis slope with final mode selection destroys quality.
	modeLambda    int64
	trellisLambda int64

	// rd accumulates the deterministic candidate and decision counters
	// of the slice 4 search; tests read them to prove the search bound
	// and each macroblock's selected path.
	rd rdStats

	// rdTokenAbove and rdTokenLeft mirror the token writer's nonzero
	// neighbour contexts during analysis: after every macroblock's
	// decision they hold exactly the state writeTokens would carry out
	// of that macroblock, so the next macroblock's candidates can be
	// priced against the contexts their tokens would really see.
	rdTokenAbove []mbContext
	rdTokenLeft  mbContext

	// rdSubAbove and rdSubLeft mirror the frame writer's sub-mode
	// contexts during analysis, with the same per-column and per-row
	// lifetimes frameBytes gives its own copies.
	rdSubAbove [][4]predict.SubMode
	rdSubLeft  [4]predict.SubMode

	// rdNoPrune disables the slice 4 pruning bounds when a test sets
	// it. Production leaves it false; the equivalence test proves the
	// bounds never change a decision.
	rdNoPrune bool

	// rdCoeffOptOff disables the slice 5B coefficient-candidate
	// refinement when a test sets it. Production leaves it false; the
	// equivalence test isolates this layer from Method 5's retained levels.
	rdCoeffOptOff bool

	// rdCoeffTrellisOff disables only the slice 5C trellis layer of
	// the Method>=6 coefficient refinement, leaving the slice 5B
	// candidate search running. Production leaves it false; tests use
	// it to isolate the two layers' contributions.
	rdCoeffTrellisOff bool

	// rdProbOptOff disables token-probability refinement, which production
	// runs at Method 5 and above. Tests set it to
	// isolate that layer's contribution, exactly as rdCoeffOptOff and
	// rdCoeffTrellisOff do for theirs.
	rdProbOptOff bool

	// frozenTokenProbs holds the probability table the last runFrame
	// derived for serialization, or nil when no derivation shipped.
	frozenTokenProbs *token.Probs

	// probOptimizationDone records whether runFrame completed probability
	// derivation, even when it kept the default table and nothing shipped.
	probOptimizationDone bool

	// probDerivations counts how many times runFrame derived token
	// probabilities; it is one at Method 5 and two after a profitable
	// Method 6 reconsideration.
	probDerivations int

	// probReconsiderations counts the bounded high-effort analyses run
	// under a table derived from the preceding pass. It is at most one.
	probReconsiderations int

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
	q := quantize.New(quantize.IndexForQuality(cfg.Quality))
	e := &encoder{
		cfg:           cfg,
		src:           src,
		rec:           yuv.NewPlanes(src.Width, src.Height),
		q:             q,
		mbw:           src.MBW,
		mbh:           src.MBH,
		mbs:           make([]macroblock, src.MBW*src.MBH),
		subCtxAbove:   make([][4]predict.SubMode, src.MBW),
		modeLambda:    cost.ModeLambda(q),
		trellisLambda: cost.TrellisLambda(q),
		rateProbs:     &token.DefaultProbs,
		rdTokenAbove:  make([]mbContext, src.MBW),
		rdSubAbove:    make([][4]predict.SubMode, src.MBW),
	}
	buildQuantizeTables := src.Width*src.Height >= minQuantizeTablePixels
	e.qY1.init(q.Y1, buildQuantizeTables)
	e.qUV.init(q.UV, buildQuantizeTables)
	return e
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

// runFrame encodes one frame end to end, with probability refinement layered
// on top of analysis. Methods below the probability boundary stop after one
// pass. Method 5 derives an optimized table once from its final macroblocks
// and freezes it for the header and token writer. Method 6 additionally runs
// one bounded reconsideration under that table, then re-derives the emitted
// table from the reconsidered records so serialization never uses stale
// token statistics.
//
// The counters record what happened: probDerivations is 1 -- and
// probOptimizationDone true -- once the one derivation completed, even
// when optimizeTokenProbs kept the default table. A profitable Method 6 pass
// records two derivations and one reconsideration. Below Method 5 nothing is
// recorded.
func (e *encoder) runFrame() {
	e.run()
	if e.cfg.Method < minProbOptMethod {
		return
	}
	frozen := e.optimizeTokenProbs()
	e.probOptimizationDone = true
	e.probDerivations = 1
	if frozen == nil {
		e.frozenTokenProbs = nil
		return
	}
	e.frozenTokenProbs = frozen
	if e.cfg.Method < minProbReconsiderMethod {
		return
	}

	// Build the high-effort pass in fresh state, then adopt it atomically.
	// Keeping the first pass intact until the second completes avoids a
	// broad reset primitive and makes the one-pass Method 5 boundary exact.
	refined := newEncoder(e.src, e.cfg)
	refined.rateProbs = frozen
	refined.forceBPred = e.forceBPred
	refined.rdNoPrune = e.rdNoPrune
	refined.rdCoeffOptOff = e.rdCoeffOptOff
	refined.rdCoeffTrellisOff = e.rdCoeffTrellisOff
	refined.rdProbOptOff = e.rdProbOptOff
	refined.run()
	final := refined.optimizeTokenProbs()
	refined.probOptimizationDone = true
	refined.probDerivations = 2
	refined.probReconsiderations = 1
	refined.frozenTokenProbs = final
	*e = *refined
}

// encodeMacroblock chooses the prediction modes, codes the residual, and
// writes the reconstruction back into the recon planes.
func (e *encoder) encodeMacroblock(mbx, mby int) {
	mb := &e.mbs[mby*e.mbw+mbx]

	switch {
	case e.forceBPred:
		// WP-2 slice 2A: the forced B_PRED luma path. It picks and
		// codes all sixteen 4x4 blocks itself, from immediately
		// reconstructed neighbours, with no Y2 block.
		mb.bpred = true
		e.codeLumaBPred(mbx, mby, mb)
		e.chooseChromaMode(mbx, mby, mb)
		e.codeChroma(mbx, mby, mb)
	case e.bPredAllowed():
		// WP-2 slice 4: the reconstructed-neighbour rate-distortion
		// luma search of rd_select.go. Chroma runs first because it
		// never depends on luma, and knowing the chroma blocks'
		// emptiness up front makes each luma candidate's skip state
		// -- and therefore its exact token accounting -- complete.
		e.chooseChromaMode(mbx, mby, mb)
		e.codeChroma(mbx, mby, mb)
		e.rdChooseLuma(mbx, mby, mb)
	default:
		e.chooseLumaMode(mbx, mby, mb)
		e.codeLuma(mbx, mby, mb)
		e.chooseChromaMode(mbx, mby, mb)
		e.codeChroma(mbx, mby, mb)
	}

	mb.skip = true
	for i := 0; i < numBlocks; i++ {
		if mb.nz[i] {
			mb.skip = false
			break
		}
	}
}

// chooseLumaMode picks the whole-block luma mode with the smallest sum of
// squared errors against the source, leaves its predictor in bestY, and
// returns that smallest sum. Methods below the RD boundary use this path
// directly; the higher-effort search evaluates whole modes independently.
func (e *encoder) chooseLumaMode(mbx, mby int, mb *macroblock) int64 {
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
	return int64(best)
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
		levels := e.qY1.quantizeBlock(&coeffs[b])
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
		levels := e.qUV.quantizeBlock(&coeff)
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

// quantizeBlock applies the rounding bias, dead-zone quantization, and token
// clamp in one pass. The int16 conversion after adding the bias is deliberate:
// it preserves the old two-stage path's wrap semantics for every possible
// transform coefficient, including values outside the encoder's normal FDCT
// range.
func quantizeBlock(coeff *[16]int16, f quantize.Factors) [16]int16 {
	dcBias := int32(f.DC) * biasNumerator / biasDenominator
	acBias := int32(f.AC) * biasNumerator / biasDenominator
	var levels [16]int16
	levels[0] = quantizeLevel(coeff[0], f.DC, dcBias)
	for i := 1; i < len(levels); i++ {
		levels[i] = quantizeLevel(coeff[i], f.AC, acBias)
	}
	return levels
}

func quantizeLevel(coeff, factor int16, bias int32) int16 {
	v := int32(coeff)
	switch {
	case v > 0:
		v += bias
	case v < 0:
		v -= bias
	}
	level := int16(v) / factor
	if level > token.MaxLevel {
		return token.MaxLevel
	}
	if level < -token.MaxLevel {
		return -token.MaxLevel
	}
	return level
}

// FDCT coefficients produced from uint8 sample residuals fit comfortably in
// this interval (the maximum DC magnitude is 8*255 == 2040). Keeping a wider
// power-of-two margin covers the complete normal transform domain while
// making table setup cheap enough for thumbnails.
const (
	quantizeTableMin  = -4096
	quantizeTableSize = 8192
)

// Below this size, the bounded RD search does not perform enough divisions
// to repay table construction. The fused scalar path remains exact and avoids
// charging thumbnails a fixed setup cost.
const minQuantizeTablePixels = 128 * 128

// blockQuantizer uses lookups for normal FDCT coefficients and the exact
// scalar operation for every other int16 value. Y2 is intentionally left on
// the scalar path: it occurs once per whole-macroblock candidate rather than
// sixteen times, and its Walsh-Hadamard range would make setup dominate small
// images.
type blockQuantizer struct {
	factors quantize.Factors
	dcBias  int32
	acBias  int32
	dc      *[quantizeTableSize]int16
	ac      *[quantizeTableSize]int16
}

func (q *blockQuantizer) init(f quantize.Factors, build bool) {
	q.factors = f
	q.dcBias = int32(f.DC) * biasNumerator / biasDenominator
	q.acBias = int32(f.AC) * biasNumerator / biasDenominator
	if !build {
		return
	}
	q.dc = new([quantizeTableSize]int16)
	q.ac = new([quantizeTableSize]int16)
	for i := range q.dc {
		coeff := int16(i + quantizeTableMin)
		q.dc[i] = quantizeLevel(coeff, f.DC, q.dcBias)
		q.ac[i] = quantizeLevel(coeff, f.AC, q.acBias)
	}
}

func (q *blockQuantizer) quantizeBlock(coeff *[16]int16) [16]int16 {
	if q.dc == nil {
		return quantizeBlock(coeff, q.factors)
	}
	var levels [16]int16
	dcIndex := int(coeff[0]) - quantizeTableMin
	if uint(dcIndex) < quantizeTableSize {
		levels[0] = q.dc[dcIndex]
	} else {
		levels[0] = quantizeLevel(coeff[0], q.factors.DC, q.dcBias)
	}
	for i := 1; i < len(levels); i++ {
		acIndex := int(coeff[i]) - quantizeTableMin
		if uint(acIndex) < quantizeTableSize {
			levels[i] = q.ac[acIndex]
		} else {
			levels[i] = quantizeLevel(coeff[i], q.factors.AC, q.acBias)
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

// fromScanOrder is toScanOrder's inverse: it rewrites a scan-order block
// back into raster order. The Method>=6 coefficient search scores its
// candidates off-plane in raster order -- dequantize, invert, add to the
// predictor -- while pricing and storing them in scan order.
func fromScanOrder(scan *[16]int16) [16]int16 {
	var raster [16]int16
	for i, pos := range blockdsp.ZigZag {
		raster[pos] = scan[i]
	}
	return raster
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

	// At Method 5 and above token probabilities are measured from
	// the final macroblocks and only strictly-profitable updates ship.
	// One frozen table feeds both the header and the partition, so the two
	// cannot disagree. Methods below the boundary, and encodes
	// where nothing wins, keep the default table.
	//
	// Production's runFrame freezes the derived table in frozenTokenProbs
	// (nil when no derivation shipped); legacy/internal callers of run()
	// followed by frameBytes derive it lazily here instead.
	var tokenProbs *token.Probs
	if e.probOptimizationDone {
		tokenProbs = e.frozenTokenProbs
	} else if e.cfg.Method >= minProbOptMethod {
		tokenProbs = e.optimizeTokenProbs()
	}

	first := boolenc.New(1024 + len(e.mbs)*2)
	frame.WriteHeader(first, frame.Header{
		Width:           e.src.Width,
		Height:          e.src.Height,
		FilterSimple:    true,
		FilterLevel:     e.filterLevel,
		FilterSharpness: 0,
		QuantIndex:      int(e.q.Index),
		SkipProb:        skipProb,
		TokenProbs:      tokenProbs,
	})
	// Per-macroblock prediction records, in raster order. The sub-mode
	// contexts mirror the decoder's own state: one four-entry vector per
	// macroblock column for the row above -- subCtxAbove[mbx] is what
	// the macroblock directly above this one left there, fresh zeroes
	// (B_DC_PRED) on the top macroblock row -- and one vector per
	// macroblock row for the left, restarted at every row. Resetting the
	// column table here keeps serializing twice, as the tests do,
	// identical to serializing once.
	for i := range e.subCtxAbove {
		e.subCtxAbove[i] = [4]predict.SubMode{}
	}
	var leftSub [4]predict.SubMode
	for mby := 0; mby < e.mbh; mby++ {
		leftSub = [4]predict.SubMode{}
		for mbx := 0; mbx < e.mbw; mbx++ {
			mb := &e.mbs[mby*e.mbw+mbx]
			aboveSub := &e.subCtxAbove[mbx]
			first.WriteBool(skipProb, mb.skip)
			if mb.bpred {
				writeBPredModes(first, mb, aboveSub, &leftSub)
			} else {
				writeLumaMode(first, mb.yMode)
				// A whole-block macroblock seeds all four above and
				// left entries with the sub-mode value its mode maps
				// to, exactly as the decoder's records do.
				ctx := subModeContextOf(mb.yMode)
				for i := range leftSub {
					aboveSub[i] = ctx
					leftSub[i] = ctx
				}
			}
			writeChromaMode(first, mb.uvMode)
		}
	}

	tokens := e.writeTokens(tokenProbs)
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
