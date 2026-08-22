package token

// This file holds the Slice 6A observer tests. They prove, from the
// outside, that a Writer's observer sees exactly the branches the writer
// codes -- the same coordinates, the same bits, in coding order -- and
// that observation never changes a single coded byte. The reference walk
// below is written from RFC 6386 sections 13.2 and 13.3 directly and
// shares no code with Writer.

import (
	"bytes"
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
)

// branch is one observed decision.
type branch struct {
	plane int
	band  int
	ctx   int
	node  int
	bit   bool
}

// recorder collects every branch an observer receives.
type recorder struct {
	seen []branch
}

func (r *recorder) ObserveBranch(plane, band, ctx, node int, bit bool) {
	r.seen = append(r.seen, branch{plane, band, ctx, node, bit})
}

// refBand returns the coefficient band of scan position n, re-derived
// from RFC 6386 section 13.3: position 0 is band 0, positions 1 and 2 are
// bands 1 and 2, position 3 is band 3, position 4 is band 6, positions 5
// to 13 are bands 4, 5, 6..., position 14 and 15 are bands 6 and 7. The
// table form in tables.go must agree; this literal spelling is the
// independent copy.
var refBands = [17]int{0, 1, 2, 3, 6, 4, 5, 6, 6, 6, 6, 6, 6, 6, 6, 7, 0}

// refCategory returns the large category, 0 to 3, holding mag >= 11.
// Category bases are 11, 19, 35, 67, RFC 6386 section 13.2.
func refCategory(mag int) int {
	bases := [4]int{11, 19, 35, 67}
	for cat := 3; cat >= 0; cat-- {
		if mag >= bases[cat] {
			return cat
		}
	}
	panic("refCategory: magnitude below the large categories")
}

// refBlockBranches enumerates, in coding order, every branch WriteBlock
// must code for one block, walking the token tree of RFC 6386 chapter 13
// on its own. Node numbers follow the probability slots: 0 end-of-block,
// 1 zero, 2 one-vs-more, then the magnitude subtree 3 to 10.
func refBlockBranches(plane, ctx0, first int, levels *[16]int16) []branch {
	var out []branch

	last := -1
	for i := 15; i >= first; i-- {
		if levels[i] != 0 {
			last = i
			break
		}
	}

	n := first
	band := refBands[n]
	ctx := ctx0
	add := func(node int, bit bool) {
		out = append(out, branch{plane, band, ctx, node, bit})
	}

	if last < 0 {
		add(0, false)
		return out
	}
	add(0, true)

	for n < 16 {
		v := levels[n]
		if v == 0 {
			add(1, false)
			n++
			band = refBands[n]
			ctx = 0
			continue
		}
		add(1, true)
		mag := int(v)
		if mag < 0 {
			mag = -mag
		}
		nextCtx := 1
		if mag == 1 {
			add(2, false)
		} else {
			add(2, true)
			switch {
			case mag <= 4:
				add(3, false)
				if mag == 2 {
					add(4, false)
				} else {
					add(4, true)
					add(5, mag == 4)
				}
			case mag <= 10:
				add(3, true)
				add(6, false)
				if mag <= 6 {
					add(7, false)
				} else {
					add(7, true)
				}
			default:
				cat := refCategory(mag)
				add(3, true)
				add(6, true)
				b1 := cat >> 1
				add(8, b1 == 1)
				add(9+b1, cat&1 == 1)
			}
			nextCtx = 2
		}
		n++
		if n == 16 {
			return out
		}
		band = refBands[n]
		ctx = nextCtx
		if n > last {
			add(0, false)
			return out
		}
		add(0, true)
	}
	return out
}

// codeBlock runs WriteBlock against a fresh encoder and returns the
// branches its observer saw plus the coded bytes.
func codeBlock(probs *Probs, plane, ctx, first int, levels *[16]int16) ([]branch, []byte) {
	enc := boolenc.New(64)
	w := NewWriter(enc, probs)
	rec := &recorder{}
	w.SetObserver(rec)
	w.WriteBlock(plane, ctx, first, levels)
	return rec.seen, enc.Finish()
}

// TestObserverMatchesReferenceWalk feeds blocks that reach every node of
// every plane's tree and requires the observed stream to equal the
// independent reference enumeration exactly: same length, same order,
// same coordinates, same bits.
func TestObserverMatchesReferenceWalk(t *testing.T) {
	// One block per plane, each exercising empty, single-small, single-
	// large, and many-coefficient shapes across all three contexts.
	mk := func(vals ...int16) *[16]int16 {
		var a [16]int16
		copy(a[:], vals)
		return &a
	}
	blocks := map[int][]*[16]int16{
		YAfterY2: {mk(), mk(3), mk(0, 0, 0, 40), mk(1, -1, 2, 300)},
		Y2:       {mk(), mk(1), mk(0, 7), mk(2, -5, 11, 67)},
		UV:       {mk(), mk(-2), mk(0, 9), mk(4, 4, 19, 1500)},
		YWithDC:  {mk(), mk(5), mk(0, 10), mk(6, -35, 120, 2114)},
	}

	for plane := 0; plane < NumPlanes; plane++ {
		for _, levels := range blocks[plane] {
			for first := 0; first <= 1; first++ {
				if plane == YAfterY2 && first == 0 {
					continue // that block always starts at 1
				}
				for ctx := 0; ctx < NumContexts; ctx++ {
					got, _ := codeBlock(&DefaultProbs, plane, ctx, first, levels)
					want := refBlockBranches(plane, ctx, first, levels)
					if len(got) != len(want) {
						t.Fatalf("plane %d first %d ctx %d levels %v: %d branches, want %d (%v vs %v)",
							plane, first, ctx, *levels, len(got), len(want), got, want)
					}
					for i := range got {
						if got[i] != want[i] {
							t.Fatalf("plane %d first %d ctx %d levels %v: branch %d is %+v, want %+v",
								plane, first, ctx, *levels, i, got[i], want[i])
						}
					}
				}
			}
		}
	}
}

// TestObserverCoversEveryTableCell proves the fixtures above reach every
// (plane, band, context, node) cell at least once, so the histogram the
// encoder later builds has been exercised over its whole shape.
func TestObserverCoversEveryTableCell(t *testing.T) {
	hit := [NumPlanes][NumBands][NumContexts][NumProbs]bool{}

	// Build, per plane, a fixture set that reaches every structurally
	// codable cell. Lone coefficients give the context-0 traffic and
	// the end-of-block exits. Chained blocks -- everything nonzero up
	// to m-1, whose magnitude picks context 1 (magnitude 1) or context
	// 2 (magnitude 5), followed by a payload at m -- walk every subtree
	// exit at both of those contexts, including the zero and the
	// end-of-block departures.
	mags := []int16{1, 2, 3, 4, 5, 6, 7, 9, 10, 11, 18, 34, 66, 67, 68, 150, 800, 2114}
	pays := []int16{0, 1, 2, 4, 6, 12, 40, 100}
	shapesFor := func(first int) []*[16]int16 {
		var out []*[16]int16
		add := func(b *[16]int16) { out = append(out, b) }

		add(&[16]int16{})
		for pos := first; pos < 16; pos++ {
			for _, mag := range mags {
				b := &[16]int16{}
				b[pos] = mag
				add(b)
			}
		}
		for m := first + 1; m < 16; m++ {
			for _, prevMag := range []int16{1, 5} {
				for _, pay := range pays {
					b := &[16]int16{}
					for j := first; j < m-1; j++ {
						b[j] = 3
					}
					b[m-1] = prevMag
					b[m] = pay
					if pay == 0 && m < 15 {
						b[m+1] = 9 // carry the walk past the zero
					}
					add(b)
				}
				tail := &[16]int16{}
				for j := first; j < m-1; j++ {
					tail[j] = 3
				}
				tail[m-1] = prevMag
				tail[15] = 7 // a live end-of-block decision right after m-1
				add(tail)
			}
		}
		var dense [16]int16
		for i := range dense {
			dense[i] = int16(1 + i%2114)
		}
		add(&dense)
		return out
	}

	for plane := 0; plane < NumPlanes; plane++ {
		first := 0
		if plane == YAfterY2 {
			first = 1
		}
		for ctx := 0; ctx < NumContexts; ctx++ {
			for _, lv := range shapesFor(first) {
				got, _ := codeBlock(&DefaultProbs, plane, ctx, first, lv)
				for _, b := range got {
					hit[b.plane][b.band][b.ctx][b.node] = true
				}
			}
		}
	}
	for p := 0; p < NumPlanes; p++ {
		first := 0
		if p == YAfterY2 {
			first = 1
		}
		startBand := int(Bands[first])
		for b := 0; b < NumBands; b++ {
			// Plane YAfterY2 codes from scan position 1, whose band is
			// 1: RFC 6386 section 13.3 gives it no position-0 traffic,
			// so its band-0 cells have no codable branch.
			if p == YAfterY2 && b == 0 {
				continue
			}
			for c := 0; c < NumContexts; c++ {
				for n := 0; n < NumProbs; n++ {
					// The end-of-block flag at context 0 exists only at
					// a block's start band: every other end-of-block
					// decision follows a coded coefficient, whose
					// context update is 1 or 2, and after a zero flag
					// the next decision is another token flag, never
					// an end of block.
					if n == 0 && c == 0 && b != startBand {
						continue
					}
					if !hit[p][b][c][n] {
						t.Fatalf("cell plane=%d band=%d ctx=%d node=%d was never observed", p, b, c, n)
					}
				}
			}
		}
	}
}

// TestObservationChangesNoBytes codes identical blocks with and without
// an observer attached and requires byte-identical partitions.
func TestObservationChangesNoBytes(t *testing.T) {
	mk := func(vals ...int16) *[16]int16 {
		var a [16]int16
		copy(a[:], vals)
		return &a
	}
	shapes := []*[16]int16{mk(), mk(1, 2, 3), mk(0, 0, 90), mk(8, -8, 800)}
	for plane := 0; plane < NumPlanes; plane++ {
		for _, lv := range shapes {
			encQuiet := boolenc.New(64)
			wQuiet := NewWriter(encQuiet, &DefaultProbs)
			wQuiet.WriteBlock(plane, 1, 0, lv)

			encLoud := boolenc.New(64)
			wLoud := NewWriter(encLoud, &DefaultProbs)
			wLoud.SetObserver(&recorder{})
			wLoud.WriteBlock(plane, 1, 0, lv)

			if !bytes.Equal(encQuiet.Finish(), encLoud.Finish()) {
				t.Fatalf("plane %d levels %v: observing changed the coded bytes", plane, *lv)
			}
		}
	}
}

// TestWriteProbsNilMatchesNoUpdateStream builds the all-"no update"
// stream by hand from UpdateProbs and requires WriteProbs(nil) and
// WriteProbUpdates to produce exactly those bytes.
func TestWriteProbsNilMatchesNoUpdateStream(t *testing.T) {
	ref := boolenc.New(256)
	for i := 0; i < NumPlanes; i++ {
		for j := 0; j < NumBands; j++ {
			for k := 0; k < NumContexts; k++ {
				for l := 0; l < NumProbs; l++ {
					ref.WriteBool(UpdateProbs[i][j][k][l], false)
				}
			}
		}
	}
	want := ref.Finish()

	nilStream := boolenc.New(256)
	WriteProbs(nilStream, nil)
	if got := nilStream.Finish(); !bytes.Equal(want, got) {
		t.Fatal("WriteProbs(nil) does not match the hand-built no-update stream")
	}

	updates := boolenc.New(256)
	WriteProbUpdates(updates)
	if got := updates.Finish(); !bytes.Equal(want, got) {
		t.Fatal("WriteProbUpdates does not match the hand-built no-update stream")
	}
}

// readBackProbs decodes a section 13.4 update stream with the paired
// boolean decoder and returns the table a key-frame decoder would hold.
// It fails the test on any gate probability of zero or premature EOF.
func readBackProbs(t *testing.T, data []byte) Probs {
	t.Helper()
	d := boolenc.NewDecoder(data)
	var out Probs
	for i := 0; i < NumPlanes; i++ {
		for j := 0; j < NumBands; j++ {
			for k := 0; k < NumContexts; k++ {
				for l := 0; l < NumProbs; l++ {
					if d.ReadBool(UpdateProbs[i][j][k][l]) {
						out[i][j][k][l] = uint8(d.ReadLiteral(8))
					} else {
						out[i][j][k][l] = DefaultProbs[i][j][k][l]
					}
				}
			}
		}
	}
	if d.UnexpectedEOF() {
		t.Fatal("update stream ended early")
	}
	return out
}

// TestWriteProbsRoundTrip writes a table that differs from the defaults
// everywhere, reads it back through the independent boolean decoder, and
// requires equality. A second stream mixes defaults and changes; only
// those entries may differ after the round trip.
func TestWriteProbsRoundTrip(t *testing.T) {
	var full Probs
	for i := 0; i < NumPlanes; i++ {
		for j := 0; j < NumBands; j++ {
			for k := 0; k < NumContexts; k++ {
				for l := 0; l < NumProbs; l++ {
					full[i][j][k][l] = uint8(1 + ((i*8+j)*3+k*11+l)%254)
					if full[i][j][k][l] == DefaultProbs[i][j][k][l] {
						full[i][j][k][l] = 200
						if full[i][j][k][l] == DefaultProbs[i][j][k][l] {
							full[i][j][k][l] = 100
						}
					}
				}
			}
		}
	}
	stream := boolenc.New(512)
	WriteProbs(stream, &full)
	if got := readBackProbs(t, stream.Finish()); got != full {
		t.Fatalf("full-table round trip mismatch:\n got %v\nwant %v", got, full)
	}

	var mixed Probs = DefaultProbs
	mixed[UV][4][1][2] ^= 0x55
	if mixed[UV][4][1][2] == DefaultProbs[UV][4][1][2] {
		mixed[UV][4][1][2] = 33
	}
	mixed[Y2][0][0][0] = DefaultProbs[Y2][0][0][0]
	stream = boolenc.New(512)
	WriteProbs(stream, &mixed)
	back := readBackProbs(t, stream.Finish())
	diffs := 0
	for i := 0; i < NumPlanes; i++ {
		for j := 0; j < NumBands; j++ {
			for k := 0; k < NumContexts; k++ {
				for l := 0; l < NumProbs; l++ {
					if back[i][j][k][l] != mixed[i][j][k][l] {
						t.Fatalf("entry [%d][%d][%d][%d]: read back %d, want %d",
							i, j, k, l, back[i][j][k][l], mixed[i][j][k][l])
					}
					if back[i][j][k][l] != DefaultProbs[i][j][k][l] {
						diffs++
					}
				}
			}
		}
	}
	if diffs != 1 {
		t.Fatalf("mixed table signalled %d differences, want exactly 1", diffs)
	}
}
