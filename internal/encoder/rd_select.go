package encoder

// This file is work package WP-2 slice 4: the reconstructed-neighbour
// rate-distortion luma search that replaces the conservative detailed-
// block rule of bpred_select.go at Method 5 and above. Methods below the
// effort boundary never reach this code, so their bytes cannot move.
//
// For every macroblock the search prices five candidates -- the four
// whole-block modes and one B_PRED pass -- on the same scale:
//
//	score = SSE(reconstruction, source)*256 + lambda * exactSyntaxRate
//
// where the reconstruction is what a decoder would build (prediction
// from reconstructed neighbours plus the dequantized residual) and the
// rate is the exact price, in 1/256-bit units from internal/cost, of
// two things only: the candidate's mode record(s) -- cost.LumaMode, or
// cost.BPredFlag plus sixteen cost.SubModeCost records -- and the
// coefficient tokens the candidate would really emit, one
// cost.BlockCost per coded block threaded through the same neighbour-
// context evolution the token writer performs.
//
// The scope boundary is deliberate and stated plainly. Chroma mode and
// chroma tokens do not depend on the luma candidate, so they take the
// same value under every candidate and cancel from the comparison; they
// are priced nowhere. A macroblock whose every block comes out empty --
// which, given chroma has already been coded, some candidates reach and
// others do not -- emits no coefficient tokens at all, so its charged
// token rate is zero, exactly matching the byte stream. The skip-flag
// bit itself sits outside Slice 4's priced scope: it is one more bit
// per macroblock whose probability depends on frame-wide statistics,
// and this search does not model it. What the search guarantees is
// that every term it does compare is exact, including emptiness.
//
// All candidates predict from the reconstruction state available to
// the decoder: the whole-block candidates read the macroblock's
// reconstructed border and code into scratch; the B_PRED candidate
// codes block by block into the reconstruction plane itself, so each
// 4x4 block sees its predecessors' samples, and the region is restored
// byte for byte whenever the candidate loses or is abandoned early.
//
// The search is bounded and deterministic. Every candidate set is
// finite (five candidates, ten sub-modes each), the B_PRED attempt is
// gated by an explicit floor on its achievable score, and a partial
// B_PRED pass is abandoned the moment its exact partial score plus a
// floor on the remaining pieces can no longer beat the best whole-
// block candidate; while the candidate could still finish empty the
// token share of that floor stays zero, so the bound never assumes a
// cost the writer might not pay. Ties go to the lowest stable
// candidate ordinal (DC=0, V=1, H=2, TM=3, B_PRED=4). Everything is
// integer arithmetic over fixed tables, so the result is identical at
// every GOMAXPROCS.

import (
	"m31labs.dev/turboquant/blockdsp"

	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/token"
)

// anyNonZeroScan reports whether a scan-order level vector holds any
// nonzero coefficient, the emptiness test the writers apply when they
// set a block's nz flag.
func anyNonZeroScan(levels *[16]int16) bool {
	for i := 0; i < 16; i++ {
		if levels[i] != 0 {
			return true
		}
	}
	return false
}

// rdStats records the deterministic counters of the reconstructed-
// neighbour rate-distortion luma search of WP-2 slice 4. The counters
// are plain integers updated in raster order, so they are identical at
// every GOMAXPROCS and across repeats of the same encode.
type rdStats struct {
	// CandidatesEvaluated counts every candidate whose score was
	// charged: the four whole-block modes of each macroblock plus one
	// more for every B_PRED candidate that passed its pruning gate.
	// It equals 4*mbw*mbh + BpredAttempts.
	CandidatesEvaluated int64
	// BpredAttempts counts macroblocks whose B_PRED candidate passed
	// the pruning gate and entered the sixteen-block pass.
	BpredAttempts int64
	// BpredAborts counts B_PRED passes abandoned early by the
	// per-block bound. An abandoned candidate cannot win, so
	// BpredAborts is at most BpredAttempts - DecisionsBPred.
	BpredAborts int64
	// PrunedBPred counts macroblocks whose B_PRED candidate was
	// rejected by the gate before any block was coded. With
	// BpredAttempts it partitions the macroblocks:
	// BpredAttempts + PrunedBPred == mbw*mbh.
	PrunedBPred int64
	// DecisionsWhole and DecisionsBPred count the winners by path;
	// their sum is mbw*mbh.
	DecisionsWhole int64
	DecisionsBPred int64
}

// Candidate ordinals fix the canonical tie order. The whole-block modes
// keep their predict.Mode numbering and B_PRED follows as ordinal 4, so
// "lowest ordinal" is "lowest mode, B_PRED last".
const (
	rdOrdinalBPred = 4
)

// The pruning floors are exact lower bounds on what one still-uncoded
// piece of the B_PRED candidate can add to its rate. They are computed
// once at package init from the same fixed tables everything else
// reads, so they are identical on every platform.
var (
	// rdSubModeFloor is the cheapest possible ten-way sub-mode record
	// under any key-frame context pair.
	rdSubModeFloor = minSubModeCost()
	// rdBlockFloor is the cheapest possible coefficient block: an
	// immediate end-of-block flag in the YWithDC plane under the most
	// favourable band and neighbour context.
	rdBlockFloor = minBlockEOBCost()
)

// minSubModeCost walks every context pair and sub-mode of the key-frame
// sub-mode probability table and returns the smallest record price.
func minSubModeCost() cost.Cost {
	best := cost.SubModeCost(0, 0, predict.SubMode(0))
	for a := 0; a < len(predict.KeyFrameSubModeProbs); a++ {
		for l := 0; l < len(predict.KeyFrameSubModeProbs[a]); l++ {
			for m := predict.SubMode(0); m < predict.NumSubModes; m++ {
				if c := cost.SubModeCost(predict.SubMode(a), predict.SubMode(l), m); c < best {
					best = c
				}
			}
		}
	}
	return best
}

// minBlockEOBCost returns the price of the cheapest block the token
// layer can carry in the YWithDC plane: one false end-of-block flag,
// taken over every scan position's band and every neighbour context.
func minBlockEOBCost() cost.Cost {
	best := cost.Cost(1) << 60
	for ctx := 0; ctx <= 2; ctx++ {
		for n := 0; n < 16; n++ {
			p := token.DefaultProbs[token.YWithDC][token.Bands[n]][ctx][0]
			if c := cost.BitCostZero(p); c < best {
				best = c
			}
		}
	}
	return best
}

// rdCandidate is one luma hypothesis with its charged rate-distortion
// score. rate is in cost.Cost units (1/256 bit); score folds rate and
// distortion into one integer comparison scale, sse<<8 + lambda*rate.
type rdCandidate struct {
	ordinal int
	bpred   bool
	sse     int64
	rate    cost.Cost
	score   int64
}

// betterThan reports whether c beats best under the canonical rule:
// strictly lower score wins, and equal scores go to the lower ordinal.
func (c rdCandidate) betterThan(best rdCandidate) bool {
	if c.score != best.score {
		return c.score < best.score
	}
	return c.ordinal < best.ordinal
}

// rdScore folds a candidate's distortion and rate onto the comparison
// scale. Both terms are small relative to int64: SSE is at most
// 256*255^2 and the rate of one macroblock's luma well under a few
// thousand bits, so the product cannot overflow.
func (e *encoder) rdScore(sse int64, rate cost.Cost) int64 {
	return sse<<8 + int64(rate)*e.lambda
}

// rdTokenView is the token-neighbour state one macroblock's candidates
// are priced against: a copy of the shadow the previous macroblocks
// left, threaded forward within the macroblock exactly as writeTokens
// threads its own copies.
type rdTokenView struct {
	leftY2, upY2     uint8
	leftLuma, upLuma [4]uint8
}

// rdTokenViewAt returns the entering token-neighbour view of macroblock
// column mbx: the above entries the rows before this one left in the
// column's shadow and the left entries the earlier columns of this row
// left in the row shadow. Candidates price against a copy; the shadows
// themselves advance once, at commit time.
func (e *encoder) rdTokenViewAt(mbx int) rdTokenView {
	above := &e.rdTokenAbove[mbx]
	return rdTokenView{
		leftY2:   e.rdTokenLeft.y2,
		upY2:     above.y2,
		leftLuma: e.rdTokenLeft.luma,
		upLuma:   above.luma,
	}
}

// rdCommit commits the winning candidate into the macroblock record,
// the reconstruction plane, and the neighbour-context shadows. It runs
// exactly once per macroblock: whole-block winners write their scratch
// coding out here, B_PRED winners were already reconstructed block by
// block during their walk, and both paths then leave the token and
// sub-mode shadows holding precisely what the writers would carry out
// of this macroblock -- writeTokens' per-row mbContext threading for
// the coefficient contexts, frameBytes' sub-mode seeding and threading
// for the mode contexts. Emptiness decides the skipped state exactly as
// encodeMacroblock computes it.
func (e *encoder) rdCommit(mbx, mby int, mb *macroblock, best rdCandidate, coding *rdCoding) {
	if best.bpred {
		// The walk wrote the record and the reconstruction plane;
		// only the winner bookkeeping remains.
		e.rd.DecisionsBPred++
	} else {
		e.rd.DecisionsWhole++
		mb.bpred = false
		mb.yMode = predict.Mode(best.ordinal)
		mb.levels[blockY2] = coding.y2Levels
		mb.nz[blockY2] = anyNonZeroScan(&coding.y2Levels)
		for b := 0; b < 16; b++ {
			mb.levels[blockLuma+b] = coding.luma[b]
			mb.nz[blockLuma+b] = anyNonZeroScan(&coding.luma[b])
		}
		x0, y0 := mbx*16, mby*16
		for r := 0; r < 16; r++ {
			copy(e.rec.Y[(y0+r)*e.rec.YStride+x0:][:16], coding.recon[r][:])
		}
	}

	// Skipped is a property of the record, not of the path: every
	// block empty means the writer emits no tokens at all.
	skipped := true
	for i := 0; i < numBlocks; i++ {
		if mb.nz[i] {
			skipped = false
			break
		}
	}

	// Token contexts: thread both axes from the record's nz flags,
	// clearing on skip exactly as writeTokens does (a B_PRED
	// macroblock owns no Y2 block, so its Y2 flags survive).
	left, up := &e.rdTokenLeft, &e.rdTokenAbove[mbx]
	if skipped {
		prevLeftY2, prevUpY2 := left.y2, up.y2
		*left = mbContext{}
		*up = mbContext{}
		if best.bpred {
			left.y2, up.y2 = prevLeftY2, prevUpY2
		}
	} else {
		if !best.bpred {
			nz := btou(mb.nz[blockY2])
			left.y2, up.y2 = nz, nz
		}
		for y := 0; y < 4; y++ {
			nz := left.luma[y]
			for x := 0; x < 4; x++ {
				nz = btou(mb.nz[blockLuma+4*y+x])
				up.luma[x] = nz
			}
			left.luma[y] = nz
		}
		e.threadChromaShadows(mbx, mb, blockU)
		e.threadChromaShadows(mbx, mb, blockV)
	}

	// Sub-mode contexts: modes are written whatever the skip state.
	// A whole-block macroblock seeds all eight entries with the value
	// its mode maps to; a B_PRED macroblock threads one entry per
	// coded block, above axis by column and left axis by row.
	if best.bpred {
		for b := 0; b < 16; b++ {
			sub := mb.subModes[b]
			e.rdSubAbove[mbx][b%4] = sub
			e.rdSubLeft[b/4] = sub
		}
	} else {
		ctx := subModeContextOf(mb.yMode)
		for i := range e.rdSubAbove[mbx] {
			e.rdSubAbove[mbx][i] = ctx
			e.rdSubLeft[i] = ctx
		}
	}
}

// threadChromaShadows advances the chroma entries of the token shadows
// across one plane of a committed macroblock, the way writeTokens'
// chroma pass advances its own copies. Chroma never enters a candidate
// comparison -- it is identical under every candidate -- but keeping
// the full shadow faithful costs nothing and keeps commit exact.
func (e *encoder) threadChromaShadows(mbx int, mb *macroblock, base int) {
	left, up := &e.rdTokenLeft, &e.rdTokenAbove[mbx]
	lu, uu := &left.u, &up.u
	if base == blockV {
		lu, uu = &left.v, &up.v
	}
	for y := 0; y < 2; y++ {
		nz := btou(mb.nz[base+2*y])
		for x := 0; x < 2; x++ {
			nz = btou(mb.nz[base+2*y+x])
			uu[x] = nz
		}
		lu[y] = nz
	}
}

// rdCoding carries one whole-block candidate's committed coding result
// between evaluation and commit: the scan-order levels and nonzero
// flags of the Walsh-Hadamard and sixteen luma blocks, and the 16x16
// clamped reconstruction the decoder would hold.
type rdCoding struct {
	y2Levels [16]int16
	luma     [16][16]int16
	recon    [16][16]uint8
}

// rdChooseLuma runs the slice 4 search for one macroblock and commits
// the winning candidate into mb and the reconstruction plane. Chroma
// must already be coded: the chroma nonzero flags decide whether a
// luma candidate can reach the skipped state whose tokens vanish.
func (e *encoder) rdChooseLuma(mbx, mby int, mb *macroblock) {
	// Left-hand contexts restart at every macroblock row, exactly as
	// the writers restart their own: writeTokens zeroes its left
	// mbContext and frameBytes zeroes its left sub-mode vector at
	// each row start. The analysis walks the same raster order, so
	// the first column of a row prices against fresh contexts.
	if mbx == 0 {
		e.rdTokenLeft = mbContext{}
		e.rdSubLeft = [4]predict.SubMode{}
	}

	// Stage A: price all four whole-block candidates from scratch
	// reconstructions. Nothing is written to the planes yet.
	nb := e.neighbors(&e.nbY, e.rec.Y, e.rec.YStride, mbx*16, mby*16, 16, mbx > 0, mby > 0)
	src := e.src.Y[(mby*16)*e.src.YStride+mbx*16:]
	tok := e.rdTokenViewAt(mbx)
	chromaEmpty := true
	for i := blockU; i < numBlocks; i++ {
		if mb.nz[i] {
			chromaEmpty = false
			break
		}
	}

	var best rdCandidate
	var bestCoding rdCoding
	haveBest := false
	for m := predict.Mode(0); m < predict.NumModes; m++ {
		predict.Predict(e.predY[:], 16, 16, m, nb)
		cand, coding := e.rdEvalWhole(mbx, mby, m, src, &tok, chromaEmpty)
		e.rd.CandidatesEvaluated++
		if !haveBest || cand.betterThan(best) {
			best, bestCoding = cand, coding
			haveBest = true
		}
	}

	// Stage B: the B_PRED candidate behind its pruning gate.
	e.rdTryBPred(mbx, mby, mb, &best, &tok, chromaEmpty)

	// Commit the winner and hand the exact post-macroblock context
	// state to the next macroblock's pricing.
	e.rdCommit(mbx, mby, mb, best, &bestCoding)
}

// rdEvalWhole prices one whole-block candidate: it transforms,
// quantizes, and reconstructs the sixteen luma blocks behind the
// Walsh-Hadamard block exactly as codeLuma does -- but into scratch,
// leaving the planes untouched -- and charges cost.LumaMode plus the
// exact token costs of the Y2 and sixteen luma blocks under the
// neighbour contexts the candidate would meet. When chromaEmpty and
// the candidate's luma all come out empty, writeTokens emits no tokens
// for this macroblock at all, so the token charge is dropped.
func (e *encoder) rdEvalWhole(mbx, mby int, m predict.Mode, src []uint8, tok *rdTokenView, chromaEmpty bool) (rdCandidate, rdCoding) {
	var coding rdCoding
	var coeffs [16][16]int16
	srcBase := (mby * 16) * e.src.YStride
	for b := 0; b < 16; b++ {
		bx, by := (b%4)*4, (b/4)*4
		var residual [16]int16
		for y := 0; y < 4; y++ {
			srcRow := e.src.Y[srcBase+(by+y)*e.src.YStride+mbx*16+bx:]
			predRow := e.predY[(by+y)*16+bx:]
			for x := 0; x < 4; x++ {
				residual[y*4+x] = int16(srcRow[x]) - int16(predRow[x])
			}
		}
		coeffs[b] = blockdsp.FDCT4x4(&residual)
	}

	var dc [16]int16
	for b := 0; b < 16; b++ {
		dc[b] = coeffs[b][0]
	}
	y2 := blockdsp.FWHT4x4(&dc)
	y2Levels := quantizeBlock(&y2, e.q.Y2)
	coding.y2Levels = toScanOrder(&y2Levels)

	y2Dequant := blockdsp.DequantizeBlock(&y2Levels, e.q.Y2.DC, e.q.Y2.AC)
	reconDC := blockdsp.IWHT4x4(&y2Dequant)

	var sse int64
	for b := 0; b < 16; b++ {
		levels := quantizeBlock(&coeffs[b], e.q.Y1)
		// Position 0 belongs to the Walsh-Hadamard block, so this
		// block never codes it.
		levels[0] = 0
		coding.luma[b] = toScanOrder(&levels)

		dequant := blockdsp.DequantizeBlock(&levels, e.q.Y1.DC, e.q.Y1.AC)
		dequant[0] = reconDC[b]
		residual := blockdsp.IDCT4x4(&dequant)

		bx, by := (b%4)*4, (b/4)*4
		for y := 0; y < 4; y++ {
			predRow := e.predY[(by+y)*16+bx:]
			recRow := coding.recon[by+y][bx:]
			srcRow := e.src.Y[srcBase+(by+y)*e.src.YStride+mbx*16+bx:]
			for x := 0; x < 4; x++ {
				v := clamp8(int32(predRow[x]) + int32(residual[y*4+x]))
				recRow[x] = v
				d := int32(v) - int32(srcRow[x])
				sse += int64(d * d)
			}
		}
	}

	tokenRate, lumaEmpty := e.rdPriceWholeTokens(&coding.y2Levels, &coding.luma, tok)
	rate := cost.LumaMode(m)
	if !(chromaEmpty && lumaEmpty) {
		rate += tokenRate
	}

	return rdCandidate{
		ordinal: int(m),
		sse:     sse,
		rate:    rate,
		score:   e.rdScore(sse, rate),
	}, coding
}

// rdPriceWholeTokens charges the Y2 block and the sixteen luma blocks
// of a whole-block candidate, threading both context axes exactly as
// writeTokens threads its own: the above entry of a column updates as
// soon as that column's block is coded, so the next subblock row sees
// the value this row just produced, and the left entry of a row starts
// from the entering shadow and ends at its last block's flag. It
// reports the total token cost and whether every block came out empty.
func (e *encoder) rdPriceWholeTokens(y2Levels *[16]int16, luma *[16][16]int16, tok *rdTokenView) (cost.Cost, bool) {
	probs := &token.DefaultProbs
	tokenRate := cost.Cost(0)
	tokenRate += cost.BlockCost(token.Y2, int(tok.leftY2+tok.upY2), 0, y2Levels, probs)
	lumaEmpty := !anyNonZeroScan(y2Levels)
	up := tok.upLuma
	for y := 0; y < 4; y++ {
		nz := tok.leftLuma[y]
		for x := 0; x < 4; x++ {
			ctx := int(nz + up[x])
			block := &luma[4*y+x]
			tokenRate += cost.BlockCost(token.YAfterY2, ctx, 1, block, probs)
			nz = btou(anyNonZeroScan(block))
			up[x] = nz
			if nz != 0 {
				lumaEmpty = false
			}
		}
	}
	return tokenRate, lumaEmpty
}

// rdBPredWalk carries the mutable state of one B_PRED pass: the four
// context axes threaded forward across the sixteen blocks, the rate
// split into the share that is certainly emitted and the share that a
// skipped finish would drop, the running distortion, and the skip
// bookkeeping. Its arrays are copies; the frame-level shadows are
// updated once, at commit time, from the record.
type rdBPredWalk struct {
	enc *encoder

	// Sub-mode contexts, advanced per block exactly as
	// writeBPredModes advances its own copies.
	aboveCtx [4]predict.SubMode // one entry per macroblock column
	leftCtx  [4]predict.SubMode // one entry per macroblock row

	// Coefficient-token contexts, advanced per block exactly as
	// writeTokens advances its own copies.
	left [4]uint8 // one flag per macroblock row
	up   [4]uint8 // one flag per macroblock column

	// records accumulates the syntax that reaches the bitstream
	// whatever the macroblock's final emptiness: the B_PRED branch
	// flag and one sub-mode record per coded block. tokenRate
	// accumulates every block's coefficient-token cost; certTokens
	// is the part of it already certain to be emitted. A partial
	// pass bounds itself with records+certTokens so the bound never
	// assumes a cost a skipped finish would not pay.
	records    cost.Cost
	tokenRate  cost.Cost
	certTokens cost.Cost

	// skipPossible says the candidate could still finish fully empty
	// behind an empty chroma and be written as skipped. It starts as
	// chromaEmpty and dies at the first non-empty luma block, which
	// is also when everything charged so far becomes certain.
	skipPossible bool
	anyNonZero   bool
	sse          int64
}

// codeBlock codes one 4x4 block of the B_PRED pass behind sub-mode sub:
// it prices the sub-mode record under the current sub-mode contexts,
// predicts, transforms, quantizes, charges the exact YWithDC token cost
// under the current token contexts, reconstructs into the plane,
// measures the reconstruction distortion, and advances all four context
// axes once, in writer order. It returns the block's nonzero flag.
func (w *rdBPredWalk) codeBlock(bx, by, b int, sub predict.SubMode, nb *predict.SubNeighbors, src []uint8, pred []uint8, levels *[16]int16) uint8 {
	e := w.enc
	w.records += cost.SubModeCost(w.aboveCtx[b%4], w.leftCtx[b/4], sub)

	predict.PredictSub(pred[:], 4, sub, nb)
	var residual [16]int16
	for y := 0; y < 4; y++ {
		srcRow := src[y*e.src.YStride:]
		predRow := pred[y*4:]
		for x := 0; x < 4; x++ {
			residual[y*4+x] = int16(srcRow[x]) - int16(predRow[x])
		}
	}
	coeff := blockdsp.FDCT4x4(&residual)
	q := quantizeBlock(&coeff, e.q.Y1)
	*levels = toScanOrder(&q)

	// Exact token cost in the YWithDC plane, threaded like the luma
	// rows of writeTokens but starting at coefficient 0.
	ctx := int(w.left[b/4] + w.up[b%4])
	tokenCost := cost.BlockCost(token.YWithDC, ctx, 0, levels, &token.DefaultProbs)
	w.tokenRate += tokenCost

	dequant := blockdsp.DequantizeBlock(&q, e.q.Y1.DC, e.q.Y1.AC)
	residualOut := blockdsp.IDCT4x4(&dequant)
	e.reconstruct(e.rec.Y, e.rec.YStride, bx, by, pred[:], 4, 0, 0, &residualOut)

	// Distortion is measured on the reconstruction, matching the
	// whole-block candidates' scale.
	for y := 0; y < 4; y++ {
		recRow := e.rec.Y[(by+y)*e.rec.YStride+bx:]
		srcRow := src[y*e.src.YStride:]
		for x := 0; x < 4; x++ {
			d := int32(recRow[x]) - int32(srcRow[x])
			w.sse += int64(d * d)
		}
	}

	nz := btou(anyNonZeroScan(levels))
	if nz != 0 {
		w.anyNonZero = true
		if w.skipPossible {
			// First non-empty block: the candidate can no
			// longer be skipped, so every token charged
			// before this one -- the empty blocks'
			// end-of-block flags -- is now certain too.
			// tokenRate already holds this block's cost, so
			// subtract it here; the unconditional charge
			// below adds it exactly once.
			w.skipPossible = false
			w.certTokens += w.tokenRate - tokenCost
		}
	}
	w.aboveCtx[b%4] = sub
	w.leftCtx[b/4] = sub
	w.up[b%4] = nz
	w.left[b/4] = nz
	if !w.skipPossible {
		w.certTokens += tokenCost
	}
	return nz
}

// rdWalkBPred codes and prices the sixteen blocks of the B_PRED
// candidate in raster order and reports whether the completed candidate
// beat the incumbent. Each block picks its sub-mode by greedy SSE
// against the source (ties to the lowest sub-mode, exactly as
// codeLumaBPred decides) and then hands the coding to codeBlock. A
// partial pass dies early once its exact partial score plus a floor on
// the remaining pieces can no longer strictly beat best: the floor's
// sub-mode share is always paid, while its token share joins only once
// tokens are certain, because a still-possible skipped finish pays none.
func (e *encoder) rdWalkBPred(mbx, mby int, best *rdCandidate, tok *rdTokenView, chromaEmpty bool) (cand rdCandidate, subModes [16]predict.SubMode, lumaLevels [16][16]int16, won bool) {
	x0, y0 := mbx*16, mby*16
	paddedWidth := ((e.src.Width + 15) / 16) * 16
	var nb predict.SubNeighbors
	var pred [16]uint8

	w := rdBPredWalk{
		enc:          e,
		aboveCtx:     e.rdSubAbove[mbx],
		leftCtx:      e.rdSubLeft,
		left:         tok.leftLuma,
		up:           tok.upLuma,
		skipPossible: chromaEmpty,
	}
	w.records = cost.BPredFlag()

	for b := 0; b < 16; b++ {
		bx, by := x0+(b%4)*4, y0+(b/4)*4
		predict.GatherSubNeighbors(&nb, e.rec.Y, e.rec.YStride, bx, by, paddedWidth)
		srcRow0 := e.src.Y[by*e.src.YStride+bx:]

		bestSSE := int32(-1)
		var bestSub predict.SubMode
		for m := predict.SubMode(0); m < predict.NumSubModes; m++ {
			predict.PredictSub(pred[:], 4, m, &nb)
			s := blockdsp.SSE4x4(srcRow0, e.src.YStride, pred[:], 4)
			if bestSSE < 0 || s < bestSSE {
				bestSSE = s
				bestSub = m
			}
		}
		subModes[b] = bestSub
		w.codeBlock(bx, by, b, bestSub, &nb, srcRow0, pred[:], &lumaLevels[b])

		// Per-block bound: an exact partial score plus a true
		// lower bound on the rest, unbeatable against the
		// incumbent, ends the pass. Ties belong to the incumbent.
		if !e.rdNoPrune && b < 15 {
			remaining := cost.Cost(15-b) * rdSubModeFloor
			if !w.skipPossible {
				remaining += cost.Cost(15-b) * rdBlockFloor
			}
			if e.rdScore(w.sse, w.records+w.certTokens+remaining) >= best.score {
				e.rd.BpredAborts++
				return rdCandidate{}, subModes, lumaLevels, false
			}
		}
	}
	// The last block's context updates have no reader within the
	// macroblock, and the commit-time shadow update rewrites the
	// final flags from the record, so no further threading is needed.

	rate := w.records
	if !w.skipPossible {
		// Not skipped, or impossible to have been: every token
		// was emitted. A finished-empty candidate emits none.
		rate += w.tokenRate
	}
	cand = rdCandidate{
		ordinal: rdOrdinalBPred,
		bpred:   true,
		sse:     w.sse,
		rate:    rate,
		score:   e.rdScore(w.sse, rate),
	}
	return cand, subModes, lumaLevels, true
}

// rdTryBPred evaluates the sixteen-block candidate of one macroblock.
// It runs the B_PRED coder over the live reconstruction plane -- each
// block must see its predecessors' samples -- after saving the region
// and the macroblock record; on an early abort or a lost comparison
// both are restored byte for byte. best carries the leading whole-
// block score the candidate must strictly beat; tok carries the
// entering token contexts and is left unmodified (the winner's context
// update happens once, at commit time); chromaEmpty says whether a
// fully empty luma would leave this macroblock skipped. A candidate
// that does finish fully empty emits no coefficient tokens and is
// charged none, exactly as writeTokens writes none; until that
// outcome is certain the pruning bounds only claim costs that are
// unavoidable.
func (e *encoder) rdTryBPred(mbx, mby int, mb *macroblock, best *rdCandidate, tok *rdTokenView, chromaEmpty bool) {
	if !e.rdBPredGate(best, chromaEmpty) {
		e.rd.PrunedBPred++
		return
	}
	e.rd.BpredAttempts++
	e.rd.CandidatesEvaluated++

	restore := e.rdSaveRegion(mbx, mby, mb)
	defer func() {
		if restore != nil {
			restore()
		}
	}()

	cand, subModes, lumaLevels, completed := e.rdWalkBPred(mbx, mby, best, tok, chromaEmpty)
	if !completed || !cand.betterThan(*best) {
		// Abandoned early, finished without beating the
		// incumbent, or merely tying it: ties belong to the
		// lower ordinal, and B_PRED is the last one.
		restore()
		return
	}

	// Winner: commit the candidate's record now. The reconstruction
	// plane already holds it.
	mb.bpred = true
	mb.subModes = subModes
	mb.levels[blockY2] = [16]int16{}
	mb.nz[blockY2] = false
	for b := 0; b < 16; b++ {
		mb.levels[blockLuma+b] = lumaLevels[b]
		mb.nz[blockLuma+b] = anyNonZeroScan(&lumaLevels[b])
	}
	*best = cand
	restore = nil
}

// rdBPredGate applies the attempt-level pruning bound and reports
// whether the sixteen-block pass may run. The bound is a true lower
// bound: BPredFlag plus sixteen minimum sub-mode records are always
// paid; sixteen minimum blocks join only when coefficient tokens are
// certain to be emitted -- with non-empty chroma the macroblock cannot
// be skipped, so every block's tokens will really appear. If even the
// floor cannot strictly beat the incumbent -- ties belong to the lower
// ordinal anyway -- there is nothing to try. rdNoPrune disables the
// gate for the equivalence test.
func (e *encoder) rdBPredGate(best *rdCandidate, chromaEmpty bool) bool {
	if e.rdNoPrune {
		return true
	}
	blockFloor := cost.Cost(0)
	if !chromaEmpty {
		blockFloor = rdBlockFloor
	}
	floor := cost.BPredFlag() + cost.Cost(16)*(rdSubModeFloor+blockFloor)
	return int64(floor)*e.lambda < best.score
}

// rdSaveRegion snapshots the macroblock record and its 16x16 area of
// the reconstruction plane, returning a function that restores both
// byte for byte.
func (e *encoder) rdSaveRegion(mbx, mby int, mb *macroblock) func() {
	savedMB := *mb
	x0, y0 := mbx*16, mby*16
	var savedRec [16][16]uint8
	for r := 0; r < 16; r++ {
		copy(savedRec[r][:], e.rec.Y[(y0+r)*e.rec.YStride+x0:][:16])
	}
	return func() {
		*mb = savedMB
		for r := 0; r < 16; r++ {
			copy(e.rec.Y[(y0+r)*e.rec.YStride+x0:][:16], savedRec[r][:])
		}
	}
}
