package token

import (
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
)

type rateRecorder struct {
	costQ8    uint64
	decisions int
}

func (r *rateRecorder) WriteBool(prob uint8, bit bool) {
	r.costQ8 += uint64(boolenc.DecisionCostQ8(prob, bit))
	r.decisions++
}

func (r *rateRecorder) reset() {
	r.costQ8 = 0
	r.decisions = 0
}

// traceBlock is the test oracle for the RFC 6386 coefficient syntax. It
// records branch probabilities and values independently of BlockCostQ8 and
// of the production Writer's arithmetic-coder state.
func traceBlock(recorder *rateRecorder, probs *Probs, plane, ctx, first int, levels *[16]int16) bool {
	last := -1
	for i := 15; i >= first; i-- {
		if levels[i] != 0 {
			last = i
			break
		}
	}

	planeProbs := &probs[plane]
	n := first
	p := &planeProbs[Bands[n]][ctx]
	if last < 0 {
		recorder.WriteBool(p[0], false)
		return false
	}
	recorder.WriteBool(p[0], true)

	for n < 16 {
		v := levels[n]
		if v == 0 {
			recorder.WriteBool(p[1], false)
			n++
			p = &planeProbs[Bands[n]][0]
			continue
		}

		recorder.WriteBool(p[1], true)
		mag := int(v)
		if mag < 0 {
			mag = -mag
		}
		if mag > MaxLevel {
			panic("test oracle: level exceeds codable magnitude")
		}

		nextCtx := 2
		if mag == 1 {
			recorder.WriteBool(p[2], false)
			nextCtx = 1
		} else {
			recorder.WriteBool(p[2], true)
			traceMagnitude(recorder, p, mag)
		}
		recorder.WriteBool(128, v < 0)

		n++
		if n == 16 {
			return true
		}
		p = &planeProbs[Bands[n]][nextCtx]
		if n > last {
			recorder.WriteBool(p[0], false)
			return true
		}
		recorder.WriteBool(p[0], true)
	}
	return true
}

func traceMagnitude(recorder *rateRecorder, p *[NumProbs]uint8, mag int) {
	switch {
	case mag <= 4:
		recorder.WriteBool(p[3], false)
		if mag == 2 {
			recorder.WriteBool(p[4], false)
			return
		}
		recorder.WriteBool(p[4], true)
		recorder.WriteBool(p[5], mag == 4)

	case mag <= 10:
		recorder.WriteBool(p[3], true)
		recorder.WriteBool(p[6], false)
		if mag <= 6 {
			recorder.WriteBool(p[7], false)
			recorder.WriteBool(cat1Prob, mag == 6)
			return
		}
		recorder.WriteBool(p[7], true)
		recorder.WriteBool(cat2Prob0, (mag-7)>>1 == 1)
		recorder.WriteBool(cat2Prob1, (mag-7)&1 == 1)

	default:
		cat, base, bits := 3, 67, 11
		switch {
		case mag <= 18:
			cat, base, bits = 0, 11, 3
		case mag <= 34:
			cat, base, bits = 1, 19, 4
		case mag <= 66:
			cat, base, bits = 2, 35, 5
		}
		b1, b0 := cat>>1, cat&1
		recorder.WriteBool(p[3], true)
		recorder.WriteBool(p[6], true)
		recorder.WriteBool(p[8], b1 == 1)
		recorder.WriteBool(p[9+b1], b0 == 1)

		rest := mag - base
		for i := 0; i < bits; i++ {
			recorder.WriteBool(ExtraProbs[cat][i], rest>>(bits-1-i)&1 == 1)
		}
	}
}

func traceProbUpdate(recorder *rateRecorder, updateProb, newProb uint8, update bool) {
	recorder.WriteBool(updateProb, update)
	if !update {
		return
	}
	for bit := 7; bit >= 0; bit-- {
		recorder.WriteBool(128, newProb>>uint(bit)&1 != 0)
	}
}

func walkingProbs() Probs {
	var probs Probs
	n := 0
	for i := range probs {
		for j := range probs[i] {
			for k := range probs[i][j] {
				for l := range probs[i][j][k] {
					probs[i][j][k][l] = uint8(1 + (n*73)%255)
					n++
				}
			}
		}
	}
	return probs
}

func checkBlockCost(t *testing.T, probs *Probs, plane, ctx, first int, levels *[16]int16) {
	var recorder rateRecorder
	wantNZ := traceBlock(&recorder, probs, plane, ctx, first, levels)
	gotCost, gotNZ := BlockCostQ8(probs, plane, ctx, first, levels)
	if gotCost != recorder.costQ8 || gotNZ != wantNZ {
		t.Fatalf("plane=%d ctx=%d first=%d levels=%v: cost/nz = %d/%v, syntax trace = %d/%v (%d decisions)",
			plane, ctx, first, *levels, gotCost, gotNZ, recorder.costQ8, wantNZ, recorder.decisions)
	}
}

func TestBlockCostQ8EveryMagnitudePositionPlaneAndContext(t *testing.T) {
	probs := walkingProbs()
	for plane := 0; plane < NumPlanes; plane++ {
		for ctx := 0; ctx < NumContexts; ctx++ {
			for first := 0; first <= 1; first++ {
				for pos := first; pos < 16; pos++ {
					for mag := -MaxLevel; mag <= MaxLevel; mag++ {
						var levels [16]int16
						levels[pos] = int16(mag)
						checkBlockCost(t, &probs, plane, ctx, first, &levels)
					}
				}
			}
		}
	}
}

func TestBlockCostQ8MultiCoefficientContexts(t *testing.T) {
	probsSets := []struct {
		name  string
		probs *Probs
	}{
		{name: "default", probs: &DefaultProbs},
	}
	walking := walkingProbs()
	probsSets = append(probsSets, struct {
		name  string
		probs *Probs
	}{name: "walking", probs: &walking})

	patterns := [][16]int16{
		{},
		{1, -1, 2, -2, 4, -4, 6, -6, 10, -10, 11, -18, 35, -66, 67, -2114},
		{0, 0, 0, 1, 0, 0, 2, 0, 0, 0, -3, 0, 0, 0, 0, -1},
		{-2114, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2114},
	}
	for _, set := range probsSets {
		t.Run(set.name, func(t *testing.T) {
			for plane := 0; plane < NumPlanes; plane++ {
				for ctx := 0; ctx < NumContexts; ctx++ {
					for first := 0; first <= 1; first++ {
						for i := range patterns {
							levels := patterns[i]
							checkBlockCost(t, set.probs, plane, ctx, first, &levels)
						}
					}
				}
			}
		})
	}
}

func TestBlockCostAndWriterRejectEveryOutOfRangeLevel(t *testing.T) {
	values := []int16{MaxLevel + 1, -MaxLevel - 1, 32767, -32768}
	for _, value := range values {
		levels := [16]int16{value}
		assertPanics(t, "writer", value, func() {
			traceBlock(&rateRecorder{}, &DefaultProbs, YWithDC, 0, 0, &levels)
		})
		assertPanics(t, "cost", value, func() {
			BlockCostQ8(&DefaultProbs, YWithDC, 0, 0, &levels)
		})
	}
}

func assertPanics(t *testing.T, operation string, value int16, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s accepted level %d", operation, value)
		}
	}()
	fn()
}

func TestProbUpdateCostQ8EveryGateAndReplacement(t *testing.T) {
	newProbs := []uint8{0, 1, 2, 127, 128, 254, 255}
	for gate := 1; gate <= 255; gate++ {
		for _, update := range []bool{false, true} {
			for _, newProb := range newProbs {
				var recorder rateRecorder
				traceProbUpdate(&recorder, uint8(gate), newProb, update)
				if got, want := ProbUpdateCostQ8(uint8(gate), update), recorder.costQ8; got != want {
					t.Fatalf("gate=%d update=%v replacement=%d: cost %d, syntax trace %d", gate, update, newProb, got, want)
				}
				wantDecisions := 1
				if update {
					wantDecisions += 8
				}
				if recorder.decisions != wantDecisions {
					t.Fatalf("gate=%d update=%v replacement=%d: wrote %d decisions, want %d", gate, update, newProb, recorder.decisions, wantDecisions)
				}
			}
		}
	}

	if got := ProbUpdateCostQ8(255, false); got != 3 {
		t.Errorf("no-update cost at probability 255 = %d, want 3", got)
	}
	if got := ProbUpdateCostQ8(255, true); got != 3840 {
		t.Errorf("update cost at probability 255 = %d, want 3840", got)
	}
}

func TestAllDefaultProbUpdateCostsMatchSyntax(t *testing.T) {
	var recorder rateRecorder
	for i := 0; i < NumPlanes; i++ {
		for j := 0; j < NumBands; j++ {
			for k := 0; k < NumContexts; k++ {
				for l := 0; l < NumProbs; l++ {
					traceProbUpdate(&recorder, UpdateProbs[i][j][k][l], 0, false)
				}
			}
		}
	}
	if got, want := ProbUpdatesCostQ8(), recorder.costQ8; got != want {
		t.Fatalf("default probability-update cost = %d, syntax trace = %d", got, want)
	}
	if recorder.decisions != NumPlanes*NumBands*NumContexts*NumProbs {
		t.Fatalf("default probability updates wrote %d decisions, want %d", recorder.decisions, NumPlanes*NumBands*NumContexts*NumProbs)
	}
}

func TestWriteProbUpdateRoundTrip(t *testing.T) {
	type update struct {
		gate, replacement uint8
		present           bool
	}
	updates := []update{
		{gate: 1, replacement: 0, present: false},
		{gate: 255, replacement: 0, present: true},
		{gate: 17, replacement: 1, present: true},
		{gate: 128, replacement: 127, present: true},
		{gate: 254, replacement: 255, present: true},
	}
	enc := boolenc.New(0)
	for _, update := range updates {
		WriteProbUpdate(enc, update.gate, update.replacement, update.present)
	}
	dec := boolenc.NewDecoder(enc.Finish())
	for i, update := range updates {
		if got := dec.ReadBool(update.gate); got != update.present {
			t.Fatalf("update %d present = %v, want %v", i, got, update.present)
		}
		if !update.present {
			continue
		}
		if got := uint8(dec.ReadLiteral(8)); got != update.replacement {
			t.Fatalf("update %d replacement = %d, want %d", i, got, update.replacement)
		}
	}
	if dec.UnexpectedEOF() {
		t.Fatal("decoder exhausted probability-update stream")
	}
}

func TestWriteAllDefaultProbUpdatesRoundTrip(t *testing.T) {
	enc := boolenc.New(0)
	WriteProbUpdates(enc)
	dec := boolenc.NewDecoder(enc.Finish())
	for i := 0; i < NumPlanes; i++ {
		for j := 0; j < NumBands; j++ {
			for k := 0; k < NumContexts; k++ {
				for l := 0; l < NumProbs; l++ {
					if dec.ReadBool(UpdateProbs[i][j][k][l]) {
						t.Fatalf("default probability update [%d][%d][%d][%d] was present", i, j, k, l)
					}
				}
			}
		}
	}
	if dec.UnexpectedEOF() {
		t.Fatal("decoder exhausted default probability-update stream")
	}
}
