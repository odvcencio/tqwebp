package encoder

import (
	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/quantize"
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
