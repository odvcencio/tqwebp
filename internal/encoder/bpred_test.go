package encoder

import (
	"bytes"
	"fmt"
	"image"
	"math/rand/v2"
	"runtime"
	"testing"

	"m31labs.dev/turboquant/blockdsp"

	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/yuv"
	"m31labs.dev/tqwebp/oracle"
)

// This file holds the work package WP-2 slice 2A tests: the forced B_PRED
// macroblock path. Every test encodes with forceBPred set, decodes the
// bytes with golang.org/x/image/vp8 through the oracle, and requires
// byte-exact equality with the encoder's own reconstruction. The default
// production path keeps its own tests elsewhere in the package; nothing
// here touches it.

// bpredNoiseRGBA builds a deterministic w by h RGBA image whose pixels
// vary sample to sample, so quantized 4x4 residuals are nonzero almost
// everywhere.
func bpredNoiseRGBA(w, h int, seed uint64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(seed, uint64(w)*1000+uint64(h)))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0] = uint8(r.UintN(256))
		img.Pix[i+1] = uint8(r.UintN(256))
		img.Pix[i+2] = uint8(r.UintN(256))
		img.Pix[i+3] = 0xff
	}
	return img
}

// encodeForcedBPred runs the full pipeline with every macroblock forced
// onto the B_PRED path and returns the encoder, the file bytes, and the
// encoder's own reconstruction.
func encodeForcedBPred(m image.Image, cfg Config) (*encoder, []byte, *image.YCbCr, error) {
	enc := newEncoder(yuv.Convert(m), cfg)
	enc.forceBPred = true
	enc.run()
	var buf writerBuffer
	err := enc.writeFile(&buf)
	return enc, buf.data, enc.reconstruction(), err
}

// checkBPredExact decodes data with the independent decoder and requires
// byte-exact equality, on every plane, with the encoder's reconstruction.
func checkBPredExact(t *testing.T, data []byte, recon *image.YCbCr) {
	t.Helper()
	decoded, err := oracle.DecodeWebPPlanes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := oracle.CompareExact(reconSource{recon}, decoded); err != nil {
		t.Fatalf("exact reconstruction: %v", err)
	}
}

// TestForcedBPredExactMultiMacroblock decodes forced multi-macroblock
// frames whose residuals are nonzero almost everywhere. It also inspects
// the macroblock records directly: every macroblock must carry the B_PRED
// mark, no macroblock may own a Y2 record, and the luma blocks must
// actually carry coefficients.
func TestForcedBPredExactMultiMacroblock(t *testing.T) {
	for _, size := range [][2]int{{48, 32}, {64, 48}} {
		for _, q := range []int{35, 75, 90} {
			img := bpredNoiseRGBA(size[0], size[1], 101)
			t.Run(fmt.Sprintf("%dx%d/q%d", size[0], size[1], q), func(t *testing.T) {
				enc, data, recon, err := encodeForcedBPred(img, Config{Quality: q})
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				checkBPredExact(t, data, recon)

				nonZeroLuma := 0
				for i := range enc.mbs {
					mb := &enc.mbs[i]
					if !mb.bpred {
						t.Fatalf("macroblock %d did not take the B_PRED path", i)
					}
					if mb.nz[blockY2] {
						t.Errorf("macroblock %d marked the Y2 block nonzero", i)
					}
					for _, v := range mb.levels[blockY2] {
						if v != 0 {
							t.Fatalf("macroblock %d holds Y2 levels in a B_PRED record", i)
						}
					}
					for b := 0; b < 16; b++ {
						if mb.nz[blockLuma+b] {
							nonZeroLuma++
						}
					}
				}
				if nonZeroLuma == 0 {
					t.Error("no luma block carried a coefficient; the frame cannot exercise the token path")
				}
			})
		}
	}
}

// TestForcedBPredExactBorderAndOddSizes decodes forced frames whose
// macroblock grids touch every border -- a single macroblock row, a
// single macroblock column, and sizes no macroblock grid divides, where
// the 4x4 top-right extension rules and the padded reconstruction width
// decide the picture.
func TestForcedBPredExactBorderAndOddSizes(t *testing.T) {
	sizes := [][2]int{
		{1, 1}, {3, 5}, {16, 1}, {1, 16},
		{48, 16}, {16, 48}, {17, 17}, {31, 47}, {97, 61},
	}
	for _, s := range sizes {
		w, h := s[0], s[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			img := bpredNoiseRGBA(w, h, uint64(w*7+h))
			_, data, recon, err := encodeForcedBPred(img, Config{Quality: 75})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			checkBPredExact(t, data, recon)

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

// TestDefaultPathStaysWholeBlock pins the slice boundary: the production
// path never marks a macroblock as B_PRED, so its bytes cannot move.
func TestDefaultPathStaysWholeBlock(t *testing.T) {
	img := bpredNoiseRGBA(33, 33, 202)
	enc := newEncoder(yuv.Convert(img), Config{Quality: 75})
	enc.run()
	for i := range enc.mbs {
		if enc.mbs[i].bpred {
			t.Fatalf("macroblock %d took the B_PRED path on the default run", i)
		}
		if enc.mbs[i].yMode < predict.DC || enc.mbs[i].yMode >= predict.NumModes {
			t.Fatalf("macroblock %d chose an out-of-range luma mode", i)
		}
	}
}

// TestMixedWholeBlockAndBPredFrame crafts a two-macroblock frame by hand:
// macroblock 0 keeps one whole-block luma mode, macroblock 1 is B_PRED
// with hand-picked sub-modes and one nonzero direct-current level. It
// pins the contextual sub-mode syntax end to end -- the whole-block flag,
// the above-per-column and left-per-row contexts, the seeding a whole-
// block macroblock leaves behind, and the YWithDC token plane -- because
// any slip desynchronizes the decoder's sub-mode choices and moves its
// picture away from the reconstruction built here.
func TestMixedWholeBlockAndBPredFrame(t *testing.T) {
	subModes := [16]predict.SubMode{
		predict.BVE, predict.BLD, predict.BHU, predict.BDC,
		predict.BTM, predict.BVR, predict.BVL, predict.BHD,
		predict.BHE, predict.BRD, predict.BHU, predict.BLD,
		predict.BVE, predict.BDC, predict.BTM, predict.BHE,
	}
	const dcLevel = 3

	for _, whole := range []predict.Mode{predict.DC, predict.V, predict.H, predict.TM} {
		t.Run("mb0_"+whole.String(), func(t *testing.T) {
			planes := yuv.NewPlanes(32, 16)
			enc := newEncoder(planes, Config{Quality: 75, Method: 4})

			mb0 := &enc.mbs[0]
			mb0.yMode = whole
			mb0.uvMode = predict.DC
			mb1 := &enc.mbs[1]
			mb1.bpred = true
			mb1.uvMode = predict.DC
			mb1.subModes = subModes
			// One nonzero luma level in macroblock 1's first block, so
			// the YWithDC plane carries a real direct-current value.
			mb1.levels[blockLuma][0] = dcLevel
			mb1.nz[blockLuma] = true

			// Build the reconstruction the decoder must land on.
			// Macroblock 0: the whole-block predictor over the frame
			// border (127 above, 129 left). Macroblock 1: each 4x4
			// sub-mode predictor over macroblock 0's samples, then the
			// inverse-transformed direct-current residual of block 0.
			nb0 := enc.neighbors(&enc.nbY, enc.rec.Y, enc.rec.YStride, 0, 0, 16, false, false)
			predict.Predict(enc.predY[:], 16, 16, whole, nb0)
			for y := 0; y < 16; y++ {
				copy(enc.rec.Y[y*enc.rec.YStride:][:16], enc.predY[y*16:][:16])
			}
			// Chroma of macroblock 0: DC with no above and no left.
			// Chroma of macroblock 1: DC over macroblock 0's column,
			// with the frame edge above. Both run through the same
			// predictors the decoder applies.
			for _, c := range []struct {
				plane []uint8
				buf   *neighborBuf
			}{{enc.rec.U, &enc.nbU}, {enc.rec.V, &enc.nbV}} {
				for mbx := 0; mbx < 2; mbx++ {
					nbc := enc.neighbors(c.buf, c.plane, enc.rec.CStride, mbx*8, 0, 8, mbx > 0, false)
					predict.Predict(enc.predU[:], 8, 8, predict.DC, nbc)
					for y := 0; y < 8; y++ {
						copy(c.plane[y*enc.rec.CStride+mbx*8:][:8], enc.predU[y*8:][:8])
					}
				}
			}

			var levels [16]int16
			levels[0] = dcLevel
			dequant := blockdsp.DequantizeBlock(&levels, enc.q.Y1.DC, enc.q.Y1.AC)
			resid := blockdsp.IDCT4x4(&dequant)

			var nb predict.SubNeighbors
			var pred, block [16]uint8
			for b := 0; b < 16; b++ {
				x0, y0 := 16+(b%4)*4, (b/4)*4
				predict.GatherSubNeighbors(&nb, enc.rec.Y, enc.rec.YStride, x0, y0, 32)
				predict.PredictSub(pred[:], 4, subModes[b], &nb)
				for y := 0; y < 4; y++ {
					for x := 0; x < 4; x++ {
						v := int32(pred[y*4+x])
						if b == 0 {
							v += int32(resid[y*4+x])
						}
						block[y*4+x] = clamp8(v)
					}
				}
				for y := 0; y < 4; y++ {
					copy(enc.rec.Y[(y0+y)*enc.rec.YStride+x0:][:4], block[y*4:][:4])
				}
			}

			payload, err := enc.frameBytes()
			if err != nil {
				t.Fatalf("assemble frame: %v", err)
			}
			_ = payload
			var buf writerBuffer
			if err := enc.writeFile(&buf); err != nil {
				t.Fatalf("write file: %v", err)
			}
			checkBPredExact(t, buf.data, enc.reconstruction())
		})
	}
}

// TestForcedBPredDeterminism extends test T7 to the forced path: the same
// input produces the same bytes at every value of GOMAXPROCS.
func TestForcedBPredDeterminism(t *testing.T) {
	img := bpredNoiseRGBA(37, 23, 303)
	encode := func() []byte {
		_, data, _, err := encodeForcedBPred(img, Config{Quality: 68})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return data
	}

	want := encode()
	if got := encode(); !bytes.Equal(got, want) {
		t.Error("two forced runs at the same settings produced different bytes")
	}

	restore := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(restore)
	for _, procs := range []int{1, 2, 4, 8} {
		runtime.GOMAXPROCS(procs)
		if got := encode(); !bytes.Equal(got, want) {
			t.Errorf("GOMAXPROCS %d produced different bytes on the forced path", procs)
		}
	}
}
