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

// TestLambdaRoundsHalvesTowardZero pins the rounding rule on a case
// that lands exactly on a half: at index 16 the step is 20, so
// 85*400+50 = 34050 and the slope is 34.05 bits' worth -- the integer
// derivation truncates to 340. Any future reimplementation that routes
// through floating point and rounds half away from zero would produce
// 341 here, which this pin makes loud.
func TestLambdaRoundsHalvesTowardZero(t *testing.T) {
	q := quantize.New(quantize.Index(16))
	if s := int64(q.Y1.AC); s != 20 {
		t.Fatalf("index 16 carries luma step %d, test assumes 20", s)
	}
	if got, want := Lambda(q), int64(340); got != want {
		t.Fatalf("Lambda(index 16) = %d, want %d", got, want)
	}
}
