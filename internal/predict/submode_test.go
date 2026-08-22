package predict

import (
	"crypto/sha256"
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
)

// TestSubModeStableValues pins the numeric sub-mode values. They are the
// reference numbering -- DC, TM, VE, HE, RD, VR, LD, VL, HD, HU -- shared
// by golang.org/x/image/vp8, libwebp, and the token tree below;
// reordering them would silently corrupt every future dump, tool, and
// mode-search table.
func TestSubModeStableValues(t *testing.T) {
	for _, tc := range []struct {
		m    SubMode
		want int
	}{
		{BDC, 0},
		{BTM, 1},
		{BVE, 2},
		{BHE, 3},
		{BRD, 4},
		{BVR, 5},
		{BLD, 6},
		{BVL, 7},
		{BHD, 8},
		{BHU, 9},
	} {
		if int(tc.m) != tc.want {
			t.Errorf("sub-mode %s = %d, want %d", tc.m, tc.m, tc.want)
		}
	}
	if NumSubModes != 10 {
		t.Errorf("NumSubModes = %d, want 10", NumSubModes)
	}
}

// TestSubModeString checks the RFC names every diagnostic prints.
func TestSubModeString(t *testing.T) {
	names := map[SubMode]string{
		BDC: "B_DC_PRED", BTM: "B_TM_PRED", BVE: "B_VE_PRED", BHE: "B_HE_PRED",
		BLD: "B_LD_PRED", BRD: "B_RD_PRED", BVR: "B_VR_PRED", BVL: "B_VL_PRED",
		BHD: "B_HD_PRED", BHU: "B_HU_PRED",
	}
	for m, want := range names {
		if got := m.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", m, got, want)
		}
	}
	if got := SubMode(200).String(); got != "invalid" {
		t.Errorf("invalid sub-mode String() = %q", got)
	}
}

// lcgPlane fills a plane with a deterministic spread of samples.
func lcgPlane(w, h int) []uint8 {
	p := make([]uint8, w*h)
	state := uint32(0x12345678)
	for i := range p {
		state = state*1664525 + 1013904223
		p[i] = uint8(state >> 24)
	}
	return p
}

// TestGatherSubNeighborsEdges walks the neighbourhood gather over every
// border combination: interior blocks, top row, left column, top-left
// corner, rightmost extension replication, the internal right edge below
// a macroblock's first sub-block row (macroblock top border rule), its
// frame-rightmost variant, and a stride wider than the padded width.
func TestGatherSubNeighborsEdges(t *testing.T) {
	const stride = 40 // deliberately wider than paddedWidth
	const pw = 32
	const rows = 24 // tall enough for macroblock-row-two blocks
	plane := make([]uint8, stride*rows)
	for y := 0; y < rows; y++ {
		for x := 0; x < stride; x++ {
			plane[y*stride+x] = uint8(x*3 + y*11 + 1)
		}
	}
	at := func(x, y int) uint8 { return plane[y*stride+x] }

	var nb SubNeighbors

	// Interior block with a genuine top-right extension.
	GatherSubNeighbors(&nb, plane, stride, 8, 8, pw)
	for i := 0; i < 8; i++ {
		if want := at(8+i, 7); nb.Top[i] != want {
			t.Errorf("interior Top[%d] = %d, want %d", i, nb.Top[i], want)
		}
	}
	for j := 0; j < 4; j++ {
		if want := at(7, 8+j); nb.Left[j] != want {
			t.Errorf("interior Left[%d] = %d, want %d", j, nb.Left[j], want)
		}
	}
	if nb.Corner != at(7, 7) {
		t.Errorf("interior Corner = %d, want %d", nb.Corner, at(7, 7))
	}

	// Rightmost subblock column of the frame's rightmost macroblock, in
	// its FIRST sub-block row: the extension replicates the last sample
	// of the row above the macroblock.
	GatherSubNeighbors(&nb, plane, stride, 28, 16, pw)
	for i := 0; i < 4; i++ {
		if want := at(28+i, 15); nb.Top[i] != want {
			t.Errorf("right-edge Top[%d] = %d, want %d", i, nb.Top[i], want)
		}
	}
	last := at(pw-1, 15)
	for i := 4; i < 8; i++ {
		if nb.Top[i] != last {
			t.Errorf("replicated Top[%d] = %d, want %d", i, nb.Top[i], last)
		}
	}

	// A lower sub-block row of the same top-row macroblock has a genuine
	// row above, but its crossing extension reads MissingTop all the
	// same: the macroblock top border on the frame's top row is filled
	// with MissingTop, never with pixels of the unreconstructed right
	// macroblock.
	GatherSubNeighbors(&nb, plane, stride, 28, 8, pw)
	for i := 4; i < 8; i++ {
		if nb.Top[i] != MissingTop {
			t.Errorf("top-row MB lower subrow Top[%d] = %d, want %d", i, nb.Top[i], MissingTop)
		}
	}

	// Top row: everything above reads MissingTop, including the corner,
	// whatever the column is, while the left column stays genuine.
	GatherSubNeighbors(&nb, plane, stride, 8, 0, pw)
	for i := range nb.Top {
		if nb.Top[i] != MissingTop {
			t.Errorf("top row Top[%d] = %d, want %d", i, nb.Top[i], MissingTop)
		}
	}
	if nb.Corner != MissingTop {
		t.Errorf("top row Corner = %d, want %d", nb.Corner, MissingTop)
	}
	for j := 0; j < 4; j++ {
		if want := at(7, j); nb.Left[j] != want {
			t.Errorf("top row Left[%d] = %d, want %d", j, nb.Left[j], want)
		}
	}

	// Leftmost column with a row above: the left column and the corner
	// both read MissingLeft -- what the decoder seeds its workspace
	// column with -- while the row above stays genuine.
	GatherSubNeighbors(&nb, plane, stride, 0, 8, pw)
	for j := range nb.Left {
		if nb.Left[j] != MissingLeft {
			t.Errorf("left col Left[%d] = %d, want %d", j, nb.Left[j], MissingLeft)
		}
	}
	if nb.Corner != MissingLeft {
		t.Errorf("left col Corner = %d, want %d", nb.Corner, MissingLeft)
	}
	for i := 0; i < 8; i++ {
		if want := at(i, 7); nb.Top[i] != want {
			t.Errorf("left col Top[%d] = %d, want %d", i, nb.Top[i], want)
		}
	}

	// Top-left block: the top fill wins the corner.
	GatherSubNeighbors(&nb, plane, stride, 0, 0, pw)
	if nb.Corner != MissingTop {
		t.Errorf("top-left Corner = %d, want %d", nb.Corner, MissingTop)
	}
	for j := range nb.Left {
		if nb.Left[j] != MissingLeft {
			t.Errorf("top-left Left[%d] = %d", j, nb.Left[j])
		}
	}

	// Narrow padded width: an extension beyond it replicates.
	const narrow = 20
	GatherSubNeighbors(&nb, plane, stride, 16, 4, narrow)
	if want := at(narrow-1, 3); nb.Top[4] != want || nb.Top[7] != want {
		t.Errorf("narrow replication Top[4]=%d Top[7]=%d, want %d", nb.Top[4], nb.Top[7], want)
	}

	// Internal right edge, below the macroblock's first sub-block row:
	// the extension would reach into the macroblock to the right, which
	// a raster walk has not reconstructed yet, so it repeats the
	// macroblock top border extension -- row 15 here, not row 19.
	GatherSubNeighbors(&nb, plane, stride, 12, 20, pw) // block (i=3, j=1) of MB (0,1)
	for i := 0; i < 4; i++ {
		if want := at(12+i, 19); nb.Top[i] != want {
			t.Errorf("internal edge Top[%d] = %d, want %d", i, nb.Top[i], want)
		}
	}
	for i := 4; i < 8; i++ {
		if want := at(12+i, 15); nb.Top[i] != want {
			t.Errorf("internal edge Top[%d] = %d, want %d from the macroblock top border", i, nb.Top[i], want)
		}
	}

	// The same edge in the frame's rightmost macroblock: the macroblock
	// top border itself ends in one replicated sample, and that value --
	// not the current row above's last sample -- fills the extension.
	GatherSubNeighbors(&nb, plane, stride, 28, 20, pw)
	borderLast := at(pw-1, 15)
	rowLast := at(pw-1, 19)
	if borderLast == rowLast {
		t.Fatalf("test plane must distinguish border row %d from row 19", 15)
	}
	for i := 4; i < 8; i++ {
		if nb.Top[i] != borderLast {
			t.Errorf("right MB edge Top[%d] = %d, want %d", i, nb.Top[i], borderLast)
		}
	}
	for i := 0; i < 4; i++ {
		if want := at(28+i, 19); nb.Top[i] != want {
			t.Errorf("right MB edge Top[%d] = %d, want %d", i, nb.Top[i], want)
		}
	}

	// A lower sub-block row of the top macroblock row has a genuine row
	// above for its own four samples, but its right-edge extension still
	// reads MissingTop: the macroblock top border on the frame's top row
	// is all MissingTop, whatever pixels sit to the right.
	GatherSubNeighbors(&nb, plane, stride, 12, 4, pw)
	for i := 0; i < 4; i++ {
		if want := at(12+i, 3); nb.Top[i] != want {
			t.Errorf("top MB lower row Top[%d] = %d, want %d", i, nb.Top[i], want)
		}
	}
	for i := 4; i < 8; i++ {
		if nb.Top[i] != MissingTop {
			t.Errorf("top MB lower row Top[%d] = %d, want %d", i, nb.Top[i], MissingTop)
		}
	}
}

// refNB is the reference neighbourhood: plain ints, no shared code with
// SubNeighbors beyond the RFC itself.
type refNB struct {
	top    [8]int32
	left   [4]int32
	corner int32
}

// refGather is an independent transcription of golang.org/x/image/vp8's
// neighbourhood preparation (its prepareYBR), written against a raw
// plane. The property test below therefore fails only if production and
// reference disagree; it cannot pass because of a shared helper.
func refGather(plane []uint8, stride, x0, y0, pw int) refNB {
	var nb refNB
	if y0 > 0 {
		for i := 0; i < 4; i++ {
			nb.top[i] = int32(plane[(y0-1)*stride+x0+i])
		}
		if x0%16 != 12 || y0%16 == 0 {
			// Extension inside the block's macroblock, or crossing on
			// the macroblock's first sub-block row, where it still comes
			// from the reconstructed row above the macroblock.
			if x0+8 <= pw {
				for i := 0; i < 4; i++ {
					nb.top[4+i] = int32(plane[(y0-1)*stride+x0+4+i])
				}
			} else {
				v := int32(plane[(y0-1)*stride+pw-1])
				nb.top[4], nb.top[5], nb.top[6], nb.top[7] = v, v, v, v
			}
		} else if mbTop := y0 &^ 15; mbTop == 0 {
			// Lower sub-block row of the top macroblock row: prepareYBR
			// leaves every extension slot at its MissingTop seed.
			for i := 4; i < 8; i++ {
				nb.top[i] = 127
			}
		} else {
			// Rightmost sub-block column below the first row: the
			// extension slots repeat the macroblock top border extension,
			// never the unreconstructed macroblock to the right.
			border := (mbTop - 1) * stride
			if x0+8 <= pw {
				for i := 0; i < 4; i++ {
					nb.top[4+i] = int32(plane[border+x0+4+i])
				}
			} else {
				v := int32(plane[border+pw-1])
				nb.top[4], nb.top[5], nb.top[6], nb.top[7] = v, v, v, v
			}
		}
	} else {
		for i := range nb.top {
			nb.top[i] = 127
		}
	}
	for j := 0; j < 4; j++ {
		if x0 > 0 {
			nb.left[j] = int32(plane[(y0+j)*stride+x0-1])
		} else {
			nb.left[j] = 129
		}
	}
	switch {
	case y0 == 0:
		nb.corner = 127
	case x0 == 0:
		nb.corner = 129
	default:
		nb.corner = int32(plane[(y0-1)*stride+x0-1])
	}
	return nb
}

func refAvg2(a, b int32) int32    { return (a + b + 1) / 2 }
func refAvg3(a, b, c int32) int32 { return (a + 2*b + c + 2) / 4 }

func refClip(v int32) int32 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// refPredict mirrors predFunc4 of x/image/vp8 for one sub-mode.
func refPredict(m SubMode, nb refNB) [16]int32 {
	var out [16]int32
	set := func(y, x int, v int32) { out[y*4+x] = refClip(v) }
	switch m {
	case BDC:
		sum := int32(4)
		for i := 0; i < 4; i++ {
			sum += nb.top[i] + nb.left[i]
		}
		v := sum / 8
		for i := range out {
			out[i] = refClip(v)
		}
	case BTM:
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				set(j, i, nb.top[i]+nb.left[j]-nb.corner)
			}
		}
	case BVE:
		a, b, c, d, e, f := nb.corner, nb.top[0], nb.top[1], nb.top[2], nb.top[3], nb.top[4]
		cols := [4]int32{refAvg3(a, b, c), refAvg3(b, c, d), refAvg3(c, d, e), refAvg3(d, e, f)}
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				set(j, i, cols[i])
			}
		}
	case BHE:
		a, p, q, r, s := nb.corner, nb.left[0], nb.left[1], nb.left[2], nb.left[3]
		rows := [4]int32{refAvg3(a, p, q), refAvg3(r, q, p), refAvg3(s, r, q), refAvg3(s, s, r)}
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				set(j, i, rows[j])
			}
		}
	case BLD:
		a, b, c, d, e, f, g, h := nb.top[0], nb.top[1], nb.top[2], nb.top[3], nb.top[4], nb.top[5], nb.top[6], nb.top[7]
		abc, bcd, cde, def := refAvg3(a, b, c), refAvg3(b, c, d), refAvg3(c, d, e), refAvg3(d, e, f)
		efg, fgh, ghh := refAvg3(e, f, g), refAvg3(f, g, h), refAvg3(g, h, h)
		grid := [4][4]int32{{abc, bcd, cde, def}, {bcd, cde, def, efg}, {cde, def, efg, fgh}, {def, efg, fgh, ghh}}
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				set(j, i, grid[j][i])
			}
		}
	case BVL:
		a, b, c, d, e, f, g, h := nb.top[0], nb.top[1], nb.top[2], nb.top[3], nb.top[4], nb.top[5], nb.top[6], nb.top[7]
		ab, bc, cd, de := refAvg2(a, b), refAvg2(b, c), refAvg2(c, d), refAvg2(d, e)
		abc, bcd, cde, def := refAvg3(a, b, c), refAvg3(b, c, d), refAvg3(c, d, e), refAvg3(d, e, f)
		efg, fgh := refAvg3(e, f, g), refAvg3(f, g, h)
		grid := [4][4]int32{{ab, bc, cd, de}, {abc, bcd, cde, def}, {bc, cd, de, efg}, {bcd, cde, def, fgh}}
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				set(j, i, grid[j][i])
			}
		}
	case BRD:
		s, r, q, p := nb.left[3], nb.left[2], nb.left[1], nb.left[0]
		a, b, c, d, e := nb.corner, nb.top[0], nb.top[1], nb.top[2], nb.top[3]
		srq, rqp, qpa, pab := refAvg3(s, r, q), refAvg3(r, q, p), refAvg3(q, p, a), refAvg3(p, a, b)
		abc, bcd, cde := refAvg3(a, b, c), refAvg3(b, c, d), refAvg3(c, d, e)
		grid := [4][4]int32{{pab, abc, bcd, cde}, {qpa, pab, abc, bcd}, {rqp, qpa, pab, abc}, {srq, rqp, qpa, pab}}
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				set(j, i, grid[j][i])
			}
		}
	case BVR:
		r, q, p := nb.left[2], nb.left[1], nb.left[0]
		a, b, c, d, e := nb.corner, nb.top[0], nb.top[1], nb.top[2], nb.top[3]
		ab, bc, cd, de := refAvg2(a, b), refAvg2(b, c), refAvg2(c, d), refAvg2(d, e)
		rqp, qpa, pab := refAvg3(r, q, p), refAvg3(q, p, a), refAvg3(p, a, b)
		abc, bcd, cde := refAvg3(a, b, c), refAvg3(b, c, d), refAvg3(c, d, e)
		grid := [4][4]int32{{ab, bc, cd, de}, {pab, abc, bcd, cde}, {qpa, ab, bc, cd}, {rqp, pab, abc, bcd}}
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				set(j, i, grid[j][i])
			}
		}
	case BHD:
		s, r, q, p := nb.left[3], nb.left[2], nb.left[1], nb.left[0]
		a, b, c, d := nb.corner, nb.top[0], nb.top[1], nb.top[2]
		sr, rq, qp, pa := refAvg2(s, r), refAvg2(r, q), refAvg2(q, p), refAvg2(p, a)
		srq, rqp, qpa, pab := refAvg3(s, r, q), refAvg3(r, q, p), refAvg3(q, p, a), refAvg3(p, a, b)
		abc, bcd := refAvg3(a, b, c), refAvg3(b, c, d)
		grid := [4][4]int32{{pa, pab, abc, bcd}, {qp, qpa, pa, pab}, {rq, rqp, qp, qpa}, {sr, srq, rq, rqp}}
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				set(j, i, grid[j][i])
			}
		}
	case BHU:
		s, r, q, p := nb.left[3], nb.left[2], nb.left[1], nb.left[0]
		pq, qr, rs := refAvg2(p, q), refAvg2(q, r), refAvg2(r, s)
		pqr, qrs, rss := refAvg3(p, q, r), refAvg3(q, r, s), refAvg3(r, s, s)
		grid := [4][4]int32{{pq, pqr, qr, qrs}, {qr, qrs, rs, rss}, {rs, rss, s, s}, {s, s, s, s}}
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				set(j, i, grid[j][i])
			}
		}
	}
	return out
}

// TestPredictSubMatchesReference compares all ten scalar predictors
// against the independently transcribed decoder reference over a sweep
// of plane positions that covers interior blocks, every border, and the
// right-edge replication rule.
func TestPredictSubMatchesReference(t *testing.T) {
	const stride = 29
	const pw = 24
	plane := lcgPlane(stride, 20)

	var nb SubNeighbors
	dst := make([]uint8, 4*stride)
	for y0 := 0; y0+4 <= 20; y0 += 3 {
		for x0 := 0; x0 < pw; x0 += 3 {
			GatherSubNeighbors(&nb, plane, stride, x0, y0, pw)
			ref := refGather(plane, stride, x0, y0, pw)
			for m := SubMode(0); m < NumSubModes; m++ {
				for i := range dst {
					dst[i] = 0xAA // poison: every cell must be written
				}
				PredictSub(dst, stride, m, &nb)
				want := refPredict(m, ref)
				for j := 0; j < 4; j++ {
					for i := 0; i < 4; i++ {
						got := dst[j*stride+i]
						if uint8(want[j*4+i]) != got {
							t.Fatalf("mode %s at (%d,%d) cell (%d,%d): got %d want %d",
								m, x0, y0, j, i, got, want[j*4+i])
						}
					}
				}
			}
		}
	}
}

// canonicalNeighborhoods are the fixed inputs behind the golden digest.
func canonicalNeighborhoods() []SubNeighbors {
	return []SubNeighbors{
		{Top: [8]uint8{10, 20, 30, 40, 50, 60, 70, 80}, Left: [4]uint8{90, 100, 110, 120}, Corner: 5},
		{Top: [8]uint8{127, 127, 127, 127, 127, 127, 127, 127}, Left: [4]uint8{129, 129, 129, 129}, Corner: 127},
		{Top: [8]uint8{255, 0, 128, 1, 254, 3, 127, 2}, Left: [4]uint8{200, 50, 220, 40}, Corner: 99},
		{Top: [8]uint8{7, 7, 7, 7, 7, 7, 7, 7}, Left: [4]uint8{9, 9, 9, 9}, Corner: 11},
		{Top: [8]uint8{}, Left: [4]uint8{}, Corner: 255},
	}
}

// digestOutputs hashes every predictor output for the canonical inputs.
func digestOutputs() [32]byte {
	h := sha256.New()
	var nb SubNeighbors
	dst := make([]uint8, 4*7) // nontrivial stride
	for _, n := range canonicalNeighborhoods() {
		nb = n
		for m := SubMode(0); m < NumSubModes; m++ {
			for i := range dst {
				dst[i] = 0xAA
			}
			PredictSub(dst, 7, m, &nb)
			h.Write([]byte{byte(m)})
			for y := 0; y < 4; y++ {
				h.Write(dst[y*7 : y*7+4])
			}
		}
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// hexDigest formats a sha256 sum as lowercase hex.
func hexDigest(sum [32]byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 64)
	for i, v := range sum {
		out[2*i] = digits[v>>4]
		out[2*i+1] = digits[v&15]
	}
	return string(out)
}

// probDigest hashes the contextual table in stable order.
func probDigest() string {
	h := sha256.New()
	for _, row := range KeyFrameSubModeProbs {
		for _, cell := range row {
			var b [9]byte
			copy(b[:], cell[:])
			h.Write(b[:])
		}
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return hexDigest(sum)
}

// TestPredictSubGoldenDigest pins exact predictor outputs. Update the
// constant only together with the differential decoder test.
func TestPredictSubGoldenDigest(t *testing.T) {
	const want = "156f928e2147cd51f9989a76d61cd4a7403ca292fd8ea6f2e433420f0266a215"
	if len(want) != 64 {
		t.Fatalf("golden digest not pinned yet; current value %s", hexDigest(digestOutputs()))
	}
	if got := hexDigest(digestOutputs()); got != want {
		t.Fatalf("predictor outputs changed: got %s want %s", got, want)
	}
}

// TestKeyFrameSubModeProbDigest pins the contextual probability table in
// native order -- extracted verbatim from golang.org/x/image/vp8's predProb
// table -- after spot-checking entries against that source.
func TestKeyFrameSubModeProbDigest(t *testing.T) {
	checks := []struct {
		above, left SubMode
		want        [9]uint8
	}{
		{BDC, BDC, [9]uint8{231, 120, 48, 89, 115, 113, 120, 152, 112}},
		{BLD, BDC, [9]uint8{125, 98, 42, 88, 104, 85, 117, 175, 82}},
		{BRD, BDC, [9]uint8{138, 31, 36, 171, 27, 166, 38, 44, 229}},
		{BVR, BDC, [9]uint8{104, 55, 44, 218, 9, 54, 53, 130, 226}},
		{BHU, BHU, [9]uint8{112, 19, 12, 61, 195, 128, 48, 4, 24}},
	}
	for _, c := range checks {
		if KeyFrameSubModeProbs[c.above][c.left] != c.want {
			t.Errorf("probs[%s][%s] = %v, want %v", c.above, c.left,
				KeyFrameSubModeProbs[c.above][c.left], c.want)
		}
	}
	for a := SubMode(0); a < NumSubModes; a++ {
		for l := SubMode(0); l < NumSubModes; l++ {
			for _, p := range KeyFrameSubModeProbs[a][l] {
				if p == 0 {
					t.Fatalf("probs[%s][%s] contains 0, which cannot be coded", a, l)
				}
			}
		}
	}

	const want = "6684aedff5b28fd6c97f8f8436504e16318d5e180990dee4f082eded83bcd99c"
	got := probDigest()
	if len(want) != 64 {
		t.Logf("probability digest not pinned yet; current value %s", got)
		return
	}
	if got != want {
		t.Fatalf("key-frame probability table changed: got %s want %s", got, want)
	}
}

// decisionRecorder captures what WriteSubMode emits.
type decisionRecorder struct {
	steps []decision
}

type decision struct {
	prob uint8
	bit  bool
}

func (r *decisionRecorder) WriteBool(prob uint8, bit bool) {
	r.steps = append(r.steps, decision{prob, bit})
}

// TestWriteSubModeSequences checks the tree-derived decision sequence of
// every sub-mode: which probability slot each decision uses and which
// bit it carries.
func TestWriteSubModeSequences(t *testing.T) {
	cases := []struct {
		m       SubMode
		probIdx []int
		bits    []bool
	}{
		{BDC, []int{0}, []bool{false}},
		{BTM, []int{0, 1}, []bool{true, false}},
		{BVE, []int{0, 1, 2}, []bool{true, true, false}},
		{BHE, []int{0, 1, 2, 3, 4}, []bool{true, true, true, false, false}},
		{BRD, []int{0, 1, 2, 3, 4, 5}, []bool{true, true, true, false, true, false}},
		{BVR, []int{0, 1, 2, 3, 4, 5}, []bool{true, true, true, false, true, true}},
		{BLD, []int{0, 1, 2, 3, 6}, []bool{true, true, true, true, false}},
		{BVL, []int{0, 1, 2, 3, 6, 7}, []bool{true, true, true, true, true, false}},
		{BHD, []int{0, 1, 2, 3, 6, 7, 8}, []bool{true, true, true, true, true, true, false}},
		{BHU, []int{0, 1, 2, 3, 6, 7, 8}, []bool{true, true, true, true, true, true, true}},
	}
	const above, left = BHE, BVR
	row := &KeyFrameSubModeProbs[above][left]

	for _, tc := range cases {
		var rec decisionRecorder
		WriteSubMode(&rec, above, left, tc.m)
		if len(rec.steps) != len(tc.probIdx) {
			t.Errorf("%s: emitted %d decisions, want %d", tc.m, len(rec.steps), len(tc.probIdx))
			continue
		}
		for i, d := range rec.steps {
			wantProb := row[tc.probIdx[i]]
			if d.prob != wantProb || d.bit != tc.bits[i] {
				t.Errorf("%s step %d: got (%d,%t), want (%d,%t)",
					tc.m, i, d.prob, d.bit, wantProb, tc.bits[i])
			}
		}
	}
}

// TestWriteSubModeRoundTrip codes all thousand (above,left,mode)
// combinations into one boolean stream and decodes them back through the
// tree, requiring the original sub-modes. This proves writer, tree, and
// boolean coder agree without any external decoder involved.
func TestWriteSubModeRoundTrip(t *testing.T) {
	enc := boolenc.New(1 << 16)
	type triple struct {
		a, l, m SubMode
	}
	var seq []triple
	for a := SubMode(0); a < NumSubModes; a++ {
		for l := SubMode(0); l < NumSubModes; l++ {
			for m := SubMode(0); m < NumSubModes; m++ {
				WriteSubMode(enc, a, l, m)
				seq = append(seq, triple{a, l, m})
			}
		}
	}
	dec := boolenc.NewDecoder(enc.Finish())
	for _, tr := range seq {
		p := &KeyFrameSubModeProbs[tr.a][tr.l]
		node := 0
		for {
			i := node / 2
			if dec.ReadBool(p[i]) {
				node = int(SubModeTree[node+1])
			} else {
				node = int(SubModeTree[node])
			}
			if node <= 0 {
				if got := SubMode(-node); got != tr.m {
					t.Fatalf("(%s,%s): decoded %s, want %s", tr.a, tr.l, got, tr.m)
				}
				break
			}
		}
	}
	if dec.UnexpectedEOF() {
		t.Fatal("boolean decoder ran past the end of the stream")
	}
}
