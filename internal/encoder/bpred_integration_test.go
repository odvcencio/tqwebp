package encoder

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"m31labs.dev/tqwebp/internal/corpus"
	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/yuv"
	"m31labs.dev/tqwebp/oracle"
)

// integrationBModes places top-right-using diagonal modes on every
// macroblock-right subblock while still exercising all ten modes.
var integrationBModes = [16]predict.BMode{
	predict.BDC, predict.BTM, predict.BVE, predict.BLD,
	predict.BHE, predict.BRD, predict.BVR, predict.BVL,
	predict.BHU, predict.BHD, predict.BVE, predict.BLD,
	predict.BTM, predict.BHE, predict.BRD, predict.BVL,
}

func TestForcedBPredExactReconstruction(t *testing.T) {
	sizes := []image.Point{
		{X: 1, Y: 1},
		{X: 4, Y: 19},
		{X: 15, Y: 17},
		{X: 17, Y: 15},
		{X: 31, Y: 47},
		{X: 49, Y: 33},
	}
	for _, size := range sizes {
		for _, quality := range []int{1, 50, 100} {
			name := fmt.Sprintf("%dx%d/q%d", size.X, size.Y, quality)
			t.Run(name, func(t *testing.T) {
				img := corpus.Generate(corpus.Spec{
					Name:   name,
					Class:  corpus.Screenshot,
					Width:  size.X,
					Height: size.Y,
					Seed:   uint64(size.X*1000 + size.Y*10 + quality),
				})
				data, recon, enc := encodeWithBPredHarness(t, img, Config{Quality: quality}, func(_, _ int) ([16]predict.BMode, bool) {
					return integrationBModes, true
				})

				for i := range enc.mbs {
					if !enc.mbs[i].useBPred {
						t.Fatalf("macroblock %d did not use B_PRED", i)
					}
					if got := enc.bPredModes[i]; got != integrationBModes {
						t.Fatalf("macroblock %d modes = %v, want %v", i, got, integrationBModes)
					}
					assertNoY2(t, i, &enc.mbs[i])
				}
				assertIndependentExact(t, data, recon)
				writeForcedBPredArtifact(t, fmt.Sprintf("forced_%dx%d_q%d.webp", size.X, size.Y, quality), data)
			})
		}
	}
}

func TestForcedBPredCarriesOwnLumaDC(t *testing.T) {
	img := corpus.Generate(corpus.Spec{
		Name: "bpred-dc", Class: corpus.Photo, Width: 16, Height: 16, Seed: 0x4b1d,
	})
	data, recon, enc := encodeWithBPredHarness(t, img, Config{Quality: 75}, func(_, _ int) ([16]predict.BMode, bool) {
		return integrationBModes, true
	})
	mb := &enc.mbs[0]
	assertNoY2(t, 0, mb)

	var ownDC bool
	for b := 0; b < 16; b++ {
		if mb.levels[blockLuma+b][0] != 0 {
			ownDC = true
			break
		}
	}
	if !ownDC {
		t.Fatal("forced B_PRED fixture did not exercise a luma DC token")
	}
	assertIndependentExact(t, data, recon)
}

func TestMixedBPredAndWholeBlockExactReconstruction(t *testing.T) {
	img := corpus.Generate(corpus.Spec{
		Name: "bpred-mixed", Class: corpus.Photo, Width: 65, Height: 49, Seed: 0x71a9,
	})
	for _, quality := range []int{1, 50, 100} {
		t.Run(fmt.Sprintf("q%d", quality), func(t *testing.T) {
			data, recon, enc := encodeWithBPredHarness(t, img, Config{Quality: quality}, func(mbx, mby int) ([16]predict.BMode, bool) {
				return integrationBModes, (mbx+2*mby)%3 == 1
			})

			var bPredCount, wholeCount int
			for i := range enc.mbs {
				if enc.mbs[i].useBPred {
					bPredCount++
					assertNoY2(t, i, &enc.mbs[i])
				} else {
					wholeCount++
				}
			}
			if bPredCount == 0 || wholeCount == 0 {
				t.Fatalf("mixed fixture selected %d B_PRED and %d whole-block macroblocks", bPredCount, wholeCount)
			}
			assertIndependentExact(t, data, recon)
			writeForcedBPredArtifact(t, fmt.Sprintf("mixed_65x49_q%d.webp", quality), data)
		})
	}
}

func TestSkippedBPredPreservesY2Context(t *testing.T) {
	// Build three macroblocks in one row. The first and third carry Y2. The
	// middle macroblock is forced to B_PRED and its source is synthesized from
	// its exact predictors, making it skipped. A decoder retains the first
	// macroblock's left Y2 context across that skipped B_PRED record when it
	// reads the third macroblock.
	src := yuv.NewPlanes(48, 16)
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			src.Y[y*src.YStride+x] = 200
		}
		for x := 32; x < 48; x++ {
			src.Y[y*src.YStride+x] = 50
		}
	}
	for i := range src.U {
		src.U[i] = 128
		src.V[i] = 128
	}

	cfg := Config{Quality: 75}
	first := newEncoder(src, cfg)
	first.encodeMacroblock(0, 0)

	// Recreate the middle macroblock's decoder workspace and copy each B_TM
	// predictor into the source before moving to the next block.
	reconstruction := make([]uint8, len(src.Y))
	for y := 0; y < 16; y++ {
		copy(reconstruction[y*src.YStride:y*src.YStride+16], first.rec.Y[y*first.rec.YStride:y*first.rec.YStride+16])
	}
	var tmModes [16]predict.BMode
	for i := range tmModes {
		tmModes[i] = predict.BTM
	}
	var pred [16]uint8
	for b := 0; b < 16; b++ {
		bx, by := (b%4)*4, (b/4)*4
		nb := predict.BNeighborsForBlock(reconstruction, src.YStride, src.MBW, 1, 0, b)
		predict.Predict4(pred[:], 4, tmModes[b], &nb)
		for y := 0; y < 4; y++ {
			dst := (by+y)*src.YStride + 16 + bx
			copy(src.Y[dst:dst+4], pred[y*4:y*4+4])
			copy(reconstruction[dst:dst+4], pred[y*4:y*4+4])
		}
	}

	enc := newEncoder(src, cfg)
	enc.setBPredModes(1, tmModes)
	enc.run()
	if !enc.mbs[0].nz[blockY2] {
		t.Fatal("first whole-block macroblock did not exercise a nonzero Y2 context")
	}
	if !enc.mbs[1].useBPred || !enc.mbs[1].skip {
		t.Fatalf("middle macroblock useBPred=%v skip=%v, want true/true", enc.mbs[1].useBPred, enc.mbs[1].skip)
	}
	assertNoY2(t, 1, &enc.mbs[1])
	if !enc.mbs[2].nz[blockY2] {
		t.Fatal("third whole-block macroblock did not exercise a nonzero Y2 token")
	}

	var out byteWriter
	if err := enc.writeFile(&out); err != nil {
		t.Fatalf("encode skipped B_PRED context fixture: %v", err)
	}
	assertIndependentExact(t, out.data, enc.reconstruction())
	writeForcedBPredArtifact(t, "skipped_bpred_y2_context.webp", out.data)
}

func TestForcedBPredDeterminism(t *testing.T) {
	img := corpus.Generate(corpus.Spec{
		Name: "bpred-determinism", Class: corpus.Screenshot, Width: 33, Height: 19, Seed: 0x91ce,
	})
	encode := func() []byte {
		data, _, _ := encodeWithBPredHarness(t, img, Config{Quality: 68}, func(_, _ int) ([16]predict.BMode, bool) {
			return integrationBModes, true
		})
		return data
	}

	want := encode()
	if got := encode(); !bytes.Equal(got, want) {
		t.Fatal("two forced B_PRED encodes produced different bytes")
	}
	restore := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(restore)
	for _, procs := range []int{1, 2, 4, 8} {
		runtime.GOMAXPROCS(procs)
		if got := encode(); !bytes.Equal(got, want) {
			t.Errorf("GOMAXPROCS %d produced different forced B_PRED bytes", procs)
		}
	}
}

func TestWholeBlockPathLeavesBPredStorageNil(t *testing.T) {
	img := corpus.Generate(corpus.Spec{
		Name: "whole-only", Class: corpus.Flat, Width: 48, Height: 32, Seed: 0x2281,
	})
	enc := newEncoder(yuv.Convert(img), Config{Quality: 75})
	enc.run()
	if enc.bPredModes != nil {
		t.Fatalf("whole-block path allocated %d B_PRED mode records", len(enc.bPredModes))
	}
	for i := range enc.mbs {
		if enc.mbs[i].useBPred {
			t.Fatalf("whole-block path selected B_PRED at macroblock %d", i)
		}
	}
}

func TestClearBlockContextsRetainsY2(t *testing.T) {
	c := mbContext{
		y2:   1,
		luma: [4]uint8{1, 1, 1, 1},
		u:    [2]uint8{1, 1},
		v:    [2]uint8{1, 1},
	}
	clearBlockContexts(&c)
	if c.y2 != 1 {
		t.Fatal("clearing B_PRED block contexts also cleared Y2")
	}
	if c.luma != [4]uint8{} || c.u != [2]uint8{} || c.v != [2]uint8{} {
		t.Fatalf("ordinary block contexts were not cleared: %+v", c)
	}
}

type bPredHarnessDecision func(mbx, mby int) ([16]predict.BMode, bool)

func encodeWithBPredHarness(t *testing.T, img image.Image, cfg Config, choose bPredHarnessDecision) ([]byte, *image.YCbCr, *encoder) {
	t.Helper()
	enc := newEncoder(yuv.Convert(img), cfg)
	for mby := 0; mby < enc.mbh; mby++ {
		for mbx := 0; mbx < enc.mbw; mbx++ {
			modes, enabled := choose(mbx, mby)
			if enabled {
				enc.setBPredModes(mby*enc.mbw+mbx, modes)
			}
		}
	}
	enc.run()
	var out byteWriter
	if err := enc.writeFile(&out); err != nil {
		t.Fatalf("forced B_PRED encode: %v", err)
	}
	return out.data, enc.reconstruction(), enc
}

func assertNoY2(t *testing.T, mbIndex int, mb *macroblock) {
	t.Helper()
	if mb.nz[blockY2] {
		t.Fatalf("B_PRED macroblock %d marked Y2 nonzero", mbIndex)
	}
	if mb.levels[blockY2] != [16]int16{} {
		t.Fatalf("B_PRED macroblock %d carries Y2 levels %v", mbIndex, mb.levels[blockY2])
	}
}

func assertIndependentExact(t *testing.T, data []byte, recon *image.YCbCr) {
	t.Helper()
	decoded, err := oracle.DecodeWebPPlanes(data)
	if err != nil {
		t.Fatalf("independent decode: %v", err)
	}
	if err := oracle.CompareExact(reconSource{recon}, decoded); err != nil {
		t.Fatal(err)
	}
}

// writeForcedBPredArtifact is an opt-in bridge to external decoder gates.
// Normal tests stay hermetic. Campaign verification can set
// TQWEBP_FORCED_ARTIFACT_DIR and feed the resulting files to libwebp and
// webpinfo without adding a production-only forcing API.
func writeForcedBPredArtifact(t *testing.T, name string, data []byte) {
	t.Helper()
	dir := os.Getenv("TQWEBP_FORCED_ARTIFACT_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create forced B_PRED artifact directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write forced B_PRED artifact: %v", err)
	}
}
