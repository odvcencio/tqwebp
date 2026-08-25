package cost

import "m31labs.dev/tqwebp/internal/quantize"

// VP8 uses different rate-distortion slopes for macroblock mode decisions
// and coefficient trellis decisions. Keeping them separate is essential:
// the trellis slope is deliberately about two orders of magnitude larger
// and must not be reused to choose between whole-block and B_PRED modes.
const (
	modeLambdaShift    = 7
	trellisLambdaNum   = 7
	trellisLambdaShift = 3
)

// lumaStep returns the rounded mean of the sixteen Y1 quantizer factors:
// one DC factor and fifteen AC factors. It is the scalar q_i4 used by the
// reference VP8 encoder after expanding the luma matrix.
func lumaStep(q quantize.Quantizer) int64 {
	return (int64(q.Y1.DC) + 15*int64(q.Y1.AC) + 8) >> 4
}

func atLeastOne(v int64) int64 {
	if v < 1 {
		return 1
	}
	return v
}

// ModeLambda returns the slope for final whole-block versus B_PRED mode
// selection. With rate in Q8 units, the encoder compares
//
//	SSE*256 + ModeLambda*rateQ8.
//
// The q_i4^2/128 rule matches libwebp's lambda_mode derivation and is kept
// integer-only for deterministic results.
func ModeLambda(q quantize.Quantizer) int64 {
	s := lumaStep(q)
	return atLeastOne(s * s >> modeLambdaShift)
}

// TrellisLambda returns the slope for bounded coefficient refinement. The
// 7*q_i4^2/8 rule matches libwebp's luma-4x4 trellis derivation. It is much
// larger than ModeLambda because it ranks nearby coefficient levels inside
// an already-considered coding mode; it must never be used as the final mode
// selection slope.
func TrellisLambda(q quantize.Quantizer) int64 {
	s := lumaStep(q)
	return atLeastOne(trellisLambdaNum * s * s >> trellisLambdaShift)
}

// ModeLambdaForQuality derives ModeLambda through the public quality map.
func ModeLambdaForQuality(quality int) int64 {
	return ModeLambda(quantize.New(quantize.IndexForQuality(quality)))
}

// TrellisLambdaForQuality derives TrellisLambda through the public quality map.
func TrellisLambdaForQuality(quality int) int64 {
	return TrellisLambda(quantize.New(quantize.IndexForQuality(quality)))
}
