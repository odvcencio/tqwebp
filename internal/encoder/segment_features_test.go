package encoder

import (
	"image"
	"strings"
	"testing"

	"m31labs.dev/tqwebp/internal/yuv"
)

// TestExtractSegmentFeatureExactBlocks pins extractSegmentFeature against
// five hand-built 16x16 luma blocks whose three statistics can be worked
// out exactly on paper. Each block lives at macroblock (0,0) of freshly
// zeroed planes from yuv.NewPlanes(16, 16), so the function reads exactly
// the samples the test wrote and nothing else.
//
// The arithmetic behind the pinned values:
//
//		Mean     = floor(sum/256) over the 256 samples.
//		Variance = sumSquares*256 - sum*sum.
//		Edge     = total absolute difference over the 240 horizontal
//		           and 240 vertical adjacent sample pairs.
//
//	 1. Flat zero: sum 0, so {Mean: 0, Variance: 0, Edge: 0}.
//	 2. Flat 73:   sum 18672 -> Mean 73; identical neighbours give no edge.
//	 3. Ramp sample(x,y)=uint8(x): sum = 16*(0+..+15) = 1920 -> Mean 7;
//	    sumSquares = 16*(0^2+..+15^2) = 16*1240 = 19840, so Variance =
//	    19840*256 - 1920^2 = 5079040 - 3686400 = 1392640; every row holds
//	    fifteen unit steps for 16*15 = 240, vertical steps are zero.
//	 4. Vertical half-plane (0 for x<8, 255 otherwise): sum = 128*255 =
//	    32640 -> Mean 127; sumSquares = 128*65025 = 8323200, so Variance =
//	    2130739200 - 1065369600 = 1065369600, the documented maximum;
//	    each row crosses once with a jump of 255 for 16*255 = 4080.
//	 5. Checkerboard ((x+y) even -> 0, odd -> 255): 128 samples of each
//	    value give sum 32640 -> Mean 127 and the same maximum Variance
//	    1065369600; every one of the 480 adjacent pairs differs by 255,
//	    reaching the documented maximum Edge 122400.
func TestExtractSegmentFeatureExactBlocks(t *testing.T) {
	fill := func(p *yuv.Planes, sample func(x, y int) uint8) {
		for dy := 0; dy < 16; dy++ {
			row := p.Y[dy*p.YStride:]
			for dx := 0; dx < 16; dx++ {
				row[dx] = sample(dx, dy)
			}
		}
	}

	flat0 := func(int, int) uint8 { return 0 }
	flat73 := func(int, int) uint8 { return 73 }
	ramp := func(x, _ int) uint8 { return uint8(x) }
	halfPlane := func(x, _ int) uint8 {
		if x < 8 {
			return 0
		}
		return 255
	}
	checkerboard := func(x, y int) uint8 {
		if (x+y)%2 == 0 {
			return 0
		}
		return 255
	}

	cases := []struct {
		name string
		samp func(x, y int) uint8
		want segmentFeature
	}{
		{"flat zero", flat0, segmentFeature{Mean: 0, Variance: 0, Edge: 0}},
		{"flat 73", flat73, segmentFeature{Mean: 73, Variance: 0, Edge: 0}},
		{"horizontal ramp", ramp, segmentFeature{Mean: 7, Variance: 1392640, Edge: 240}},
		{"vertical half-plane", halfPlane, segmentFeature{Mean: 127, Variance: 1065369600, Edge: 4080}},
		{"checkerboard", checkerboard, segmentFeature{Mean: 127, Variance: 1065369600, Edge: 122400}},
	}

	results := make([]segmentFeature, len(cases))
	for i, tc := range cases {
		p := yuv.NewPlanes(16, 16)
		fill(p, tc.samp)
		got := extractSegmentFeature(p, 0, 0)
		results[i] = got
		if got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}

	// Documented bounds: Mean fits a byte, Variance peaks at
	// 1,065,369,600 when half the samples are 0 and half are 255,
	// and Edge peaks at 480*255 = 122,400.
	const maxVariance = 1065369600 // 128*255^2*256 - (128*255)^2
	const maxEdge = 480 * 255
	for i, tc := range cases {
		if results[i].Mean > 255 {
			t.Errorf("%s: Mean %d exceeds 255", tc.name, results[i].Mean)
		}
		if results[i].Variance > maxVariance {
			t.Errorf("%s: Variance %d exceeds %d", tc.name, results[i].Variance, maxVariance)
		}
		if results[i].Edge > maxEdge {
			t.Errorf("%s: Edge %d exceeds %d", tc.name, results[i].Edge, maxEdge)
		}
	}

	// Determinism: one hundred repeat calls on the same planes must
	// reproduce the first result exactly.
	for i, tc := range cases {
		p := yuv.NewPlanes(16, 16)
		fill(p, tc.samp)
		first := extractSegmentFeature(p, 0, 0)
		for n := 0; n < 100; n++ {
			if again := extractSegmentFeature(p, 0, 0); again != first {
				t.Fatalf("%s: repeat call %d gave %+v, first call gave %+v", tc.name, n, again, first)
			}
		}
		if first != results[i] {
			t.Fatalf("%s: fresh planes gave %+v, original run gave %+v", tc.name, first, results[i])
		}
	}

	// Allocation-freedom: the extractor must not allocate.
	p := yuv.NewPlanes(16, 16)
	fill(p, checkerboard)
	if allocs := testing.AllocsPerRun(100, func() {
		extractSegmentFeature(p, 0, 0)
	}); allocs != 0 {
		t.Errorf("extractSegmentFeature allocates %.2f objects per run, want 0", allocs)
	}
}

// TestExtractSegmentFeatureRejectsInvalidInput checks that every invalid
// argument combination panics with a string message carrying the package's
// "tqwebp/encoder:" prefix.
func TestExtractSegmentFeatureRejectsInvalidInput(t *testing.T) {
	p := yuv.NewPlanes(16, 16) // MBW == MBH == 1

	cases := []struct {
		name string
		call func()
	}{
		{"nil planes", func() { extractSegmentFeature(nil, 0, 0) }},
		{"mbx negative", func() { extractSegmentFeature(p, -1, 0) }},
		{"mby negative", func() { extractSegmentFeature(p, 0, -1) }},
		{"mbx equals MBW", func() { extractSegmentFeature(p, p.MBW, 0) }},
		{"mby equals MBH", func() { extractSegmentFeature(p, 0, p.MBH) }},
	}

	for _, tc := range cases {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Errorf("%s: extractSegmentFeature did not panic", tc.name)
					return
				}
				msg, ok := r.(string)
				if !ok {
					t.Errorf("%s: panic value is %T, want string", tc.name, r)
					return
				}
				if !strings.HasPrefix(msg, "tqwebp/encoder:") {
					t.Errorf("%s: panic message %q does not start with \"tqwebp/encoder:\"", tc.name, msg)
				}
			}()
			tc.call()
		}()
	}
}

// expectedPaddedBlockFeature independently computes the segment features of
// the padded 16x16 macroblock at (mbx, mby) of a 17x17-visible-plus-pad
// frame built from img. It rebuilds the block by sampling the original
// visible Gray pixels with explicit coordinate clamping -- exactly the edge
// replication yuv.Convert applies -- and applies the documented formulas
// directly. It deliberately does not call extractSegmentFeature.
func expectedPaddedBlockFeature(img *image.Gray, mbx, mby int) segmentFeature {
	sample := func(dx, dy int) uint8 {
		x := mbx*16 + dx
		y := mby*16 + dy
		if x > 16 {
			x = 16
		}
		if y > 16 {
			y = 16
		}
		v := img.GrayAt(x, y).Y
		return yuv.RGBToY(v, v, v)
	}

	var sum, sumSquares, edge uint32
	for dy := 0; dy < 16; dy++ {
		for dx := 0; dx < 16; dx++ {
			v := uint32(sample(dx, dy))
			sum += v
			sumSquares += v * v
			if dx > 0 {
				a, b := sample(dx-1, dy), sample(dx, dy)
				if a > b {
					edge += uint32(a - b)
				} else {
					edge += uint32(b - a)
				}
			}
		}
	}
	for dx := 0; dx < 16; dx++ {
		for dy := 1; dy < 16; dy++ {
			a, b := sample(dx, dy-1), sample(dx, dy)
			if a > b {
				edge += uint32(a - b)
			} else {
				edge += uint32(b - a)
			}
		}
	}

	return segmentFeature{
		Mean:     uint8(sum >> 8),
		Variance: sumSquares*256 - sum*sum,
		Edge:     edge,
	}
}

// TestExtractSegmentFeaturePaddedOddSize converts a 17x17 Gray image whose
// last visible row and column carry a distinct pattern, then pins the
// extractor's output for every macroblock that touches padding against the
// independently computed expectation, repeating each extraction one hundred
// times.
func TestExtractSegmentFeaturePaddedOddSize(t *testing.T) {
	const size = 17 // one more than 16: forces one padded column and row

	img := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			v := uint8((x*31 + y*17 + 3*x*y) % 256)
			if x == size-1 {
				v ^= 0xAA
			}
			if y == size-1 {
				v ^= 0x55
			}
			img.Pix[y*img.Stride+x] = v
		}
	}

	p := yuv.Convert(img)
	if p.MBW != 2 || p.MBH != 2 {
		t.Fatalf("Convert(17x17) gave MBW=%d MBH=%d, want 2 and 2", p.MBW, p.MBH)
	}

	cases := []struct {
		name     string
		mbx, mby int
	}{
		{"right padded column", 1, 0},
		{"bottom padded row", 0, 1},
		{"bottom-right corner", 1, 1},
	}

	for _, tc := range cases {
		want := expectedPaddedBlockFeature(img, tc.mbx, tc.mby)
		for n := 0; n < 100; n++ {
			got := extractSegmentFeature(p, tc.mbx, tc.mby)
			if got != want {
				t.Fatalf("%s (%d,%d): run %d got %+v, want %+v", tc.name, tc.mbx, tc.mby, n, got, want)
			}
		}
	}
}
