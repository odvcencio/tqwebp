package cost

import (
	"testing"

	"m31labs.dev/tqwebp/internal/quantize"
)

func TestLambdasFollowReferenceFormulaAcrossAllIndexes(t *testing.T) {
	for idx := 0; idx <= 127; idx++ {
		q := quantize.New(quantize.Index(idx))
		s := (int64(q.Y1.DC) + 15*int64(q.Y1.AC) + 8) >> 4
		wantMode := s * s >> 7
		wantTrellis := 7 * s * s >> 3
		if wantMode < 1 {
			wantMode = 1
		}
		if wantTrellis < 1 {
			wantTrellis = 1
		}
		if got := ModeLambda(q); got != wantMode {
			t.Fatalf("ModeLambda(index %d) = %d, want %d", idx, got, wantMode)
		}
		if got := TrellisLambda(q); got != wantTrellis {
			t.Fatalf("TrellisLambda(index %d) = %d, want %d", idx, got, wantTrellis)
		}
	}
}

func TestLambdaGoldenQualityBoundaries(t *testing.T) {
	tests := []struct {
		quality       int
		mode, trellis int64
	}{
		{0, 595, 66654},
		{5, 140, 15711},
		{10, 51, 5740},
		{20, 20, 2275},
		{30, 15, 1694},
		{40, 10, 1197},
		{50, 7, 840},
		{60, 6, 735},
		{70, 3, 423},
		{75, 2, 252},
		{80, 1, 196},
		{85, 1, 147},
		{90, 1, 105},
		{95, 1, 42},
		{99, 1, 21},
		{100, 1, 14},
	}
	for _, tt := range tests {
		if got := ModeLambdaForQuality(tt.quality); got != tt.mode {
			t.Errorf("ModeLambdaForQuality(%d) = %d, want %d", tt.quality, got, tt.mode)
		}
		if got := TrellisLambdaForQuality(tt.quality); got != tt.trellis {
			t.Errorf("TrellisLambdaForQuality(%d) = %d, want %d", tt.quality, got, tt.trellis)
		}
	}
}

func TestLambdasAreMonotoneAndDistinct(t *testing.T) {
	prevMode, prevTrellis := int64(0), int64(0)
	for idx := 0; idx <= 127; idx++ {
		q := quantize.New(quantize.Index(idx))
		mode, trellis := ModeLambda(q), TrellisLambda(q)
		if mode < prevMode || trellis < prevTrellis {
			t.Fatalf("lambda decreased at index %d: mode %d->%d trellis %d->%d",
				idx, prevMode, mode, prevTrellis, trellis)
		}
		if trellis < mode {
			t.Fatalf("index %d: trellis lambda %d below mode lambda %d", idx, trellis, mode)
		}
		prevMode, prevTrellis = mode, trellis
	}
	if ModeLambdaForQuality(75) == TrellisLambdaForQuality(75) {
		t.Fatal("default-quality mode and trellis lambdas unexpectedly coincide")
	}
}

func TestLambdasClampWithQualityAndStayPositive(t *testing.T) {
	for _, pair := range [][2]int{{-3, 0}, {107, 100}} {
		if got, want := ModeLambdaForQuality(pair[0]), ModeLambdaForQuality(pair[1]); got != want {
			t.Errorf("mode quality %d = %d, want quality %d value %d", pair[0], got, pair[1], want)
		}
		if got, want := TrellisLambdaForQuality(pair[0]), TrellisLambdaForQuality(pair[1]); got != want {
			t.Errorf("trellis quality %d = %d, want quality %d value %d", pair[0], got, pair[1], want)
		}
	}
	for quality := 0; quality <= 100; quality++ {
		if ModeLambdaForQuality(quality) < 1 || TrellisLambdaForQuality(quality) < 1 {
			t.Fatalf("quality %d produced a non-positive lambda", quality)
		}
	}
}
