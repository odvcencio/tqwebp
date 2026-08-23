package encoder

// This file proves priceSegmentControl against a visibly independent
// test-local section 9.3 bit counter: the reference derives every plain
// header bit straight from the Segmentation struct fields -- the enabled
// flag, the two top-level flags, the absolute-vs-delta flag, the eight
// feature gates with their magnitude and sign literals, and the three
// probability gates with their 8-bit literals -- without ever calling
// production code. The id side is priced independently through
// frame.EffectiveSegmentTreeProbs plus direct cost.SegmentIDCost sums.
// No map and no floating point appears anywhere except the float64
// return of testing.AllocsPerRun.

import (
	"strings"
	"testing"

	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/frame"
)

// controlPanicPrefix is the panic message prefix every rejection of
// priceSegmentControl must carry.
const controlPanicPrefix = "tqwebp/encoder:"

// refHeaderBits derives the expected plain section 9.3 header bits
// directly from the struct fields: one L(1) flag per decision, L(7)+L(1)
// per open quantizer gate, L(6)+L(1) per open loop-filter gate, and
// L(8) per shipped tree probability. It never calls production code.
func refHeaderBits(s frame.Segmentation) cost.Cost {
	// The segmentation-enabled flag is charged in every configuration.
	bits := cost.Literal(1)
	if !s.Enabled {
		return bits
	}
	// The two top-level flags follow the enabled bit unconditionally.
	bits += cost.Literal(1) // update_map
	bits += cost.Literal(1) // update_data
	if s.UpdateData {
		bits += cost.Literal(1) // absolute vs delta
		for _, f := range s.Quantizer {
			bits += cost.Literal(1) // gate
			if f.Enabled {
				bits += cost.Literal(7) + cost.Literal(1)
			}
		}
		for _, f := range s.LoopFilter {
			bits += cost.Literal(1) // gate
			if f.Enabled {
				bits += cost.Literal(6) + cost.Literal(1)
			}
		}
	}
	if s.UpdateMap {
		for _, p := range s.TreeProbs {
			bits += cost.Literal(1) // update gate
			if p.Update {
				bits += cost.Literal(8)
			}
		}
	}
	return bits
}

// refIDCost prices every id independently: the effective table comes
// from frame.EffectiveSegmentTreeProbs and each id is summed through a
// direct cost.SegmentIDCost call.
func refIDCost(s frame.Segmentation, ids []uint8) cost.Cost {
	probs := frame.EffectiveSegmentTreeProbs(s)
	var total cost.Cost
	for _, id := range ids {
		total += cost.SegmentIDCost(&probs, id)
	}
	return total
}

// requireControlCost prices s plus ids through production and through
// the independent references and compares HeaderCost, IDCost, and
// TotalCost exactly.
func requireControlCost(t *testing.T, label string, s frame.Segmentation, ids []uint8) {
	t.Helper()
	got := priceSegmentControl(s, ids)
	wantHeader := refHeaderBits(s)
	wantID := refIDCost(s, ids)
	if got.HeaderCost != wantHeader {
		t.Errorf("%s: HeaderCost = %d, want %d", label, got.HeaderCost, wantHeader)
	}
	if got.IDCost != wantID {
		t.Errorf("%s: IDCost = %d, want %d", label, got.IDCost, wantID)
	}
	if got.TotalCost != got.HeaderCost+got.IDCost {
		t.Errorf("%s: TotalCost = %d, want HeaderCost+IDCost = %d", label, got.TotalCost, got.HeaderCost+got.IDCost)
	}
}

// TestPriceSegmentControlDisabled pins the disabled baseline: the cost
// is exactly the one L(1) segmentation-enabled flag, ids are absent, and
// TotalCost equals HeaderCost. Dormant feature values far out of range
// stay legal because only enabled gates are validated.
func TestPriceSegmentControlDisabled(t *testing.T) {
	cases := []struct {
		name string
		seg  frame.Segmentation
	}{
		{"zero-value", frame.Segmentation{}},
		{"dormant-quantizer-out-of-range", func() frame.Segmentation {
			var s frame.Segmentation
			vals := [4]int{128, -128, 1 << 20, -(1 << 20)}
			for i := range s.Quantizer {
				s.Quantizer[i] = frame.SegmentFeature{Enabled: false, Value: vals[i]}
			}
			return s
		}()},
		{"dormant-loop-filter-out-of-range", func() frame.Segmentation {
			var s frame.Segmentation
			vals := [4]int{64, -64, 1 << 20, -(1 << 20)}
			for i := range s.LoopFilter {
				s.LoopFilter[i] = frame.SegmentFeature{Enabled: false, Value: vals[i]}
			}
			return s
		}()},
		{"dormant-both-families-out-of-range", func() frame.Segmentation {
			var s frame.Segmentation
			for i := range s.Quantizer {
				s.Quantizer[i] = frame.SegmentFeature{Enabled: false, Value: 1000 - i}
			}
			for i := range s.LoopFilter {
				s.LoopFilter[i] = frame.SegmentFeature{Enabled: false, Value: -1000 + i}
			}
			return s
		}()},
	}
	for _, c := range cases {
		got := priceSegmentControl(c.seg, nil)
		if got.HeaderCost != cost.Literal(1) {
			t.Errorf("%s: HeaderCost = %d, want cost.Literal(1) = %d", c.name, got.HeaderCost, cost.Literal(1))
		}
		if got.IDCost != 0 {
			t.Errorf("%s: IDCost = %d, want 0", c.name, got.IDCost)
		}
		if got.TotalCost != got.HeaderCost {
			t.Errorf("%s: TotalCost = %d, want HeaderCost = %d", c.name, got.TotalCost, got.HeaderCost)
		}
		requireControlCost(t, c.name, c.seg, nil)
	}
}

// TestPriceSegmentControlEnabledShapes exercises the enabled flag
// combinations -- neither update, UpdateData only in delta and absolute
// modes, UpdateMap only, and both -- with all four quantizer and all
// four loop-filter gates open, positive, negative, zero, and extreme
// values, and a full probability update mask.
func TestPriceSegmentControlEnabledShapes(t *testing.T) {
	quantVals := [4]int{1, -1, 127, -127}
	filterVals := [4]int{1, -1, 63, -63}
	fullSeg := func(abs bool) frame.Segmentation {
		var s frame.Segmentation
		s.Enabled = true
		s.UpdateData = true
		s.Absolute = abs
		for i := range s.Quantizer {
			s.Quantizer[i] = frame.SegmentFeature{Enabled: true, Value: quantVals[i]}
		}
		for i := range s.LoopFilter {
			s.LoopFilter[i] = frame.SegmentFeature{Enabled: true, Value: filterVals[i]}
		}
		s.UpdateMap = true
		for i := range s.TreeProbs {
			s.TreeProbs[i] = frame.SegmentProbability{Update: true, Value: uint8(64 + 32*i)}
		}
		return s
	}

	cases := []struct {
		name string
		seg  frame.Segmentation
	}{
		{"enabled-neither-update", func() frame.Segmentation {
			var s frame.Segmentation
			s.Enabled = true
			return s
		}()},
		{"enabled-update-data-delta", func() frame.Segmentation {
			s := fullSeg(false)
			s.UpdateMap = false
			s.TreeProbs = [3]frame.SegmentProbability{}
			return s
		}()},
		{"enabled-update-data-absolute", func() frame.Segmentation {
			s := fullSeg(true)
			s.UpdateMap = false
			s.TreeProbs = [3]frame.SegmentProbability{}
			return s
		}()},
		{"enabled-update-map-only", func() frame.Segmentation {
			s := fullSeg(false)
			s.UpdateData = false
			s.Quantizer = [4]frame.SegmentFeature{}
			s.LoopFilter = [4]frame.SegmentFeature{}
			return s
		}()},
		{"enabled-both-delta", fullSeg(false)},
		{"enabled-both-absolute", fullSeg(true)},
		{"enabled-zero-values", func() frame.Segmentation {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateData = true
			for i := range s.Quantizer {
				s.Quantizer[i] = frame.SegmentFeature{Enabled: true, Value: 0}
			}
			for i := range s.LoopFilter {
				s.LoopFilter[i] = frame.SegmentFeature{Enabled: true, Value: 0}
			}
			return s
		}()},
		{"enabled-extreme-boundary-values", func() frame.Segmentation {
			s := fullSeg(false)
			s.Quantizer = [4]frame.SegmentFeature{
				{Enabled: true, Value: 127},
				{Enabled: true, Value: -127},
				{Enabled: true, Value: 0},
				{Enabled: true, Value: -1},
			}
			s.LoopFilter = [4]frame.SegmentFeature{
				{Enabled: true, Value: 63},
				{Enabled: true, Value: -63},
				{Enabled: true, Value: 0},
				{Enabled: true, Value: -1},
			}
			return s
		}()},
	}
	for _, c := range cases {
		requireControlCost(t, c.name, c.seg, nil)
	}

	// Pin the exact plain-bit arithmetic of two shapes by hand, so the
	// reference counter itself is cross-checked against literals:
	// neither-update is exactly three flags, and the full delta shape
	// is three flags plus the absolute flag plus eight gates with their
	// magnitude and sign literals plus three probability gates with
	// three 8-bit literals.
	neither := frame.Segmentation{Enabled: true}
	if got, want := priceSegmentControl(neither, nil).HeaderCost, 3*cost.Literal(1); got != want {
		t.Errorf("neither-update HeaderCost = %d, want %d", got, want)
	}
	both := fullSeg(false)
	wantBoth := 3*cost.Literal(1) + cost.Literal(1) +
		4*(cost.Literal(1)+cost.Literal(7)+cost.Literal(1)) +
		4*(cost.Literal(1)+cost.Literal(6)+cost.Literal(1)) +
		3*(cost.Literal(1)+cost.Literal(8))
	if got := priceSegmentControl(both, nil).HeaderCost; got != wantBoth {
		t.Errorf("both-delta HeaderCost = %d, want %d", got, wantBoth)
	}
}

// TestPriceSegmentControlIDCosts prices representative id maps that
// contain all of 0..3 under several effective probability tables and
// compares Header, ID, and Total exactly against the independent
// references.
func TestPriceSegmentControlIDCosts(t *testing.T) {
	idMaps := [][]uint8{
		{0, 1, 2, 3},
		{1, 3, 2, 2, 0, 3, 1, 2, 3, 3, 0, 2},
		{0, 0, 1, 1, 2, 2, 3, 3},
		{3, 3, 3, 3},
		{2, 0, 2, 1, 3, 2},
	}
	probTables := [][3]frame.SegmentProbability{
		{
			{Update: true, Value: 128},
			{Update: true, Value: 64},
			{Update: true, Value: 200},
		},
		{
			{Update: true, Value: 255},
			{Update: false, Value: 0},
			{Update: true, Value: 1},
		},
		{
			{Update: false, Value: 0},
			{Update: false, Value: 0},
			{Update: false, Value: 0},
		},
	}
	for tIdx, probs := range probTables {
		for _, ids := range idMaps {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateMap = true
			s.TreeProbs = probs
			label := "probs=" + itoaControl(tIdx) + " ids-len=" + itoaControl(len(ids))
			got := priceSegmentControl(s, ids)

			// The independent references, compared field by field.
			wantHeader := refHeaderBits(s)
			wantID := refIDCost(s, ids)
			if got.HeaderCost != wantHeader {
				t.Errorf("%s: HeaderCost = %d, want %d", label, got.HeaderCost, wantHeader)
			}
			if got.IDCost != wantID {
				t.Errorf("%s: IDCost = %d, want %d", label, got.IDCost, wantID)
			}
			if got.TotalCost != got.HeaderCost+got.IDCost {
				t.Errorf("%s: TotalCost = %d, want HeaderCost+IDCost = %d", label, got.TotalCost, got.HeaderCost+got.IDCost)
			}
			if got.IDCost == 0 {
				t.Errorf("%s: IDCost = 0 for a nonempty id map", label)
			}

			// A third derivation: the effective table read back from
			// frame.EffectiveSegmentTreeProbs and summed directly.
			eff := frame.EffectiveSegmentTreeProbs(s)
			var direct cost.Cost
			for _, id := range ids {
				direct += cost.SegmentIDCost(&eff, id)
			}
			if got.IDCost != direct {
				t.Errorf("%s: IDCost = %d, want direct SegmentIDCost sum %d", label, got.IDCost, direct)
			}
		}
	}
}

// itoaControl renders a small non-negative integer without strconv so
// the reference paths stay self-contained.
func itoaControl(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [8]byte
	n := len(buf)
	for v > 0 {
		n--
		buf[n] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[n:])
}

// TestPriceSegmentControlExhaustiveMasks sweeps every mask of the three
// probability update gates and every single-family feature gate mask --
// quantizer and loop filter separately, in both absolute modes -- and
// compares production HeaderCost with the independent counter exactly.
func TestPriceSegmentControlExhaustiveMasks(t *testing.T) {
	quantVals := [4]int{5, -5, 127, -127}
	filterVals := [4]int{3, -3, 63, -63}

	// All 8 masks of the three probability update gates.
	for mask := 0; mask < 8; mask++ {
		var s frame.Segmentation
		s.Enabled = true
		s.UpdateMap = true
		for i := range s.TreeProbs {
			s.TreeProbs[i] = frame.SegmentProbability{
				Update: mask&(1<<i) != 0,
				Value:  uint8(32 + 16*i),
			}
		}
		requireControlCost(t, "prob-mask-"+itoaControl(mask), s, nil)
	}

	// All 16 single-family quantizer masks, both absolute modes.
	for mask := 0; mask < 16; mask++ {
		for _, abs := range [2]bool{false, true} {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateData = true
			s.Absolute = abs
			for i := range s.Quantizer {
				s.Quantizer[i] = frame.SegmentFeature{
					Enabled: mask&(1<<i) != 0,
					Value:   quantVals[i],
				}
			}
			label := "quant-mask-" + itoaControl(mask)
			if abs {
				label += "-absolute"
			}
			requireControlCost(t, label, s, nil)
		}
	}

	// All 16 single-family loop-filter masks, both absolute modes.
	for mask := 0; mask < 16; mask++ {
		for _, abs := range [2]bool{false, true} {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateData = true
			s.Absolute = abs
			for i := range s.LoopFilter {
				s.LoopFilter[i] = frame.SegmentFeature{
					Enabled: mask&(1<<i) != 0,
					Value:   filterVals[i],
				}
			}
			label := "filter-mask-" + itoaControl(mask)
			if abs {
				label += "-absolute"
			}
			requireControlCost(t, label, s, nil)
		}
	}

	// The full cross product of one quantizer mask with one loop-filter
	// mask and one probability mask, proving the families compose.
	for qMask := 0; qMask < 16; qMask += 5 {
		for fMask := 0; fMask < 16; fMask += 10 {
			for pMask := 0; pMask < 8; pMask += 3 {
				var s frame.Segmentation
				s.Enabled = true
				s.UpdateData = true
				s.UpdateMap = true
				for i := range s.Quantizer {
					s.Quantizer[i] = frame.SegmentFeature{
						Enabled: qMask&(1<<i) != 0,
						Value:   quantVals[i],
					}
				}
				for i := range s.LoopFilter {
					s.LoopFilter[i] = frame.SegmentFeature{
						Enabled: fMask&(1<<i) != 0,
						Value:   filterVals[i],
					}
				}
				for i := range s.TreeProbs {
					s.TreeProbs[i] = frame.SegmentProbability{
						Update: pMask&(1<<i) != 0,
						Value:  uint8(200 - 16*i),
					}
				}
				label := "cross-q" + itoaControl(qMask) +
					"-f" + itoaControl(fMask) +
					"-p" + itoaControl(pMask)
				requireControlCost(t, label, s, nil)
			}
		}
	}
}

// TestPriceSegmentControlDeterministicAndAllocationFree proves 100
// repeated pricings of a valid nontrivial plan are identical and that
// pricing allocates nothing at all.
func TestPriceSegmentControlDeterministicAndAllocationFree(t *testing.T) {
	var s frame.Segmentation
	s.Enabled = true
	s.UpdateMap = true
	s.UpdateData = true
	s.Quantizer = [4]frame.SegmentFeature{
		{Enabled: true, Value: 12},
		{Enabled: false, Value: -40},
		{Enabled: true, Value: -127},
		{Enabled: true, Value: 0},
	}
	s.LoopFilter = [4]frame.SegmentFeature{
		{Enabled: true, Value: -20},
		{Enabled: true, Value: 63},
		{Enabled: false, Value: 7},
		{Enabled: true, Value: -1},
	}
	s.TreeProbs = [3]frame.SegmentProbability{
		{Update: true, Value: 128},
		{Update: false, Value: 9},
		{Update: true, Value: 32},
	}
	ids := []uint8{1, 3, 2, 2, 0, 3, 1, 2, 3, 3, 0, 2}

	first := priceSegmentControl(s, ids)
	for i := 1; i < 100; i++ {
		again := priceSegmentControl(s, ids)
		if again != first {
			t.Fatalf("call %d: %+v, want identical %+v", i, again, first)
		}
	}
	requireControlCost(t, "nontrivial-plan", s, ids)
	const runs = 100
	allocs := testing.AllocsPerRun(runs, func() { priceSegmentControl(s, ids) })
	if allocs != 0 {
		t.Fatalf("priceSegmentControl allocated %v objects per run, want 0", allocs)
	}
}

// TestPriceSegmentControlPanics requires every illegal input to panic
// with a message prefixed tqwebp/encoder.
func TestPriceSegmentControlPanics(t *testing.T) {
	cases := []struct {
		name string
		seg  frame.Segmentation
		ids  []uint8
	}{
		{"ids-when-disabled", frame.Segmentation{}, []uint8{0, 1}},
		{"ids-without-update-map", func() frame.Segmentation {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateData = true
			return s
		}(), []uint8{0}},
		{"invalid-id-at-start", func() frame.Segmentation {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateMap = true
			return s
		}(), []uint8{4, 0, 1}},
		{"invalid-id-in-middle", func() frame.Segmentation {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateMap = true
			return s
		}(), []uint8{1, 4, 2}},
		{"invalid-id-at-end", func() frame.Segmentation {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateMap = true
			return s
		}(), []uint8{0, 1, 4}},
		{"enabled-quantizer-plus-128", func() frame.Segmentation {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateData = true
			s.Quantizer[0] = frame.SegmentFeature{Enabled: true, Value: 128}
			return s
		}(), nil},
		{"enabled-quantizer-minus-128", func() frame.Segmentation {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateData = true
			s.Quantizer[3] = frame.SegmentFeature{Enabled: true, Value: -128}
			return s
		}(), nil},
		{"enabled-loop-filter-plus-64", func() frame.Segmentation {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateData = true
			s.LoopFilter[1] = frame.SegmentFeature{Enabled: true, Value: 64}
			return s
		}(), nil},
		{"enabled-loop-filter-minus-64", func() frame.Segmentation {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateData = true
			s.LoopFilter[2] = frame.SegmentFeature{Enabled: true, Value: -64}
			return s
		}(), nil},
		{"updated-probability-zero-with-ids", func() frame.Segmentation {
			var s frame.Segmentation
			s.Enabled = true
			s.UpdateMap = true
			s.TreeProbs[0] = frame.SegmentProbability{Update: true, Value: 0}
			return s
		}(), []uint8{1, 2, 3}},
	}
	for _, c := range cases {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Errorf("%s: priceSegmentControl did not panic", c.name)
					return
				}
				msg, ok := r.(string)
				if !ok {
					t.Errorf("%s: panic value %v is not a string", c.name, r)
					return
				}
				if !strings.HasPrefix(msg, controlPanicPrefix) {
					t.Errorf("%s: panic message %q lacks prefix %q", c.name, msg, controlPanicPrefix)
				}
			}()
			priceSegmentControl(c.seg, c.ids)
		}()
	}
}

// TestPriceSegmentControlDormantGatesOutOfRange proves that enabled
// segmentation with dormant (gate-closed) feature values far outside
// their fields neither panics nor changes any cost.
func TestPriceSegmentControlDormantGatesOutOfRange(t *testing.T) {
	for _, abs := range [2]bool{false, true} {
		var s frame.Segmentation
		s.Enabled = true
		s.UpdateData = true
		s.Absolute = abs
		quantVals := [4]int{128, -128, 1 << 24, -(1 << 24)}
		filterVals := [4]int{64, -64, 1 << 24, -(1 << 24)}
		for i := range s.Quantizer {
			s.Quantizer[i] = frame.SegmentFeature{Enabled: false, Value: quantVals[i]}
		}
		for i := range s.LoopFilter {
			s.LoopFilter[i] = frame.SegmentFeature{Enabled: false, Value: filterVals[i]}
		}
		s.UpdateMap = true
		for i := range s.TreeProbs {
			s.TreeProbs[i] = frame.SegmentProbability{Update: true, Value: uint8(48 + 8*i)}
		}
		label := "dormant-out-of-range"
		if abs {
			label += "-absolute"
		}
		requireControlCost(t, label, s, nil)
	}
}
