package cost

import (
	"math"
	"reflect"
	"testing"

	"m31labs.dev/tqwebp/internal/token"
)

// The tests in this file audit the Slice 6A probability optimizer against
// oracles built here. None of them restate probopt.go's arithmetic: the
// reference prices are recomputed from the exact ideal code length
// -log2(p/256) with math.Log2, and every Optimize verdict is recomputed
// node by node from that reference before production is asked for its own.

// refCosts holds independently recomputed 1/256-bit prices of one false
// and one true branch at every codable Q8 probability.
type refCosts struct {
	zero [256]int64
	one  [256]int64
}

// newRefCosts derives both price columns from logarithms. The rounding
// rule is round-half-up on non-negative values, which is how the package
// documents zeroCostQ8 ("rounded to the nearest unit").
func newRefCosts() refCosts {
	var r refCosts
	for p := 1; p <= 255; p++ {
		r.zero[p] = int64(math.Floor(-math.Log2(float64(p)/256)*256 + 0.5))
		r.one[p] = int64(math.Floor(-math.Log2(float64(256-p)/256)*256 + 0.5))
	}
	return r
}

// branch returns the exact reference price of nFalse false branches and
// nTrue true branches coded at probability p.
func (r refCosts) branch(p uint8, nFalse, nTrue int) int64 {
	return int64(nFalse)*r.zero[p] + int64(nTrue)*r.one[p]
}

// argmin enumerates p = 1..255 and returns the lowest probability whose
// total price is minimal, together with that price, the number of tied
// minimizers, and the runner-up price (math.MaxInt64 when unique).
func (r refCosts) argmin(nFalse, nTrue int) (bestP uint8, bestC int64, ties int, second int64) {
	bestC = math.MaxInt64
	second = math.MaxInt64
	for p := 1; p <= 255; p++ {
		c := r.branch(uint8(p), nFalse, nTrue)
		switch {
		case c < bestC:
			second = bestC
			bestC = c
			bestP = uint8(p)
			ties = 1
		case c == bestC:
			ties++
		case c < second:
			second = c
		}
	}
	return bestP, bestC, ties, second
}

// TestBranchCostMatchesLogReference proves BranchCost prices branches
// exactly as -log2 says, for every codable probability and a grid of
// counts. A drifted table or a swapped zero/one column fails here even
// though every production function would stay self-consistent.
func TestBranchCostMatchesLogReference(t *testing.T) {
	ref := newRefCosts()
	for p := 1; p <= 255; p++ {
		if got := BitCostZero(uint8(p)); got != Cost(ref.zero[p]) {
			t.Fatalf("BitCostZero(%d) = %d, want %d", p, got, ref.zero[p])
		}
		if got := BitCostOne(uint8(p)); got != Cost(ref.one[p]) {
			t.Fatalf("BitCostOne(%d) = %d, want %d", p, got, ref.one[p])
		}
	}
	counts := [][2]int{
		{0, 1}, {1, 0}, {1, 1}, {3, 5}, {12, 4}, {40, 7},
		{0, 250}, {250, 0}, {128, 128}, {1000, 3},
	}
	for _, c := range counts {
		for p := 1; p <= 255; p++ {
			want := ref.branch(uint8(p), c[0], c[1])
			if got := BranchCost(uint8(p), c[0], c[1]); got != Cost(want) {
				t.Fatalf("BranchCost(%d, %d, %d) = %d, want %d", p, c[0], c[1], got, want)
			}
		}
	}
}

// TestOptimalProbIsLowestTiedArgmin scans a broad grid of observation
// counts and requires OptimalProb to equal the lowest-probability
// minimizer of the independent enumeration. The grid must contain tied
// minima, otherwise the tie rule would go untested; the test fails if it
// finds none.
func TestOptimalProbIsLowestTiedArgmin(t *testing.T) {
	ref := newRefCosts()
	tieCases := 0
	case_ := func(nFalse, nTrue int) {
		t.Helper()
		if nFalse <= 0 && nTrue <= 0 {
			panic("unobserved case in observed grid")
		}
		wantP, _, ties, _ := ref.argmin(nFalse, nTrue)
		if ties > 1 {
			tieCases++
		}
		if got := OptimalProb(nFalse, nTrue); got != wantP {
			t.Fatalf("OptimalProb(%d,%d) = %d, want lowest tied minimizer %d",
				nFalse, nTrue, got, wantP)
		}
	}
	for nf := 0; nf <= 16; nf++ {
		for nt := 0; nt <= 16; nt++ {
			if nf+nt > 0 {
				case_(nf, nt)
			}
		}
	}
	for _, c := range [][2]int{
		{1, 17}, {17, 1}, {23, 41}, {64, 8}, {8, 64}, {99, 101}, {200, 55},
	} {
		case_(c[0], c[1])
	}
	if tieCases == 0 {
		t.Fatal("grid contained no tied minima; the lowest-tie rule is untested")
	}
	t.Logf("%d of %d grid cases had tied minima", tieCases, 17*17-1+7)
}

// TestOptimalProbUnobservedIsEvenOdds pins the no-evidence answer.
func TestOptimalProbUnobservedIsEvenOdds(t *testing.T) {
	if got := OptimalProb(0, 0); got != 128 {
		t.Fatalf("OptimalProb(0,0) = %d, want 128", got)
	}
}

// ledger is this file's independent version of Optimize: given raw
// per-node counts and an old table, it decides every entry exactly as
// the update ledger says -- candidate = lowest tied enumeration
// minimum, ship only when the new total (branch cost at the candidate
// plus true-side gate plus literal) is strictly below the old total
// (branch cost at old plus false-side gate).
type ledger map[[4]int]uint8

// decide returns the expected new table and whether anything ships.
func (r refCosts) decide(counts func(plane, band, ctx, node int) (int, int), old *token.Probs) (ledger, bool) {
	out := ledger{}
	won := false
	for i := 0; i < token.NumPlanes; i++ {
		for j := 0; j < token.NumBands; j++ {
			for k := 0; k < token.NumContexts; k++ {
				for l := 0; l < token.NumProbs; l++ {
					nf, nt := counts(i, j, k, l)
					if nf+nt == 0 {
						continue // no evidence: never ship
					}
					newP, _, _, _ := r.argmin(nf, nt)
					oldP := old[i][j][k][l]
					gate := token.UpdateProbs[i][j][k][l]
					oldTotal := r.branch(oldP, nf, nt) +
						r.zero[gate]
					newTotal := r.branch(newP, nf, nt) +
						r.one[gate] +
						ProbUpdateLiteralBits*int64(PlainBit)
					if newTotal < oldTotal {
						out[[4]int{i, j, k, l}] = newP
						won = true
					}
				}
			}
		}
	}
	return out, won
}

// checkAgainstLedger runs Optimize over h and compares every entry with
// the independent ledger: shipped nodes carry the candidate, everything
// else keeps old, and nil means nothing shipped anywhere.
func checkAgainstLedger(t *testing.T, h *Histogram, old *token.Probs) {
	t.Helper()
	ref := newRefCosts()
	want, wantWon := ref.decide(h.Counts, old)
	got := h.Optimize(old)
	if !wantWon {
		if got != nil {
			t.Fatalf("Optimize returned a table %+v where the ledger says nothing wins", *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("Optimize returned nil where the ledger ships %d entries", len(want))
	}
	for i := 0; i < token.NumPlanes; i++ {
		for j := 0; j < token.NumBands; j++ {
			for k := 0; k < token.NumContexts; k++ {
				for l := 0; l < token.NumProbs; l++ {
					key := [4]int{i, j, k, l}
					newP, ship := want[key]
					have := (*got)[i][j][k][l]
					if ship && have != newP {
						t.Errorf("node %v: Optimize kept %d, ledger ships %d", key, have, newP)
					}
					if !ship && have != old[i][j][k][l] {
						t.Errorf("node %v: Optimize wrote %d, ledger keeps old %d", key, have, old[i][j][k][l])
					}
				}
			}
		}
	}
}

// fill writes one node's counts into h.
func fill(h *Histogram, plane, band, ctx, node, nFalse, nTrue int) {
	for i := 0; i < nFalse; i++ {
		h.ObserveBranch(plane, band, ctx, node, false)
	}
	for i := 0; i < nTrue; i++ {
		h.ObserveBranch(plane, band, ctx, node, true)
	}
}

// TestOptimizeVerdictsMatchIndependentLedger drives Optimize over single
// node observations chosen to hit every verdict class: strict wins, ties
// that keep old, losses that keep old, candidates that pay but cannot
// cover the update, and no-op candidates equal to old. Every run is
// checked entry by entry against the ledger built above.
func TestOptimizeVerdictsMatchIndependentLedger(t *testing.T) {
	nodes := [][4]int{{0, 0, 0, 0}, {2, 5, 1, 3}, {3, 7, 2, 10}}
	counts := [][2]int{
		{200, 0}, {0, 200}, {60, 4}, {1, 1}, {15, 1}, {1, 15},
		{9, 9}, {30, 30}, {2, 9}, {9, 2}, {120, 8}, {8, 120},
	}
	oldPs := []uint8{1, 32, 63, 64, 128, 200, 254, 255}
	for _, node := range nodes {
		for _, c := range counts {
			for _, oldP := range oldPs {
				old := token.DefaultProbs
				old[node[0]][node[1]][node[2]][node[3]] = oldP
				h := &Histogram{}
				fill(h, node[0], node[1], node[2], node[3], c[0], c[1])
				checkAgainstLedger(t, h, &old)
			}
		}
	}
}

// TestOptimizeClassifiesWinTieAndNoOpCandidates pins concrete instances
// of each verdict so a future refactor cannot silently narrow the strict
// inequality without the classification test noticing. The classes are
// found by scanning the same grid with the reference tables, so the
// example values come from the oracle, not from production.
func TestOptimizeClassifiesWinTieAndNoOpCandidates(t *testing.T) {
	ref := newRefCosts()
	node := [4]int{1, 2, 1, 2}

	// Re-run three representative shapes through Optimize itself and
	// confirm the verdicts. The shapes are located by scanning the same
	// grid with the reference tables: one strict winner, one pure tie
	// (identical branch price), and one no-op candidate equal to old.
	type shape struct {
		nf, nt int
		oldP   uint8
		ship   bool
	}
	var win, keep, noop *shape
	scan := func(nf, nt int, oldPs []uint8) {
		newP, _, _, _ := ref.argmin(nf, nt)
		gate := token.UpdateProbs[node[0]][node[1]][node[2]][node[3]]
		price := ref.one[gate] + ProbUpdateLiteralBits*int64(PlainBit) -
			ref.zero[gate]
		for _, oldP := range oldPs {
			if oldP == newP {
				if noop == nil {
					noop = &shape{nf, nt, oldP, false}
				}
				continue
			}
			saving := ref.branch(oldP, nf, nt) - ref.branch(newP, nf, nt)
			if saving > price && win == nil {
				win = &shape{nf, nt, oldP, true}
			}
			if saving == 0 && keep == nil {
				keep = &shape{nf, nt, oldP, false}
			}
		}
	}
	for nf := 1; nf <= 96 && (win == nil || keep == nil || noop == nil); nf++ {
		for nt := 1; nt <= 96; nt++ {
			scan(nf, nt, []uint8{1, 63, 128, 200, 255})
		}
	}
	if win == nil || keep == nil || noop == nil {
		t.Fatalf("scan incomplete: win=%v keep=%v noop=%v", win != nil, keep != nil, noop != nil)
	}
	shapes := [3]*shape{win, keep, noop}
	for i, s := range shapes {
		old := token.DefaultProbs
		old[node[0]][node[1]][node[2]][node[3]] = s.oldP
		h := &Histogram{}
		fill(h, node[0], node[1], node[2], node[3], s.nf, s.nt)
		got := h.Optimize(&old)
		if !s.ship {
			if got != nil {
				t.Fatalf("shape %d (%d,%d old=%d): Optimize shipped %+v, want nil",
					i, s.nf, s.nt, s.oldP, *got)
			}
			continue
		}
		if got == nil || (*got)[node[0]][node[1]][node[2]][node[3]] == s.oldP {
			t.Fatalf("shape %d (%d,%d old=%d): Optimize did not ship a winner", i, s.nf, s.nt, s.oldP)
		}
	}
}

// TestOptimizeMultiNodeIsAllOrNothing builds one histogram carrying a
// clear winner, a clear loser, and silent nodes, then requires the
// returned table to mix updates and defaults exactly as the ledger says,
// including the nil result when no node at all wins.
func TestOptimizeMultiNodeIsAllOrNothing(t *testing.T) {
	old := token.DefaultProbs

	winNode := [4]int{0, 0, 0, 0}
	loseNode := [4]int{2, 3, 0, 1}
	old[loseNode[0]][loseNode[1]][loseNode[2]][loseNode[3]] = 129

	h := &Histogram{}
	fill(h, winNode[0], winNode[1], winNode[2], winNode[3], 90, 6)
	fill(h, loseNode[0], loseNode[1], loseNode[2], loseNode[3], 3, 3)
	checkAgainstLedger(t, h, &old)

	// Only losers now: the whole call must return nil.
	empty := &Histogram{}
	fill(empty, loseNode[0], loseNode[1], loseNode[2], loseNode[3], 3, 3)
	checkAgainstLedger(t, empty, &old)

	// Fully unobserved histogram: nil, not the default table pointer.
	checkAgainstLedger(t, &Histogram{}, &old)
	if got := (&Histogram{}).Optimize(&old); got != nil {
		t.Fatalf("unobserved Optimize = %+v, want nil", *got)
	}
}

// TestOptimizeIsDeterministicAcrossRepeats reruns identical derivations
// and requires identical tables, exercising repeat stability of the
// integer walk.
func TestOptimizeIsDeterministicAcrossRepeats(t *testing.T) {
	old := token.DefaultProbs
	h := &Histogram{}
	for i := 0; i < token.NumPlanes; i++ {
		for j := 0; j < token.NumBands; j++ {
			fill(h, i, j, 0, j%token.NumProbs, 3*i+j+1, (i*j+j)%7)
		}
	}
	first := h.Optimize(&old)
	for rep := 0; rep < 5; rep++ {
		again := h.Optimize(&old)
		if first == nil && again == nil {
			continue
		}
		if first == nil || again == nil || !reflect.DeepEqual(*first, *again) {
			t.Fatalf("repeat %d changed the derivation: %+v vs %+v", rep, first, again)
		}
	}
}

// TestOptimizeLedgerChargesFalseGateSide is the adversarial cost probe
// for the Slice 6A repair: shipping must require the exact totals
//
//	BranchCost(new) + ProbUpdateCost(gate, true)
//	    < BranchCost(old) + ProbUpdateCost(gate, false)
//
// so BOTH sides of the section 13.4 gate decision are priced. A ledger
// that drops the false-side gate cost from the old total -- or compares
// bare branch savings against the true-side gate plus literal -- rejects
// marginal updates the exact rule accepts; every shape in the band
// below sits exactly where those two rules disagree.
func TestOptimizeLedgerChargesFalseGateSide(t *testing.T) {
	ref := newRefCosts()
	node := [4]int{3, 0, 2, 7}
	gate := token.UpdateProbs[node[0]][node[1]][node[2]][node[3]]
	lit := ProbUpdateLiteralBits * int64(PlainBit)
	exactPrice := ref.one[gate] + lit - ref.zero[gate] // exact threshold: PUC(true)-PUC(false)
	naivePrice := ref.one[gate] + lit                  // threshold without the false side

	type probe struct {
		nf, nt int
		oldP   uint8
	}

	// Collect shapes where the exact totals ship but the naive
	// threshold (false side dropped) declines:
	// exactPrice < saving <= naivePrice.
	var band []probe
scan:
	for nf := 1; nf <= 96; nf++ {
		for nt := 1; nt <= 96; nt++ {
			newP, _, _, _ := ref.argmin(nf, nt)
			newBranch := ref.branch(newP, nf, nt)
			for op := 1; op <= 255; op++ {
				oldP := uint8(op)
				if oldP == newP {
					continue // no-op candidates ship under neither rule
				}
				saving := ref.branch(oldP, nf, nt) - newBranch
				if saving > exactPrice && saving <= naivePrice {
					band = append(band, probe{nf, nt, oldP})
					if len(band) == 8 {
						break scan
					}
					break
				}
			}
		}
	}
	if len(band) == 0 {
		t.Fatal("scan found no shape where the naive ledger diverges from the exact totals; the probe is vacuous")
	}

	for _, c := range band {
		old := token.DefaultProbs
		old[node[0]][node[1]][node[2]][node[3]] = c.oldP
		h := &Histogram{}
		fill(h, node[0], node[1], node[2], node[3], c.nf, c.nt)

		newP, _, _, _ := ref.argmin(c.nf, c.nt)
		oldTotal := ref.branch(c.oldP, c.nf, c.nt) + ref.zero[gate]
		newTotal := ref.branch(newP, c.nf, c.nt) + ref.one[gate] + lit
		if newTotal >= oldTotal {
			t.Fatalf("scan bug: shape (%d,%d) old=%d landed outside the divergence band",
				c.nf, c.nt, c.oldP)
		}
		if got := h.Optimize(&old); got == nil || (*got)[node[0]][node[1]][node[2]][node[3]] != newP {
			t.Fatalf("shape (%d,%d) old=%d: exact totals win (%d vs %d) but Optimize did not ship %d",
				c.nf, c.nt, c.oldP, newTotal, oldTotal, newP)
		}
	}

	// Guard against over-correction at both edges of the exact
	// threshold: one unit past it a genuine winner must still ship,
	// and exactly on it a tie must keep old.
	var winCase, tieCase probe
	foundWin, foundTie := false, false
edgeScan:
	for nf := 1; nf <= 128; nf++ {
		for nt := 1; nt <= 128; nt++ {
			newP, _, _, _ := ref.argmin(nf, nt)
			newBranch := ref.branch(newP, nf, nt)
			for _, oldP := range []uint8{1, 63, 128, 200, 255} {
				if oldP == newP {
					continue
				}
				switch saving := ref.branch(oldP, nf, nt) - newBranch - exactPrice; saving {
				case 1:
					if !foundWin {
						winCase = probe{nf, nt, oldP}
						foundWin = true
					}
				case 0:
					if !foundTie {
						tieCase = probe{nf, nt, oldP}
						foundTie = true
					}
				}
				if foundWin && foundTie {
					break edgeScan
				}
			}
		}
	}
	if !foundWin || !foundTie {
		t.Fatalf("threshold edge scan incomplete: win=%v tie=%v", foundWin, foundTie)
	}
	for _, tc := range []struct {
		p        probe
		wantShip bool
	}{
		{winCase, true},
		{tieCase, false},
	} {
		old := token.DefaultProbs
		old[node[0]][node[1]][node[2]][node[3]] = tc.p.oldP
		h := &Histogram{}
		fill(h, node[0], node[1], node[2], node[3], tc.p.nf, tc.p.nt)
		got := h.Optimize(&old)
		if !tc.wantShip {
			if got != nil {
				t.Fatalf("exact-threshold tie (%d,%d) old=%d shipped %+v; ties must keep old",
					tc.p.nf, tc.p.nt, tc.p.oldP, *got)
			}
			continue
		}
		newP, _, _, _ := ref.argmin(tc.p.nf, tc.p.nt)
		if got == nil || (*got)[node[0]][node[1]][node[2]][node[3]] != newP {
			t.Fatalf("one unit past the exact threshold (%d,%d) old=%d: Optimize did not ship %d",
				tc.p.nf, tc.p.nt, tc.p.oldP, newP)
		}
	}
}
