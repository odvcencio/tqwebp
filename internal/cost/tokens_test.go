package cost

import (
	"math"
	"math/rand/v2"
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/token"
)

// refTable computes the full decision-price table freshly from the
// logarithm, once per binary, so the token sweeps below can run
// exhaustively without re-evaluating transcendentals millions of times.
// Column 0 prices the false branch at probability p, column 1 the true
// branch. It is built here, in the test, independently of the package's
// own zeroCostQ8; the exhaustive equality between them is exactly
// TestBitCostExhaustiveAgainstLogReference.
func refTable() *[256][2]Cost {
	var t [256][2]Cost
	for p := 1; p <= 255; p++ {
		t[p][0] = Cost(math.Round(-math.Log2(float64(p)/256.0) * 256.0))
		t[p][1] = Cost(math.Round(-math.Log2(float64(256-p)/256.0) * 256.0))
	}
	return &t
}

// counter accumulates reference prices over a replayed decision walk.
type counter struct {
	tab   *[256][2]Cost
	total Cost
}

func (c *counter) bit(p uint8, b bool) {
	if b {
		c.total += c.tab[p][1]
		return
	}
	c.total += c.tab[p][0]
}

// refBlockCost walks one coefficient block exactly as RFC 6386 chapter
// 13's tree prescribes -- end-of-block flag, nonzero flags with context
// updates, the magnitude categories, their extra bits, the sign flag --
// pricing every decision against the fresh reference table. This is a
// deliberate transcription of the normative tree, kept beside the
// production walker so the two must agree number for number; the
// category constants below are written out literally rather than read
// from internal/token's exports, so a typo in either copy shows up as a
// mismatch here.
func refBlockCost(tab *[256][2]Cost, plane, ctx, first int, levels *[16]int16, probs *token.Probs) Cost {
	const cat1, cat2a, cat2b = 159, 165, 145
	rc := &counter{tab: tab}
	pp := &probs[plane]

	last := -1
	for i := 15; i >= first; i-- {
		if levels[i] != 0 {
			last = i
			break
		}
	}

	n := first
	p := &pp[token.Bands[n]][ctx]
	if last < 0 {
		rc.bit(p[0], false)
		return rc.total
	}
	rc.bit(p[0], true)
	for n < 16 {
		v := levels[n]
		if v == 0 {
			rc.bit(p[1], false)
			n++
			p = &pp[token.Bands[n]][0]
			continue
		}
		rc.bit(p[1], true)
		mag := int(v)
		if mag < 0 {
			mag = -mag
		}
		nextCtx := 2
		switch {
		case mag == 1:
			rc.bit(p[2], false)
			nextCtx = 1
		case mag <= 4:
			rc.bit(p[2], true)
			rc.bit(p[3], false)
			if mag == 2 {
				rc.bit(p[4], false)
			} else {
				rc.bit(p[4], true)
				rc.bit(p[5], mag == 4)
			}
		case mag <= 10:
			rc.bit(p[2], true)
			rc.bit(p[3], true)
			rc.bit(p[6], false)
			if mag <= 6 {
				rc.bit(p[7], false)
				rc.bit(cat1, mag == 6)
			} else {
				rc.bit(p[7], true)
				rc.bit(cat2a, (mag-7)>>1 == 1)
				rc.bit(cat2b, (mag-7)&1 == 1)
			}
		default:
			cat := 3
			base := 3 + (8 << 3)
			switch {
			case mag < 3+(8<<0)+(1<<3):
				cat, base = 0, 3+(8<<0)
			case mag < 3+(8<<1)+(1<<4):
				cat, base = 1, 3+(8<<1)
			case mag < 3+(8<<2)+(1<<5):
				cat, base = 2, 3+(8<<2)
			}
			b1, b0 := cat>>1, cat&1
			rc.bit(p[2], true)
			rc.bit(p[3], true)
			rc.bit(p[6], true)
			rc.bit(p[8], b1 == 1)
			rc.bit(p[9+b1], b0 == 1)
			bits := [4]int{3, 4, 5, 11}[cat]
			rest := mag - base
			for i := 0; i < bits; i++ {
				rc.bit(token.ExtraProbs[cat][i], rest>>(bits-1-i)&1 == 1)
			}
		}
		rc.bit(128, v < 0)
		n++
		if n == 16 {
			return rc.total
		}
		p = &pp[token.Bands[n]][nextCtx]
		if n > last {
			rc.bit(p[0], false)
			return rc.total
		}
		rc.bit(p[0], true)
	}
	return rc.total
}

// TestBlockCostExhaustiveSingleCoefficients sweeps every single-
// coefficient block shape: all four planes, all three neighbour
// contexts, both start positions, every scan position, every codable
// magnitude, against the reference walk. Sign symmetry is checked on a
// sample spanning every magnitude category, since a sign flip changes
// only the polarity of the final even-odds flag, never its price.
func TestBlockCostExhaustiveSingleCoefficients(t *testing.T) {
	tab := refTable()
	for plane := 0; plane < token.NumPlanes; plane++ {
		for ctx := 0; ctx < 3; ctx++ {
			for first := 0; first <= 1; first++ {
				for pos := first; pos < 16; pos++ {
					for mag := 1; mag <= token.MaxLevel; mag++ {
						var levels [16]int16
						levels[pos] = int16(mag)
						got := BlockCost(plane, ctx, first, &levels, &token.DefaultProbs)
						want := refBlockCost(tab, plane, ctx, first, &levels, &token.DefaultProbs)
						if got != want {
							t.Fatalf("single coeff plane=%d ctx=%d first=%d pos=%d mag=%d: %d, want %d",
								plane, ctx, first, pos, mag, got, want)
						}
					}
				}
			}
		}
	}

	// Sign symmetry sample: category boundaries and representatives.
	for _, mag := range []int16{1, 2, 3, 4, 5, 6, 7, 10, 11, 18, 19, 34, 35, 66, 67, 1057, 2113, 2114} {
		for plane := 0; plane < token.NumPlanes; plane++ {
			for ctx := 0; ctx < 3; ctx++ {
				var posLevels [16]int16
				var negLevels [16]int16
				posLevels[9] = mag
				negLevels[9] = -mag
				pos := BlockCost(plane, ctx, 0, &posLevels, &token.DefaultProbs)
				neg := BlockCost(plane, ctx, 0, &negLevels, &token.DefaultProbs)
				if pos != neg {
					t.Fatalf("sign moved the price: plane=%d ctx=%d mag=%d", plane, ctx, mag)
				}
			}
		}
	}
}

// TestBlockCostRandomMultiTokenBlocks throws seeded random blocks --
// dense, sparse, empty, extreme -- at both walkers and requires exact
// agreement, covering context chains and mid-block ends of block that
// single-coefficient shapes cannot reach.
func TestBlockCostRandomMultiTokenBlocks(t *testing.T) {
	tab := refTable()
	r := rand.New(rand.NewPCG(0xC057, 42))
	for iter := 0; iter < 20000; iter++ {
		var levels [16]int16
		first := 0
		if r.IntN(2) == 0 && iter%2 == 0 {
			first = 1
		}
		count := r.IntN(17 - first)
		for j := 0; j < count; j++ {
			pos := first + r.IntN(16-first)
			mag := 1 + r.IntN(40)
			if r.IntN(64) == 0 {
				mag = 1 + r.IntN(int(token.MaxLevel))
			}
			v := int16(mag)
			if r.IntN(2) == 0 {
				v = -v
			}
			levels[pos] = v
		}
		plane := r.IntN(token.NumPlanes)
		ctx := r.IntN(3)
		got := BlockCost(plane, ctx, first, &levels, &token.DefaultProbs)
		want := refBlockCost(tab, plane, ctx, first, &levels, &token.DefaultProbs)
		if got != want {
			t.Fatalf("iter %d plane=%d ctx=%d first=%d levels=%v: %d, want %d",
				iter, plane, ctx, first, levels, got, want)
		}
	}
}

// TestBlockCostGoldenPins pins whole-block totals as literals so silent
// repricing cannot slip through a shared bug in both walkers.
func TestBlockCostGoldenPins(t *testing.T) {
	at := func(pos int, v int16) [16]int16 {
		var l [16]int16
		l[pos] = v
		return l
	}
	cases := []struct {
		name   string
		plane  int
		ctx    int
		first  int
		levels [16]int16
		want   Cost
	}{
		{"empty-y-after-y2", token.YAfterY2, 0, 0, [16]int16{}, 256},
		{"uv-dc-one", token.UV, 0, 0, at(0, 1), 1948},
		{"y-mag2-pos5-ctx1", token.YAfterY2, 1, 0, at(5, 2), 3672},
		{"y2-mag3", token.Y2, 2, 0, at(3, 3), 4758},
		{"y2-mag4", token.Y2, 2, 0, at(3, 4), 6805},
		{"y2-mag5", token.Y2, 2, 0, at(3, 5), 6586},
		{"y2-mag6", token.Y2, 2, 0, at(3, 6), 6768},
		{"y2-mag7", token.Y2, 2, 0, at(3, 7), 6782},
		{"y2-mag10", token.Y2, 2, 0, at(3, 10), 7101},
		{"uv-mag11-cat3-start", token.UV, 1, 0, at(7, 11), 10203},
		{"uv-mag18-cat3-end", token.UV, 1, 0, at(7, 18), 10660},
		{"uv-mag19-cat4-start", token.UV, 1, 0, at(7, 19), 10415},
		{"uv-mag34-cat4-end", token.UV, 1, 0, at(7, 34), 10975},
		{"uv-mag35-cat5-start", token.UV, 1, 0, at(7, 35), 10653},
		{"uv-mag66-cat5-end", token.UV, 1, 0, at(7, 66), 11265},
		{"uv-mag67-cat6-start", token.UV, 1, 0, at(7, 67), 11091},
		{"uv-maxlevel", token.UV, 1, 0, at(7, token.MaxLevel), 17553},
		{
			"multi-token-bpred-style", token.YWithDC, 2, 0,
			[16]int16{2, 0, -1, 0, 0, 5, 0, 0, 0, -40, 0, 0, 0, 0, 0, 2114}, 26244,
		},
		{"y-first1-mag9", token.YAfterY2, 1, 1, at(1, 9), 5877},
	}
	for _, c := range cases {
		levels := c.levels
		if got := BlockCost(c.plane, c.ctx, c.first, &levels, &token.DefaultProbs); got != c.want {
			t.Errorf("%s: BlockCost = %d, want pinned %d", c.name, got, c.want)
		}
	}
}

// TestBlockCostSignPolarityOnlyChangesTheFlag asserts the price identity
// behind the sign-symmetry checks: flipping every sign of a multi-token
// block leaves its total unchanged.
func TestBlockCostSignPolarityOnlyChangesTheFlag(t *testing.T) {
	a := [16]int16{2, 0, -1, 0, 0, 5, 0, 0, 0, -40, 0, 0, 0, 0, 0, 2114}
	b := [16]int16{-2, 0, 1, 0, 0, -5, 0, 0, 0, 40, 0, 0, 0, 0, 0, -2114}
	ga := BlockCost(token.YWithDC, 2, 0, &a, &token.DefaultProbs)
	gb := BlockCost(token.YWithDC, 2, 0, &b, &token.DefaultProbs)
	if ga != gb {
		t.Fatalf("global sign flip moved the price: %d vs %d", ga, gb)
	}
}

// TestEmptyBlockCostsMatchTheirFirstDecision checks every empty block
// shape: it prices exactly one end-of-block decision at the entry
// probability.
func TestEmptyBlockCostsMatchTheirFirstDecision(t *testing.T) {
	tab := refTable()
	for plane := 0; plane < token.NumPlanes; plane++ {
		for ctx := 0; ctx < 3; ctx++ {
			for first := 0; first <= 1; first++ {
				var levels [16]int16
				got := BlockCost(plane, ctx, first, &levels, &token.DefaultProbs)
				entry := token.DefaultProbs[plane][token.Bands[first]][ctx][0]
				if want := tab[entry][0]; got != want {
					t.Fatalf("empty plane=%d ctx=%d first=%d: %d, want %d", plane, ctx, first, got, want)
				}
			}
		}
	}
}

// TestBlockCostRejectsImpossibleInputs keeps out-of-range arguments and
// oversized levels loud, mirroring the writer's own refusals.
func TestBlockCostRejectsImpossibleInputs(t *testing.T) {
	var levels [16]int16
	mustPanic := func(name string, f func()) {
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		f()
	}
	mustPanic("plane", func() { BlockCost(token.NumPlanes, 0, 0, &levels, &token.DefaultProbs) })
	mustPanic("context", func() { BlockCost(0, 3, 0, &levels, &token.DefaultProbs) })
	mustPanic("first", func() { BlockCost(0, 0, 16, &levels, &token.DefaultProbs) })
	var huge [16]int16
	huge[0] = token.MaxLevel + 1
	mustPanic("level", func() { BlockCost(0, 0, 0, &huge, &token.DefaultProbs) })
}

// TestBlockCostTracksRealWriterStreams is a smoke check, not a proof:
// batches of priced blocks are also coded by the real token writer, and
// the actual partition length stays within a few percent of the ideal
// total. Structural correctness is proven elsewhere (the exhaustive
// sweeps above and the full-frame replay parser in the encoder
// package); this catches wholesale disconnects cheaply.
//
// The bound is asymmetric-tolerant on purpose: the boolean coder picks
// its split from the current range -- 1+((range-1)*p)>>8 -- so its
// realized rates deviate from the ideal -log2 sums by a few percent,
// systematically downward on skew-heavy streams such as these sparse
// random blocks.
func TestBlockCostTracksRealWriterStreams(t *testing.T) {
	r := rand.New(rand.NewPCG(0xC057, 7))
	const batchBlocks = 128
	for batch := 0; batch < 20; batch++ {
		enc := boolenc.New(batchBlocks * 24)
		w := token.NewWriter(enc, &token.DefaultProbs)
		var ideal Cost
		for i := 0; i < batchBlocks; i++ {
			var levels [16]int16
			count := r.IntN(6)
			for j := 0; j < count; j++ {
				pos := r.IntN(16)
				v := int16(1 + r.IntN(30))
				if r.IntN(2) == 0 {
					v = -v
				}
				levels[pos] = v
			}
			plane := []int{token.YAfterY2, token.Y2, token.UV, token.YWithDC}[r.IntN(4)]
			ctx := r.IntN(3)
			first := r.IntN(2)
			ideal += BlockCost(plane, ctx, first, &levels, &token.DefaultProbs)
			w.WriteBlock(plane, ctx, first, &levels)
		}
		out := enc.Finish()
		idealBytes := PartitionZeroBytes(ideal)
		slack := 8 + idealBytes/20 // 5% coder deviation plus flush slack
		if diff := idealBytes - len(out); diff < -slack || diff > slack {
			t.Fatalf("batch %d: ideal %d bytes vs written %d (slack %d)", batch, idealBytes, len(out), slack)
		}
	}
}

// TestProbUpdateCostExhaustiveOverNormativeEntries covers every update
// decision the key frame writes -- all 1056 entries of the normative
// update table, in both outcomes -- against fresh logarithms, plus the
// eight-bit continuation an update carries.
func TestProbUpdateCostExhaustiveOverNormativeEntries(t *testing.T) {
	for i := 0; i < token.NumPlanes; i++ {
		for j := 0; j < token.NumBands; j++ {
			for k := 0; k < token.NumContexts; k++ {
				for l := 0; l < token.NumProbs; l++ {
					entry := token.UpdateProbs[i][j][k][l]
					if got, want := ProbUpdateCost(entry, false), refUnits(int(entry), false); got != want {
						t.Fatalf("no-update at entry %d: %d, want %d", entry, got, want)
					}
					if got, want := ProbUpdateCost(entry, true),
						refUnits(int(entry), true)+Literal(8); got != want {
						t.Fatalf("update at entry %d: %d, want %d", entry, got, want)
					}
				}
			}
		}
	}
	if got := ProbUpdateCost(255, false); got != 1 {
		t.Errorf("ProbUpdateCost(255, false) = %d, want 1", got)
	}
	if got := ProbUpdateCost(255, true); got != 4096 {
		t.Errorf("ProbUpdateCost(255, true) = %d, want 4096", got)
	}
	if got := ProbUpdateCost(176, true); got != refUnits(176, true)+8*Unit {
		t.Errorf("ProbUpdateCost(176, true) = %d, want %d", got, refUnits(176, true)+8*Unit)
	}
}
