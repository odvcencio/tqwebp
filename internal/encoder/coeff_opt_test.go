package encoder

// Tests for the Slice 5A coefficient-search foundation. Every check
// compares the production helper against an independently written
// reference enumerator and scorer, or against hand-computed literal
// golden cases, so a shared mistake cannot hide.

import (
	"fmt"
	"runtime"
	"testing"

	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/token"
)

// referenceCoeffCandidates is the test's own enumerator, written
// separately from enumerateCoeffCandidates: it deduplicates through a
// map and emits every family of candidates from freshly copied bases.
// It must agree with the production list element for element,
// including order.
func referenceCoeffCandidates(initial [16]int16) [][16]int16 {
	seen := make(map[[16]int16]bool)
	out := make([][16]int16, 0, 64)
	emit := func(v [16]int16) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}

	emit(initial)
	for i := 0; i < 16; i++ {
		lvl := initial[i]
		if lvl == 0 {
			continue
		}
		toward := initial
		if lvl > 0 {
			toward[i] = lvl - 1
		} else {
			toward[i] = lvl + 1
		}
		emit(toward)

		zeroed := initial
		zeroed[i] = 0
		emit(zeroed)
	}
	for cut := 0; cut <= 15; cut++ {
		suffix := initial
		for j := cut; j < 16; j++ {
			suffix[j] = 0
		}
		emit(suffix)
	}
	return out
}

// referenceBest picks the winner over a caller-built candidate list
// with the same strict-smaller rule, scoring straight through
// cost.BlockCost, so it exercises the production search's arithmetic
// from the outside.
func referenceBest(plane, ctx, first int, cands [][16]int16, lambda int64, distortion func(*[16]int16) int64) ([16]int16, int, int64) {
	rate := func(v *[16]int16) int64 {
		return int64(cost.BlockCost(plane, ctx, first, v, &token.DefaultProbs))
	}
	best := cands[0]
	ord := 0
	score := distortion(&best)*256 + lambda*rate(&best)
	for i := 1; i < len(cands); i++ {
		s := distortion(&cands[i])*256 + lambda*rate(&cands[i])
		if s < score {
			best, ord, score = cands[i], i, s
		}
	}
	return best, ord, score
}

// sseAgainst returns the squared distance to a fixed target block, the
// injected spatial distortion these tests use most often.
func sseAgainst(target [16]int16) func(*[16]int16) int64 {
	return func(v *[16]int16) int64 {
		var s int64
		for i := range v {
			d := int64(v[i]) - int64(target[i])
			s += d * d
		}
		return s
	}
}

// zeroDistortion ignores the candidate entirely.
func zeroDistortion(*[16]int16) int64 { return 0 }

// negL1 rewards magnitude, the opposite pull of a real distortion.
func negL1(v *[16]int16) int64 {
	var s int64
	for i := range v {
		if v[i] < 0 {
			s -= int64(-v[i])
		} else {
			s -= int64(v[i])
		}
	}
	return s
}

// testVec builds a level vector from a prefix, zero-padded to 16.
func testVec(vals ...int16) [16]int16 {
	var v [16]int16
	copy(v[:], vals)
	return v
}

// vecsEqual reports element-wise equality of two candidate lists.
func vecsEqual(a, b [][16]int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// vecKey renders a vector as a stable map key.
func vecKey(v [16]int16) string { return fmt.Sprintf("%v", v) }

// nonZeroCount counts nonzero scan positions.
func nonZeroCount(v *[16]int16) int {
	n := 0
	for i := range v {
		if v[i] != 0 {
			n++
		}
	}
	return n
}

// smallVectorSpace enumerates every vector whose first four scan
// positions range over {-1,0,1,2} and whose remaining positions are
// zero, plus literal extras that place nonzeros deep in the tail.
func smallVectorSpace() [][16]int16 {
	space := make([][16]int16, 0, 300)
	for a := -1; a <= 2; a++ {
		for b := -1; b <= 2; b++ {
			for c := -1; c <= 2; c++ {
				for d := -1; d <= 2; d++ {
					space = append(space, testVec(int16(a), int16(b), int16(c), int16(d)))
				}
			}
		}
	}
	space = append(space,
		testVec(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 9),
		testVec(5, 0, 0, 0, 0, 0, 0, 0, 7),
		testVec(1, 0, 2, 0, 4, 0, 6, 0, 8, 0, 10, 0, 12, 0, 14, 0),
		testVec(0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1),
		testVec(2, -2, 2, -2, 2, -2, 2, -2, 2, -2, 2, -2, 2, -2, 2, -2),
		testVec(-1, -2, -3),
		testVec(1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1),
		testVec(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16),
	)
	return space
}

// gridInitials are the vectors the argument-grid tests sweep.
var gridInitials = [][16]int16{
	testVec(9, 8, 7),
	testVec(1, 1, 1, 1),
	testVec(0, 5, 0, 3, 0, 0, 0, 12),
	testVec(2, -2, 2, -2),
	testVec(4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 6),
	testVec(1, 2, 3, 4, 5, 6, 7, 8),
	testVec(),
}

// gridLambdas spans the rate weight from free to dominant.
var gridLambdas = []int64{0, 13, 137, 1361, 13613}

// gridDistortions covers no distortion, pull toward a halved target,
// and magnitude reward.
func gridDistortions(initial [16]int16) []struct {
	name string
	fn   func(*[16]int16) int64
} {
	half := initial
	for i := range half {
		half[i] /= 2
	}
	return []struct {
		name string
		fn   func(*[16]int16) int64
	}{
		{"zero", zeroDistortion},
		{"sse-half-target", sseAgainst(half)},
		{"neg-l1", negL1},
	}
}

func TestEnumerateGoldenCases(t *testing.T) {
	cases := []struct {
		name     string
		initial  [16]int16
		want     [][16]int16 // nil skips the exact-list check
		wantLen  int         // expected unique count
		wantEnum int         // expected offers before dedup
	}{
		{
			name:     "all zero keeps only the retain",
			initial:  testVec(),
			want:     [][16]int16{testVec()},
			wantLen:  1,
			wantEnum: 17, // 1 retain + 0 mutations + 16 no-op cuts
		},
		{
			name:     "single positive collapses to three",
			initial:  testVec(3),
			want:     [][16]int16{testVec(3), testVec(2), testVec()},
			wantLen:  3,
			wantEnum: 19, // 1 + 2 + 16 offers; every cut repeats an earlier candidate
		},
		{
			name:     "single negative steps toward zero upward",
			initial:  testVec(-3),
			want:     [][16]int16{testVec(-3), testVec(-2), testVec()},
			wantLen:  3,
			wantEnum: 19,
		},
		{
			name:    "two leading nonzeros order mutations before ascending cuts",
			initial: testVec(2, 3),
			want: [][16]int16{
				testVec(2, 3), // retain
				testVec(1, 3), // pos0 toward zero
				testVec(0, 3), // pos0 zeroed
				testVec(2, 2), // pos1 toward zero
				testVec(2, 0), // pos1 zeroed
				testVec(),     // cut 0 clears every coefficient: the all-zero candidate at ordinal 5
			},
			wantLen:  6,
			wantEnum: 21, // 1 + 4 + 16 offers; cut 0 supplies the all-zero candidate
		},
		{
			name:     "tail-only nonzero absorbs every cut",
			initial:  testVec(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 9),
			want:     [][16]int16{testVec(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 9), testVec(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 8), testVec()},
			wantLen:  3,
			wantEnum: 19,
		},
		{
			name:     "sixteen magnitudes of two or more reach forty-eight",
			initial:  testVec(2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17),
			wantLen:  48,
			wantEnum: 49,
		},
		{
			name:     "a unit magnitude merges one mutation pair",
			initial:  testVec(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16),
			wantLen:  47,
			wantEnum: 49,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, enumerated := enumerateCoeffCandidates(&tc.initial)
			if enumerated != tc.wantEnum {
				t.Errorf("enumerated offers = %d, want %d", enumerated, tc.wantEnum)
			}
			if len(got) != tc.wantLen {
				t.Errorf("unique candidates = %d, want %d", len(got), tc.wantLen)
			}
			if tc.want != nil && !vecsEqual(got, tc.want) {
				t.Errorf("candidate list mismatch\n got %v\nwant %v", got, tc.want)
			}
			if len(got) > maxCoeffCandidates {
				t.Errorf("unique candidates %d exceed bound %d", len(got), maxCoeffCandidates)
			}
		})
	}
}

func TestEnumerateMatchesIndependentReference(t *testing.T) {
	for _, initial := range smallVectorSpace() {
		got, enumerated := enumerateCoeffCandidates(&initial)
		want := referenceCoeffCandidates(initial)

		if !vecsEqual(got, want) {
			t.Fatalf("candidate list mismatch for %v\n got %v\nwant %v", initial, got, want)
		}
		if wantEnum := 1 + 2*nonZeroCount(&initial) + 16; enumerated != wantEnum {
			t.Errorf("%v: enumerated offers = %d, want %d", initial, enumerated, wantEnum)
		}
		if len(got) > maxCoeffCandidates {
			t.Errorf("%v: %d unique candidates exceed bound %d", initial, len(got), maxCoeffCandidates)
		}
		// Dedup must preserve first appearance: every entry differs
		// from every earlier entry.
		for i := 1; i < len(got); i++ {
			for j := 0; j < i; j++ {
				if got[i] == got[j] {
					t.Fatalf("%v: candidate %d duplicates candidate %d", initial, i, j)
				}
			}
		}
	}
}

func TestSearchMatchesReferenceAcrossArgumentGrid(t *testing.T) {
	// Winners observed per argument value, for the sensitivity
	// assertions below.
	type axisHits map[int]map[string]bool // axis value -> winner key -> seen
	planeHits := axisHits{}
	ctxHits := axisHits{}
	firstHits := axisHits{}
	note := func(hits axisHits, value int, key string) {
		if hits[value] == nil {
			hits[value] = map[string]bool{}
		}
		hits[value][key] = true
	}

	for _, initial := range gridInitials {
		refCands := referenceCoeffCandidates(initial)
		for _, dist := range gridDistortions(initial) {
			for _, lambda := range gridLambdas {
				for plane := 0; plane < token.NumPlanes; plane++ {
					for ctx := 0; ctx <= 2; ctx++ {
						for first := 0; first <= 2; first++ {
							wantLevels, wantOrd, wantScore := referenceBest(plane, ctx, first, refCands, lambda, dist.fn)
							gotLevels, gotStats := searchCoeffCandidates(plane, ctx, first, &initial, lambda, dist.fn)

							if gotLevels != wantLevels {
								t.Fatalf("plane=%d ctx=%d first=%d lambda=%d dist=%s init=%v: winner %v, want %v",
									plane, ctx, first, lambda, dist.name, initial, gotLevels, wantLevels)
							}
							if gotStats.WinningOrdinal != wantOrd {
								t.Errorf("plane=%d ctx=%d first=%d lambda=%d dist=%s init=%v: ordinal %d, want %d",
									plane, ctx, first, lambda, dist.name, initial, gotStats.WinningOrdinal, wantOrd)
							}
							if gotStats.BestScore != wantScore {
								t.Errorf("plane=%d ctx=%d first=%d lambda=%d dist=%s init=%v: score %d, want %d",
									plane, ctx, first, lambda, dist.name, initial, gotStats.BestScore, wantScore)
							}
							if gotStats.CandidatesUnique != len(refCands) || gotStats.CandidatesScored != len(refCands) {
								t.Errorf("plane=%d ctx=%d first=%d init=%v: unique/scored %d/%d, want %d/%d",
									plane, ctx, first, initial, gotStats.CandidatesUnique, gotStats.CandidatesScored, len(refCands), len(refCands))
							}
							if gotStats.Improved != (gotLevels != initial) {
								t.Errorf("Improved=%v disagrees with winner change for init %v", gotStats.Improved, initial)
							}

							key := vecKey(wantLevels)
							note(planeHits, plane, key)
							note(ctxHits, ctx, key)
							note(firstHits, first, key)
						}
					}
				}
			}
		}
	}

	// Sensitivity: each forwarded argument must actually move the
	// outcome somewhere in the swept space; a constant winner set
	// would mean the argument is being ignored.
	for _, ax := range []struct {
		name string
		hits axisHits
	}{
		{"plane", planeHits},
		{"context", ctxHits},
		{"first", firstHits},
	} {
		distinct := map[string]bool{}
		for _, keys := range ax.hits {
			for k := range keys {
				distinct[k] = true
			}
		}
		if len(distinct) < 2 {
			t.Errorf("axis %q produced %d distinct winners across its values; the argument looks unforwarded", ax.name, len(distinct))
		}
	}
}

func TestTieKeepsEarliestCandidate(t *testing.T) {
	// initial [1] enumerates exactly two candidates: [1] at ordinal
	// 0 and [0] at ordinal 1 (every suffix cut repeats one of
	// them). With first=1 the writer never sees position 0, so both
	// candidates price identically -- a genuine tie -- and the
	// strictly-smaller comparison must keep the retain at ordinal
	// 0.
	initial := testVec(1)
	levels, stats := searchCoeffCandidates(token.YAfterY2, 1, 1, &initial, 37, zeroDistortion)
	if levels != initial || stats.WinningOrdinal != 0 {
		t.Errorf("first=1 tie: winner %v ordinal %d, want retain %v ordinal 0", levels, stats.WinningOrdinal, initial)
	}

	// With first=0 the tie dissolves: zeroing position 0 strictly
	// saves its token, so the zeroed candidate at ordinal 1 wins.
	levels, stats = searchCoeffCandidates(token.YAfterY2, 1, 0, &initial, 37, zeroDistortion)
	if want := testVec(); levels != want || stats.WinningOrdinal != 1 {
		t.Errorf("first=0: winner %v ordinal %d, want %v ordinal 1", levels, stats.WinningOrdinal, want)
	}

	// A completely flat score surface -- lambda 0, distortion 0 --
	// makes every candidate tie, and the retain at ordinal 0 must
	// win.
	full := testVec(5, -3, 2)
	levels, stats = searchCoeffCandidates(token.UV, 2, 0, &full, 0, zeroDistortion)
	if levels != full || stats.WinningOrdinal != 0 || stats.Improved {
		t.Errorf("flat scores: winner %v ordinal %d improved %v, want retain ordinal 0", levels, stats.WinningOrdinal, stats.Improved)
	}
}

func TestDistortionControlsChoice(t *testing.T) {
	initial := testVec(9, 8, 7)
	half := testVec(4, 4, 3)

	// With no rate weight the injected distortion alone decides:
	// the candidate nearest the halved target wins, and it is not
	// the retain.
	levels, stats := searchCoeffCandidates(token.YAfterY2, 1, 0, &initial, 0, sseAgainst(half))
	refCands := referenceCoeffCandidates(initial)
	wantLevels, wantOrd, _ := referenceBest(token.YAfterY2, 1, 0, refCands, 0, sseAgainst(half))
	if levels != wantLevels || stats.WinningOrdinal != wantOrd || wantLevels == initial {
		t.Errorf("lambda=0: winner %v ordinal %d, want pure-distortion winner %v ordinal %d (must differ from retain)",
			levels, stats.WinningOrdinal, wantLevels, wantOrd)
	}

	// Rewarding magnitude instead pulls the winner back to the
	// retain: only it carries full magnitude.
	levels, stats = searchCoeffCandidates(token.YAfterY2, 1, 0, &initial, 0, negL1)
	if levels != initial || stats.WinningOrdinal != 0 {
		t.Errorf("neg-l1: winner %v ordinal %d, want retain ordinal 0", levels, stats.WinningOrdinal)
	}

	// A distortion that penalizes any deviation from the input by
	// more than any rate saving could recover pins the retain in
	// place even at a large lambda.
	pinning := func(v *[16]int16) int64 {
		if *v == initial {
			return 0
		}
		return 1 << 40
	}
	levels, stats = searchCoeffCandidates(token.YAfterY2, 1, 0, &initial, 13613, pinning)
	if levels != initial || stats.WinningOrdinal != 0 {
		t.Errorf("pinning distortion: winner %v ordinal %d, want retain ordinal 0", levels, stats.WinningOrdinal)
	}

	// Zero distortion plus positive lambda makes the pure-rate
	// winner take over; the independent scorer names the same
	// candidate.
	levels, stats = searchCoeffCandidates(token.YAfterY2, 1, 0, &initial, 137, zeroDistortion)
	wantLevels, wantOrd, _ = referenceBest(token.YAfterY2, 1, 0, refCands, 137, zeroDistortion)
	if levels != wantLevels || stats.WinningOrdinal != wantOrd {
		t.Errorf("zero distortion: winner %v ordinal %d, want pure-rate winner %v ordinal %d",
			levels, stats.WinningOrdinal, wantLevels, wantOrd)
	}

	// Adding a constant to every distortion value shifts every
	// score equally: the winner stays and the reported score moves
	// by exactly the constant times 256.
	baseLevels, baseStats := searchCoeffCandidates(token.Y2, 0, 1, &initial, 137, sseAgainst(half))
	shifted := func(v *[16]int16) int64 { return sseAgainst(half)(v) + 1337 }
	gotLevels, gotStats := searchCoeffCandidates(token.Y2, 0, 1, &initial, 137, shifted)
	if gotLevels != baseLevels {
		t.Errorf("constant distortion shift changed the winner: %v vs %v", gotLevels, baseLevels)
	}
	if want := baseStats.BestScore + 1337*256; gotStats.BestScore != want {
		t.Errorf("shifted score %d, want %d", gotStats.BestScore, want)
	}

	// Each unique candidate is scored exactly once: the counting
	// wrapper sees precisely CandidatesScored calls.
	calls := 0
	counting := func(v *[16]int16) int64 {
		calls++
		return sseAgainst(half)(v)
	}
	_, stats = searchCoeffCandidates(token.UV, 2, 0, &initial, 137, counting)
	if calls != stats.CandidatesScored {
		t.Errorf("distortion called %d times, want %d", calls, stats.CandidatesScored)
	}
}

func TestSearchStatsAndBounds(t *testing.T) {
	for _, initial := range smallVectorSpace() {
		_, stats := searchCoeffCandidates(token.YAfterY2, 1, 0, &initial, 37, sseAgainst(testVec(1, 1)))

		if stats.CandidatesEnumerated != 1+2*nonZeroCount(&initial)+16 {
			t.Errorf("%v: enumerated %d, want %d", initial, stats.CandidatesEnumerated, 1+2*nonZeroCount(&initial)+16)
		}
		if stats.CandidatesUnique > maxCoeffCandidates {
			t.Errorf("%v: %d unique candidates exceed bound %d", initial, stats.CandidatesUnique, maxCoeffCandidates)
		}
		if stats.CandidatesUnique > stats.CandidatesEnumerated {
			t.Errorf("%v: unique %d exceeds enumerated %d", initial, stats.CandidatesUnique, stats.CandidatesEnumerated)
		}
		if stats.CandidatesScored != stats.CandidatesUnique {
			t.Errorf("%v: scored %d != unique %d", initial, stats.CandidatesScored, stats.CandidatesUnique)
		}
		if stats.WinningOrdinal < 0 || stats.WinningOrdinal >= stats.CandidatesUnique {
			t.Errorf("%v: ordinal %d outside [0,%d)", initial, stats.WinningOrdinal, stats.CandidatesUnique)
		}
	}

	// The densest legal shape -- sixteen magnitudes of two or more
	// -- hits the proven maximum of 48 unique candidates from 49
	// offers: the cut at the last nonzero position always
	// duplicates that position's zeroing mutation, and 48 < 49.
	full := testVec(2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17)
	_, stats := searchCoeffCandidates(token.YAfterY2, 0, 0, &full, 37, zeroDistortion)
	if stats.CandidatesEnumerated != 49 || stats.CandidatesUnique != 48 {
		t.Errorf("dense vector: enumerated/unique %d/%d, want 49/48", stats.CandidatesEnumerated, stats.CandidatesUnique)
	}
}

func TestSearchIsDeterministic(t *testing.T) {
	initial := testVec(3, -1, 4, 1, 5, 0, 0, 2)
	run := func() ([16]int16, coeffSearchStats) {
		return searchCoeffCandidates(token.YWithDC, 2, 0, &initial, 421, sseAgainst(testVec(3, 0, 4, 0, 5)))
	}

	wantLevels, wantStats := run()
	for i := 0; i < 24; i++ {
		gotLevels, gotStats := run()
		if gotLevels != wantLevels || gotStats != wantStats {
			t.Fatalf("run %d diverged: (%v, %+v) vs (%v, %+v)", i, gotLevels, gotStats, wantLevels, wantStats)
		}
	}

	// Parallelism must not matter: nothing in the helper shares
	// state.
	for _, procs := range []int{1, 3, 8} {
		old := runtime.GOMAXPROCS(procs)
		gotLevels, gotStats := run()
		runtime.GOMAXPROCS(old)
		if gotLevels != wantLevels || gotStats != wantStats {
			t.Errorf("GOMAXPROCS=%d diverged: (%v, %+v)", procs, gotLevels, gotStats)
		}
	}
}

func TestCostParityWithWriterPricing(t *testing.T) {
	// The rate term must be exactly what the frame would pay under
	// the default probability table: recompute one known candidate
	// end to end and compare against the reported best score.
	initial := testVec(6, 0, 2, 0, 0, 0, 1)
	const lambda = 91
	levels, stats := searchCoeffCandidates(token.YAfterY2, 2, 1, &initial, lambda, sseAgainst(testVec(5, 0, 2, 0, 0, 0, 1)))

	var rate cost.Cost
	rate = cost.BlockCost(token.YAfterY2, 2, 1, &levels, &token.DefaultProbs)
	dist := sseAgainst(testVec(5, 0, 2, 0, 0, 0, 1))(&levels)
	if want := int64(dist)*256 + lambda*int64(rate); stats.BestScore != want {
		t.Errorf("best score %d, want distortion %d *256 + lambda*rate %d = %d", stats.BestScore, dist, int64(rate), want)
	}
}

// flippedProbs returns a copy of token.DefaultProbs with every
// probability mirrored around 128 (256-p, clamped into 1..255), a
// deterministic custom table that prices blocks differently from the
// default one everywhere.
func flippedProbs() token.Probs {
	var out token.Probs
	for p := range out {
		for b := range out[p] {
			for c := range out[p][b] {
				for n := range out[p][b][c] {
					out[p][b][c][n] = uint8(256 - int(token.DefaultProbs[p][b][c][n]))
				}
			}
		}
	}
	return out
}

func TestScoreCoeffCandidateWithProbsMatchesBlockCostOracle(t *testing.T) {
	custom := flippedProbs()
	if custom == token.DefaultProbs {
		t.Fatal("custom table equals the default table; test would be vacuous")
	}

	const plane, ctx, first = token.UV, 1, 3
	const lambda = 73
	target := testVec(2, -3, 0, 5, 0, 0, 1)
	distortion := sseAgainst(target)

	for _, v := range [][16]int16{
		testVec(2, -3, 0, 5),
		testVec(2, -3),
		testVec(0, 0, 0, 0, 0, 0, 0, 7),
		testVec(1),
	} {
		got := scoreCoeffCandidateWithProbs(plane, ctx, first, &custom, &v, lambda, distortion)
		rate := int64(cost.BlockCost(plane, ctx, first, &v, &custom))
		want := distortion(&v)*256 + lambda*rate
		if got != want {
			t.Errorf("levels %v: with-probs score %d, want BlockCost-oracle %d", v, got, want)
		}
	}
}

func TestSearchCoeffCandidatesWithProbsMatchesBlockCostOracle(t *testing.T) {
	custom := flippedProbs()
	const plane, ctx, first = token.YAfterY2, 2, 0
	const lambda = 57

	for _, initial := range smallVectorSpace() {
		winner, stats := searchCoeffCandidatesWithProbs(plane, ctx, first, &custom, &initial, lambda, sseAgainst(testVec(1, 2)))

		// The reported best score must price the winner exactly as
		// cost.BlockCost does under the supplied table.
		rate := int64(cost.BlockCost(plane, ctx, first, &winner, &custom))
		want := sseAgainst(testVec(1, 2))(&winner)*256 + lambda*rate
		if stats.BestScore != want {
			t.Errorf("%v: best score %d, want oracle %d", initial, stats.BestScore, want)
		}

		// The winner must beat -- strictly, on ties keeping the
		// earliest -- every independently enumerated candidate
		// scored through the same supplied table.
		cands := referenceCoeffCandidates(initial)
		bestOrd, bestScore := 0, int64(0)
		for i, c := range cands {
			s := sseAgainst(testVec(1, 2))(&c)*256 + lambda*int64(cost.BlockCost(plane, ctx, first, &c, &custom))
			if i == 0 || s < bestScore {
				bestOrd, bestScore = i, s
			}
		}
		if winner != cands[bestOrd] {
			t.Errorf("%v: winner %v, want oracle winner %v (ordinal %d)", initial, winner, cands[bestOrd], bestOrd)
		}
		if stats.WinningOrdinal != bestOrd {
			t.Errorf("%v: winning ordinal %d, want oracle ordinal %d", initial, stats.WinningOrdinal, bestOrd)
		}
	}
}

func TestCoeffCandidateDefaultWrapperEqualsWithProbsDefault(t *testing.T) {
	initials := [][16]int16{
		testVec(4, 0, -2, 1),
		testVec(9, 9, 0, 0, 3),
		testVec(0),
		testVec(2, 2, 2, 2, 2, 2, 2, 2),
	}

	for _, initial := range initials {
		for _, cfg := range []struct {
			plane, ctx, first int
			lambda            int64
			target            [16]int16
		}{
			{token.YWithDC, 0, 0, 101, testVec(3, 0, 0, 1)},
			{token.UV, 2, 5, -19, testVec(4, 0, 2)},
			{token.Y2, 1, 1, 0, testVec(6)},
			{token.YAfterY2, 1, 0, 77, testVec(1, 1, 1)},
		} {
			wantLevels, wantStats := searchCoeffCandidates(cfg.plane, cfg.ctx, cfg.first, &initial, cfg.lambda, sseAgainst(cfg.target))
			gotLevels, gotStats := searchCoeffCandidatesWithProbs(cfg.plane, cfg.ctx, cfg.first, &token.DefaultProbs, &initial, cfg.lambda, sseAgainst(cfg.target))
			if gotLevels != wantLevels || gotStats != wantStats {
				t.Errorf("%v cfg %+v: wrapper (%v, %+v) != with-probs default (%v, %+v)",
					initial, cfg, wantLevels, wantStats, gotLevels, gotStats)
			}

			v := testVec(2, -1, 0, 4)
			wantScore := scoreCoeffCandidate(cfg.plane, cfg.ctx, cfg.first, &v, cfg.lambda, sseAgainst(cfg.target))
			gotScore := scoreCoeffCandidateWithProbs(cfg.plane, cfg.ctx, cfg.first, &token.DefaultProbs, &v, cfg.lambda, sseAgainst(cfg.target))
			if gotScore != wantScore {
				t.Errorf("score wrapper %d != with-probs default %d", wantScore, gotScore)
			}
		}
	}
}

func TestSearchCoeffCandidatesWithProbsTieKeepsRetained(t *testing.T) {
	custom := flippedProbs()
	initial := testVec(3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1)

	// Lambda zero flattens every rate difference: all candidates tie
	// at distortion-free score 0, and strict-less comparison must
	// keep the retained input rather than any later offer.
	winner, stats := searchCoeffCandidatesWithProbs(token.YWithDC, 1, 0, &custom, &initial, 0, zeroDistortion)
	if stats.BestScore != 0 {
		t.Fatalf("all-tie search reported score %d, want 0", stats.BestScore)
	}
	if stats.WinningOrdinal != 0 {
		t.Errorf("tie resolved to ordinal %d, want 0 (retained)", stats.WinningOrdinal)
	}
	if winner != initial {
		t.Errorf("tie changed levels to %v, want retained %v", winner, initial)
	}
	if stats.Improved {
		t.Error("tie reported Improved=true, want false")
	}

	// Same shape with the default wrapper: a tie among later
	// candidates must still keep the earlier one.
	winner, stats = searchCoeffCandidates(token.YAfterY2, 0, 0, &initial, 0, zeroDistortion)
	if stats.WinningOrdinal != 0 || winner != initial || stats.Improved {
		t.Errorf("default wrapper broke a tie: (%v, ordinal %d, improved %v)", winner, stats.WinningOrdinal, stats.Improved)
	}
}
