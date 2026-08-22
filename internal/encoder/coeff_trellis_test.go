package encoder

// The tests below pin the two properties the coefficient trellis must
// never lose: its additive edge prices reproduce cost.BlockCost
// exactly, and its bounded work plus strict-tie selection hold on
// dense adversarial inputs. Both stay deterministic and independent
// of trellis internals.

import (
	"testing"

	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/token"
)

// trellisLevels expands a sparse position,value list into scan-order
// levels. All unspecified positions stay zero.
func trellisLevels(pairs ...int) [16]int16 {
	var lv [16]int16
	for i := 0; i+1 < len(pairs); i += 2 {
		lv[pairs[i]] = int16(pairs[i+1])
	}
	return lv
}

// TestCoeffTrellisPathRateParity proves trellisPathRate equals
// cost.BlockCost exactly over a compact deterministic table spanning
// empty blocks, zero runs, both signs, category-boundary magnitudes
// through token.MaxLevel, early and final end-of-block flags, every
// token plane, and every neighbour context.
func TestCoeffTrellisPathRateParity(t *testing.T) {
	cases := []struct {
		name   string
		levels [16]int16
	}{
		{"empty", trellisLevels()},
		{"early-eob-single-one", trellisLevels(2, 1)},
		{"final-eob-last-position", trellisLevels(15, -1)},
		{"interior-zero-run", trellisLevels(0, -1, 14, 1)},
		{"small-magnitudes-mixed-signs", trellisLevels(0, 1, 1, -1, 4, 2, 7, -2)},
		{"category-boundaries", trellisLevels(1, 4, 3, -5, 6, 10, 8, -11)},
		{"large-category-boundaries", trellisLevels(2, 34, 5, -35, 9, token.MaxLevel)},
		{"max-level-signs-with-runs", trellisLevels(0, -token.MaxLevel, 7, token.MaxLevel, 12, -35, 15, 34)},
	}
	for _, tc := range cases {
		for plane := 0; plane < token.NumPlanes; plane++ {
			for ctx := 0; ctx <= 2; ctx++ {
				got := trellisPathRate(plane, ctx, 0, &tc.levels)
				want := cost.BlockCost(plane, ctx, 0, &tc.levels, &token.DefaultProbs)
				if got != want {
					t.Errorf("%s: plane %d ctx %d: trellisPathRate=%d cost.BlockCost=%d",
						tc.name, plane, ctx, got, want)
				}
			}
		}
	}
}

// TestCoeffTrellisTieAndBounds proves the final selection retains the
// earliest baseline on exact ties and that the hard work bounds --
// probes, edge relaxations, and one exact scoring per candidate --
// hold on dense adversarial levels.
func TestCoeffTrellisTieAndBounds(t *testing.T) {
	levelSSE := func(levels *[16]int16) int64 {
		var s int64
		for _, v := range *levels {
			s += int64(v) * int64(v)
		}
		return s
	}

	var allMax, altSign, denseOnes [16]int16
	for i := range allMax {
		allMax[i] = token.MaxLevel
		altSign[i] = token.MaxLevel
		denseOnes[i] = 1
		if i%2 == 1 {
			altSign[i] = -altSign[i]
		}
	}

	// Exact-tie proof: with zero lambda and a constant distortion
	// every candidate scores identically, so the strictly-smaller
	// replacement rule must retain baselines[0], not the trellis
	// proposal.
	tieRetained := trellisLevels(0, 5, 8, -3, 15, 1)
	tieOther := trellisLevels(0, 4, 8, -3)
	tieDist := func(*[16]int16) int64 { return 42 }
	tieGot, tieStats := runCoeffTrellis(trellisWholeScan, token.YWithDC, 1,
		&tieRetained, 0, tieDist, tieRetained, tieOther)
	if tieGot != tieRetained {
		t.Errorf("exact tie: returned %v, want earlier baseline %v", tieGot, tieRetained)
	}
	if tieStats.TrellisWon || tieStats.Improved {
		t.Errorf("exact tie: TrellisWon=%v Improved=%v, want both false", tieStats.TrellisWon, tieStats.Improved)
	}
	if want := int64(cost.Unit * 42); tieStats.BestScore != want {
		t.Errorf("exact tie: BestScore=%d, want %d", tieStats.BestScore, want)
	}
	if tieStats.ExactScorings != 3 {
		t.Errorf("exact tie: ExactScorings=%d, want 3", tieStats.ExactScorings)
	}

	boundsCases := []struct {
		name         string
		ctx          int
		plane        int
		numBaselines int
		retained     [16]int16
	}{
		{"all-max-level", 0, token.YWithDC, 1, allMax},
		{"alternating-signs-max-level", 2, token.YWithDC, 2, altSign},
		{"dense-ones", 1, token.YWithDC, 1, denseOnes},
		{"sparse-extremes", 0, token.NumPlanes - 1, 2,
			trellisLevels(0, -token.MaxLevel, 15, token.MaxLevel)},
		{"mixed-adversarial", 2, token.YWithDC, 1,
			trellisLevels(1, 35, 4, -34, 6, token.MaxLevel, 10, -11, 13, 5)},
	}
	for _, bc := range boundsCases {
		baselines := [][16]int16{bc.retained}
		for i := 1; i < bc.numBaselines; i++ {
			baselines = append(baselines, trellisLevels(0, i))
		}
		retained := bc.retained
		_, stats := runCoeffTrellis(trellisWholeScan, bc.plane, bc.ctx,
			&retained, 300, levelSSE, baselines...)
		if stats.Probes > trellisMaxProbes {
			t.Errorf("%s: Probes=%d exceeds %d", bc.name, stats.Probes, trellisMaxProbes)
		}
		if stats.EdgesRelaxed > trellisMaxRelaxations {
			t.Errorf("%s: EdgesRelaxed=%d exceeds %d", bc.name, stats.EdgesRelaxed, trellisMaxRelaxations)
		}
		if want := len(baselines) + 1; stats.ExactScorings != want {
			t.Errorf("%s: ExactScorings=%d, want baselines+%d=%d",
				bc.name, stats.ExactScorings, 1, want)
		}
	}
}

// TestCoeffTrellisReducedWindowOracle proves the bounded backward
// program returns exactly the exhaustive optimum of a reduced window.
// The test rebuilds the documented per-position choice sets --
// retained, one step toward zero, zero, deduplicated keeping the
// earliest ordinal -- independently of every production choice or
// path helper, enumerates all joint combinations of one choice per
// window position, and prices each resulting vector with the exact
// caller-side objective: crafted additive distortion times 256 plus
// lambda times cost.BlockCost. The distortion is a weighted squared
// distance to per-position targets, so it is exactly separable and
// the trellis's one-position marginal probes price every joint
// combination without approximation; because the enumeration covers
// the whole graph, its winner is the true optimum under strict
// enumeration-order tie-breaking, and runCoeffTrellis must return
// that same winner with that same score.
//
// The crafted case hides its optimum behind a joint mutation: the
// exhaustive winner steps two separated scan positions toward their
// targets at once, a move no one-position-at-a-time candidate offers,
// and is not a suffix cut. The test asserts both facts -- exactly two
// separated moved positions, absent from enumerateCoeffCandidates
// over a populated candidate set -- so the proof cannot pass
// vacuously. The window is four positions over at most three choices
// each, so the enumeration stays bounded and deterministic.
func TestCoeffTrellisReducedWindowOracle(t *testing.T) {
	const (
		plane  = token.YWithDC
		ctx    = 1
		lambda = 300
		first  = 2
		last   = 5
	)

	// Retained levels: coded levels inside the reduced window at
	// scan positions 2..5, zeros before it as the window contract
	// requires, and nothing after it so the window owns the whole
	// end of the block.
	retained := trellisLevels(2, 3, 3, -2, 4, 1, 5, 4)

	// Crafted additive distortion: per-position weight times squared
	// distance to a per-position target level. Separability makes
	// the trellis's marginal probes exact for every joint
	// combination, while the targets place the distortion optimum
	// strictly between the retained levels and zero so the winner
	// keeps coded nonzeros instead of collapsing to a clear.
	var weight, target [16]int64
	weight[2], target[2] = 3000, 3  // anchored against any move
	weight[3], target[3] = 2000, -1 // wants one step up from -2
	weight[4], target[4] = 2500, 1  // anchored against zeroing
	weight[5], target[5] = 400, 3   // wants one step down from 4
	dist := func(levels *[16]int16) int64 {
		var s int64
		for i := 0; i < 16; i++ {
			d := int64(levels[i]) - target[i]
			s += weight[i] * d * d
		}
		return s
	}
	score := func(levels *[16]int16) int64 {
		return dist(levels)*256 +
			lambda*int64(cost.BlockCost(plane, ctx, first, levels, &token.DefaultProbs))
	}

	// Independent rebuild of the documented choice-set rule; no
	// production helper is called for choices or paths anywhere in
	// this oracle.
	window := []int{2, 3, 4, 5}
	sets := make([][]int16, len(window))
	for k, p := range window {
		r := retained[p]
		var cs []int16
		add := func(x int16) {
			for _, v := range cs {
				if v == x {
					return
				}
			}
			cs = append(cs, x)
		}
		add(r)
		if r > 0 {
			add(r - 1)
		} else if r < 0 {
			add(r + 1)
		}
		add(0)
		sets[k] = cs
	}

	// Exhaustive odometer enumeration, last window position fastest;
	// strictly-smaller replacement preserves enumeration-order ties.
	var idx []int = make([]int, len(window))
	combos := 0
	firstCombo := true
	var oracleBest [16]int16
	var oracleScore int64
	for {
		cand := retained
		for k, p := range window {
			cand[p] = sets[k][idx[k]]
		}
		if s := score(&cand); firstCombo || s < oracleScore {
			oracleBest, oracleScore = cand, s
			firstCombo = false
		}
		combos++
		k := len(window) - 1
		for k >= 0 {
			idx[k]++
			if idx[k] < len(sets[k]) {
				break
			}
			idx[k] = 0
			k--
		}
		if k < 0 {
			break
		}
	}
	if combos < 2 || combos > 81 { // 3 choices x 4 positions, bounded
		t.Fatalf("enumerated %d combinations; want a small non-empty product", combos)
	}

	// Non-vacuity, fact one: the oracle winner must be a genuine
	// joint mutation -- changed at exactly two separated scan
	// positions -- or the case proves nothing about joint moves.
	var moved []int
	for i := 0; i < 16; i++ {
		if oracleBest[i] != retained[i] {
			moved = append(moved, i)
		}
	}
	if len(moved) != 2 || moved[1]-moved[0] < 2 {
		t.Fatalf("exhaustive winner %v moved positions %v; want exactly two separated positions",
			oracleBest, moved)
	}

	// Non-vacuity, fact two: that joint winner must be absent from
	// the one-mutation-plus-suffix-cuts candidate enumeration, which
	// must itself be populated.
	cands, _ := enumerateCoeffCandidates(&retained)
	if len(cands) < 2 {
		t.Fatalf("enumerateCoeffCandidates offered %d candidates; want a populated set", len(cands))
	}
	for _, c := range cands {
		if c == oracleBest {
			t.Fatalf("joint winner %v already offered by enumerateCoeffCandidates; case is vacuous",
				oracleBest)
		}
	}

	// Production must return exactly the exhaustive winner and its
	// exact score, through the trellis path carrying the win.
	got, stats := runCoeffTrellis(trellisConfig{firstPos: first, lastPos: last},
		plane, ctx, &retained, lambda, dist)
	if got != oracleBest {
		t.Errorf("runCoeffTrellis winner %v, exhaustive oracle winner %v", got, oracleBest)
	}
	if want := oracleScore; stats.BestScore != want {
		t.Errorf("runCoeffTrellis BestScore %d, exhaustive oracle score %d", stats.BestScore, want)
	}
	if !stats.TrellisWon {
		t.Errorf("TrellisWon=false: the trellis path did not carry the joint winning mutation")
	}
	if !stats.Improved {
		t.Errorf("Improved=false: returned vector %v equals retained", got)
	}
}

// TestCoeffTrellisEndHereNeedsZeroableSuffix pins the end-here
// transition: stopping the block after position i silently decodes
// positions i+1..last as zero, so the stop must carry those zeroing
// distortions and must vanish outright once some later position
// offers no zero. A custom choice set strips position 5 -- keyed by
// its unique retained value -- down to that single value, so every
// early stop would leave position 5 at a value its set does not
// offer. The trellis must return exactly the exhaustive optimum over
// the stripped sets, with every window level drawn from its own set.
func TestCoeffTrellisEndHereNeedsZeroableSuffix(t *testing.T) {
	const (
		plane  = token.YWithDC
		ctx    = 1
		lambda = 300
		first  = 2
		last   = 5
	)

	retained := trellisLevels(2, 3, 3, -2, 4, 1, 5, 4)

	// Separable quadratic distortion: marginal probes are exact, so
	// any residual gap between the trellis proposal and the exhaustive
	// optimum isolates the end-here accounting alone.
	var weight, target [16]int64
	weight[2], target[2] = 3000, 3
	weight[3], target[3] = 2000, -1 // one step up from -2
	weight[4], target[4] = 2500, 1
	weight[5], target[5] = 400, 3 // wants zeroing, but zero is not offered
	dist := func(levels *[16]int16) int64 {
		var s int64
		for i := 0; i < 16; i++ {
			d := int64(levels[i]) - target[i]
			s += weight[i] * d * d
		}
		return s
	}
	score := func(levels *[16]int16) int64 {
		return dist(levels)*256 +
			lambda*int64(cost.BlockCost(plane, ctx, first, levels, &token.DefaultProbs))
	}

	// Default rule everywhere except the no-zero position, identified
	// by its unique retained value inside the window.
	choices := func(r int16) trellisChoiceSet {
		if r == 4 {
			var cs trellisChoiceSet
			cs.v[0], cs.n = 4, 1
			return cs
		}
		return defaultTrellisChoices(r)
	}
	window := []int{2, 3, 4, 5}
	sets := make([][]int16, len(window))
	for k, p := range window {
		cs := choices(retained[p])
		for i := 0; i < cs.n; i++ {
			sets[k] = append(sets[k], cs.v[i])
		}
	}

	// Exhaustive odometer enumeration, strictly-smaller replacement in
	// enumeration order; every combination keeps position 5 coded, so
	// each one is representable by continuing through the window end.
	var idx []int = make([]int, len(window))
	combos := 0
	firstCombo := true
	var oracleBest [16]int16
	var oracleScore int64
	for {
		cand := retained
		for k, p := range window {
			cand[p] = sets[k][idx[k]]
		}
		if s := score(&cand); firstCombo || s < oracleScore {
			oracleBest, oracleScore = cand, s
			firstCombo = false
		}
		combos++
		k := len(window) - 1
		for k >= 0 {
			idx[k]++
			if idx[k] < len(sets[k]) {
				break
			}
			idx[k] = 0
			k--
		}
		if k < 0 {
			break
		}
	}
	if combos < 2 || combos > 81 { // at most three choices x four positions
		t.Fatalf("enumerated %d combinations; want a small non-empty product", combos)
	}

	// Non-vacuity: the winner must keep the unzeroable position coded
	// and must displace the retained levels outright, or the case
	// cannot distinguish priced from unpriced suffix zeroing.
	if oracleBest[last] == 0 {
		t.Fatalf("exhaustive winner %v zeroes the no-zero position; case proves nothing", oracleBest)
	}
	if oracleBest == retained {
		t.Fatalf("exhaustive winner %v equals retained; case proves nothing", oracleBest)
	}

	got, stats := runCoeffTrellis(trellisConfig{firstPos: first, lastPos: last, choices: choices},
		plane, ctx, &retained, lambda, dist)
	if got != oracleBest {
		t.Errorf("runCoeffTrellis winner %v, exhaustive winner %v", got, oracleBest)
	}
	if want := oracleScore; stats.BestScore != want {
		t.Errorf("runCoeffTrellis BestScore %d, exhaustive score %d", stats.BestScore, want)
	}
	for k, p := range window {
		offered := false
		for _, v := range sets[k] {
			offered = offered || got[p] == v
		}
		if !offered {
			t.Errorf("winner level %d at position %d is not offered by its choice set", got[p], p)
		}
	}
}

// trellisSkewedProbs returns a deliberately skewed probability table:
// deterministic, far from the defaults, and pushed toward certainty so
// any table-plumbing mistake shows up as an exact-cost mismatch.
func trellisSkewedProbs() *token.Probs {
	p := new(token.Probs)
	for i := range p {
		for j := range p[i] {
			for k := range p[i][j] {
				for l := range p[i][j][k] {
					if (i+j+k+l)%2 == 0 {
						p[i][j][k][l] = 255
					} else {
						p[i][j][k][l] = 1
					}
				}
			}
		}
	}
	return p
}

// TestCoeffTrellisDefaultWrappersMatchExplicit proves each default
// wrapper equals its explicit-default WithProbs variant exactly.
func TestCoeffTrellisDefaultWrappersMatchExplicit(t *testing.T) {
	blocks := [][16]int16{
		trellisLevels(),
		trellisLevels(2, 1),
		trellisLevels(15, -1),
		trellisLevels(0, -1, 14, 1),
		trellisLevels(1, 4, 3, -5, 6, 10, 8, -11),
	}
	for _, levels := range blocks {
		for plane := 0; plane < token.NumPlanes; plane++ {
			for ctx := 0; ctx <= 2; ctx++ {
				got := trellisPathRate(plane, ctx, 0, &levels)
				want := trellisPathRateWithProbs(plane, ctx, 0, &levels, &token.DefaultProbs)
				if got != want {
					t.Errorf("path: plane %d ctx %d: wrapper=%d WithProbs(default)=%d",
						plane, ctx, got, want)
				}

				// Tail: positions after lastPos hold at least one nonzero.
				tailLevels := trellisLevels(0, -1, 9, token.MaxLevel, 14, -2)
				gotTail := trellisTailRate(plane, 8, ctx, &tailLevels)
				wantTail := trellisTailRateWithProbs(plane, 8, ctx, &tailLevels, &token.DefaultProbs)
				if gotTail != wantTail {
					t.Errorf("tail: plane %d ctx %d: wrapper=%d WithProbs(default)=%d",
						plane, ctx, gotTail, wantTail)
				}
			}
		}
	}
}

// TestCoeffTrellisTailOfDefaultWrapperMatchesExplicit proves the
// trellisTailOf default wrapper equals trellisTailOfWithProbs priced
// with &token.DefaultProbs exactly -- hasNZ verdicts and all three
// exiting-context rates -- for empty and non-empty tails, and that a
// skewed table actually changes the price so the plumbing is live.
func TestCoeffTrellisTailOfDefaultWrapperMatchesExplicit(t *testing.T) {
	cases := []struct {
		name   string
		last   int
		levels [16]int16
	}{
		{"empty-tail", 15, trellisLevels(0, -1)},
		{"tail-nonzero", 8, trellisLevels(0, -1, 9, token.MaxLevel, 14, -2)},
		{"tail-single-one", 13, trellisLevels(2, 1, 14, 1)},
	}
	skewed := trellisSkewedProbs()
	for _, tc := range cases {
		for plane := 0; plane < token.NumPlanes; plane++ {
			got := trellisTailOf(plane, tc.last, &tc.levels)
			want := trellisTailOfWithProbs(plane, tc.last, &tc.levels, &token.DefaultProbs)
			if got != want {
				t.Errorf("%s: plane %d: tail=%+v WithProbs(default)=%+v",
					tc.name, plane, got, want)
			}
			skew := trellisTailOfWithProbs(plane, tc.last, &tc.levels, skewed)
			if skew.hasNZ && skew == want {
				t.Errorf("%s: plane %d: skewed table priced identically to defaults", tc.name, plane)
			}
		}
	}
}

// TestCoeffTrellisPathRateWithProbsParity proves trellisPathRateWithProbs
// under a deliberately skewed table equals cost.BlockCost priced with
// that same table, on representative codable blocks.
func TestCoeffTrellisPathRateWithProbsParity(t *testing.T) {
	skewed := trellisSkewedProbs()
	cases := []struct {
		name   string
		levels [16]int16
	}{
		{"empty", trellisLevels()},
		{"early-eob-single-one", trellisLevels(2, 1)},
		{"final-eob-last-position", trellisLevels(15, -1)},
		{"interior-zero-run", trellisLevels(0, -1, 14, 1)},
		{"small-magnitudes-mixed-signs", trellisLevels(0, 1, 1, -1, 4, 2, 7, -2)},
		{"category-boundaries", trellisLevels(1, 4, 3, -5, 6, 10, 8, -11)},
		{"large-category-boundaries", trellisLevels(2, 34, 5, -35, 9, token.MaxLevel)},
	}
	for _, tc := range cases {
		for plane := 0; plane < token.NumPlanes; plane++ {
			for ctx := 0; ctx <= 2; ctx++ {
				got := trellisPathRateWithProbs(plane, ctx, 0, &tc.levels, skewed)
				want := cost.BlockCost(plane, ctx, 0, &tc.levels, skewed)
				if got != want {
					t.Errorf("%s: plane %d ctx %d: trellisPathRateWithProbs=%d cost.BlockCost(skewed)=%d",
						tc.name, plane, ctx, got, want)
				}
			}
		}
	}
}

// TestCoeffTrellisRunDefaultWrapperMatchesExplicit proves the default
// run wrapper equals the explicit-default WithProbs run exactly:
// winner levels and every stats counter and flag, over reduced-window,
// whole-scan, and baseline-bearing configurations.
func TestCoeffTrellisRunDefaultWrapperMatchesExplicit(t *testing.T) {
	const (
		plane  = token.YWithDC
		ctx    = 1
		lambda = 300
	)
	dist := func(levels *[16]int16) int64 {
		var s int64
		for _, v := range *levels {
			s += int64(v) * int64(v)
		}
		return s
	}
	cases := []struct {
		name      string
		cfg       trellisConfig
		retained  [16]int16
		baselines int
	}{
		{"whole-scan", trellisWholeScan,
			trellisLevels(1, -2, 4, 3, 7, -5, 13, 2), 0},
		{"whole-scan-with-baselines", trellisWholeScan,
			trellisLevels(1, -2, 4, 3, 7, -5, 13, 2), 2},
		{"reduced-window", trellisConfig{firstPos: 2, lastPos: 5},
			trellisLevels(2, 3, 3, -2, 4, 1, 5, 4), 1},
		{"reduced-window-empty-tail", trellisConfig{firstPos: 4, lastPos: 15},
			trellisLevels(4, -9, 8, 12, 14, -34), 0},
	}
	for _, tc := range cases {
		retained := tc.retained
		baselines := [][16]int16{retained}
		for i := 1; i < tc.baselines; i++ {
			baselines = append(baselines, trellisLevels(0, i))
		}
		gotWrapper, statsWrapper := runCoeffTrellis(tc.cfg, plane, ctx,
			&retained, lambda, dist, baselines...)
		gotExplicit, statsExplicit := runCoeffTrellisWithProbs(tc.cfg, plane, ctx,
			&retained, lambda, dist, &token.DefaultProbs, baselines...)
		if gotWrapper != gotExplicit {
			t.Errorf("%s: wrapper winner %v != explicit-default winner %v",
				tc.name, gotWrapper, gotExplicit)
		}
		if statsWrapper != statsExplicit {
			t.Errorf("%s: wrapper stats %+v != explicit-default stats %+v",
				tc.name, statsWrapper, statsExplicit)
		}
	}
}

// TestCoeffTrellisRunWithSkewedTableDeterministic proves a skewed-table
// run is deterministic -- repeated runs return identical winners and
// identical stats -- and that the skewed table is actually live by
// producing a different objective from the default run.
func TestCoeffTrellisRunWithSkewedTableDeterministic(t *testing.T) {
	const (
		plane  = token.YWithDC
		ctx    = 1
		lambda = 300
	)
	dist := func(levels *[16]int16) int64 {
		var s int64
		for _, v := range *levels {
			s += int64(v) * int64(v)
		}
		return s
	}
	cfg := trellisWholeScan
	retained := trellisLevels(0, -1, 3, 6, 6, -11, 10, 4, 14, 21)
	baselines := [][16]int16{retained}

	gotA, statsA := runCoeffTrellisWithProbs(cfg, plane, ctx, &retained,
		lambda, dist, trellisSkewedProbs(), baselines...)
	gotB, statsB := runCoeffTrellisWithProbs(cfg, plane, ctx, &retained,
		lambda, dist, trellisSkewedProbs(), baselines...)
	if gotA != gotB || statsA != statsB {
		t.Errorf("skewed runs diverged: %v/%+v vs %v/%+v",
			gotA, statsA, gotB, statsB)
	}

	gotDefault, _ := runCoeffTrellis(cfg, plane, ctx, &retained,
		lambda, dist, baselines...)
	skewedScore := int64(dist(&gotA))*256 +
		lambda*int64(cost.BlockCost(plane, ctx, cfg.firstPos, &gotA, trellisSkewedProbs()))
	defaultScore := int64(dist(&gotDefault))*256 +
		lambda*int64(cost.BlockCost(plane, ctx, cfg.firstPos, &gotDefault, &token.DefaultProbs))
	if skewedScore == defaultScore {
		t.Errorf("skewed-table objective %d equals default objective %d; plumbing appears inert",
			skewedScore, defaultScore)
	}
}

// TestCoeffTrellisCustomTableFinalSelection proves the final selection
// under a custom probability table matches an independently computed
// objective -- crafted additive distortion times 256 plus lambda times
// cost.BlockCost priced with that same table -- over an exhaustive
// enumeration of the documented per-position choice sets in a reduced
// window, and that strict-earliest tie-breaking retains baselines[0]
// when every candidate scores identically under that table.
func TestCoeffTrellisCustomTableFinalSelection(t *testing.T) {
	const (
		plane  = token.YWithDC
		ctx    = 1
		lambda = 300
		first  = 2
		last   = 5
	)

	custom := new(token.Probs)
	for i := range custom {
		for j := range custom[i] {
			for k := range custom[i][j] {
				for l := range custom[i][j][k] {
					custom[i][j][k][l] = uint8((i*31 + j*7 + k*3 + l*13) % 256)
				}
			}
		}
	}

	retained := trellisLevels(2, 3, 3, -2, 4, 1, 5, 4)

	// Separable quadratic distortion keeps the marginal probes exact.
	var weight, target [16]int64
	weight[2], target[2] = 3000, 3
	weight[3], target[3] = 2000, -1
	weight[4], target[4] = 2500, 1
	weight[5], target[5] = 400, 3
	dist := func(levels *[16]int16) int64 {
		var s int64
		for i := 0; i < 16; i++ {
			d := int64(levels[i]) - target[i]
			s += weight[i] * d * d
		}
		return s
	}
	score := func(levels *[16]int16) int64 {
		return dist(levels)*256 +
			lambda*int64(cost.BlockCost(plane, ctx, first, levels, custom))
	}

	// Independent rebuild of the documented choice-set rule.
	window := []int{first, 3, 4, last}
	sets := make([][]int16, len(window))
	for k, p := range window {
		r := retained[p]
		var cs []int16
		add := func(x int16) {
			for _, v := range cs {
				if v == x {
					return
				}
			}
			cs = append(cs, x)
		}
		add(r)
		if r > 0 {
			add(r - 1)
		} else if r < 0 {
			add(r + 1)
		}
		add(0)
		sets[k] = cs
	}

	// Exhaustive odometer enumeration under the custom-table objective;
	// strictly-smaller replacement preserves enumeration-order ties.
	idx := make([]int, len(window))
	firstCombo := true
	var oracleBest [16]int16
	var oracleScore int64
	for {
		cand := retained
		for k, p := range window {
			cand[p] = sets[k][idx[k]]
		}
		if s := score(&cand); firstCombo || s < oracleScore {
			oracleBest, oracleScore = cand, s
			firstCombo = false
		}
		k := len(window) - 1
		for k >= 0 {
			idx[k]++
			if idx[k] < len(sets[k]) {
				break
			}
			idx[k] = 0
			k--
		}
		if k < 0 {
			break
		}
	}
	if oracleBest == retained {
		t.Fatalf("custom-table exhaustive winner %v equals retained; case proves nothing", oracleBest)
	}

	got, stats := runCoeffTrellisWithProbs(trellisConfig{firstPos: first, lastPos: last},
		plane, ctx, &retained, lambda, dist, custom)
	if got != oracleBest {
		t.Errorf("custom-table winner %v, exhaustive winner %v", got, oracleBest)
	}
	if want := oracleScore; stats.BestScore != want {
		t.Errorf("custom-table BestScore %d, independently computed objective %d", stats.BestScore, want)
	}
	if !stats.TrellisWon {
		t.Errorf("TrellisWon=false under the custom table")
	}

	// Strict-earliest ties under the same custom table: zero lambda and
	// a constant distortion score every candidate identically, so the
	// strictly-smaller replacement rule must retain baselines[0].
	constantDist := func(*[16]int16) int64 { return 42 }
	baselineOther := trellisLevels(0, 4, 3, -3)
	tieRetained := retained
	tieGot, tieStats := runCoeffTrellisWithProbs(trellisWholeScan, plane, ctx,
		&tieRetained, 0, constantDist, custom, tieRetained, baselineOther)
	if tieGot != tieRetained {
		t.Errorf("exact tie: returned %v, want earlier baseline %v", tieGot, tieRetained)
	}
	if tieStats.TrellisWon || tieStats.Improved {
		t.Errorf("exact tie: TrellisWon=%v Improved=%v, want both false",
			tieStats.TrellisWon, tieStats.Improved)
	}
	if want := int64(cost.Unit * 42); tieStats.BestScore != want {
		t.Errorf("exact tie: BestScore=%d, want %d", tieStats.BestScore, want)
	}
	if tieStats.ExactScorings != 3 {
		t.Errorf("exact tie: ExactScorings=%d, want 3", tieStats.ExactScorings)
	}
}
