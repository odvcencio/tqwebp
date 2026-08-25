package encoder

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/token"
	"m31labs.dev/tqwebp/internal/yuv"
	"m31labs.dev/tqwebp/oracle"
)

// The tests in this file audit the Slice 6A token-probability optimizer
// from outside its own logic. The derivation is re-priced here from raw
// branch counts with an independent -log2 ledger, the frame header is
// re-parsed bit by bit with a boolean decoder, and the finished file is
// decoded with golang.org/x/image/vp8 through the oracle. Production
// pricing (cost.Optimize) is never asked for an expected value.

// probOptFixture builds a deterministic 64x48 image mixing a smooth
// gradient with per-sample noise, so several coefficient bands carry
// nonzero counts and at least one probability update strictly pays.
func probOptFixture() image.Image {
	const w, h = 64, 48
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(0x6a, uint64(w)*1000+uint64(h)))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i+0] = uint8(int(x*4+y) + int(r.UintN(48)))
			img.Pix[i+1] = uint8(255 - x*3 - int(r.UintN(32)))
			img.Pix[i+2] = uint8(y*5 + x/2 + int(r.UintN(24)))
			img.Pix[i+3] = 0xff
		}
	}
	return img
}

// analyse runs the analysis passes only, with optional encoder tweaks,
// so tests can inspect the macroblocks and call the serialization
// pieces directly.
func analyse(m image.Image, cfg Config, mods ...func(*encoder)) *encoder {
	enc := newEncoder(yuv.Convert(m), cfg)
	for _, mod := range mods {
		mod(enc)
	}
	enc.run()
	return enc
}

// refCosts prices branches from logarithms, independently of
// internal/cost's tables.
type refCosts struct {
	zero [256]int64
	one  [256]int64
}

func newRefCosts() refCosts {
	var r refCosts
	logUnit := func(p int) int64 {
		return int64(math.Floor(-math.Log2(float64(p)/256)*256 + 0.5))
	}
	for p := 1; p <= 255; p++ {
		r.zero[p] = logUnit(p)
		r.one[p] = logUnit(256 - p)
	}
	return r
}

func (r refCosts) branch(p uint8, nFalse, nTrue int) int64 {
	return int64(nFalse)*r.zero[p] + int64(nTrue)*r.one[p]
}

func (r refCosts) argmin(nFalse, nTrue int) uint8 {
	bestP, bestC := uint8(1), int64(1)<<62
	for p := 1; p <= 255; p++ {
		c := r.branch(uint8(p), nFalse, nTrue)
		if c < bestC {
			bestC, bestP = c, uint8(p)
		}
	}
	return bestP
}

const refLiteralBits = 8 // the RFC 6386 section 13.4 literal width

// probCounts tallies every codable branch coordinate.
type probCounts [token.NumPlanes][token.NumBands][token.NumContexts][token.NumProbs][2]int32

type countingObserver struct{ counts probCounts }

func (o *countingObserver) ObserveBranch(plane, band, ctx, node int, bit bool) {
	b := 0
	if bit {
		b = 1
	}
	o.counts[plane][band][ctx][node][b]++
}

// measureCounts walks the analysed macroblocks with a fresh writer and
// observer -- production's own traversal, but a measurement taken here,
// in the test, rather than a copy of optimizeTokenProbs's internals.
func measureCounts(e *encoder) probCounts {
	sink := boolenc.New(4096 + len(e.mbs)*16)
	w := token.NewWriter(sink, &token.DefaultProbs)
	hist := &countingObserver{}
	w.SetObserver(hist)
	e.codeTokens(w)
	return hist.counts
}

// expectedUpdates derives the shipped table from raw histogram counts:
// ship the lowest-tied enumeration minimum only when its new total
// (branch cost plus true-side gate plus literal) is strictly below the
// old total (branch cost at old plus false-side gate). It mirrors RFC
// 6386 section 13.4's ledger, not cost.Optimize's code.
func expectedUpdates(counts *probCounts) (*token.Probs, int) {
	ref := newRefCosts()
	out := token.DefaultProbs
	shipped := 0
	for i := 0; i < token.NumPlanes; i++ {
		for j := 0; j < token.NumBands; j++ {
			for k := 0; k < token.NumContexts; k++ {
				for l := 0; l < token.NumProbs; l++ {
					nf := int(counts[i][j][k][l][0])
					nt := int(counts[i][j][k][l][1])
					if nf+nt == 0 {
						continue
					}
					newP := ref.argmin(nf, nt)
					gate := token.UpdateProbs[i][j][k][l]
					oldTotal := ref.branch(token.DefaultProbs[i][j][k][l], nf, nt) +
						ref.zero[gate]
					newTotal := ref.branch(newP, nf, nt) +
						ref.one[gate] +
						refLiteralBits*256
					if newTotal < oldTotal {
						out[i][j][k][l] = newP
						shipped++
					}
				}
			}
		}
	}
	return &out, shipped
}

// parseHeaderTokenProbs decodes a bare VP8 key frame's first partition
// through section 9.8 exactly as a decoder reads it and returns the
// coefficient probability table the frame selected.
func parseHeaderTokenProbs(t *testing.T, vp8 []byte) *token.Probs {
	t.Helper()
	if len(vp8) < 10 || vp8[3] != 0x9d || vp8[4] != 0x01 || vp8[5] != 0x2a {
		t.Fatalf("frame does not start with a key-frame tag and start code: % x", vp8[:minInt(10, len(vp8))])
	}
	firstLen := int(vp8[0])>>5 | int(vp8[1])<<3 | int(vp8[2])<<11
	if firstLen <= 0 || 10+firstLen > len(vp8) {
		t.Fatalf("first partition length %d does not fit %d bytes", firstLen, len(vp8))
	}
	d := boolenc.NewDecoder(vp8[10 : 10+firstLen])

	d.ReadFlag()     // colour space
	d.ReadFlag()     // clamping
	d.ReadBool(128)  // segmentation off
	d.ReadFlag()     // filter simple
	d.ReadLiteral(6) // filter level
	d.ReadLiteral(3) // filter sharpness
	d.ReadBool(128)  // no filter deltas
	d.ReadLiteral(2) // partition count exponent
	d.ReadLiteral(7) // base quantizer
	for i := 0; i < 5; i++ {
		d.ReadBool(128) // quantizer deltas absent
	}
	d.ReadBool(128) // refresh entropy probabilities

	table := token.DefaultProbs
	for i := 0; i < token.NumPlanes; i++ {
		for j := 0; j < token.NumBands; j++ {
			for k := 0; k < token.NumContexts; k++ {
				for l := 0; l < token.NumProbs; l++ {
					if d.ReadBool(token.UpdateProbs[i][j][k][l]) {
						table[i][j][k][l] = uint8(d.ReadLiteral(8))
					}
				}
			}
		}
	}
	if d.UnexpectedEOF() {
		t.Fatal("header read past the end of the first partition")
	}
	return &table
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestProbabilityBoundaryShipsPositiveUpdates proves the first probability
// derivation reaches updates that strictly pay: at least one entry ships,
// every shipped entry matches the independent -log2 ledger, and no
// shipped entry equals the default it would replace.
func TestProbabilityBoundaryShipsPositiveUpdates(t *testing.T) {
	enc := analyse(probOptFixture(), Config{Quality: 75, Method: minProbOptMethod})
	probs := enc.optimizeTokenProbs()
	if probs == nil {
		t.Fatal("optimizeTokenProbs returned nil at the probability boundary")
	}

	counts := measureCounts(enc)
	want, shipped := expectedUpdates(&counts)
	if shipped == 0 {
		t.Fatal("independent ledger found no winning update; fixture cannot exercise the optimizer")
	}
	if !reflect.DeepEqual(*probs, *want) {
		for i := range probs {
			for j := range probs[i] {
				for k := range probs[i][j] {
					for l, v := range probs[i][j][k] {
						if v != want[i][j][k][l] {
							t.Errorf("plane %d band %d ctx %d node %d: got %d, independent ledger says %d",
								i, j, k, l, v, want[i][j][k][l])
						}
					}
				}
			}
		}
		t.Fatal("derived table differs from the independent ledger")
	}
	diff := 0
	for i := range probs {
		for j := range probs[i] {
			for k := range probs[i][j] {
				for l, v := range probs[i][j][k] {
					if v != token.DefaultProbs[i][j][k][l] {
						diff++
					}
				}
			}
		}
	}
	if diff != shipped {
		t.Fatalf("%d entries differ from defaults but the ledger shipped %d", diff, shipped)
	}
	t.Logf("%d of %d nodes shipped profitable updates", diff,
		token.NumPlanes*token.NumBands*token.NumContexts*token.NumProbs)
}

// TestProbabilityBoundaryHeaderTableEqualsTokenTableAndDecodes proves three
// consistencies of one boundary encode: the table the header signals
// (re-parsed independently from the bitstream) equals the derived table,
// re-coding the token partition against that parsed table reproduces the
// shipped partition byte for byte, and the whole file still decodes to
// pixels identical to the encoder's reconstruction.
func TestProbabilityBoundaryHeaderTableEqualsTokenTableAndDecodes(t *testing.T) {
	enc := analyse(probOptFixture(), Config{Quality: 75, Method: minProbOptMethod})

	var fileBuf writerBuffer
	if err := enc.writeFile(&fileBuf); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	vp8, err := enc.frameBytes()
	if err != nil {
		t.Fatalf("frameBytes: %v", err)
	}

	hdr := parseHeaderTokenProbs(t, vp8)
	derived := enc.optimizeTokenProbs()
	if derived == nil {
		t.Fatal("no derived table; test premise lost")
	}
	if !reflect.DeepEqual(*hdr, *derived) {
		t.Fatal("header-signalled table differs from the derived table")
	}

	// The shipped token partition must be exactly what coding against the
	// header-selected table produces.
	firstLen := int(vp8[0])>>5 | int(vp8[1])<<3 | int(vp8[2])<<11
	wantTokens := enc.writeTokens(hdr)
	if !bytes.Equal(wantTokens, vp8[10+firstLen:]) {
		t.Fatal("token partition was not coded against the header-selected table")
	}
	if bytes.Equal(wantTokens, enc.writeTokens(&token.DefaultProbs)) {
		t.Fatal("partition coded against defaults equals the optimized partition; comparison has no power")
	}

	decoded, err := oracle.DecodeWebPPlanes(fileBuf.data)
	if err != nil {
		t.Fatalf("oracle decode: %v", err)
	}
	if err := oracle.CompareExact(reconSource{enc.reconstruction()}, decoded); err != nil {
		t.Fatalf("decoded picture differs from reconstruction: %v", err)
	}
}

// TestMethodsBelowProbabilityBoundaryStayUpdateSilent encodes every lower
// method twice -- once normally, once with the probability refinement
// disabled through the same switch production leaves off -- and requires
// identical bytes, no derived table, an all-defaults header table, and
// identical decision counters.
func TestMethodsBelowProbabilityBoundaryStayUpdateSilent(t *testing.T) {
	img := probOptFixture()
	for m := 0; m < minProbOptMethod; m++ {
		t.Run(fmt.Sprintf("method%d", m), func(t *testing.T) {
			a := analyse(img, Config{Quality: 75, Method: m})
			b := analyse(img, Config{Quality: 75, Method: m}, func(e *encoder) { e.rdProbOptOff = true })

			if got := a.optimizeTokenProbs(); got != nil {
				t.Fatalf("Method %d derived probability updates %+v", m, *got)
			}
			if got := b.optimizeTokenProbs(); got != nil {
				t.Fatalf("Method %d with the refinement off derived %+v", m, *got)
			}
			var bufA, bufB writerBuffer
			if err := a.writeFile(&bufA); err != nil {
				t.Fatalf("writeFile: %v", err)
			}
			if err := b.writeFile(&bufB); err != nil {
				t.Fatalf("writeFile: %v", err)
			}
			if !bytes.Equal(bufA.data, bufB.data) {
				t.Fatalf("Method %d bytes changed when the default-off refinement switched states", m)
			}
			if a.rd != b.rd {
				t.Fatalf("Method %d counters differ: %+v vs %+v", m, a.rd, b.rd)
			}
			vp8, err := a.frameBytes()
			if err != nil {
				t.Fatalf("frameBytes: %v", err)
			}
			if hdr := parseHeaderTokenProbs(t, vp8); *hdr != token.DefaultProbs {
				t.Fatalf("Method %d header signalled non-default probabilities", m)
			}
		})
	}
}

// TestProbabilityBoundaryStableAcrossRepeatsAndGOMAXPROCS encodes the fixture
// at the first probability-enabled method in pinned subprocesses and
// hashes file bytes, derived table, and decision counters; every hash
// must match the in-process run.
func TestProbabilityBoundaryStableAcrossRepeatsAndGOMAXPROCS(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess matrix skipped in short mode")
	}
	want := probOptStateHash(t)
	again := probOptStateHash(t)
	if want != again {
		t.Fatalf("repeat changed probability-boundary state: %s vs %s", again, want)
	}
	for _, gmp := range []int{1, 2, 3, 8} {
		got := runProbOptHashSubprocess(t, gmp)
		if got != want {
			t.Fatalf("GOMAXPROCS=%d hash %s differs from in-process hash %s", gmp, got, want)
		}
	}
}

// TestProbOptStateHashSubprocess is the payload of the GOMAXPROCS matrix;
// it prints "hash <hex>" on stdout and runs only when invoked explicitly.
func TestProbOptStateHashSubprocess(t *testing.T) {
	if os.Getenv("TQWEBP_PROBOPT_HASH") == "" {
		t.Skip("helper for TestProbabilityBoundaryStableAcrossRepeatsAndGOMAXPROCS")
	}
	fmt.Printf("hash %s\n", probOptStateHash(t))
}

// probOptStateHash encodes the fixture at the probability boundary and hashes
// bytes together with the derived table and the decision counters.
func probOptStateHash(t *testing.T) string {
	t.Helper()
	enc := analyse(probOptFixture(), Config{Quality: 75, Method: minProbOptMethod})
	var buf writerBuffer
	if err := enc.writeFile(&buf); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	h := sha256.New()
	h.Write(buf.data)
	if probs := enc.optimizeTokenProbs(); probs != nil {
		for _, row := range *probs {
			for _, ctx := range row {
				for _, node := range ctx {
					h.Write(node[:])
				}
			}
		}
	} else {
		h.Write([]byte{0})
	}
	fmt.Fprintf(h, "%d/%d/%d/%d", enc.rd.CandidatesEvaluated, enc.rd.BpredAttempts,
		enc.rd.BpredAborts, enc.rd.PrunedBPred)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// runProbOptHashSubprocess reruns the hash helper in this test binary
// under one GOMAXPROCS setting and returns the printed hash.
func runProbOptHashSubprocess(t *testing.T, gmp int) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProbOptStateHashSubprocess$")
	cmd.Env = append(os.Environ(),
		"TQWEBP_PROBOPT_HASH=1",
		fmt.Sprintf("GOMAXPROCS=%d", gmp),
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("subprocess GOMAXPROCS=%d: %v: %s", gmp, err, out)
	}
	var hash string
	if _, err := fmt.Sscanf(string(out), "hash %s", &hash); err != nil || len(hash) != 64 {
		t.Fatalf("subprocess GOMAXPROCS=%d printed %q", gmp, out)
	}
	return hash
}
