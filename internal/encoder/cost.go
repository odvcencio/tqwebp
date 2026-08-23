package encoder

import (
	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/quantize"
	"m31labs.dev/tqwebp/internal/token"
)

func decisionCostQ8(prob uint8, bit bool) uint64 {
	return uint64(boolenc.DecisionCostQ8(prob, bit))
}

// writeSkip emits one macroblock skip decision. Keeping this syntax helper
// beside skipCostQ8 lets exhaustive tests compare the cost model with the
// actual decision path.
func writeSkip(enc *boolenc.Encoder, prob uint8, skip bool) {
	enc.WriteBool(prob, skip)
}

func skipCostQ8(prob uint8, skip bool) uint64 {
	return decisionCostQ8(prob, skip)
}

// writeSegmentID emits the two decisions in VP8's four-leaf segment tree.
// Segment IDs 0/1 use probs[1] below the false root branch; IDs 2/3 use
// probs[2] below the true branch.
func writeSegmentID(enc *boolenc.Encoder, segment uint8, probs *[3]uint8) {
	if segment >= 4 {
		panic("tqwebp: invalid segment ID")
	}
	hi := segment >= 2
	enc.WriteBool(probs[0], hi)
	enc.WriteBool(probs[1+btou(hi)], segment&1 != 0)
}

func segmentIDCostQ8(segment uint8, probs *[3]uint8) uint64 {
	if segment >= 4 {
		panic("tqwebp: invalid segment ID")
	}
	hi := segment >= 2
	return decisionCostQ8(probs[0], hi) +
		decisionCostQ8(probs[1+btou(hi)], segment&1 != 0)
}

func lumaModeCostQ8(mode predict.Mode) uint64 {
	cost := decisionCostQ8(use16x16Prob, true)
	switch mode {
	case predict.DC:
		return cost + decisionCostQ8(lumaDCvsRestProb, false) + decisionCostQ8(lumaDCvsVProb, false)
	case predict.V:
		return cost + decisionCostQ8(lumaDCvsRestProb, false) + decisionCostQ8(lumaDCvsVProb, true)
	case predict.H:
		return cost + decisionCostQ8(lumaDCvsRestProb, true) + decisionCostQ8(lumaHvsTMProb, false)
	case predict.TM:
		return cost + decisionCostQ8(lumaDCvsRestProb, true) + decisionCostQ8(lumaHvsTMProb, true)
	default:
		panic("tqwebp: unknown luma mode")
	}
}

func chromaModeCostQ8(mode predict.Mode) uint64 {
	switch mode {
	case predict.DC:
		return decisionCostQ8(chromaDCProb, false)
	case predict.V:
		return decisionCostQ8(chromaDCProb, true) + decisionCostQ8(chromaVProb, false)
	case predict.H:
		return decisionCostQ8(chromaDCProb, true) + decisionCostQ8(chromaVProb, true) + decisionCostQ8(chromaHProb, false)
	case predict.TM:
		return decisionCostQ8(chromaDCProb, true) + decisionCostQ8(chromaVProb, true) + decisionCostQ8(chromaHProb, true)
	default:
		panic("tqwebp: unknown chroma mode")
	}
}

func bModeCostQ8(mode, above, left predict.BMode) uint64 {
	if mode >= predict.NumBModes || above >= predict.NumBModes || left >= predict.NumBModes {
		panic("tqwebp: invalid 4x4 luma mode")
	}
	p := &bModeProbs[above][left]
	cost := decisionCostQ8(p[0], mode != predict.BDC)
	if mode == predict.BDC {
		return cost
	}
	cost += decisionCostQ8(p[1], mode != predict.BTM)
	if mode == predict.BTM {
		return cost
	}
	cost += decisionCostQ8(p[2], mode != predict.BVE)
	if mode == predict.BVE {
		return cost
	}

	cost += decisionCostQ8(p[3], mode >= predict.BLD)
	if mode < predict.BLD {
		cost += decisionCostQ8(p[4], mode != predict.BHE)
		if mode == predict.BHE {
			return cost
		}
		return cost + decisionCostQ8(p[5], mode != predict.BRD)
	}

	cost += decisionCostQ8(p[6], mode != predict.BLD)
	if mode == predict.BLD {
		return cost
	}
	cost += decisionCostQ8(p[7], mode != predict.BVL)
	if mode == predict.BVL {
		return cost
	}
	return cost + decisionCostQ8(p[8], mode != predict.BHD)
}

// bPredLumaModeCostQ8 prices the B_PRED branch and sixteen contextual mode
// leaves. It updates boundary contexts exactly like writeBPredLumaMode.
func bPredLumaModeCostQ8(modes *[16]predict.BMode, above, left *[4]predict.BMode) uint64 {
	cost := decisionCostQ8(use16x16Prob, false)
	for y := 0; y < 4; y++ {
		leftMode := left[y]
		for x := 0; x < 4; x++ {
			mode := modes[4*y+x]
			cost += bModeCostQ8(mode, above[x], leftMode)
			above[x] = mode
			leftMode = mode
		}
		left[y] = leftMode
	}
	return cost
}

// lumaRateQ8 separates first-partition pressure from coefficient-partition
// rate while retaining their common Q8 unit. Selection uses the total; the
// control component remains available to the partition-zero safety pass.
type lumaRateQ8 struct {
	control uint64
	tokens  uint64
}

func (r lumaRateQ8) total() uint64 {
	if r.tokens > ^uint64(0)-r.control {
		panic("tqwebp: luma rate overflows uint64")
	}
	return r.control + r.tokens
}

// lumaCandidateRateQ8 prices one already-quantized luma candidate against
// the exact mode and coefficient contexts entering its macroblock. skipProb
// is fixed for the comparison; both candidates therefore see the same branch
// model even when their skip values differ.
func (e *encoder) lumaCandidateRateQ8(mbx, mby int, mb *macroblock, modes *[16]predict.BMode, useBPred bool, skipProb, leftY2, upY2 uint8) lumaRateQ8 {
	rate := lumaRateQ8{control: skipCostQ8(skipProb, mb.skip)}
	if useBPred {
		if modes == nil {
			panic("tqwebp: B_PRED rate requested without submodes")
		}
		aboveModes, leftModes := e.bModeBoundaryContexts(mbx, mby)
		rate.control += bPredLumaModeCostQ8(modes, &aboveModes, &leftModes)
	} else {
		rate.control += lumaModeCostQ8(mb.yMode)
	}

	if mb.skip {
		return rate
	}
	left, up := e.lumaTokenBoundaryContexts(mbx, mby)
	if useBPred {
		rate.tokens = lumaBlockTokenCostQ8(mb, token.YWithDC, 0, &left, &up)
		return rate
	}

	y2Cost, _ := token.BlockCostQ8(&token.DefaultProbs, token.Y2, int(leftY2+upY2), 0, &mb.levels[blockY2])
	rate.tokens = y2Cost
	rate.tokens += lumaBlockTokenCostQ8(mb, token.YAfterY2, 1, &left, &up)
	return rate
}

func lumaBlockTokenCostQ8(mb *macroblock, plane, first int, left, up *[4]uint8) uint64 {
	var cost uint64
	for y := 0; y < 4; y++ {
		nz := left[y]
		for x := 0; x < 4; x++ {
			blockCost, blockNZ := token.BlockCostQ8(
				&token.DefaultProbs,
				plane,
				int(nz+up[x]),
				first,
				&mb.levels[blockLuma+4*y+x],
			)
			if blockCost > ^uint64(0)-cost {
				panic("tqwebp: luma token rate overflows uint64")
			}
			cost += blockCost
			nz = btou(blockNZ)
			up[x] = nz
		}
		left[y] = nz
	}
	return cost
}

// lumaTokenBoundaryContexts recovers the ordinary luma contexts directly
// from the already-selected macroblocks above and to the left.
func (e *encoder) lumaTokenBoundaryContexts(mbx, mby int) (left, above [4]uint8) {
	if mby > 0 {
		mb := &e.mbs[(mby-1)*e.mbw+mbx]
		for x := 0; x < 4; x++ {
			above[x] = btou(mb.nz[blockLuma+12+x])
		}
	}
	if mbx > 0 {
		mb := &e.mbs[mby*e.mbw+mbx-1]
		for y := 0; y < 4; y++ {
			left[y] = btou(mb.nz[blockLuma+4*y+3])
		}
	}
	return left, above
}

// bModeBoundaryContexts recovers the contextual 4x4 modes at a macroblock
// boundary. A whole-block neighbor contributes its mapped mode four times;
// a B_PRED neighbor contributes the bottom row or right column it selected.
func (e *encoder) bModeBoundaryContexts(mbx, mby int) (above, left [4]predict.BMode) {
	if mby > 0 {
		index := (mby-1)*e.mbw + mbx
		mb := &e.mbs[index]
		if mb.useBPred {
			copy(above[:], e.bPredModes[index][12:16])
		} else {
			mode := bModeForLumaMode(mb.yMode)
			for x := range above {
				above[x] = mode
			}
		}
	}
	if mbx > 0 {
		index := mby*e.mbw + mbx - 1
		mb := &e.mbs[index]
		if mb.useBPred {
			for y := range left {
				left[y] = e.bPredModes[index][4*y+3]
			}
		} else {
			mode := bModeForLumaMode(mb.yMode)
			for y := range left {
				left[y] = mode
			}
		}
	}
	return above, left
}

func (e *encoder) y2Contexts(mbx, mby int) (left, above uint8) {
	if mbx > 0 {
		left = e.mbs[mby*e.mbw+mbx-1].y2Right
	}
	if mby > 0 {
		above = e.mbs[(mby-1)*e.mbw+mbx].y2Below
	}
	return left, above
}

func (e *encoder) updateY2Contexts(mbx, mby int, mb *macroblock) {
	left, above := e.y2Contexts(mbx, mby)
	if mb.useBPred {
		mb.y2Right = left
		mb.y2Below = above
		return
	}
	nz := btou(mb.nz[blockY2])
	mb.y2Right = nz
	mb.y2Below = nz
}

// bPredImprovesReconstruction applies the quantizer-scaled candidate prune.
// A small local win can still make later predictors worse because the
// one-macroblock RD score cannot see beyond the current reconstruction
// boundary. Requiring a bounded margin keeps those unstable candidates out.
func bPredImprovesReconstruction(wholeDistortion, bPredDistortion int32, minImprovement int64) bool {
	if minImprovement < 0 {
		panic("tqwebp: negative B_PRED distortion margin")
	}
	return int64(wholeDistortion)-int64(bPredDistortion) > minImprovement
}

// preferBPred applies the canonical tie-break: B_PRED wins only on a strictly
// smaller reconstructed rate-distortion score. Exact ties stay whole-block.
func preferBPred(wholeDistortion, bPredDistortion int32, wholeRate, bPredRate lumaRateQ8, lambda uint64) bool {
	return rdScore(bPredDistortion, bPredRate.total(), lambda) <
		rdScore(wholeDistortion, wholeRate.total(), lambda)
}

// lumaLambda derives the integer rate multiplier used for reconstructed-luma
// macroblock decisions. qAverage is the mean of one Y1 DC factor and fifteen
// Y1 AC factors. The square-over-128 rule is libwebp's lambda_mode derivation
// in src/enc/quant_enc.c; the minimum of one keeps rate significant at the
// finest quantizers.
//
// With rate measured in Q8 units, rdScore computes exactly:
//
//	256 * distortion + lumaLambda(q) * encodedRateQ8
//
// so comparisons contain no division and no platform floating-point drift.
func lumaLambda(q quantize.Quantizer) uint64 {
	if q.Y1.DC <= 0 || q.Y1.AC <= 0 {
		panic("tqwebp: non-positive luma quantizer")
	}
	qAverage := (uint64(q.Y1.DC) + 15*uint64(q.Y1.AC) + 8) >> 4
	lambda := qAverage * qAverage >> 7
	if lambda < 1 {
		return 1
	}
	return lambda
}

func rdScore(distortion int32, rateQ8, lambda uint64) uint64 {
	if distortion < 0 {
		panic("tqwebp: negative distortion")
	}
	distortionQ8 := uint64(distortion) * boolenc.CostScaleQ8
	if lambda != 0 && rateQ8 > (^uint64(0)-distortionQ8)/lambda {
		panic("tqwebp: RD score overflows uint64")
	}
	return distortionQ8 + rateQ8*lambda
}
