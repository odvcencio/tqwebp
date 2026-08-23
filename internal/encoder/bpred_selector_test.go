package encoder

import (
	"bytes"
	"image"
	"testing"

	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/yuv"
)

func TestBPredSelectorEffortBoundary(t *testing.T) {
	var img image.Image
	for _, fixture := range loadCorpus(t) {
		if fixture.Spec.Name == "screenshot_panel_grid" {
			img = fixture.Img
			break
		}
	}
	if img == nil {
		t.Fatal("screenshot_panel_grid fixture is missing")
	}

	whole := newEncoder(yuv.Convert(img), Config{Quality: 75, Method: 0})
	whole.run()
	if whole.bPredModes != nil {
		t.Fatalf("method 0 allocated %d B_PRED mode records", len(whole.bPredModes))
	}
	var wholeOut byteWriter
	if err := whole.writeFile(&wholeOut); err != nil {
		t.Fatalf("method 0 encode: %v", err)
	}

	searched := newEncoder(yuv.Convert(img), Config{Quality: 75, Method: bPredMinMethod})
	searched.run()
	selected := 0
	for i := range searched.mbs {
		mb := &searched.mbs[i]
		if !mb.useBPred {
			continue
		}
		selected++
		assertNoY2(t, i, mb)
		for block, mode := range searched.bPredModes[i] {
			if mode >= predict.NumBModes {
				t.Fatalf("macroblock %d block %d selected invalid mode %d", i, block, mode)
			}
		}
	}
	const wantSelected = 51
	if selected != wantSelected {
		t.Fatalf("method 1 selected %d B_PRED macroblocks, want %d", selected, wantSelected)
	}

	var searchedOut byteWriter
	if err := searched.writeFile(&searchedOut); err != nil {
		t.Fatalf("method 1 encode: %v", err)
	}
	if bytes.Equal(searchedOut.data, wholeOut.data) {
		t.Fatal("method 1 produced method 0 bytes despite selecting B_PRED")
	}
	assertIndependentExact(t, searchedOut.data, searched.reconstruction())
}

func TestBPredSelectorKeepsSmoothWholePath(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0] = 38
		img.Pix[i+1] = 142
		img.Pix[i+2] = 219
		img.Pix[i+3] = 0xff
	}

	encode := func(method int) ([]byte, *encoder) {
		t.Helper()
		enc := newEncoder(yuv.Convert(img), Config{Quality: 75, Method: method})
		enc.run()
		var out byteWriter
		if err := enc.writeFile(&out); err != nil {
			t.Fatalf("method %d encode: %v", method, err)
		}
		return out.data, enc
	}

	wholeData, _ := encode(0)
	searchedData, searched := encode(bPredMinMethod)
	if searched.bPredModes != nil {
		t.Fatalf("smooth method-1 path allocated %d B_PRED mode records", len(searched.bPredModes))
	}
	if !bytes.Equal(searchedData, wholeData) {
		t.Fatal("smooth method-1 path changed the whole-block bitstream")
	}
}

func TestPreferBPredRDBoundaries(t *testing.T) {
	tests := []struct {
		name                 string
		wholeDist, bPredDist int32
		wholeRate, bPredRate lumaRateQ8
		lambda               uint64
		want                 bool
	}{
		{name: "lower distortion", wholeDist: 100, bPredDist: 99, wholeRate: lumaRateQ8{control: 100, tokens: 200}, bPredRate: lumaRateQ8{control: 100, tokens: 200}, lambda: 1, want: true},
		{name: "equal distortion lower control rate", wholeDist: 100, bPredDist: 100, wholeRate: lumaRateQ8{control: 101, tokens: 200}, bPredRate: lumaRateQ8{control: 100, tokens: 200}, lambda: 1, want: true},
		{name: "equal distortion lower token rate", wholeDist: 100, bPredDist: 100, wholeRate: lumaRateQ8{control: 100, tokens: 201}, bPredRate: lumaRateQ8{control: 100, tokens: 200}, lambda: 1, want: true},
		{name: "rate repays distortion", wholeDist: 100, bPredDist: 101, wholeRate: lumaRateQ8{tokens: 2}, bPredRate: lumaRateQ8{}, lambda: 256, want: true},
		{name: "rate does not repay distortion", wholeDist: 100, bPredDist: 99, wholeRate: lumaRateQ8{}, bPredRate: lumaRateQ8{tokens: 1000}, lambda: 1, want: false},
		{name: "exact tie stays whole", wholeDist: 100, bPredDist: 100, wholeRate: lumaRateQ8{control: 100, tokens: 200}, bPredRate: lumaRateQ8{control: 100, tokens: 200}, lambda: 999, want: false},
		{name: "worse on both axes", wholeDist: 100, bPredDist: 101, wholeRate: lumaRateQ8{control: 100, tokens: 200}, bPredRate: lumaRateQ8{control: 101, tokens: 201}, lambda: 1, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := preferBPred(tt.wholeDist, tt.bPredDist, tt.wholeRate, tt.bPredRate, tt.lambda)
			if got != tt.want {
				t.Fatalf("preferBPred(%d, %d, %+v, %+v, %d) = %v, want %v",
					tt.wholeDist, tt.bPredDist, tt.wholeRate, tt.bPredRate, tt.lambda, got, tt.want)
			}
		})
	}
}

func TestBPredReconstructionPruneBoundaries(t *testing.T) {
	tests := []struct {
		name                 string
		wholeDist, bPredDist int32
		margin               int64
		want                 bool
	}{
		{name: "clears margin", wholeDist: 200, bPredDist: 99, margin: 100, want: true},
		{name: "margin tie", wholeDist: 200, bPredDist: 100, margin: 100, want: false},
		{name: "worse candidate", wholeDist: 100, bPredDist: 101, margin: 0, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bPredImprovesReconstruction(tt.wholeDist, tt.bPredDist, tt.margin); got != tt.want {
				t.Fatalf("bPredImprovesReconstruction(%d, %d, %d) = %v, want %v", tt.wholeDist, tt.bPredDist, tt.margin, got, tt.want)
			}
		})
	}
}

func TestSearchedBModesUseCanonicalTie(t *testing.T) {
	planes := yuv.NewPlanes(32, 32)
	for i := range planes.Y {
		planes.Y[i] = 128
	}
	enc := newEncoder(planes, Config{Quality: 75, Method: bPredMinMethod})
	for i := range enc.rec.Y {
		enc.rec.Y[i] = 128
	}

	var mb macroblock
	var modes [16]predict.BMode
	for i := range modes {
		modes[i] = predict.NumBModes
	}
	enc.codeBPredLuma(1, 1, &mb, &modes, true)
	for block, mode := range modes {
		if mode != predict.BDC {
			t.Errorf("block %d tied mode = %s, want %s", block, mode, predict.BDC)
		}
	}
	assertNoY2(t, 3, &mb)
	if got := testLumaCoefficientCount(&mb); got != 0 {
		t.Fatalf("flat searched macroblock has %d coefficients, want 0", got)
	}
}

func testLumaCoefficientCount(mb *macroblock) int {
	count := 0
	for block := blockY2; block < blockLuma+16; block++ {
		for _, level := range mb.levels[block] {
			if level != 0 {
				count++
			}
		}
	}
	return count
}

func TestCopyLuma16RoundTripAtSingleMacroblockStride(t *testing.T) {
	const stride = 16
	source := make([]uint8, stride*32)
	for i := range source {
		source[i] = uint8(i*29 + 7)
	}
	var scratch [16 * 16]uint8
	copyLuma16(scratch[:], 16, 0, 0, source, stride, 0, 16)

	restored := make([]uint8, len(source))
	copyLuma16(restored, stride, 0, 16, scratch[:], 16, 0, 0)
	if !bytes.Equal(restored[stride*16:], source[stride*16:]) {
		t.Fatal("16-pixel-stride round trip restored the wrong macroblock row")
	}
	if !bytes.Equal(restored[:stride*16], make([]uint8, stride*16)) {
		t.Fatal("16-pixel-stride round trip overwrote the preceding macroblock row")
	}
}
