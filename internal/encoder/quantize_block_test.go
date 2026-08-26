package encoder

import (
	"testing"

	"m31labs.dev/turboquant/blockdsp"

	"m31labs.dev/tqwebp/internal/quantize"
	"m31labs.dev/tqwebp/internal/token"
)

// TestQuantizeLevelExhaustive pins the fused hot path to the original
// bias-then-blockdsp behavior for every signed 16-bit input and every factor
// the encoder can derive from a VP8 quantizer index.
func TestQuantizeLevelExhaustive(t *testing.T) {
	type factorBias struct {
		factor int16
		bias   int32
	}
	factors := make(map[factorBias]struct{})
	for index := quantize.Index(0); index <= 127; index++ {
		q := quantize.New(index)
		for _, f := range [...]quantize.Factors{q.Y1, q.Y2, q.UV} {
			factors[factorBias{f.DC, int32(f.DC) * biasNumerator / biasDenominator}] = struct{}{}
			factors[factorBias{f.AC, int32(f.AC) * biasNumerator / biasDenominator}] = struct{}{}
		}
	}

	for fb := range factors {
		for value := -32768; value <= 32767; value++ {
			got := quantizeLevel(int16(value), fb.factor, fb.bias)
			want := referenceQuantizeLevel(int16(value), fb.factor, fb.bias)
			if got != want {
				t.Fatalf("factor=%d bias=%d coefficient=%d: got %d, want %d", fb.factor, fb.bias, value, got, want)
			}
		}
	}
}

func TestQuantizeBlockMatchesOriginalPipeline(t *testing.T) {
	coeff := [16]int16{
		-32768, -32767, -2115, -2114, -17, -1, 0, 1,
		3, 4, 15, 2113, 2114, 2115, 32766, 32767,
	}
	for index := quantize.Index(0); index <= 127; index++ {
		q := quantize.New(index)
		for _, f := range [...]quantize.Factors{q.Y1, q.Y2, q.UV} {
			got := quantizeBlock(&coeff, f)
			want := referenceQuantizeBlock(&coeff, f)
			if got != want {
				t.Fatalf("index=%d factors=%+v: got %v, want %v", index, f, got, want)
			}
		}
	}
}

func TestBlockQuantizerMatchesOriginalPipeline(t *testing.T) {
	coeff := [16]int16{
		-32768, -32767, -2115, -2114, -17, -1, 0, 1,
		3, 4, 15, 2113, 2114, 2115, 32766, 32767,
	}
	for _, index := range [...]quantize.Index{0, 1, 24, 75, 117, 127} {
		q := quantize.New(index)
		for _, f := range [...]quantize.Factors{q.Y1, q.Y2, q.UV} {
			want := referenceQuantizeBlock(&coeff, f)
			for _, build := range []bool{false, true} {
				var table blockQuantizer
				table.init(f, build)
				got := table.quantizeBlock(&coeff)
				if got != want {
					t.Fatalf("index=%d factors=%+v build=%t: got %v, want %v", index, f, build, got, want)
				}
			}
		}
	}
}

func referenceQuantizeBlock(coeff *[16]int16, f quantize.Factors) [16]int16 {
	biased := *coeff
	dcBias := int32(f.DC) * biasNumerator / biasDenominator
	acBias := int32(f.AC) * biasNumerator / biasDenominator
	for i := range biased {
		bias := acBias
		if i == 0 {
			bias = dcBias
		}
		v := int32(biased[i])
		if v > 0 {
			v += bias
		} else if v < 0 {
			v -= bias
		}
		biased[i] = int16(v)
	}

	levels := blockdsp.QuantizeBlock(&biased, f.DC, f.AC)
	for i, level := range levels {
		if level > token.MaxLevel {
			levels[i] = token.MaxLevel
		} else if level < -token.MaxLevel {
			levels[i] = -token.MaxLevel
		}
	}
	return levels
}

func referenceQuantizeLevel(coeff, factor int16, bias int32) int16 {
	v := int32(coeff)
	if v > 0 {
		v += bias
	} else if v < 0 {
		v -= bias
	}
	biased := int16(v)
	var block [16]int16
	block[0] = biased
	level := blockdsp.QuantizeBlock(&block, factor, factor)[0]
	if level > token.MaxLevel {
		return token.MaxLevel
	}
	if level < -token.MaxLevel {
		return -token.MaxLevel
	}
	return level
}
