package encoder

import (
	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/frame"
)

// This file holds the exact cost primitive for the segment map of RFC
// 6386 section 9.3: given one pass of measured segment ids, derive the
// cheapest tree-probability table to signal and price both that signal
// and the id records themselves. It is byte-inert -- nothing here writes
// or mutates encoder state -- and exists so a later segmentation planner
// can compare whole plans without rederiving any of this arithmetic.
//
// # Boundary
//
// The plan deliberately covers only the segment-map tree probabilities
// and the per-macroblock id records. It excludes everything the frame
// header charges around them: the segmentation_enabled flag, the
// update_map flag, the update_data block and its feature-data header
// bits, and the feature deltas themselves. Those outer bits depend on
// decisions a full planner makes once per frame, not per plan, so they
// are charged exactly once by the later full planner rather than being
// folded into every candidate here.
//
// # Units and determinism
//
// All costs come from the internal/cost tables in 1/256-bit units.
// Everything is integer arithmetic over fixed-size state: two bounded
// passes over the id slice (one validation pass, one tally), then 255
// fixed candidate evaluations per node, with no maps and no allocations, so
// results are identical on every platform at every GOMAXPROCS.
const (
	// keyFrameSegmentTreeProb is the key-frame default of all three
	// segment-map tree probabilities, RFC 6386 section 11.5. A frame
	// that ships no update leaves the decoder at these values.
	keyFrameSegmentTreeProb uint8 = 255

	// segmentProbLiteralBits is the width of the L(8) literal that
	// carries one updated tree probability.
	segmentProbLiteralBits = 8
)

// segmentMapPlan is one derived candidate for the segment-map header:
// which of the three tree probabilities to update, the effective table
// the decoder ends up with, and the exact rate of signalling that table
// plus coding every id under it.
type segmentMapPlan struct {
	// Probs holds one entry per tree node. Update=true means the
	// frame ships a new probability in Value; Update=false means the
	// decoder keeps its previous value.
	Probs [3]frame.SegmentProbability

	// Effective is the probability table actually used to code the
	// ids: Value where Probs shipped an update, otherwise the
	// key-frame default 255.
	Effective [3]uint8

	// SignalCost prices the three L(1) update-gate decisions plus
	// one L(8) literal per shipped update.
	SignalCost cost.Cost

	// IDCost prices every id under Effective through the exact
	// segment-map tree.
	IDCost cost.Cost

	// TotalCost is SignalCost + IDCost.
	TotalCost cost.Cost
}

// deriveSegmentMapPlan derives the exact segment-map plan for ids: it
// tallies the three tree-node branches, picks the optimal Q8 probability
// per node, and ships an update only when the updated total -- branch
// cost under the new probability, plus the update gate and its 8-bit
// literal -- is strictly cheaper than coding against the kept value 255.
// A tie keeps 255.
//
// Every id must be 0..3; the function validates the whole slice before
// deriving anything and panics otherwise. An empty slice is legal and
// yields the untouched default table, no updates, three literal gate
// bits, and zero id cost.
func deriveSegmentMapPlan(ids []uint8) segmentMapPlan {
	// Validate every id before touching any state, so a bad slice
	// cannot leave a partially derived plan behind.
	for _, id := range ids {
		if id > 3 {
			panic("tqwebp/encoder: segment id out of range")
		}
	}

	// Tally the exact branches of the RFC 6386 section 9.3 tree
	// {-0, 2, -1, 4, -2, -3}. Node 0 splits id 0 from the rest;
	// node 1 is reached only by nonzero ids and splits id 1 from
	// ids 2/3; node 2 is reached only by ids 2/3 and splits id 2
	// from id 3. counts[node] holds {falseCount, trueCount}.
	var counts [3][2]int
	for _, id := range ids {
		switch id {
		case 0:
			counts[0][0]++
		case 1:
			counts[0][1]++
			counts[1][0]++
		case 2:
			counts[0][1]++
			counts[1][1]++
			counts[2][0]++
		default: // 3
			counts[0][1]++
			counts[1][1]++
			counts[2][1]++
		}
	}

	var plan segmentMapPlan
	for i := range plan.Effective {
		plan.Effective[i] = keyFrameSegmentTreeProb
	}

	gateBits := 3 * cost.Literal(1)
	signal := gateBits
	for i := 0; i < 3; i++ {
		nFalse, nTrue := counts[i][0], counts[i][1]
		candidate := cost.OptimalProb(nFalse, nTrue)
		keep := cost.BranchCost(keyFrameSegmentTreeProb, nFalse, nTrue) +
			cost.Literal(1)
		update := cost.BranchCost(candidate, nFalse, nTrue) +
			cost.Literal(1) + cost.Literal(segmentProbLiteralBits)
		// Strictly cheaper only: a tie keeps the key-frame
		// default. When candidate equals 255 the update side
		// carries the same branch cost plus an extra literal,
		// so a no-op update can never win and no separate check
		// is needed.
		if update < keep {
			plan.Probs[i] = frame.SegmentProbability{Update: true, Value: candidate}
			plan.Effective[i] = candidate
			signal += cost.Literal(segmentProbLiteralBits)
		}
	}
	plan.SignalCost = signal

	for _, id := range ids {
		plan.IDCost += cost.SegmentIDCost(&plan.Effective, id)
	}
	plan.TotalCost = plan.SignalCost + plan.IDCost
	return plan
}
