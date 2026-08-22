package cost

import "m31labs.dev/tqwebp/internal/quantize"

// The rate-distortion trade-off constant is the rational 85/100: at
// every step size s, lambda = round(85/100 * s^2). Expressed as a
// fraction and rounded with an explicit half, it derives without any
// floating point, so the same quality always yields the same integer on
// every platform.
const (
	lambdaNumerator   = 85
	lambdaDenominator = 100
)

// Lambda returns the integer rate-distortion slope for one frame's
// quantizer. Distortion is measured as the sum of squared sample errors
// of a candidate block; rate is measured in whole bits, so a search
// compares candidates on SSE + Lambda*R.
//
// The step comes from the luma alternating-current factor Y1.AC, which
// prices most coefficients in both luma paths -- the whole-block path
// quantizes its alternating-current positions with it, and the B_PRED
// path its per-block residuals too. The direct-current factors move in
// lockstep with it across the index range (the tables of RFC 6386
// section 14.1 are near-proportional), so one scalar carries the
// trade-off.
//
// The value grows monotonically with the quantizer index: coarse steps
// make distortion cheap relative to bits, fine steps the reverse. It is
// exact integer arithmetic throughout.
func Lambda(q quantize.Quantizer) int64 {
	s := int64(q.Y1.AC)
	return (lambdaNumerator*s*s + lambdaDenominator/2) / lambdaDenominator
}

// LambdaForQuality derives the rate-distortion slope straight from the
// public quality knob, through the same quality-to-index map and
// index-to-factor tables the encoder itself uses. Quality values below
// 0 and above 100 clamp exactly as Encode clamps them.
func LambdaForQuality(quality int) int64 {
	return Lambda(quantize.New(quantize.IndexForQuality(quality)))
}
