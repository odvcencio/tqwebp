package cost

import (
	"math"
	"math/rand/v2"
	"sync"
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/token"
)

// refUnits is the independent reference price of one decision: the ideal
// code length -log2(p/256) -- or its complement for the true branch --
// scaled into 1/256-bit units and rounded to nearest. It exists so no
// test below has to trust the package's own table: every expected value
// in this file comes from recomputing the logarithm here.
//
// Tests only. Production cost code never computes logarithms.
func refUnits(p int, bit bool) Cost {
	q := float64(p)
	if bit {
		q = float64(256 - p)
	}
	return Cost(math.Round(-math.Log2(q/256.0) * 256.0))
}

// TestBitCostExhaustiveAgainstLogReference covers every codable
// probability in both branches against the freshly computed reference:
// 510 decisions, each required to match exactly, plus the structural
// properties the definition implies.
func TestBitCostExhaustiveAgainstLogReference(t *testing.T) {
	prev := Cost(math.MaxInt32)
	for p := 1; p <= 255; p++ {
		u8 := uint8(p)
		if got, want := BitCostZero(u8), refUnits(p, false); got != want {
			t.Fatalf("BitCostZero(%d) = %d, want %d", p, got, want)
		}
		if got, want := BitCostOne(u8), refUnits(p, true); got != want {
			t.Fatalf("BitCostOne(%d) = %d, want %d", p, got, want)
		}
		// Branch symmetry: pricing the true branch at p is pricing the
		// false branch at its complement.
		if got, want := BitCostOne(u8), BitCostZero(uint8(256-p)); got != want {
			t.Fatalf("BitCostOne(%d) = %d, want BitCostZero(%d) = %d", p, got, 256-p, want)
		}
		// Bounds: a decision costs at least one unit and at most eight
		// bits (probability 1/256).
		c := BitCostZero(u8)
		if c < 1 || c > 8*Unit {
			t.Fatalf("BitCostZero(%d) = %d outside [1, 2048]", p, c)
		}
		// The false branch must price strictly less as it becomes more
		// likely; the generated table is strictly monotone.
		if c >= prev {
			t.Fatalf("BitCostZero not strictly decreasing at p=%d: %d after %d", p, c, prev)
		}
		prev = c
	}
}

// TestBitCostGoldenBoundaries pins representative values as literals, so
// a wholesale regeneration accident cannot silently reprice everything
// even if some future reference implementation drifted with it. Powers
// of two are exact by definition; the rest are rounded ideals.
func TestBitCostGoldenBoundaries(t *testing.T) {
	zeros := map[uint8]Cost{
		1: 2048, 2: 1792, 4: 1536, 8: 1280, 16: 1024, 32: 768,
		64: 512, 128: 256, // exact powers of two
		255: 1,             // likeliest codable false branch
		254: 3,             //
		145: 210,           // the key-frame B_PRED-vs-rest probability
		156: 183, 163: 167, // luma tree nodes
		142: 218, 114: 299, 183: 124, // chroma tree nodes
		159: 176, 165: 162, // category-1/2 extra-bit probabilities
	}
	for p, want := range zeros {
		if got := BitCostZero(p); got != want {
			t.Errorf("BitCostZero(%d) = %d, want %d", p, got, want)
		}
	}
	ones := map[uint8]Cost{
		255: 2048, // true branch of the unlikeliest-false probability
		128: 256,
		145: BitCostZero(111),
		1:   1,
	}
	for p, want := range ones {
		if got := BitCostOne(p); got != want {
			t.Errorf("BitCostOne(%d) = %d, want %d", p, got, want)
		}
	}
	// Polarity routing of the combined accessor.
	if got := BitCost(90, false); got != BitCostZero(90) {
		t.Errorf("BitCost(false branch) routed wrong")
	}
	if got := BitCost(90, true); got != BitCostOne(90) {
		t.Errorf("BitCost(true branch) routed wrong")
	}
}

// TestLiteralPrices pins literal-field pricing: n plain bits.
func TestLiteralPrices(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 6, 7, 8, 19, 24} {
		if got, want := Literal(n), Cost(n)*Unit; got != want {
			t.Errorf("Literal(%d) = %d, want %d", n, got, want)
		}
	}
	// The frame tag's first-partition size subfield is 19 bits, RFC 6386
	// section 9.1 -- not the 24-bit tag word around it. Pinned as a
	// literal so a regression back to the whole tag is loud.
	if got, want := SizeField(), Cost(19*Unit); got != want {
		t.Errorf("SizeField() = %d, want 19 bits = %d", got, want)
	}
	if got, want := Literal(SizeFieldBits), SizeField(); got != want {
		t.Errorf("SizeField() = %d, want Literal(SizeFieldBits) = %d", got, want)
	}
}

// TestUncodableProbabilityPanics mirrors boolenc's refusal: pricing a
// decision at probability 0 is a caller bug and must be loud.
func TestUncodableProbabilityPanics(t *testing.T) {
	mustPanic := func(name string, f func()) {
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic on probability 0", name)
			}
		}()
		f()
	}
	mustPanic("BitCostZero", func() { BitCostZero(0) })
	mustPanic("BitCostOne", func() { BitCostOne(0) })
	mustPanic("BitCost", func() { BitCost(0, true) })
	mustPanic("Literal", func() { Literal(-1) })
}

// TestPartitionZeroBytesGolden pins the whole-byte conversion: round up,
// one byte per 2048 units.
func TestPartitionZeroBytesGolden(t *testing.T) {
	cases := []struct {
		units Cost
		want  int
	}{
		{0, 0},
		{1, 1},         // any nonzero rate occupies a byte
		{2048, 1},      // exactly one byte of rate
		{2049, 2},      // one unit over crosses the boundary
		{4096, 2},      //
		{16384, 8},     // sixty-four bits exactly
		{16385, 9},     //
		{1 << 20, 512}, // a large header partition's worth
		{8 * Unit * 1000000, 1000000},
	}
	for _, c := range cases {
		if got := PartitionZeroBytes(c.units); got != c.want {
			t.Errorf("PartitionZeroBytes(%d) = %d, want %d", c.units, got, c.want)
		}
	}
	prev := -1
	for u := Cost(0); u <= 8*Unit*37+7; u++ {
		b := PartitionZeroBytes(u)
		if b < prev {
			t.Fatalf("PartitionZeroBytes decreased at %d units", u)
		}
		prev = b
	}
}

// TestPartitionZeroBytesTracksRealStreams checks the accounting against
// reality: for random decision batches, the ideal total converted to
// bytes stays within a small fixed slack of what boolenc actually
// writes, flush padding included. The slack absorbs arithmetic-coding
// redundancy and the finish flush's up-to-three surplus bytes; a
// mispriced table would blow past it on these sizes.
func TestPartitionZeroBytesTracksRealStreams(t *testing.T) {
	r := rand.New(rand.NewPCG(0xC0DEC0DE, 3))
	for batch := 0; batch < 200; batch++ {
		n := 32 + r.IntN(1024)
		var want Cost
		decisions := make([]struct {
			p uint8
			b bool
		}, n)
		enc := boolenc.New(n / 4)
		for i := range decisions {
			p := uint8(r.IntN(255)) + 1
			b := r.IntN(2) == 1
			decisions[i] = struct {
				p uint8
				b bool
			}{p, b}
			enc.WriteBool(p, b)
			want += BitCost(p, b)
		}
		out := enc.Finish()

		est := PartitionZeroBytes(want)
		if diff := est - len(out); diff < -8 || diff > 8 {
			t.Fatalf("batch %d (%d decisions): estimate %d bytes vs actual %d", batch, n, est, len(out))
		}

		// The stream must read back with exactly the priced decisions,
		// confirming the sums were taken over a coherent stream.
		dec := boolenc.NewDecoder(out)
		for _, d := range decisions {
			if b := dec.ReadBool(d.p); b != d.b {
				t.Fatalf("batch %d: stream does not read back its priced decisions", batch)
			}
		}
		if dec.UnexpectedEOF() {
			t.Fatalf("batch %d: decoder ran out of bytes mid-stream", batch)
		}
	}
}

// TestPrimitivesAreConcurrencySafe hammers every primitive from several
// goroutines: the package holds only immutable tables, and the race
// detector proves nothing mutates.
func TestPrimitivesAreConcurrencySafe(t *testing.T) {
	levels := [16]int16{0, 3, 0, 1, 0, 0, 12, 0, 0, 0, 0, 2114, 0, 0, 0, 2}
	probs := &token.DefaultProbs
	segs := [3]uint8{42, 99, 200}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			var sum Cost
			for i := 0; i < 500; i++ {
				sum += BitCost(uint8(i%255)+1, i%2 == 0)
				sum += LumaMode(predict.Mode(i % 4))
				sum += ChromaMode(predict.Mode((i + g) % 4))
				sum += SubModeCost(predict.SubMode(i%10), predict.SubMode((i+g)%10), predict.SubMode((i+2*g)%10))
				sum += BlockCost(i%token.NumPlanes, i%3, i%2, &levels, probs)
				sum += ProbUpdateCost(token.UpdateProbs[i%4][i%8][i%3][i%11], i%17 == 0)
				sum += SegmentIDCost(&segs, uint8(i%4))
				sum += SkipCost(uint8(i%255)+1, i%3 == 0)
				sum += Cost(LambdaForQuality(i % 101))
			}
			if sum < 0 {
				t.Error("impossible negative sum")
			}
		}(g)
	}
	wg.Wait()
}
