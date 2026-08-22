package encoder

import (
	"bytes"
	"fmt"
	"image"
	"math/rand/v2"
	"runtime"
	"testing"

	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/yuv"
	"m31labs.dev/tqwebp/oracle"
)

// This file holds the work package WP-2 slice 2B tests: the conservative
// detailed-block selector behind the Method 5/6 effort boundary. The
// fixtures are built from two kinds of content:
//
//   - Detail: white noise smoothed by five 3x3 box passes, whose
//     sample-to-sample correlation dies well before a whole-block
//     predictor's nearest inputs eight samples out. This is the content
//     the detailed-block rule must adopt.
//   - Flat fill: one constant level, which the whole-block modes fit
//     exactly away from the frame border, so no candidate can halve a
//     zero error and the rule must keep those macroblocks whole.
//
// Mixing the two in one frame pins both sides of the rule in a single
// encode, and every exact-match check below runs the bytes through
// golang.org/x/image/vp8, the independent decoder.

// bpredDetailRGBA builds a w by h image of white noise smoothed by five
// passes of 3x3 box averaging. That leaves texture whose sample-to-
// sample correlation a 4x4 predictor exploits -- it reads its twelve
// immediate neighbours -- while a whole-block predictor, whose nearest
// inputs sit eight samples out along each border, still finds nothing
// to copy. On this content the sixteen-block candidate's error lands
// near a third of the whole-block error, well under the rule's half,
// at every quality tested. The construction is integer-only over a
// seeded PCG stream, so it is identical on every platform.
func bpredDetailRGBA(w, h int, seed uint64) *image.RGBA {
	r := rand.New(rand.NewPCG(seed, uint64(w)*1000+uint64(h)))
	samples := make([]uint8, w*h)
	for i := range samples {
		samples[i] = uint8(r.UintN(256))
	}
	for pass := 0; pass < 5; pass++ {
		samples = boxBlur3(samples, w, h)
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := samples[y*w+x]
			i := (y*w + x) * 4
			img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c, c, c, 0xff
		}
	}
	return img
}

// boxBlur3 averages every sample with its edge-clamped 3x3
// neighbourhood.
func boxBlur3(src []uint8, w, h int) []uint8 {
	dst := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sum := 0
			for dy := -1; dy <= 1; dy++ {
				yy := y + dy
				if yy < 0 {
					yy = 0
				} else if yy >= h {
					yy = h - 1
				}
				for dx := -1; dx <= 1; dx++ {
					xx := x + dx
					if xx < 0 {
						xx = 0
					} else if xx >= w {
						xx = w - 1
					}
					sum += int(src[yy*w+xx])
				}
			}
			dst[y*w+x] = uint8((sum + 5) / 9)
		}
	}
	return dst
}

// bpredMixedRGBA builds a w by h image whose left macroblock columns are
// flat fill and whose right macroblock columns are blurred-noise detail,
// split at a macroblock boundary so whole-block and B_PRED macroblocks
// share one frame. It needs w >= 48 to leave at least one macroblock
// column of each kind.
func bpredMixedRGBA(w, h int, seed uint64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	detail := bpredDetailRGBA(w, h, seed)
	split := (w / 32) * 16
	for y := 0; y < h; y++ {
		for x := 0; x < split; x++ {
			i := (y*w + x) * 4
			img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 100, 100, 100, 0xff
		}
		for x := split; x < w; x++ {
			src := detail.Pix[(y*w+x)*4:]
			i := (y*w + x) * 4
			img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = src[0], src[1], src[2], 0xff
		}
	}
	return img
}

// encodeWithMethod runs the full production pipeline -- the forced path
// untouched -- and returns the encoder, the file bytes, and the count of
// macroblocks the selector left on the B_PRED path.
func encodeWithMethod(m image.Image, cfg Config) (*encoder, []byte, int, error) {
	enc := newEncoder(yuv.Convert(m), cfg)
	enc.run()
	var buf writerBuffer
	err := enc.writeFile(&buf)
	selected := 0
	for i := range enc.mbs {
		if enc.mbs[i].bpred {
			selected++
		}
	}
	return enc, buf.data, selected, err
}

// touchesFrameBorder reports whether the macroblock at (mbx, mby) of an
// mbw by mbh grid touches the picture border, where the RFC's missing-
// sample fills stand in for real neighbours and even flat content leaves
// the whole-block modes a nonzero error to beat.
func touchesFrameBorder(mbx, mby, mbw, mbh int) bool {
	return mbx == 0 || mby == 0 || mbx == mbw-1 || mby == mbh-1
}

// TestBPredClearlyWinsMargin pins the detailed-block rule itself: the
// candidate wins only when its error is strictly below half of the
// whole-block error, so equal errors, exact halves, and near-misses all
// keep the whole block. Strict integer comparison is what makes the rule
// deterministic.
func TestBPredClearlyWinsMargin(t *testing.T) {
	cases := []struct {
		whole, candidate int64
		want             bool
	}{
		{0, 0, false},
		{0, 1, false},
		{100, 50, false}, // exactly half: not clearly better
		{100, 49, true},
		{100, 100, false}, // a tie keeps the whole block
		{7, 3, true},
		{7, 4, false},
		{1 << 20, (1 << 19) - 1, true},
	}
	for _, c := range cases {
		if got := bPredClearlyWins(c.whole, c.candidate); got != c.want {
			t.Errorf("bPredClearlyWins(whole=%d, candidate=%d) = %v, want %v",
				c.whole, c.candidate, got, c.want)
		}
	}
}

// TestDetailedBlockEffortBoundary pins the effort boundary on detailed
// content: methods 0 to 4 never select B_PRED and all five write the
// same bytes; methods 5 and 6 do select it, their bytes differ from the
// boundary's, and both still decode exactly.
func TestDetailedBlockEffortBoundary(t *testing.T) {
	img := bpredDetailRGBA(48, 32, 101)

	encode := func(method int) ([]byte, int, error) {
		_, data, selected, err := encodeWithMethod(img, Config{Quality: 75, Method: method})
		return data, selected, err
	}

	belowBytes, belowSelected, err := encode(4)
	if err != nil {
		t.Fatalf("encode at method 4: %v", err)
	}
	if belowSelected != 0 {
		t.Fatalf("method 4 selected %d B_PRED macroblocks, want 0", belowSelected)
	}
	for _, method := range []int{0, 1, 2, 3, 4} {
		data, selected, err := encode(method)
		if err != nil {
			t.Fatalf("encode at method %d: %v", method, err)
		}
		if selected != 0 {
			t.Errorf("method %d selected %d B_PRED macroblocks, want 0", method, selected)
		}
		if !bytes.Equal(data, belowBytes) {
			t.Errorf("method %d bytes differ from method 4 bytes", method)
		}
	}

	for _, method := range []int{5, 6} {
		data, selected, err := encode(method)
		if err != nil {
			t.Fatalf("encode at method %d: %v", method, err)
		}
		if selected == 0 {
			t.Errorf("method %d selected no B_PRED macroblock on detailed content", method)
		}
		if bytes.Equal(data, belowBytes) {
			t.Errorf("method %d bytes equal the method 4 bytes despite a selection", method)
		}
		_, recon, err := EncodeWithReconstruction(img, Config{Quality: 75, Method: method})
		if err != nil {
			t.Fatalf("encode with reconstruction at method %d: %v", method, err)
		}
		checkBPredExact(t, data, recon)
	}
}

// TestDetailedBlockSelectionExactMixedFrames encodes mixed frames --
// flat and detail macroblocks side by side, several macroblocks per
// row, odd sizes at the borders -- through the production path at
// methods 5 and 6, and requires the independent decoder to reproduce
// the encoder's own reconstruction byte for byte. It also inspects the
// records: selected macroblocks own no Y2 block, unselected ones keep a
// valid whole-block mode, and both kinds appear in every frame.
func TestDetailedBlockSelectionExactMixedFrames(t *testing.T) {
	sizes := [][2]int{{48, 32}, {33, 17}, {97, 61}}
	for _, s := range sizes {
		w, h := s[0], s[1]
		for _, q := range []int{35, 75, 90} {
			for _, method := range []int{5, 6} {
				name := fmt.Sprintf("%dx%d/q%d/m%d", w, h, q, method)
				t.Run(name, func(t *testing.T) {
					img := bpredMixedRGBA(w, h, 77)
					enc, data, selected, err := encodeWithMethod(img, Config{Quality: q, Method: method})
					if err != nil {
						t.Fatalf("encode: %v", err)
					}
					checkBPredExact(t, data, enc.reconstruction())

					if selected == 0 {
						t.Fatal("no macroblock took the B_PRED path on the detail side")
					}
					if selected == len(enc.mbs) {
						t.Fatal("every macroblock took the B_PRED path; the frame is not mixed")
					}
					for i := range enc.mbs {
						mb := &enc.mbs[i]
						if mb.bpred {
							if mb.nz[blockY2] {
								t.Errorf("macroblock %d marked the Y2 block nonzero", i)
							}
							for _, v := range mb.levels[blockY2] {
								if v != 0 {
									t.Fatalf("macroblock %d holds Y2 levels in a B_PRED record", i)
								}
							}
						} else if mb.yMode < predict.DC || mb.yMode >= predict.NumModes {
							t.Fatalf("macroblock %d chose an out-of-range luma mode", i)
						}
					}

					decoded, err := oracle.DecodeWebPPlanes(data)
					if err != nil {
						t.Fatalf("decode: %v", err)
					}
					if got := decoded.Rect.Size(); got.X != w || got.Y != h {
						t.Errorf("decoded size %v, want %dx%d", got, w, h)
					}
				})
			}
		}
	}
}

// TestDetailedBlockDeterminism proves the selector deterministic: the
// same input writes the same bytes on repeated runs and at every value
// of GOMAXPROCS, at both method 5 and method 6.
func TestDetailedBlockDeterminism(t *testing.T) {
	img := bpredMixedRGBA(48, 32, 77)
	encode := func(method int) []byte {
		_, data, selected, err := encodeWithMethod(img, Config{Quality: 68, Method: method})
		if err != nil {
			t.Fatalf("encode at method %d: %v", method, err)
		}
		if selected == 0 {
			t.Fatalf("method %d selected nothing; determinism would prove nothing", method)
		}
		return data
	}

	want5 := encode(5)
	want6 := encode(6)
	if got := encode(5); !bytes.Equal(got, want5) {
		t.Error("two method 5 runs at the same settings produced different bytes")
	}
	if got := encode(6); !bytes.Equal(got, want6) {
		t.Error("two method 6 runs at the same settings produced different bytes")
	}

	restore := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(restore)
	for _, procs := range []int{1, 2, 4, 8} {
		runtime.GOMAXPROCS(procs)
		if got := encode(5); !bytes.Equal(got, want5) {
			t.Errorf("method 5 at GOMAXPROCS %d produced different bytes", procs)
		}
		if got := encode(6); !bytes.Equal(got, want6) {
			t.Errorf("method 6 at GOMAXPROCS %d produced different bytes", procs)
		}
	}
}
