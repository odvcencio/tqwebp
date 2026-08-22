package cost

import (
	"math"
	"testing"

	"m31labs.dev/tqwebp/internal/quantize"
)

// TestLambdaFollowsTheFormulaAcrossAllIndexes recomputes the slope
// definition -- round(85/100 * s^2) with s the luma alternating-current
// factor -- for every quantizer index straight from the normative factor
// table, and requires exact agreement.
func TestLambdaFollowsTheFormulaAcrossAllIndexes(t *testing.T) {
	for idx := 0; idx <= 127; idx++ {
		q := quantize.New(quantize.Index(idx))
		s := int64(q.Y1.AC)
		want := (85*s*s + 50) / 100
		if got := Lambda(q); got != want {
			t.Fatalf("Lambda(index %d) = %d, want %d", idx, got, want)
		}
		if q.Y1.AC <= 0 {
			t.Fatalf("index %d has a nonpositive luma step", idx)
		}
	}
}

// TestLambdaForQualityGoldenBoundaries pins representative points of the
// public knob: both clamped ends, the calibrated default, and steps in
// between.
func TestLambdaForQualityGoldenBoundaries(t *testing.T) {
	golden := map[int]int64{
		0:   68558, // coarsest step, quality clamps at 0
		5:   15954,
		10:  5715,
		20:  2211,
		30:  1646,
		40:  1164,
		50:  817,
		60:  715,
		70:  620,
		75:  575, // the default-quality neighbourhood
		80:  411,
		85:  246,
		90:  103,
		95:  42,
		99:  21,
		100: 14, // finest step, quality clamps at 100
	}
	for quality, want := range golden {
		if got := LambdaForQuality(quality); got != want {
			t.Errorf("LambdaForQuality(%d) = %d, want pinned %d", quality, got, want)
		}
	}
}

// TestLambdaIsMonotone pins the ordering the search relies on: coarser
// quantization never lowers lambda, and higher quality never raises it.
func TestLambdaIsMonotone(t *testing.T) {
	prev := int64(-1)
	for idx := 0; idx <= 127; idx++ {
		got := Lambda(quantize.New(quantize.Index(idx)))
		if got < prev {
			t.Fatalf("lambda decreased from index %d to %d: %d then %d", idx-1, idx, prev, got)
		}
		prev = got
	}

	prev = -1
	lo, hi := int64(math.MaxInt64), int64(0)
	for quality := 0; quality <= 100; quality++ {
		got := LambdaForQuality(quality)
		if prev >= 0 && got > prev {
			t.Fatalf("lambda rose from quality %d to %d: %d then %d", quality-1, quality, prev, got)
		}
		prev = got
		if got < lo {
			lo = got
		}
		if got > hi {
			hi = got
		}
	}
	// The knob must actually span the trade-off: coarsest over finest at
	// least three orders of magnitude. This survives anchor
	// recalibration, unlike counting distinct values.
	if hi < 1000*lo {
		t.Errorf("slope spread too narrow: coarsest %d, finest %d", hi, lo)
	}
}

// TestLambdaClampsWithThePublicKnob keeps out-of-range qualities on the
// same slope as the boundary they clamp to.
func TestLambdaClampsWithThePublicKnob(t *testing.T) {
	if got, want := LambdaForQuality(-3), LambdaForQuality(0); got != want {
		t.Errorf("quality -3 gave %d, want the quality-0 slope %d", got, want)
	}
	if got, want := LambdaForQuality(107), LambdaForQuality(100); got != want {
		t.Errorf("quality 107 gave %d, want the quality-100 slope %d", got, want)
	}
}

// TestLambdaNearestIntegerRounding pins the rounding rule accurately.
// The slope is the rational 85*s^2/100, rounded to the nearest integer
// by the integer idiom (85*s^2 + 50) / 100: add half the denominator,
// then truncate. An exact tie -- a fraction of exactly one half --
// would require 85*s^2 == 50 (mod 100), which reduces to
// s^2 == 10 (mod 20). Squares modulo 20 are only 0, 1, 4, 5, 9, or 16,
// so no integer s satisfies it. Ties are therefore
// impossible and the idiom is plain nearest-integer rounding, not a
// half-rounding rule; the sweep below proves the same over every step
// the normative factor table can produce, whose fractions are
// multiples of 1/100 and never 1/2.
//
// The real factor table supplies steps on both sides of the boundary.
// The closest achievable fractions to one half are 40/100 and 60/100:
// index 18 carries step 22, so the slope is 411.40 and must round
// down; index 22 carries step 26, so the slope is 574.60 and must
// round up. Index 16 carries step 20, whose slope is exactly 340 -- an
// integral value that needs no rounding at all.
func TestLambdaNearestIntegerRounding(t *testing.T) {
	cases := []struct {
		index quantize.Index
		step  int64
		slope int64
	}{
		{16, 20, 340}, // 340.00: integral, no rounding
		{18, 22, 411}, // 411.40: below the half boundary, rounds down
		{22, 26, 575}, // 574.60: above the half boundary, rounds up
	}
	for _, c := range cases {
		q := quantize.New(c.index)
		if s := int64(q.Y1.AC); s != c.step {
			t.Fatalf("index %d carries luma step %d, test assumes %d", c.index, s, c.step)
		}
		if got, want := Lambda(q), c.slope; got != want {
			t.Fatalf("Lambda(index %d) = %d, want %d", c.index, got, want)
		}
	}

	// No step in the normative table lands on an exact half: the
	// numerator residue is never half the denominator.
	for idx := 0; idx <= 127; idx++ {
		s := int64(quantize.New(quantize.Index(idx)).Y1.AC)
		if residue := (lambdaNumerator * s * s) % lambdaDenominator; residue == lambdaDenominator/2 {
			t.Fatalf("index %d: step %d lands on an exact half, the impossible case", idx, s)
		}
	}
}
