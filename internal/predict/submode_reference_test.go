package predict_test

// This file is the normative check of the B_PRED scalar foundation: it
// codes small key frames whose macroblocks all use B_PRED with zero
// residuals, decodes them with golang.org/x/image/vp8 -- the decoder the
// repository's exact-match gate trusts -- and requires the decoded luma
// to equal a local cascade of the package's own neighbourhood gather and
// scalar predictors, byte for byte, over the full padded picture.
//
// Because a skipped B_PRED macroblock codes no coefficients, the decoder
// reconstructs each 4x4 block as pure predictor output. Any disagreement
// between the syntax writer, the contextual probability table, the
// border rules, or one of the ten predictors shows up here as a pixel
// difference.

import (
	"bytes"
	"image"
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/frame"
	"m31labs.dev/tqwebp/internal/predict"

	"golang.org/x/image/vp8"
)

// submodePattern assigns a sub-mode to block (j, i) of macroblock mb.
type submodePattern func(mb, j, i int) predict.SubMode

// patternMixed walks all ten sub-modes with coprime strides, so every
// block sees different above and left contexts.
func patternMixed(mb, j, i int) predict.SubMode {
	return predict.SubMode((mb*5 + 13*j + 7*i) % 10)
}

// patternPerMB gives a whole macroblock one sub-mode, exercising the
// context extremes where above equals left.
func patternPerMB(mb, j, i int) predict.SubMode {
	return predict.SubMode((3*mb + 1) % 10)
}

// patternSingle returns one fixed sub-mode everywhere.
func patternSingle(m predict.SubMode) submodePattern {
	return func(_, _, _ int) predict.SubMode { return m }
}

// cascade predicts the whole padded picture locally: the same walk the
// decoder does, driven by the package under test.
func cascade(w, h int, pattern submodePattern) []uint8 {
	mbw := (w + 15) / 16
	mbh := (h + 15) / 16
	pw, ph := mbw*16, mbh*16
	buf := make([]uint8, pw*ph)
	var nb predict.SubNeighbors
	for mb := 0; mb < mbw*mbh; mb++ {
		mbx, mby := mb%mbw, mb/mbw
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				x0, y0 := mbx*16+i*4, mby*16+j*4
				predict.GatherSubNeighbors(&nb, buf, pw, x0, y0, pw)
				predict.PredictSub(buf[y0*pw+x0:], pw, pattern(mb, j, i), &nb)
			}
		}
	}
	return buf
}

// buildBPredFrame codes a skipped, coefficient-free B_PRED key frame and
// decodes it with the reference decoder.
func buildBPredFrame(t *testing.T, w, h int, pattern submodePattern) *image.YCbCr {
	t.Helper()

	enc := boolenc.New(4096)
	frame.WriteHeader(enc, frame.Header{
		Width:        w,
		Height:       h,
		FilterSimple: true,
		FilterLevel:  0, // the decoder skips the loop filter entirely
		QuantIndex:   4,
		SkipProb:     255,
	})

	mbw := (w + 15) / 16
	mbh := (h + 15) / 16
	// The contexts mirror the reference decoder exactly: the above
	// context of block column i belongs to macroblock column mbx -- it is
	// seeded with BDC at the frame top and survives down the rows -- and
	// the left context restarts at BDC on every macroblock row but
	// persists across the macroblocks of one row. Seeding either per
	// macroblock, or carrying above sideways within a row, selects
	// probabilities the decoder never uses, and the decoded mode stream
	// drifts off the written bits.
	above := make([][4]predict.SubMode, mbw)
	for mby := 0; mby < mbh; mby++ {
		var left [4]predict.SubMode // zero value: BDC seeds every row
		for mbx := 0; mbx < mbw; mbx++ {
			mb := mby*mbw + mbx

			// A skipped macroblock: no coefficient partition data
			// follows.
			enc.WriteBool(255, true)
			// Luma mode B_PRED, the false branch of the use-16x16
			// split.
			enc.WriteBool(145, false)

			// Sixteen sub-modes in raster order, each coded against
			// the above and left contexts the decoder maintains.
			for j := 0; j < 4; j++ {
				p := left[j]
				for i := 0; i < 4; i++ {
					m := pattern(mb, j, i)
					predict.WriteSubMode(enc, above[mbx][i], p, m)
					above[mbx][i], p = m, m
				}
				left[j] = p
			}

			// Chroma mode DC_PRED.
			enc.WriteBool(142, false)
		}
	}

	payload, err := frame.Assemble(w, h, enc.Finish(), nil)
	if err != nil {
		t.Fatalf("frame.Assemble(%d,%d): %v", w, h, err)
	}

	d := vp8.NewDecoder()
	d.Init(bytes.NewReader(payload), len(payload))
	if _, err := d.DecodeFrameHeader(); err != nil {
		t.Fatalf("%dx%d: DecodeFrameHeader: %v", w, h, err)
	}
	img, err := d.DecodeFrame()
	if err != nil {
		t.Fatalf("%dx%d: DecodeFrame: %v", w, h, err)
	}
	return img
}

// TestSubModesAgainstReferenceDecoder is the differential gate.
func TestSubModesAgainstReferenceDecoder(t *testing.T) {
	cases := []struct {
		w, h    int
		note    string
		pattern submodePattern
	}{
		{16, 16, "one macroblock, all ten sub-modes", patternMixed},
		{32, 32, "four macroblocks, per-MB constant modes", patternPerMB},
		{32, 16, "two macroblocks across: extension crosses MBs", patternMixed},
		{16, 32, "two macroblocks down", patternPerMB},
		{33, 17, "rightmost and bottom MBs partially visible", patternMixed},
		{21, 13, "odd bounds, prime-ish edges", patternPerMB},
		{4, 4, "single visible subblock, all borders missing", patternSingle(predict.BHU)},
		{5, 3, "tiny frame, top-row dependence", patternSingle(predict.BLD)},
		{48, 16, "three macroblocks across, right-edge replication", patternMixed},
		{17, 31, "tall odd frame", patternPerMB},
		{48, 48, "nine macroblocks, mixed modes over internal right edges", patternMixed},
		{64, 16, "four macroblocks across, per-MB constant modes", patternPerMB},
		{33, 33, "odd bounds, three macroblocks down", patternMixed},
	}

	for _, tc := range cases {
		img := buildBPredFrame(t, tc.w, tc.h, tc.pattern)
		if img.Rect.Dx() != tc.w || img.Rect.Dy() != tc.h {
			t.Fatalf("%s: decoded %dx%d, want %dx%d", tc.note, img.Rect.Dx(), img.Rect.Dy(), tc.w, tc.h)
		}
		want := cascade(tc.w, tc.h, tc.pattern)
		mbw := (tc.w + 15) / 16
		mbh := (tc.h + 15) / 16
		pw, ph := mbw*16, mbh*16
		for y := 0; y < ph; y++ {
			for x := 0; x < pw; x++ {
				got := img.Y[y*img.YStride+x]
				if got != want[y*pw+x] {
					t.Errorf("%s (%dx%d): pixel (%d,%d) = %d, want %d",
						tc.note, tc.w, tc.h, x, y, got, want[y*pw+x])
					return
				}
			}
		}
	}
}
