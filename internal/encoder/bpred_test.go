package encoder

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/predict"
)

func TestBModeProbabilityTable(t *testing.T) {
	flat := make([]byte, 0, int(predict.NumBModes)*int(predict.NumBModes)*9)
	for above := predict.BMode(0); above < predict.NumBModes; above++ {
		for left := predict.BMode(0); left < predict.NumBModes; left++ {
			flat = append(flat, bModeProbs[above][left][:]...)
		}
	}
	got := sha256.Sum256(flat)
	const want = "6684aedff5b28fd6c97f8f8436504e16318d5e180990dee4f082eded83bcd99c"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("B_PRED probability table SHA-256 = %x, want %s", got, want)
	}
}

func TestWriteBModeEveryContextAndLeaf(t *testing.T) {
	for above := predict.BMode(0); above < predict.NumBModes; above++ {
		for left := predict.BMode(0); left < predict.NumBModes; left++ {
			for want := predict.BMode(0); want < predict.NumBModes; want++ {
				enc := boolenc.New(0)
				writeBMode(enc, want, above, left)
				dec := boolenc.NewDecoder(enc.Finish())
				got := readBMode(dec, above, left)
				if got != want {
					t.Fatalf("above=%s left=%s: decoded %s, want %s", above, left, got, want)
				}
				if dec.UnexpectedEOF() {
					t.Fatalf("above=%s left=%s mode=%s: decoder exhausted stream", above, left, want)
				}
			}
		}
	}
}

func TestWriteBPredLumaModeTraversalAndContexts(t *testing.T) {
	modes := [16]predict.BMode{
		predict.BDC, predict.BTM, predict.BVE, predict.BHE,
		predict.BRD, predict.BVR, predict.BLD, predict.BVL,
		predict.BHD, predict.BHU, predict.BDC, predict.BVR,
		predict.BLD, predict.BHE, predict.BTM, predict.BHU,
	}
	tests := []struct {
		name  string
		above [4]predict.BMode
		left  [4]predict.BMode
	}{
		{name: "missing neighbors default to DC"},
		{
			name:  "mixed neighboring modes",
			above: [4]predict.BMode{predict.BTM, predict.BVE, predict.BHE, predict.BRD},
			left:  [4]predict.BMode{predict.BVR, predict.BLD, predict.BVL, predict.BHD},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writerAbove, writerLeft := tt.above, tt.left
			enc := boolenc.New(0)
			writeBPredLumaMode(enc, &modes, &writerAbove, &writerLeft)

			dec := boolenc.NewDecoder(enc.Finish())
			if got := dec.ReadBool(use16x16Prob); got {
				t.Fatal("B_PRED root bit selected the 16x16 branch")
			}

			decoderAbove, decoderLeft := tt.above, tt.left
			for y := 0; y < 4; y++ {
				leftMode := decoderLeft[y]
				for x := 0; x < 4; x++ {
					got := readBMode(dec, decoderAbove[x], leftMode)
					want := modes[4*y+x]
					if got != want {
						t.Fatalf("subblock (%d,%d) = %s, want %s", x, y, got, want)
					}
					decoderAbove[x] = got
					leftMode = got
				}
				decoderLeft[y] = leftMode
			}
			if dec.UnexpectedEOF() {
				t.Fatal("decoder exhausted contextual mode stream")
			}
			if writerAbove != decoderAbove {
				t.Fatalf("updated above contexts = %v, want %v", writerAbove, decoderAbove)
			}
			if writerLeft != decoderLeft {
				t.Fatalf("updated left contexts = %v, want %v", writerLeft, decoderLeft)
			}
		})
	}
}

func TestBModeForLumaMode(t *testing.T) {
	tests := []struct {
		in   predict.Mode
		want predict.BMode
	}{
		{predict.DC, predict.BDC},
		{predict.V, predict.BVE},
		{predict.H, predict.BHE},
		{predict.TM, predict.BTM},
	}
	for _, tt := range tests {
		if got := bModeForLumaMode(tt.in); got != tt.want {
			t.Errorf("bModeForLumaMode(%s) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestWriteBModeRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name         string
		mode, up, lf predict.BMode
	}{
		{"mode", predict.NumBModes, predict.BDC, predict.BDC},
		{"above context", predict.BDC, predict.NumBModes, predict.BDC},
		{"left context", predict.BDC, predict.BDC, predict.NumBModes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("writeBMode accepted an invalid value")
				}
			}()
			writeBMode(boolenc.New(0), tt.mode, tt.up, tt.lf)
		})
	}
}

func TestBModeForLumaModeRejectsInvalidMode(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("bModeForLumaMode accepted an invalid value")
		}
	}()
	bModeForLumaMode(predict.NumModes)
}

// readBMode is the test oracle for the decoder side of RFC 6386 section 11.5.
// It deliberately follows the decoder tree instead of calling encoder code.
func readBMode(dec *boolenc.Decoder, above, left predict.BMode) predict.BMode {
	p := &bModeProbs[above][left]
	if !dec.ReadBool(p[0]) {
		return predict.BDC
	}
	if !dec.ReadBool(p[1]) {
		return predict.BTM
	}
	if !dec.ReadBool(p[2]) {
		return predict.BVE
	}
	if !dec.ReadBool(p[3]) {
		if !dec.ReadBool(p[4]) {
			return predict.BHE
		}
		if !dec.ReadBool(p[5]) {
			return predict.BRD
		}
		return predict.BVR
	}
	if !dec.ReadBool(p[6]) {
		return predict.BLD
	}
	if !dec.ReadBool(p[7]) {
		return predict.BVL
	}
	if !dec.ReadBool(p[8]) {
		return predict.BHD
	}
	return predict.BHU
}
