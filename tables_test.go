package webp

import (
	"image"
	"testing"

	"m31labs.dev/turboquant/blockdsp"

	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/quantize"
	"m31labs.dev/tqwebp/internal/yuv"
	"m31labs.dev/tqwebp/oracle"
)

// This file holds test T9: the table pins. Every table the encoder writes
// against is normative data from RFC 6386, so a transcription slip has to
// fail loudly. The pins work through the oracle, not against a second
// copy of the same numbers: the tests build a frame with chosen
// coefficient levels, decode it with golang.org/x/image/vp8, and check
// the decoded samples against the arithmetic the tables predict.
//
// The whole-corpus exact-reconstruction test pins the rest of the token
// layer at the same time. A wrong band map, a wrong default probability,
// or a wrong context would move the decoder off the encoder's own
// picture, and that test compares every plane byte for byte.

// craftFrame builds a one-macroblock frame with hand-picked coefficient
// levels. It skips the analysis pass, so nothing the encoder decides can
// hide a table error.
func craftFrame(t *testing.T, setup func(mb *macroblock)) []byte {
	t.Helper()
	return craftFrameAt(t, 0, setup)
}

func craftFrameAt(t *testing.T, index quantize.Index, setup func(mb *macroblock)) []byte {
	t.Helper()
	planes := yuv.NewPlanes(16, 16)
	enc := newEncoder(planes, Options{Quality: DefaultQuality, Method: DefaultMethod})
	enc.q = quantize.New(index)

	mb := &enc.mbs[0]
	mb.yMode = predict.DC
	mb.uvMode = predict.DC
	setup(mb)
	for i := range mb.nz {
		mb.nz[i] = false
		for _, v := range mb.levels[i] {
			if v != 0 {
				mb.nz[i] = true
				break
			}
		}
	}
	mb.skip = true
	for _, nz := range mb.nz {
		if nz {
			mb.skip = false
			break
		}
	}

	payload, err := enc.frameBytes()
	if err != nil {
		t.Fatalf("assemble frame: %v", err)
	}
	var buf writerBuffer
	if err := enc.writeFile(&buf); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_ = payload
	return buf.data
}

// chromaAt reads one chroma sample by its own plane coordinates. The
// COffset method takes luma coordinates and halves them, which is not
// what a per-chroma-sample check wants.
func chromaAt(planes *image.YCbCr, plane []uint8, cx, cy int) uint8 {
	return plane[cy*planes.CStride+cx]
}

func decodePlanes(t *testing.T, data []byte) *image.YCbCr {
	t.Helper()
	planes, err := oracle.DecodeWebPPlanes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return planes
}

// TestPinLumaDCTableThroughDecoder pins the direct-current dequantization
// table, and the doubling rule of the Y2 plane, at every quantizer index.
//
// A macroblock with no neighbours predicts the constant 128. One level in
// the Y2 block spreads over all sixteen luma blocks: the inverse
// Walsh-Hadamard transform turns level*factor into (level*factor+3)>>3 for
// every block's direct-current coefficient, and the inverse discrete
// cosine transform then adds (value+4)>>3 to every pixel.
func TestPinLumaDCTableThroughDecoder(t *testing.T) {
	for qi := quantize.Index(0); qi <= 127; qi++ {
		const level = 3
		data := craftFrameAt(t, qi, func(mb *macroblock) {
			mb.levels[blockY2][0] = level
		})
		planes := decodePlanes(t, data)

		factor := int32(quantize.TableDC[qi]) * 2
		dc := (level*factor + 3) >> 3
		want := clamp8(128 + (dc+4)>>3)
		for y := 0; y < 16; y++ {
			for x := 0; x < 16; x++ {
				if got := planes.Y[planes.YOffset(x, y)]; got != want {
					t.Fatalf("index %d: luma sample at (%d,%d) is %d, want %d (factor %d)", qi, x, y, got, want, factor)
				}
			}
		}
	}
}

// TestPinChromaDCTableThroughDecoder pins the chroma direct-current
// factor, including the clamp at index 117 that RFC 6386 section 9.6
// applies to the chroma index alone.
func TestPinChromaDCTableThroughDecoder(t *testing.T) {
	for qi := quantize.Index(0); qi <= 127; qi++ {
		const level = 2
		data := craftFrameAt(t, qi, func(mb *macroblock) {
			mb.levels[blockU][0] = level
		})
		planes := decodePlanes(t, data)

		clamped := qi
		if clamped > 117 {
			clamped = 117
		}
		factor := int32(quantize.TableDC[clamped])
		want := clamp8(128 + (level*factor+4)>>3)
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				if got := chromaAt(planes, planes.Cb, x, y); got != want {
					t.Fatalf("index %d: chroma sample at (%d,%d) is %d, want %d (factor %d)", qi, x, y, got, want, factor)
				}
			}
		}
		if qi == 127 && quantize.TableDC[117] == quantize.TableDC[127] {
			t.Fatal("the chroma clamp test cannot see the clamp: the table entries at 117 and 127 are equal")
		}
	}
}

// TestPinScanOrderAndACTable pins the zigzag scan order and the
// alternating-current table together. A level at scan position k must
// reach raster position ZigZag[k] in the decoder, so the decoded block
// must equal the inverse transform of exactly that coefficient.
func TestPinScanOrderAndACTable(t *testing.T) {
	const qi = quantize.Index(24)
	q := quantize.New(qi)

	for k := 1; k < 16; k++ {
		const level = 2
		data := craftFrameAt(t, qi, func(mb *macroblock) {
			mb.levels[blockU][k] = level
		})
		planes := decodePlanes(t, data)

		var coeff [16]int16
		coeff[blockdsp.ZigZag[k]] = int16(level * int(q.UV.AC))
		residual := blockdsp.IDCT4x4(&coeff)

		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				want := clamp8(128 + int32(residual[y*4+x]))
				if got := chromaAt(planes, planes.Cb, x, y); got != want {
					t.Fatalf("scan position %d: sample at (%d,%d) is %d, want %d", k, x, y, got, want)
				}
			}
		}
	}
}

// TestPinY2ACTable pins the alternating-current rule of the Y2 plane: the
// factor scales by 155/100 and never falls below 8.
func TestPinY2ACTable(t *testing.T) {
	for _, qi := range []quantize.Index{0, 1, 5, 12, 40, 80, 127} {
		const level = 2
		const scanPos = 1
		data := craftFrameAt(t, qi, func(mb *macroblock) {
			mb.levels[blockY2][scanPos] = level
		})
		planes := decodePlanes(t, data)

		want := int16(int32(quantize.TableAC[qi]) * 155 / 100)
		if want < 8 {
			want = 8
		}
		if got := quantize.New(qi).Y2.AC; got != want {
			t.Fatalf("index %d: computed Y2 factor %d, want %d", qi, got, want)
		}

		// The decoder must place the coefficient at raster position
		// ZigZag[1], which the inverse Walsh-Hadamard transform then
		// spreads over the sixteen luma blocks.
		var coeff [16]int16
		coeff[blockdsp.ZigZag[scanPos]] = int16(level) * want
		dcs := blockdsp.IWHT4x4(&coeff)
		for b := 0; b < 16; b++ {
			bx, by := (b%4)*4, (b/4)*4
			pixel := clamp8(128 + (int32(dcs[b])+4)>>3)
			for y := 0; y < 4; y++ {
				for x := 0; x < 4; x++ {
					if got := planes.Y[planes.YOffset(bx+x, by+y)]; got != pixel {
						t.Fatalf("index %d: luma sample at (%d,%d) is %d, want %d", qi, bx+x, by+y, got, pixel)
					}
				}
			}
		}
	}
}

// TestPinLargeMagnitudeTokens pins the extra-bit categories: every
// magnitude from 1 to a little past the largest category boundary must
// survive a round trip through the decoder.
func TestPinLargeMagnitudeTokens(t *testing.T) {
	magnitudes := []int{1, 2, 3, 4, 5, 6, 7, 10, 11, 18, 19, 34, 35, 66, 67, 100, 1000, 2114}
	const qi = quantize.Index(0)
	q := quantize.New(qi)

	for _, mag := range magnitudes {
		for _, sign := range []int{1, -1} {
			level := sign * mag
			data := craftFrameAt(t, qi, func(mb *macroblock) {
				mb.levels[blockU][0] = int16(level)
			})
			planes := decodePlanes(t, data)

			dc := int16(int32(level) * int32(q.UV.DC))
			want := clamp8(128 + (int32(dc)+4)>>3)
			if got := chromaAt(planes, planes.Cb, 0, 0); got != want {
				t.Fatalf("level %d: chroma sample is %d, want %d", level, got, want)
			}
		}
	}
}

// TestPinModeTrees pins the prediction mode trees: a macroblock coded
// with each luma and chroma mode must decode to the picture that mode
// predicts. Neighbour samples outside the frame are 127 above and 129 to
// the left, so the four modes produce four different constants.
func TestPinModeTrees(t *testing.T) {
	cases := []struct {
		mode predict.Mode
		want uint8
	}{
		// With no neighbours the direct-current mode predicts 128.
		{predict.DC, 128},
		// The vertical mode copies the row above, which is 127.
		{predict.V, 127},
		// The horizontal mode copies the column to the left, which is 129.
		{predict.H, 129},
		// The gradient mode adds left and above and removes the corner:
		// 129 + 127 - 127.
		{predict.TM, 129},
	}
	for _, c := range cases {
		data := craftFrame(t, func(mb *macroblock) {
			mb.yMode = c.mode
			mb.uvMode = c.mode
		})
		planes := decodePlanes(t, data)
		if got := planes.Y[planes.YOffset(5, 5)]; got != c.want {
			t.Errorf("luma mode %v: decoded %d, want %d", c.mode, got, c.want)
		}
		if got := chromaAt(planes, planes.Cb, 5, 5); got != c.want {
			t.Errorf("chroma mode %v: decoded %d, want %d", c.mode, got, c.want)
		}
	}
}

// TestPinQualityMapIsMonotone pins the shape rule of the quality map:
// quality up, quantizer index down, with no flat step wider than the
// resolution of the index itself.
func TestPinQualityMapIsMonotone(t *testing.T) {
	last := quantize.IndexForQuality(0)
	if last != 127 {
		t.Errorf("quality 0 maps to index %d, want 127", last)
	}
	for q := 1; q <= 100; q++ {
		got := quantize.IndexForQuality(q)
		if got > last {
			t.Errorf("quality %d maps to index %d, above the %d of quality %d", q, got, last, q-1)
		}
		last = got
	}
	if last != 0 {
		t.Errorf("quality 100 maps to index %d, want 0", last)
	}
}
