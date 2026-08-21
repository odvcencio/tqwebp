package encoder

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/quantize"
)

type syntaxRateRecorder struct {
	costQ8 uint64
}

func (r *syntaxRateRecorder) WriteBool(prob uint8, bit bool) {
	r.costQ8 += uint64(boolenc.DecisionCostQ8(prob, bit))
}

// The trace helpers below are test-side RFC tree walks. They are deliberately
// separate from both the production syntax writers and the fixed-point cost
// functions, so a shared branch error cannot make the cross-check pass.
func traceLumaMode(r *syntaxRateRecorder, mode predict.Mode) {
	r.WriteBool(use16x16Prob, true)
	switch mode {
	case predict.DC:
		r.WriteBool(lumaDCvsRestProb, false)
		r.WriteBool(lumaDCvsVProb, false)
	case predict.V:
		r.WriteBool(lumaDCvsRestProb, false)
		r.WriteBool(lumaDCvsVProb, true)
	case predict.H:
		r.WriteBool(lumaDCvsRestProb, true)
		r.WriteBool(lumaHvsTMProb, false)
	case predict.TM:
		r.WriteBool(lumaDCvsRestProb, true)
		r.WriteBool(lumaHvsTMProb, true)
	default:
		panic("test oracle: invalid luma mode")
	}
}

func traceChromaMode(r *syntaxRateRecorder, mode predict.Mode) {
	switch mode {
	case predict.DC:
		r.WriteBool(chromaDCProb, false)
	case predict.V:
		r.WriteBool(chromaDCProb, true)
		r.WriteBool(chromaVProb, false)
	case predict.H:
		r.WriteBool(chromaDCProb, true)
		r.WriteBool(chromaVProb, true)
		r.WriteBool(chromaHProb, false)
	case predict.TM:
		r.WriteBool(chromaDCProb, true)
		r.WriteBool(chromaVProb, true)
		r.WriteBool(chromaHProb, true)
	default:
		panic("test oracle: invalid chroma mode")
	}
}

func traceBMode(r *syntaxRateRecorder, mode, above, left predict.BMode) {
	if mode >= predict.NumBModes || above >= predict.NumBModes || left >= predict.NumBModes {
		panic("test oracle: invalid 4x4 mode")
	}
	p := &bModeProbs[above][left]
	r.WriteBool(p[0], mode != predict.BDC)
	if mode == predict.BDC {
		return
	}
	r.WriteBool(p[1], mode != predict.BTM)
	if mode == predict.BTM {
		return
	}
	r.WriteBool(p[2], mode != predict.BVE)
	if mode == predict.BVE {
		return
	}
	r.WriteBool(p[3], mode >= predict.BLD)
	if mode < predict.BLD {
		r.WriteBool(p[4], mode != predict.BHE)
		if mode != predict.BHE {
			r.WriteBool(p[5], mode != predict.BRD)
		}
		return
	}
	r.WriteBool(p[6], mode != predict.BLD)
	if mode == predict.BLD {
		return
	}
	r.WriteBool(p[7], mode != predict.BVL)
	if mode != predict.BVL {
		r.WriteBool(p[8], mode != predict.BHD)
	}
}

func traceBPredLumaMode(r *syntaxRateRecorder, modes *[16]predict.BMode, above, left *[4]predict.BMode) {
	r.WriteBool(use16x16Prob, false)
	for y := 0; y < 4; y++ {
		leftMode := left[y]
		for x := 0; x < 4; x++ {
			mode := modes[4*y+x]
			traceBMode(r, mode, above[x], leftMode)
			above[x] = mode
			leftMode = mode
		}
		left[y] = leftMode
	}
}

func traceSegmentID(r *syntaxRateRecorder, segment uint8, probs *[3]uint8) {
	if segment >= 4 {
		panic("test oracle: invalid segment ID")
	}
	hi := segment >= 2
	r.WriteBool(probs[0], hi)
	child := 1
	if hi {
		child = 2
	}
	r.WriteBool(probs[child], segment&1 != 0)
}

func TestWholeModeCostsMatchEverySyntaxLeaf(t *testing.T) {
	wantLuma := [predict.NumModes]uint64{663, 872, 919, 919}
	wantChroma := [predict.NumModes]uint64{218, 601, 648, 990}
	for mode := predict.Mode(0); mode < predict.NumModes; mode++ {
		var luma syntaxRateRecorder
		traceLumaMode(&luma, mode)
		if got := lumaModeCostQ8(mode); got != luma.costQ8 || got != wantLuma[mode] {
			t.Errorf("luma mode %s cost = %d, syntax trace = %d, reference = %d", mode, got, luma.costQ8, wantLuma[mode])
		}

		var chroma syntaxRateRecorder
		traceChromaMode(&chroma, mode)
		if got := chromaModeCostQ8(mode); got != chroma.costQ8 || got != wantChroma[mode] {
			t.Errorf("chroma mode %s cost = %d, syntax trace = %d, reference = %d", mode, got, chroma.costQ8, wantChroma[mode])
		}
	}
}

func TestBModeCostMatchesEveryContextAndLeaf(t *testing.T) {
	buf := make([]byte, 0, int(predict.NumBModes)*int(predict.NumBModes)*int(predict.NumBModes)*2)
	for above := predict.BMode(0); above < predict.NumBModes; above++ {
		for left := predict.BMode(0); left < predict.NumBModes; left++ {
			for mode := predict.BMode(0); mode < predict.NumBModes; mode++ {
				var recorder syntaxRateRecorder
				traceBMode(&recorder, mode, above, left)
				got := bModeCostQ8(mode, above, left)
				if got != recorder.costQ8 {
					t.Fatalf("above=%s left=%s mode=%s: cost = %d, syntax trace = %d", above, left, mode, got, recorder.costQ8)
				}
				if got > 1<<16-1 {
					t.Fatalf("above=%s left=%s mode=%s: cost %d does not fit the golden encoding", above, left, mode, got)
				}
				var word [2]byte
				binary.BigEndian.PutUint16(word[:], uint16(got))
				buf = append(buf, word[:]...)
			}
		}
	}
	got := sha256.Sum256(buf)
	const want = "f154943d71ea2fbb4f60d4ef1ee596c0cc6fdbb48adf72468988929cc3c3e2ae"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("contextual B-mode cost SHA-256 = %x, want %s", got, want)
	}
}

func TestBPredMacroblockCostMatchesSyntaxAndContexts(t *testing.T) {
	modeSets := [][16]predict.BMode{
		{},
		{
			predict.BDC, predict.BTM, predict.BVE, predict.BHE,
			predict.BRD, predict.BVR, predict.BLD, predict.BVL,
			predict.BHD, predict.BHU, predict.BDC, predict.BVR,
			predict.BLD, predict.BHE, predict.BTM, predict.BHU,
		},
	}
	contexts := []struct {
		above [4]predict.BMode
		left  [4]predict.BMode
	}{
		{},
		{
			above: [4]predict.BMode{predict.BTM, predict.BVE, predict.BHE, predict.BRD},
			left:  [4]predict.BMode{predict.BVR, predict.BLD, predict.BVL, predict.BHD},
		},
	}
	for _, modes := range modeSets {
		for _, context := range contexts {
			writerAbove, writerLeft := context.above, context.left
			var recorder syntaxRateRecorder
			traceBPredLumaMode(&recorder, &modes, &writerAbove, &writerLeft)

			costAbove, costLeft := context.above, context.left
			got := bPredLumaModeCostQ8(&modes, &costAbove, &costLeft)
			if got != recorder.costQ8 {
				t.Errorf("modes=%v contexts=%v/%v: cost = %d, syntax trace = %d", modes, context.above, context.left, got, recorder.costQ8)
			}
			if costAbove != writerAbove || costLeft != writerLeft {
				t.Errorf("modes=%v: cost contexts %v/%v, syntax contexts %v/%v", modes, costAbove, costLeft, writerAbove, writerLeft)
			}
		}
	}
}

func TestSkipCostEveryProbabilityAndValue(t *testing.T) {
	for prob := 1; prob <= 255; prob++ {
		for _, skip := range []bool{false, true} {
			var recorder syntaxRateRecorder
			recorder.WriteBool(uint8(prob), skip)
			if got := skipCostQ8(uint8(prob), skip); got != recorder.costQ8 {
				t.Fatalf("probability=%d skip=%v: cost = %d, syntax trace = %d", prob, skip, got, recorder.costQ8)
			}
		}
	}
}

func TestSegmentIDCostEveryLeafAndProbability(t *testing.T) {
	for segment := uint8(0); segment < 4; segment++ {
		for varied := 0; varied < 3; varied++ {
			for prob := 1; prob <= 255; prob++ {
				probs := [3]uint8{73, 149, 211}
				probs[varied] = uint8(prob)
				var recorder syntaxRateRecorder
				traceSegmentID(&recorder, segment, &probs)
				if got := segmentIDCostQ8(segment, &probs); got != recorder.costQ8 {
					t.Fatalf("segment=%d probs=%v: cost = %d, syntax trace = %d", segment, probs, got, recorder.costQ8)
				}
			}
		}
	}
}

func TestCostPrimitivesRejectInvalidModesAndSegments(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{"luma mode", func() { lumaModeCostQ8(predict.NumModes) }},
		{"chroma mode", func() { chromaModeCostQ8(predict.NumModes) }},
		{"B mode", func() { bModeCostQ8(predict.NumBModes, predict.BDC, predict.BDC) }},
		{"B above", func() { bModeCostQ8(predict.BDC, predict.NumBModes, predict.BDC) }},
		{"B left", func() { bModeCostQ8(predict.BDC, predict.BDC, predict.NumBModes) }},
		{"segment syntax", func() { writeSegmentID(boolenc.New(0), 4, &[3]uint8{1, 1, 1}) }},
		{"segment cost", func() { segmentIDCostQ8(4, &[3]uint8{1, 1, 1}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid value was accepted")
				}
			}()
			tt.fn()
		})
	}
}

func TestLumaLambdaGoldenAllQuantizers(t *testing.T) {
	buf := make([]byte, 0, 128*8)
	last := uint64(0)
	for index := quantize.Index(0); index <= 127; index++ {
		lambda := lumaLambda(quantize.New(index))
		if lambda < last {
			t.Fatalf("quantizer %d lambda %d is below prior lambda %d", index, lambda, last)
		}
		last = lambda
		var word [8]byte
		binary.BigEndian.PutUint64(word[:], lambda)
		buf = append(buf, word[:]...)
	}
	got := sha256.Sum256(buf)
	const want = "3a55bf76371451109d9a5be672599c0a7426db9c1db8dc1729af5825d9d03a0d"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("luma lambda SHA-256 = %x, want %s", got, want)
	}

	anchors := map[quantize.Index]uint64{
		0:   1,
		22:  5,
		64:  46,
		127: 595,
	}
	for index, want := range anchors {
		if got := lumaLambda(quantize.New(index)); got != want {
			t.Errorf("quantizer %d lambda = %d, want %d", index, got, want)
		}
	}
}

func TestRDScoreGolden(t *testing.T) {
	const (
		distortion = int32(123456)
		rateQ8     = uint64(7890)
		lambda     = uint64(17)
		want       = uint64(31738866)
	)
	if got := rdScore(distortion, rateQ8, lambda); got != want {
		t.Fatalf("rdScore(%d, %d, %d) = %d, want %d", distortion, rateQ8, lambda, got, want)
	}
}

func TestRDScoreRejectsNegativeDistortion(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("rdScore accepted negative distortion")
		}
	}()
	rdScore(-1, 0, 1)
}

func TestRDScoreRejectsOverflow(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("rdScore accepted an overflowing score")
		}
	}()
	rdScore(0, ^uint64(0), 2)
}

func TestLumaLambdaRejectsInvalidFactors(t *testing.T) {
	for _, factors := range []quantize.Factors{{DC: 0, AC: 1}, {DC: 1, AC: 0}, {DC: -1, AC: 1}, {DC: 1, AC: -1}} {
		t.Run("invalid", func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("lumaLambda accepted factors %+v", factors)
				}
			}()
			lumaLambda(quantize.Quantizer{Y1: factors})
		})
	}
}
