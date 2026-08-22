package encoder

// This file is work package WP-2 slice 5C: a bounded backward
// coefficient trellis that runs alongside the slice 5A/5B candidate
// search inside the Method>=6 B_PRED refinement. Methods below the
// effort boundary never reach this code, so their bytes cannot move;
// production integration is confined to refineBlockLevels in
// coeff_search.go, which keeps every decoder-visible decision -- the
// written tokens, the nz flags, the neighbour contexts, the
// reconstruction -- derived from exactly one winning level vector per
// block, priced and reconstructed through the same shared path as
// before.
//
// # The state graph
//
// The trellis prices the scan-order level suffix that starts at
// position i under the token context c the writer would carry into
// that position. Contexts are the three VP8 nonzero-count contexts
// 0,1,2; a zero level continues with context 0, a magnitude-one level
// continues with context 1, a larger magnitude with context 2, exactly
// as cost.BlockCost evolves them. Each position i offers a bounded,
// deterministically ordered choice set derived from the retained level
// r_i: the retained value itself, one step toward zero, and zero --
// at most three values, deduplicated keeping the earliest ordinal.
// Because a path picks one value per position independently, the graph
// contains joint decisions the slice 5A enumeration cannot offer, such
// as stepping one position toward zero while zeroing another; the
// existing candidates mutate one position at a time or clear a suffix.
//
// # Exact additive rate
//
// Every edge carries the exact 1/256-bit price of the boolean
// decisions the token writer makes at that position: the leading
// end-of-block-present flag once per non-empty block, zero-run
// continuation flags, the nonzero flag, the magnitude subtree, the
// even-odds sign bit, and the "more coefficients" flag that follows a
// nonzero and either ends the block or hands over to the next
// position. Summing the edge prices along any path reproduces
// cost.BlockCost of the path's level vector exactly; the focused tests
// prove this against cost.BlockCost itself over exhaustive magnitudes
// and randomized vectors. A backward dynamic program over the 3
// contexts per position therefore ranks complete paths by an objective
// that is exact in rate.
//
// # Distortion model and final scoring
//
// Squared sample error is not separable across positions -- the inverse
// transform couples them -- so the dynamic program folds in a bounded
// first-order distortion model: for each position and each offered
// value, the spatial SSE of the block with only that position moved
// away from its retained level, minus the retained block's SSE, times
// 256 and weighted by lambda exactly like the rate term. The DP's best
// path under this model is a proposal, never a verdict: the final
// winner is selected by re-scoring a fixed, canonically ordered list
// -- the retained levels, the caller's baselines (the slice 5B winner
// in production), then the trellis path -- with the caller-supplied
// decoder-equivalent spatial distortion times 256 plus lambda times
// exact cost.BlockCost under the entering context. Replacement demands
// a strictly smaller score, so the trellis never returns an objective
// worse than the earlier entries and every strict tie retains the
// earlier baseline.
//
// # Hard work bounds
//
// Per block the trellis performs at most trellisMaxProbes distortion
// probes, at most trellisMaxRelaxations dynamic-programming edge
// relaxations, and len(baselines)+1 exact scorings, all over fixed
// stack arrays with no allocation and no floating point. The bounds
// are properties of the graph shape, not of its input, and the panic
// guards below document that widening them is a deliberate act.
//
// # Determinism
//
// Construction walks fixed index orders over fixed-size arrays;
// backpointer ties keep the earliest scanned choice; scoring compares
// strictly smaller integers only. There is no map, no goroutine, and
// no shared mutable state, so results are identical on every platform
// at every GOMAXPROCS.

import (
	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/token"
)

const (
	// trellisMaxChoices caps the choice set offered at one scan
	// position: retained + one-step-toward-zero + zero, deduplicated.
	trellisMaxChoices = 3
	// trellisNumContexts is the number of VP8 token neighbour
	// contexts the graph spans.
	trellisNumContexts = 3
	// trellisMaxProbes bounds the marginal-distortion evaluations of
	// one block: every position offers at most two values other than
	// its retained level, over sixteen positions: 2*16 = 32.
	trellisMaxProbes = 32
	// trellisMaxRelaxations bounds the dynamic-programming edge
	// relaxations of one block: at most trellisMaxChoices choices for
	// each of trellisNumContexts contexts at each of sixteen scan
	// positions: 3*3*16 = 144.
	trellisMaxRelaxations = 144
	// trellisInfinity is the dynamic-programming sentinel. Real
	// per-suffix objectives are bounded far below it: rate well under
	// 10^5 units times lambda under 10^8 plus distortion deltas under
	// 10^9 leaves more than ten orders of magnitude of headroom, and
	// the relaxation loop never adds onto the sentinel.
	trellisInfinity = int64(1) << 58
)

// coeffTrellisStats records the bounded, deterministic counters of one
// trellis pass. They are plain integers updated in construction order,
// identical at every GOMAXPROCS.
type coeffTrellisStats struct {
	// Probes counts marginal-distortion evaluations: one per offered
	// value different from the retained level, window-wide. Never
	// above trellisMaxProbes.
	Probes int
	// EdgesRelaxed counts dynamic-programming edge relaxations. Never
	// above trellisMaxRelaxations.
	EdgesRelaxed int
	// ExactScorings counts full-objective scorings performed during
	// final selection: one per baseline plus one for the trellis
	// path.
	ExactScorings int
	// TrellisWon reports whether the trellis path took the win from
	// every baseline.
	TrellisWon bool
	// Improved reports whether the returned levels differ from the
	// retained input levels.
	Improved bool
	// BestScore is the exact winning objective,
	// distortion(winner)*256 + lambda*rate(winner).
	BestScore int64
}

// trellisChoiceSet is the bounded, canonically ordered set of values
// one scan position may take. Order fixes tie-breaking: earlier
// entries win ties at every stage.
type trellisChoiceSet struct {
	v [trellisMaxChoices]int16
	n int
}

// defaultTrellisChoices derives one position's choice set from its
// retained level r in canonical order: the retained value, one step
// toward zero, then zero, deduplicated keeping the earliest ordinal.
func defaultTrellisChoices(r int16) trellisChoiceSet {
	var cs trellisChoiceSet
	add := func(x int16) {
		for i := 0; i < cs.n; i++ {
			if cs.v[i] == x {
				return
			}
		}
		cs.v[cs.n] = x
		cs.n++
	}
	add(r)
	if r > 0 {
		add(r - 1)
	} else if r < 0 {
		add(r + 1)
	}
	add(0)
	return cs
}

// trellisConfig shapes one trellis graph. Production refines the whole
// scan with the default choice sets: trellisWholeScan. The window and
// choice hook exist so tests can shrink the graph and check the
// engine against an exhaustive oracle; production never narrows them.
type trellisConfig struct {
	// firstPos and lastPos bound the inclusive scan-position window
	// the trellis may rewrite. Positions outside the window stay at
	// their retained values. Positions below firstPos must be zero in
	// the retained levels, matching the writer's view of a block that
	// starts coding at firstPos.
	firstPos, lastPos int
	// choices, when non-nil, replaces the default choice-set rule at
	// every window position. It must return at least one value and at
	// most trellisMaxChoices, and it must be pure and deterministic.
	choices func(retained int16) trellisChoiceSet
}

// trellisWholeScan is the production configuration: the full scan
// window with the default bounded choice sets.
var trellisWholeScan = trellisConfig{firstPos: 0, lastPos: 15}

// trellisNextCtx returns the token context a coded magnitude hands to
// the next scan position, mirroring the writer's update.
func trellisNextCtx(mag int) int {
	switch {
	case mag == 0:
		return 0
	case mag == 1:
		return 1
	default:
		return 2
	}
}

// trellisMagSubtree prices the magnitude subtree of one coefficient
// whose value is two or larger under the branch probabilities p, the
// same walk cost.BlockCost performs through categories one to six.
func trellisMagSubtree(p *[token.NumProbs]uint8, mag int) cost.Cost {
	var c cost.Cost
	if mag <= 4 {
		c += cost.BitCostZero(p[3])
		if mag == 2 {
			return c + cost.BitCostZero(p[4])
		}
		return c + cost.BitCostOne(p[4]) + cost.BitCost(p[5], mag == 4)
	}
	if mag <= 10 {
		c += cost.BitCostOne(p[3]) + cost.BitCostZero(p[6])
		if mag <= 6 {
			return c + cost.BitCostZero(p[7]) + cost.BitCost(token.Cat1Prob, mag == 6)
		}
		m := mag - 7
		return c + cost.BitCostOne(p[7]) +
			cost.BitCost(token.Cat2Prob0, m>>1 == 1) +
			cost.BitCost(token.Cat2Prob1, m&1 == 1)
	}
	cat := 0
	for cat < 3 {
		base := 3 + (8 << uint(cat))
		if mag < base+(1<<uint(trellisExtraBits(cat))) {
			break
		}
		cat++
	}
	b1 := cat >> 1
	b0 := cat & 1
	c += cost.BitCostOne(p[3]) + cost.BitCostOne(p[6]) +
		cost.BitCost(p[8], b1 == 1) + cost.BitCost(p[9+b1], b0 == 1)
	bits := trellisExtraBits(cat)
	rest := mag - (3 + (8 << uint(cat)))
	for i := 0; i < bits; i++ {
		c += cost.BitCost(token.ExtraProbs[cat][i], rest>>(bits-1-i)&1 == 1)
	}
	return c
}

// trellisExtraBits returns the extra-bit width of large category cat.
var trellisExtraBitsTable = [4]int{3, 4, 5, 11}

func trellisExtraBits(cat int) int { return trellisExtraBitsTable[cat] }

// trellisPathRate sums the trellis edge prices along one complete
// level vector: the same additive decomposition the backward program
// minimizes. The result equals cost.BlockCost(plane, ctx, first, ...)
// exactly whenever the levels are codable; the focused tests prove
// that identity directly.
func trellisPathRate(plane, ctx, first int, levels *[16]int16) cost.Cost {
	probs := &token.DefaultProbs
	planeProbs := &probs[plane]

	last := -1
	for i := 15; i >= first; i-- {
		if levels[i] != 0 {
			last = i
			break
		}
	}

	var c cost.Cost
	p := &planeProbs[token.Bands[first]][ctx]
	if last < 0 {
		return cost.BitCostZero(p[0])
	}
	c += cost.BitCostOne(p[0])

	cur := ctx
	n := first
	for n <= last {
		v := levels[n]
		if v == 0 {
			c += cost.BitCostZero(planeProbs[token.Bands[n]][cur][1])
			cur = 0
			n++
			continue
		}
		pn := &planeProbs[token.Bands[n]][cur]
		c += cost.BitCostOne(pn[1])
		mag := int(v)
		if mag < 0 {
			mag = -mag
		}
		if mag == 1 {
			c += cost.BitCostZero(pn[2])
		} else {
			c += cost.BitCostOne(pn[2]) + trellisMagSubtree(pn, mag)
		}
		c += cost.BitCost(128, v < 0)
		cur = trellisNextCtx(mag)
		n++
		if n <= 15 {
			if n > last {
				// The coded suffix ends here: the writer
				// emits the end-of-block flag and stops.
				c += cost.BitCostZero(planeProbs[token.Bands[n]][cur][0])
			} else {
				// More coded positions follow: the writer
				// emits the continuation flag and walks on.
				c += cost.BitCostOne(planeProbs[token.Bands[n]][cur][0])
			}
		}
	}
	return c
}

// trellisTailRate prices the fixed suffix positions lastPos+1..15 at
// their retained values, given the context cur carried out of the
// window. It mirrors the writer's walk over an already-fixed tail:
// zeros continue, nonzeros pay their subtree and the following
// more-coefficients flag until the tail's last nonzero ends the
// block. The caller guarantees the tail holds at least one nonzero.
func trellisTailRate(plane, lastPos, cur int, levels *[16]int16) cost.Cost {
	probs := &token.DefaultProbs
	planeProbs := &probs[plane]

	tailLast := -1
	for i := 15; i > lastPos; i-- {
		if levels[i] != 0 {
			tailLast = i
			break
		}
	}

	var c cost.Cost
	n := lastPos + 1
	for {
		v := levels[n]
		if v == 0 {
			c += cost.BitCostZero(planeProbs[token.Bands[n]][cur][1])
			cur = 0
			n++
			continue
		}
		pn := &planeProbs[token.Bands[n]][cur]
		c += cost.BitCostOne(pn[1])
		mag := int(v)
		if mag < 0 {
			mag = -mag
		}
		if mag == 1 {
			c += cost.BitCostZero(pn[2])
		} else {
			c += cost.BitCostOne(pn[2]) + trellisMagSubtree(pn, mag)
		}
		c += cost.BitCost(128, v < 0)
		cur = trellisNextCtx(mag)
		n++
		if n > 15 {
			return c
		}
		if n > tailLast {
			// The tail's last nonzero ends the block.
			c += cost.BitCostZero(planeProbs[token.Bands[n]][cur][0])
			return c
		}
		// More tail coefficients follow: the writer pays the
		// continue flag and walks on.
		c += cost.BitCostOne(planeProbs[token.Bands[n]][cur][0])
	}
}

// trellisScope is the validated, inclusive scan-position window the
// trellis may rewrite.
type trellisScope struct {
	first, last int
}

// trellisScopeOf validates the configured window and returns it as a
// scope. Widening or misordering the bounds is a deliberate act.
func trellisScopeOf(cfg trellisConfig) trellisScope {
	if cfg.firstPos < 0 || cfg.lastPos < cfg.firstPos || cfg.lastPos > 15 {
		panic("encoder: coefficient trellis window out of range")
	}
	return trellisScope{first: cfg.firstPos, last: cfg.lastPos}
}

// trellisGraph bundles the tables one backward pass reads: the
// validated window, the token probability plane, the probed choice
// sets and marginal distortion deltas, the suffix-zero metadata, and
// the fixed-tail boundary.
type trellisGraph struct {
	sc             trellisScope
	plane          int
	sets           [16]trellisChoiceSet
	delta          [16][trellisMaxChoices]int64
	zeroOKAfter    [16]bool
	zeroDeltaAfter [16]int64
	tail           trellisTail
}

// trellisTail records the fixed suffix beyond the window: whether it
// carries any nonzero, and if so the exact price of coding it as-is
// per exiting context.
type trellisTail struct {
	hasNZ bool
	rate  [trellisNumContexts]cost.Cost
}

// trellisTailOf prices the fixed tail beyond the window. The tail is
// either empty, leaving every in-window path free to end the block,
// or carries a nonzero, forcing continuation and pricing the tail
// exactly per exiting context. Positions below first are zero by the
// checked precondition, so the writer always starts at first.
func trellisTailOf(plane, last int, levels *[16]int16) trellisTail {
	var t trellisTail
	for i := last + 1; i < 16; i++ {
		if (*levels)[i] != 0 {
			t.hasNZ = true
			break
		}
	}
	if t.hasNZ {
		for c := 0; c < trellisNumContexts; c++ {
			t.rate[c] = trellisTailRate(plane, last, c, levels)
		}
	}
	return t
}

// buildTrellisProbes derives every window position's choice set and
// the marginal distortion table in fixed window order. delta[i][k] is
// the SSE change of moving position i from its retained level to
// choice k, in raw SSE units, measured against the retained block's
// SSE. Every offered value different from the retained level costs
// one probe. The guards document an out-of-bounds choice hook and a
// breached probe budget as deliberate-widening acts.
func buildTrellisProbes(cfg trellisConfig, sc trellisScope, retained *[16]int16, dist spatialDistortionFn, stats *coeffTrellisStats) ([16]trellisChoiceSet, [16][trellisMaxChoices]int64) {
	for i := 0; i < sc.first; i++ {
		if (*retained)[i] != 0 {
			panic("encoder: coefficient trellis window starts amid coded levels")
		}
	}
	choiceOf := defaultTrellisChoices
	if cfg.choices != nil {
		choiceOf = cfg.choices
	}
	var sets [16]trellisChoiceSet
	var delta [16][trellisMaxChoices]int64
	retainedSSE := dist(retained)
	stats.Probes = 0
	for i := sc.first; i <= sc.last; i++ {
		cs := choiceOf((*retained)[i])
		if cs.n < 1 || cs.n > trellisMaxChoices {
			panic("encoder: coefficient trellis choice set out of bounds")
		}
		sets[i] = cs
		for k := 0; k < cs.n; k++ {
			if cs.v[k] == (*retained)[i] {
				continue
			}
			probe := *retained
			probe[i] = cs.v[k]
			delta[i][k] = dist(&probe) - retainedSSE
			stats.Probes++
		}
	}
	if stats.Probes > trellisMaxProbes {
		panic("encoder: coefficient trellis probe bound breached")
	}
	return sets, delta
}

// buildTrellisZeroSuffix derives the suffix-zero metadata from the
// already-built choice sets and delta table: for each window position
// i, whether every later window position i+1..last offers a zero, and
// the summed marginal distortion delta of taking those zeroes. An
// end-here transition codes nothing after i, so it implicitly zeroes
// that suffix; its price must carry those deltas, and it only exists
// when every suffix position can be zero at all.
func buildTrellisZeroSuffix(sc trellisScope, sets *[16]trellisChoiceSet, delta *[16][trellisMaxChoices]int64) ([16]bool, [16]int64) {
	var zeroOKAfter [16]bool
	var zeroDeltaAfter [16]int64
	for i := sc.first; i <= sc.last; i++ {
		ok := true
		var sum int64
		for j := i + 1; j <= sc.last; j++ {
			found := false
			for k := 0; k < sets[j].n; k++ {
				if sets[j].v[k] == 0 {
					sum += delta[j][k]
					found = true
					break
				}
			}
			if !found {
				ok = false
				break
			}
		}
		zeroOKAfter[i] = ok
		zeroDeltaAfter[i] = sum
	}
	return zeroOKAfter, zeroDeltaAfter
}

// trellisPass holds the mutable state of one backward sweep: the
// current score-to-go row, the backpointers, the end decisions, the
// whole-block-empty verdict, and everything the sweep reads.
type trellisPass struct {
	g         *trellisGraph
	retained  *[16]int16
	lambda    int64
	ctx       int
	stats     *coeffTrellisStats
	f         [trellisNumContexts]int64
	choiceIdx [16][trellisNumContexts]int8
	endHere   [16][trellisNumContexts]bool
	emptyWon  bool
}

// relaxEdge prices one offered choice k at window position i under
// entering context c against the current score-to-go row: the exact
// edge rate, the position's marginal distortion delta, and either the
// continuation or the end-of-block decision that follows the choice.
// It returns the candidate objective and whether that decision ends
// the block; trellisInfinity reports a choice the suffix premise
// rules out, which can therefore never win the column.
func (tp *trellisPass) relaxEdge(i, c, k int) (int64, bool) {
	v := tp.g.sets[i].v[k]
	var cand int64
	ended := false
	switch {
	case v == 0 && i == 15:
		// The suffix must contain a nonzero; an all-zero
		// tail at the final position breaks the premise.
		return trellisInfinity, false
	case v == 0 && tp.f[0] >= trellisInfinity:
		return trellisInfinity, false
	case v == 0:
		p1 := &token.DefaultProbs[tp.g.plane][token.Bands[i]]
		rate := cost.BitCostZero(p1[c][1])
		cand = tp.lambda*int64(rate) + 256*tp.g.delta[i][k] + tp.f[0]
	default:
		mag := int(v)
		if mag < 0 {
			mag = -mag
		}
		p1 := &token.DefaultProbs[tp.g.plane][token.Bands[i]]
		pn := &p1[c]
		rate := cost.BitCostOne(pn[1])
		if mag == 1 {
			rate += cost.BitCostZero(pn[2])
		} else {
			rate += cost.BitCostOne(pn[2]) + trellisMagSubtree(pn, mag)
		}
		rate += cost.BitCost(128, v < 0)
		own := tp.lambda*int64(rate) + 256*tp.g.delta[i][k]
		if i == 15 {
			cand = own
		} else {
			nc := trellisNextCtx(mag)
			cand, ended = tp.choosePath(i, own, nc)
		}
	}
	return cand, ended
}

// choosePath decides, after one nonzero choice at position i with
// owned cost own handing context nc to position i+1, between ending
// the block right after i and continuing into i+1. Ending is the
// canonical earlier decision; continuation must beat it strictly to
// take over. A live tail forces continuation: the writer can never
// stop inside the window while fixed nonzero positions sit behind it,
// the continuation edge already carries the flag, and the boundary
// sits behind the window, so ending here would misprice the path.
func (tp *trellisPass) choosePath(i int, own int64, nc int) (int64, bool) {
	pNext := &token.DefaultProbs[tp.g.plane][token.Bands[i+1]][nc]
	endOpt := own + tp.lambda*int64(cost.BitCostZero(pNext[0]))
	if !tp.g.tail.hasNZ {
		// Ending here codes nothing after i, so positions
		// i+1..last decode as zero: pay their zeroing deltas,
		// and forbid the stop outright when any of them
		// offers no zero. A live tail forbids ending below
		// anyway.
		if tp.g.zeroOKAfter[i] {
			endOpt += 256 * tp.g.zeroDeltaAfter[i]
		} else {
			endOpt = trellisInfinity
		}
	}
	contOpt := trellisInfinity
	if tp.f[nc] < trellisInfinity {
		contOpt = own + tp.lambda*int64(cost.BitCostOne(pNext[0])) + tp.f[nc]
	}
	cand := endOpt
	ended := true
	if contOpt < cand {
		cand = contOpt
		ended = false
	}
	if tp.g.tail.hasNZ {
		cand = contOpt
		ended = false
	}
	return cand, ended
}

// relaxPosition relaxes every offered choice at window position i
// under entering context c and returns the best objective, the
// winning choice ordinal, and whether that win ends the block. The
// strict comparison keeps the earliest scanned choice on ties.
func (tp *trellisPass) relaxPosition(i, c int) (int64, int8, bool) {
	best := trellisInfinity
	bestCh := int8(-1)
	bestEnd := false
	cs := &tp.g.sets[i]
	for k := 0; k < cs.n; k++ {
		cand, ended := tp.relaxEdge(i, c, k)
		if cand < best {
			best = cand
			bestCh = int8(k)
			bestEnd = ended
		}
	}
	return best, bestCh, bestEnd
}

// run sweeps one scan position: it writes the next score-to-go row
// and the backpointer columns for every context, then charges the
// position's relaxed edges to the bounded counter.
func (tp *trellisPass) run(i int) {
	var nf [trellisNumContexts]int64
	for c := 0; c < trellisNumContexts; c++ {
		nf[c], tp.choiceIdx[i][c], tp.endHere[i][c] = tp.relaxPosition(i, c)
	}
	tp.f = nf
	tp.stats.EdgesRelaxed += tp.g.sets[i].n * trellisNumContexts
}

// rollout rebuilds the chosen path's level vector from the
// backpointers. Positions outside the window keep their retained
// values; an empty-bested pass zeroes the window instead. A recorded
// end decision stops the walk: the block is closed after that
// position, so the remaining window positions are uncoded and stay
// zero. The reachability of every visited state is inherited from
// the pass itself -- the entry state is finite because pathScore is,
// a zero hands context 0 only when f[0] was finite, a continuing
// nonzero hands nc only when f[nc] was finite, and an ended walk
// never queries a later state -- so the panic below documents a
// broken invariant rather than a reachable condition.
func (tp *trellisPass) rollout() [16]int16 {
	winner := *tp.retained
	if tp.emptyWon {
		for i := tp.g.sc.first; i <= tp.g.sc.last; i++ {
			winner[i] = 0
		}
		return winner
	}
	c := tp.ctx
	for i := tp.g.sc.first; i <= tp.g.sc.last; i++ {
		k := tp.choiceIdx[i][c]
		if k < 0 {
			// Unreachable: dpScore would be infinite.
			panic("encoder: coefficient trellis rollout left the graph")
		}
		v := tp.g.sets[i].v[k]
		winner[i] = v
		if tp.endHere[i][c] {
			for j := i + 1; j <= tp.g.sc.last; j++ {
				winner[j] = 0
			}
			break
		}
		mag := int(v)
		if mag < 0 {
			mag = -mag
		}
		c = trellisNextCtx(mag)
	}
	return winner
}

// trellisBackward runs the bounded backward dynamic program over the
// window and returns the level vector it proposes. f[c] is the best
// score-to-go -- lambda-weighted exact rate plus 256-weighted
// distortion deltas -- for the window suffix starting at i under
// entering context c, given at least one nonzero lies in the
// remainder of the block. The boundary seeds it with the priced
// fixed tail when the tail carries a nonzero and with the infinite
// sentinel otherwise. The once-per-non-empty-block leading flag at
// position first is not an edge price; it is added exactly once
// below, alongside the whole-block-empty option whose distortion
// deltas are the zeroing deltas along the window and whose rate is
// the end-of-block-absent flag. The strictly better of the two is
// rolled out; a proposal that stayed unreachable leaves the retained
// levels untouched.
func trellisBackward(g *trellisGraph, ctx int, retained *[16]int16, lambda int64, stats *coeffTrellisStats) [16]int16 {
	tp := &trellisPass{g: g, retained: retained, lambda: lambda, ctx: ctx, stats: stats}
	for c := 0; c < trellisNumContexts; c++ {
		if g.tail.hasNZ {
			tp.f[c] = lambda * int64(g.tail.rate[c])
		} else {
			tp.f[c] = trellisInfinity
		}
	}
	for i := 0; i < 16; i++ {
		for c := 0; c < trellisNumContexts; c++ {
			tp.choiceIdx[i][c] = -1
		}
	}
	for i := g.sc.last; i >= g.sc.first; i-- {
		tp.run(i)
	}
	if stats.EdgesRelaxed > trellisMaxRelaxations {
		panic("encoder: coefficient trellis relaxation bound breached")
	}

	// Whole-block-empty option: available only when the tail is
	// empty and every window position offers zero. Its distortion
	// deltas are the zeroing deltas along the window.
	emptyAvailable := !g.tail.hasNZ
	var emptyDeltas int64
	if emptyAvailable {
		for i := g.sc.first; i <= g.sc.last; i++ {
			found := false
			for k := 0; k < g.sets[i].n; k++ {
				if g.sets[i].v[k] == 0 {
					emptyDeltas += g.delta[i][k]
					found = true
					break
				}
			}
			if !found {
				emptyAvailable = false
				emptyDeltas = 0
				break
			}
		}
	}
	planeProbs := &token.DefaultProbs[g.plane]
	pathScore := trellisInfinity
	if tp.f[ctx] < trellisInfinity {
		pathScore = lambda*int64(cost.BitCostOne(planeProbs[token.Bands[g.sc.first]][ctx][0])) + tp.f[ctx]
	}
	emptyScore := trellisInfinity
	if emptyAvailable {
		emptyScore = lambda*int64(cost.BitCostZero(planeProbs[token.Bands[g.sc.first]][ctx][0])) + 256*emptyDeltas
	}
	dpScore := pathScore
	tp.emptyWon = false
	if emptyScore < dpScore {
		dpScore = emptyScore
		tp.emptyWon = true
	}
	if dpScore >= trellisInfinity {
		return *retained
	}
	return tp.rollout()
}

// selectTrellisWinner picks the final winner by exact full-objective
// scoring over the canonical list -- baselines in the caller's order,
// then the trellis path -- with strictly-smaller replacement, so the
// earliest entry wins every strict tie. With no baselines the
// retained levels anchor the canonical order; a tie with them is not
// a trellis win.
func selectTrellisWinner(sc trellisScope, plane, ctx int, retained, winner *[16]int16, lambda int64, dist spatialDistortionFn, baselines [][16]int16, stats *coeffTrellisStats) [16]int16 {
	score := func(levels *[16]int16) int64 {
		return dist(levels)*256 + lambda*int64(cost.BlockCost(plane, ctx, sc.first, levels, &token.DefaultProbs))
	}
	var final [16]int16
	bestScore := int64(0)
	firstEntry := true
	consider := func(levels [16]int16) bool {
		s := score(&levels)
		stats.ExactScorings++
		if firstEntry || s < bestScore {
			final = levels
			bestScore = s
			firstEntry = false
			return true
		}
		return false
	}
	for _, b := range baselines {
		consider(b)
	}
	if len(baselines) == 0 {
		consider(*retained)
	}
	trellisTookWin := consider(*winner)
	stats.TrellisWon = trellisTookWin
	if len(baselines) == 0 {
		// The anchor was the retained levels themselves; a tie
		// with them is not a trellis win.
		stats.TrellisWon = trellisTookWin && *winner != *retained
	}
	stats.BestScore = bestScore
	stats.Improved = final != *retained
	return final
}

// runCoeffTrellis builds the bounded backward trellis for the
// retained scan levels under entering context ctx, extracts its best
// path under the additive model, and selects the final winner by
// exact full-objective scoring over the canonically ordered baseline
// list followed by the trellis path. Baselines[0] anchors the
// canonical order: every later entry needs a strictly smaller score
// to displace it, so the returned objective is never worse than any
// baseline's and strict ties retain the earliest entry. The retained
// levels themselves must be codable and, below cfg.firstPos, zero.
// The phases run in fixed order -- probes, tail boundary, suffix
// zeroes, backward pass, rollout -- and every guard keeps its place
// in that order.
func runCoeffTrellis(cfg trellisConfig, plane, ctx int, retained *[16]int16, lambda int64, dist spatialDistortionFn, baselines ...[16]int16) ([16]int16, coeffTrellisStats) {
	var stats coeffTrellisStats
	sc := trellisScopeOf(cfg)
	if ctx < 0 || ctx >= trellisNumContexts {
		panic("encoder: coefficient trellis context out of range")
	}
	sets, delta := buildTrellisProbes(cfg, sc, retained, dist, &stats)
	tail := trellisTailOf(plane, sc.last, retained)
	zeroOKAfter, zeroDeltaAfter := buildTrellisZeroSuffix(sc, &sets, &delta)
	g := &trellisGraph{
		sc:             sc,
		plane:          plane,
		sets:           sets,
		delta:          delta,
		zeroOKAfter:    zeroOKAfter,
		zeroDeltaAfter: zeroDeltaAfter,
		tail:           tail,
	}
	winner := trellisBackward(g, ctx, retained, lambda, &stats)
	final := selectTrellisWinner(sc, plane, ctx, retained, &winner, lambda, dist, baselines, &stats)
	return final, stats
}
