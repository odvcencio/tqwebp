package encoder

// This file proves deriveSegmentMapPlan against a visibly independent
// reference: the test tallies the three tree branches from per-id
// occurrence counts instead of the production switch, brute-forces every
// codable probability itself, applies its own copy of the strict
// keep-versus-update ledger, and sums the id records on its own. No map
// and no floating point appears anywhere in the derivation paths under
// test.

import (
	"strings"
	"testing"

	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/frame"
)

// keyFrameDefaultProb restates the key-frame default table the plan
// keeps where no update ships.
const keyFrameDefaultProb uint8 = 255

// segTreeGateBits is the number of L(1) update-gate decisions the
// segment-map header always writes, one per tree probability.
const segTreeGateBits = 3

// segTreeLiteralBits is the width of one updated probability literal.
const segTreeLiteralBits = 8

// refTally derives the three tree-node branch tallies from plain
// per-id occurrence counts, without mirroring the production switch:
// node 0 separates id 0 from ids 1..3, node 1 id 1 from ids 2..3, and
// node 2 id 2 from id 3.
func refTally(ids []uint8) [3][2]int {
	var perID [4]int
	for _, id := range ids {
		perID[id]++
	}
	return [3][2]int{
		{perID[0], perID[1] + perID[2] + perID[3]},
		{perID[1], perID[2] + perID[3]},
		{perID[2], perID[3]},
	}
}

// refOptimal scans every codable Q8 probability 1..255 with
// cost.BranchCost and keeps the first minimum, exactly as the normative
// choice demands.
func refOptimal(nFalse, nTrue int) uint8 {
	best := uint8(0)
	var bestCost cost.Cost
	for p := 1; p <= 255; p++ {
		c := cost.BranchCost(uint8(p), nFalse, nTrue)
		if best == 0 || c < bestCost {
			best, bestCost = uint8(p), c
		}
	}
	return best
}

// refPlan recomputes the whole plan from scratch: brute-forced optimal
// probabilities, its own strict keep-vs-update ledger (a tie keeps the
// default 255), an independent sum of cost.SegmentIDCost over the ids,
// and independently counted signalling bits.
func refPlan(ids []uint8) segmentMapPlan {
	counts := refTally(ids)

	var ref segmentMapPlan
	for i := range ref.Effective {
		ref.Effective[i] = keyFrameDefaultProb
	}

	signal := cost.Cost(0)
	for i := 0; i < 3; i++ {
		nFalse, nTrue := counts[i][0], counts[i][1]
		candidate := refOptimal(nFalse, nTrue)

		keep := cost.BranchCost(keyFrameDefaultProb, nFalse, nTrue) +
			cost.Literal(1) // the update-gate decision
		update := cost.BranchCost(candidate, nFalse, nTrue) +
			cost.Literal(1) + // the same gate decision
			cost.Literal(segTreeLiteralBits)

		signal += cost.Literal(1)
		if update < keep {
			ref.Probs[i] = frame.SegmentProbability{Update: true, Value: candidate}
			ref.Effective[i] = candidate
			signal += cost.Literal(segTreeLiteralBits)
		}
	}
	ref.SignalCost = signal

	for _, id := range ids {
		ref.IDCost += cost.SegmentIDCost(&ref.Effective, id)
	}
	ref.TotalCost = ref.SignalCost + ref.IDCost
	return ref
}

// requirePlansEqual compares two plans field by field.
func requirePlansEqual(t *testing.T, label string, got, want segmentMapPlan) {
	t.Helper()
	if got.Probs != want.Probs {
		t.Errorf("%s: Probs = %v, want %v", label, got.Probs, want.Probs)
	}
	if got.Effective != want.Effective {
		t.Errorf("%s: Effective = %v, want %v", label, got.Effective, want.Effective)
	}
	if got.SignalCost != want.SignalCost {
		t.Errorf("%s: SignalCost = %d, want %d", label, got.SignalCost, want.SignalCost)
	}
	if got.IDCost != want.IDCost {
		t.Errorf("%s: IDCost = %d, want %d", label, got.IDCost, want.IDCost)
	}
	if got.TotalCost != want.TotalCost {
		t.Errorf("%s: TotalCost = %d, want %d", label, got.TotalCost, want.TotalCost)
	}
}

// TestDeriveSegmentMapPlanEmpty pins the empty-slice contract: the
// untouched default table, no shipped updates, three literal gate bits,
// zero id cost, and TotalCost equal to SignalCost.
func TestDeriveSegmentMapPlanEmpty(t *testing.T) {
	for _, ids := range [][]uint8{nil, {}} {
		got := deriveSegmentMapPlan(ids)

		var wantProbs [3]frame.SegmentProbability
		var wantEffective [3]uint8
		for i := range wantEffective {
			wantEffective[i] = keyFrameDefaultProb
		}
		wantSignal := cost.Cost(segTreeGateBits) * cost.Literal(1)

		if got.Probs != wantProbs {
			t.Errorf("ids %v: Probs = %v, want no updates %v", ids, got.Probs, wantProbs)
		}
		if got.Effective != wantEffective {
			t.Errorf("ids %v: Effective = %v, want %v", ids, got.Effective, wantEffective)
		}
		if got.SignalCost != wantSignal {
			t.Errorf("ids %v: SignalCost = %d, want %d", ids, got.SignalCost, wantSignal)
		}
		if got.IDCost != 0 {
			t.Errorf("ids %v: IDCost = %d, want 0", ids, got.IDCost)
		}
		if got.TotalCost != got.SignalCost {
			t.Errorf("ids %v: TotalCost = %d, want SignalCost %d", ids, got.TotalCost, got.SignalCost)
		}
		requirePlansEqual(t, "empty-vs-reference", got, refPlan(ids))
	}
}

// TestDeriveSegmentMapPlanCases runs representative shapes -- one of
// each id, a balanced repetition, single-segment extremes, and a
// deliberately skewed mix -- against the fully independent reference.
func TestDeriveSegmentMapPlanCases(t *testing.T) {
	cases := []struct {
		name string
		ids  []uint8
	}{
		{"one-of-each", []uint8{0, 1, 2, 3}},
		{"balanced-repeated", []uint8{
			0, 1, 2, 3, 0, 1, 2, 3, 0, 1, 2, 3, 0, 1, 2, 3,
		}},
		{"all-id-0", []uint8{0, 0, 0, 0, 0, 0, 0}},
		{"all-id-3", []uint8{3, 3, 3, 3, 3, 3, 3}},
		{"skewed-mix", []uint8{
			2, 2, 3, 2, 2, 2, 1, 2, 2, 0, 2, 3, 2, 2, 2, 2, 2, 2, 1, 2,
		}},
	}
	for _, c := range cases {
		got := deriveSegmentMapPlan(c.ids)
		want := refPlan(c.ids)
		requirePlansEqual(t, c.name, got, want)

		// Cross-check the totals against arithmetic written a third
		// way: gates plus literals for signalling, direct id walk for
		// the records.
		var updates int
		for _, p := range got.Probs {
			if p.Update {
				updates++
			}
		}
		wantSignal := cost.Cost(segTreeGateBits)*cost.Literal(1) +
			cost.Cost(updates)*cost.Literal(segTreeLiteralBits)
		if got.SignalCost != wantSignal {
			t.Errorf("%s: SignalCost = %d, want %d from %d updates", c.name, got.SignalCost, wantSignal, updates)
		}
		wantID := cost.Cost(0)
		for _, id := range c.ids {
			wantID += cost.SegmentIDCost(&got.Effective, id)
		}
		if got.IDCost != wantID {
			t.Errorf("%s: IDCost = %d, want %d", c.name, got.IDCost, wantID)
		}
		if got.TotalCost != got.SignalCost+got.IDCost {
			t.Errorf("%s: TotalCost = %d, want SignalCost+IDCost = %d", c.name, got.TotalCost, got.SignalCost+got.IDCost)
		}
	}
}

// TestDeriveSegmentMapPlanExhaustiveHistograms sweeps every histogram
// (n0, n1, n2, n3) in 0..5, materializes the ids in canonical order,
// and requires the production result to match the independent reference
// exactly -- pinning strict-tie behaviour and every combination of
// reached and unreached tree nodes.
func TestDeriveSegmentMapPlanExhaustiveHistograms(t *testing.T) {
	ids := make([]uint8, 0, 20)
	for n0 := 0; n0 <= 5; n0++ {
		for n1 := 0; n1 <= 5; n1++ {
			for n2 := 0; n2 <= 5; n2++ {
				for n3 := 0; n3 <= 5; n3++ {
					ids = ids[:0]
					for i := 0; i < n0; i++ {
						ids = append(ids, 0)
					}
					for i := 0; i < n1; i++ {
						ids = append(ids, 1)
					}
					for i := 0; i < n2; i++ {
						ids = append(ids, 2)
					}
					for i := 0; i < n3; i++ {
						ids = append(ids, 3)
					}
					label := "n0=" + itoa(n0) +
						",n1=" + itoa(n1) +
						",n2=" + itoa(n2) +
						",n3=" + itoa(n3)
					requirePlansEqual(t, label, deriveSegmentMapPlan(ids), refPlan(ids))
				}
			}
		}
	}
}

// itoa renders a small non-negative integer without strconv so the
// reference path stays self-contained.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [4]byte
	n := len(buf)
	for v > 0 {
		n--
		buf[n] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[n:])
}

// TestDeriveSegmentMapPlanDeterministicAndAllocationFree proves 100
// repeated derivations of a mixed input are identical and that the
// derivation allocates nothing at all.
func TestDeriveSegmentMapPlanDeterministicAndAllocationFree(t *testing.T) {
	mixed := []uint8{1, 3, 2, 2, 0, 3, 1, 2, 3, 3, 0, 2}
	first := deriveSegmentMapPlan(mixed)
	for i := 1; i < 100; i++ {
		again := deriveSegmentMapPlan(mixed)
		requirePlansEqual(t, "repetition", again, first)
	}
	const runs = 100
	if allocs := testing.AllocsPerRun(runs, func() { deriveSegmentMapPlan(mixed) }); allocs != 0 {
		t.Fatalf("deriveSegmentMapPlan allocated %v objects per run, want 0", allocs)
	}
}

// TestDeriveSegmentMapPlanPanicsOnInvalidID requires the panic message
// prefix tqwebp/encoder: for an out-of-range id at the start, middle,
// and end of the slice, proving validation covers the whole slice.
func TestDeriveSegmentMapPlanPanicsOnInvalidID(t *testing.T) {
	cases := []struct {
		name string
		ids  []uint8
	}{
		{"invalid-at-start", []uint8{4, 0, 1, 2}},
		{"invalid-in-middle", []uint8{1, 2, 4, 3}},
		{"invalid-at-end", []uint8{0, 1, 2, 4}},
	}
	for _, c := range cases {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Errorf("%s: deriveSegmentMapPlan(%v) did not panic", c.name, c.ids)
					return
				}
				msg, ok := r.(string)
				if !ok {
					t.Errorf("%s: panic value %v is not a string", c.name, r)
					return
				}
				if !strings.HasPrefix(msg, "tqwebp/encoder:") {
					t.Errorf("%s: panic message %q lacks prefix tqwebp/encoder:", c.name, msg)
				}
			}()
			deriveSegmentMapPlan(c.ids)
		}()
	}
}
