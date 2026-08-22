package cost

import (
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/predict"
)

// decision is one priced boolean branch: probability and bit.
type decision struct {
	p   int
	bit bool
}

// price sums the reference prices of a decision sequence.
func price(ds []decision) Cost {
	var c Cost
	for _, d := range ds {
		c += refUnits(d.p, d.bit)
	}
	return c
}

// TestLumaModeCostsMatchReferenceTree transcribes the key-frame luma
// tree of RFC 6386 section 11.2 independently -- the same shape
// internal/encoder writes and golang.org/x/image/vp8 reads -- and
// requires LumaMode to price exactly its reference for all four
// whole-block modes.
func TestLumaModeCostsMatchReferenceTree(t *testing.T) {
	ref := map[predict.Mode][]decision{
		predict.DC: {{145, true}, {156, false}, {163, false}},
		predict.V:  {{145, true}, {156, false}, {163, true}},
		predict.H:  {{145, true}, {156, true}, {128, false}},
		predict.TM: {{145, true}, {156, true}, {128, true}},
	}
	for m := predict.Mode(0); m < predict.NumModes; m++ {
		if got, want := LumaMode(m), price(ref[m]); got != want {
			t.Errorf("LumaMode(%d) = %d, want %d", m, got, want)
		}
	}
	// Literal pins: any repricing of the key-frame mode trees must be a
	// deliberate act that rewrites these numbers.
	golden := map[predict.Mode]Cost{
		predict.DC: 659, predict.V: 866, predict.H: 912, predict.TM: 912,
	}
	for m, want := range golden {
		if got := LumaMode(m); got != want {
			t.Errorf("LumaMode(%d) = %d, want pinned %d", m, got, want)
		}
	}
}

// TestChromaModeCostsMatchReferenceTree does the same for the chroma
// tree of RFC 6386 section 11.3.
func TestChromaModeCostsMatchReferenceTree(t *testing.T) {
	ref := map[predict.Mode][]decision{
		predict.DC: {{142, false}},
		predict.V:  {{142, true}, {114, false}},
		predict.H:  {{142, true}, {114, true}, {183, false}},
		predict.TM: {{142, true}, {114, true}, {183, true}},
	}
	for m := predict.Mode(0); m < predict.NumModes; m++ {
		if got, want := ChromaMode(m), price(ref[m]); got != want {
			t.Errorf("ChromaMode(%d) = %d, want %d", m, got, want)
		}
	}
	golden := map[predict.Mode]Cost{
		predict.DC: 218, predict.V: 598, predict.H: 641, predict.TM: 980,
	}
	for m, want := range golden {
		if got := ChromaMode(m); got != want {
			t.Errorf("ChromaMode(%d) = %d, want pinned %d", m, got, want)
		}
	}
}

// TestBPredFlagPinned prices the B_PRED branch decision alone.
func TestBPredFlagPinned(t *testing.T) {
	if got, want := BPredFlag(), Cost(210); got != want {
		t.Errorf("BPredFlag() = %d, want %d", got, want)
	}
	if got, want := BPredFlag(), BitCost(use16x16Prob, false); got != want {
		t.Errorf("BPredFlag() = %d, want BitCost(145, false) = %d", got, want)
	}
}

// TestUnknownModesPanics keeps out-of-range modes loud.
func TestUnknownModesPanics(t *testing.T) {
	mustPanic := func(name string, f func()) {
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic on an unknown mode", name)
			}
		}()
		f()
	}
	mustPanic("LumaMode", func() { LumaMode(predict.NumModes) })
	mustPanic("LumaMode", func() { LumaMode(predict.Mode(200)) })
	mustPanic("ChromaMode", func() { ChromaMode(predict.NumModes) })
	mustPanic("SubModeCost", func() { SubModeCost(0, 0, predict.NumSubModes) })
	mustPanic("SubModeCost", func() { SubModeCost(predict.NumSubModes, 0, 0) })
	mustPanic("SubModeCost", func() { SubModeCost(0, predict.SubMode(250), 0) })
}

// rfcBModeTree is the key-frame sub-mode decision tree of RFC 6386
// section 11.4, transcribed from the specification rather than from this
// repository's own tables. Negative entries are leaves naming the ten
// sub-block predictions in the RFC's own order; positive entries point
// at the next node pair. Node k's decision reads probability entry k/2.
var rfcBModeTree = [...]int16{
	-0, 2, // B_DC_PRED | rest
	-1, 4, // B_TM_PRED | rest
	-2, 6, // B_VE_PRED | rest
	8, 12, // split {HE, RD, VR} | {LD, VL, HD, HU}
	-3, 10, // B_HE_PRED | (RD, VR)
	-4, -5, // B_RD_PRED | B_VR_PRED
	-6, 14, // B_LD_PRED | (VL, HD, HU)
	-7, 16, // B_VL_PRED | (HD, HU)
	-8, -9, // B_HD_PRED | B_HU_PRED
}

// rfcLeafToNative maps the RFC's leaf numbering onto this repository's
// native SubMode values (the mapping predict's tables document).
var rfcLeafToNative = [...]predict.SubMode{
	predict.BDC, predict.BTM, predict.BVE, predict.BHE,
	predict.BRD, predict.BVR, predict.BLD, predict.BVL,
	predict.BHD, predict.BHU,
}

// referenceSubModePath walks the transcribed tree toward leaf m and
// returns the decisions it must take, pricing them by the given context
// row on the way. It returns nil when the tree holds no such leaf.
func referenceSubModePath(row *[9]uint8, m predict.SubMode) []decision {
	var walk func(node int) []decision
	walk = func(node int) []decision {
		for b := 0; b < 2; b++ {
			entry := rfcBModeTree[node+b]
			step := decision{p: int(row[node/2]), bit: b == 1}
			if entry <= 0 {
				if rfcLeafToNative[-entry] == m {
					return []decision{step}
				}
				continue
			}
			if rest := walk(int(entry)); rest != nil {
				return append([]decision{step}, rest...)
			}
		}
		return nil
	}
	return walk(0)
}

// TestSubModeCostExhaustiveAgainstReferenceTree covers every contextual
// probability row (ten above contexts times ten left contexts) times
// every sub-mode: one thousand combinations, each against the freshly
// walked reference tree.
func TestSubModeCostExhaustiveAgainstReferenceTree(t *testing.T) {
	for above := predict.SubMode(0); above < predict.NumSubModes; above++ {
		for left := predict.SubMode(0); left < predict.NumSubModes; left++ {
			row := &predict.KeyFrameSubModeProbs[above][left]
			for m := predict.SubMode(0); m < predict.NumSubModes; m++ {
				ref := referenceSubModePath(row, m)
				if ref == nil {
					t.Fatalf("reference tree lost native sub-mode %d", m)
				}
				got := SubModeCost(above, left, m)
				if want := price(ref); got != want {
					t.Fatalf("SubModeCost(%d,%d,%d) = %d, want %d", above, left, m, got, want)
				}
			}
		}
	}
}

// TestSubModeCostMatchesWrittenStreams proves the priced decisions are
// the written ones: for every context pair and sub-mode, the real
// writer emits a stream whose decoded path is exactly the priced one --
// same probabilities, same bits, same total.
func TestSubModeCostMatchesWrittenStreams(t *testing.T) {
	for above := predict.SubMode(0); above < predict.NumSubModes; above++ {
		for left := predict.SubMode(0); left < predict.NumSubModes; left++ {
			row := &predict.KeyFrameSubModeProbs[above][left]
			for m := predict.SubMode(0); m < predict.NumSubModes; m++ {
				enc := boolenc.New(16)
				predict.WriteSubMode(enc, above, left, m)
				out := enc.Finish()

				dec := boolenc.NewDecoder(out)
				node := 0
				var priced Cost
				for {
					entry := rfcBModeTree[node]
					b := dec.ReadBool(uint8(row[node/2]))
					priced += refUnits(int(row[node/2]), b)
					next := entry
					if b {
						next = rfcBModeTree[node+1]
					}
					if next <= 0 {
						if rfcLeafToNative[-next] != predict.SubMode(m) {
							t.Fatalf("WriteSubMode(%d,%d,%d) decodes to a different sub-mode",
								above, left, m)
						}
						break
					}
					node = int(next)
				}
				if got := SubModeCost(predict.SubMode(above), predict.SubMode(left), predict.SubMode(m)); got != priced {
					t.Fatalf("SubModeCost(%d,%d,%d) = %d, stream prices %d", above, left, m, got, priced)
				}
				if dec.UnexpectedEOF() {
					t.Fatalf("stream for (%d,%d,%d) ran out of bytes", above, left, m)
				}
			}
		}
	}
}

// TestSkipCostAllProbabilities prices both skip outcomes at every
// codable skip probability against the reference logarithms.
func TestSkipCostAllProbabilities(t *testing.T) {
	for p := 1; p <= 255; p++ {
		if got, want := SkipCost(uint8(p), true), refUnits(p, true); got != want {
			t.Fatalf("SkipCost(%d, true) = %d, want %d", p, got, want)
		}
		if got, want := SkipCost(uint8(p), false), refUnits(p, false); got != want {
			t.Fatalf("SkipCost(%d, false) = %d, want %d", p, got, want)
		}
	}
}

// TestSegmentIDCostAgainstReferenceTree prices all four ids under corner
// and random probability triples against an independently walked
// segment tree, then pins the corners as literals.
func TestSegmentIDCostAgainstReferenceTree(t *testing.T) {
	ref := func(t0, t1, t2 int, id uint8) Cost {
		ds := make([]decision, 0, 3)
		if id != 0 {
			ds = append(ds, decision{t0, true})
			if id != 1 {
				ds = append(ds, decision{t1, true})
				ds = append(ds, decision{t2, id != 2})
			} else {
				ds = append(ds, decision{t1, false})
			}
		} else {
			ds = append(ds, decision{t0, false})
		}
		return price(ds)
	}
	corners := [][3]int{{1, 1, 1}, {255, 255, 255}, {128, 128, 128}}
	for _, tr := range corners {
		for id := uint8(0); id <= 3; id++ {
			probs := [3]uint8{uint8(tr[0]), uint8(tr[1]), uint8(tr[2])}
			if got, want := SegmentIDCost(&probs, id), ref(tr[0], tr[1], tr[2], id); got != want {
				t.Fatalf("SegmentIDCost(%v, %d) = %d, want %d", probs, id, got, want)
			}
		}
	}
	golden := map[[4]int][]int64{
		{1, 1, 1}:       {2048, 2049, 2050, 3},
		{255, 255, 255}: {1, 2049, 4097, 6144},
		{128, 128, 128}: {256, 512, 768, 768},
	}
	for tr, wants := range golden {
		probs := [3]uint8{uint8(tr[0]), uint8(tr[1]), uint8(tr[2])}
		for id := 0; id <= 3; id++ {
			if got := SegmentIDCost(&probs, uint8(id)); got != Cost(wants[id]) {
				t.Errorf("SegmentIDCost(%v, %d) = %d, want pinned %d", probs, id, got, wants[id])
			}
		}
	}
	// Further deterministic triples.
	triples := [][3]uint8{{42, 99, 200}, {254, 1, 128}, {73, 226, 111}}
	for _, tr := range triples {
		for id := uint8(0); id <= 3; id++ {
			probs := tr
			if got, want := SegmentIDCost(&probs, id),
				price(referenceSegmentDecisions(tr, id)); got != want {
				t.Fatalf("SegmentIDCost(%v, %d) = %d, want %d", tr, id, got, want)
			}
		}
	}
	// Out-of-range ids are loud.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("SegmentIDCost did not panic on id 4")
			}
		}()
		probs := [3]uint8{128, 128, 128}
		SegmentIDCost(&probs, 4)
	}()
}

// referenceSegmentDecisions spells the segment-tree decisions out for
// one id under one probability triple.
func referenceSegmentDecisions(tr [3]uint8, id uint8) []decision {
	var ds []decision
	if id != 0 {
		ds = append(ds, decision{int(tr[0]), true})
		if id != 1 {
			ds = append(ds, decision{int(tr[1]), true})
			ds = append(ds, decision{int(tr[2]), id != 2})
		} else {
			ds = append(ds, decision{int(tr[1]), false})
		}
	} else {
		ds = append(ds, decision{int(tr[0]), false})
	}
	return ds
}
