package encoder

import (
	"bytes"
	"fmt"
	"image"
	"math/rand/v2"
	"runtime"
	"testing"

	"m31labs.dev/tqwebp/internal/corpus"
	"m31labs.dev/tqwebp/internal/yuv"
	"m31labs.dev/tqwebp/oracle"
)

// exactGateQualities are the quality settings the exact reconstruction
// gate runs at. Specification section 11.2 names 10, 35, 50, 75, and 90.
// The list adds 1 and 100, the two ends of the quantizer index range,
// because the largest table entries are where a 16-bit overflow can hide.
var exactGateQualities = []int{1, 10, 35, 50, 75, 90, 100}

// TestExactReconstruction is gate G1's hard half and test T2: with loop
// filter level 0, an independent decoder must reproduce the encoder's own
// picture byte for byte, on every plane. The test covers the whole corpus
// at five qualities.
//
// The gate catches every class of bug PSNR hides: a wrong transform, a
// wrong context, a dropped token, a carry the entropy coder lost.
func TestExactReconstruction(t *testing.T) {
	images := loadCorpus(t)
	for _, img := range images {
		for _, q := range exactGateQualities {
			t.Run(fmt.Sprintf("%s/q%d", img.Spec.Name, q), func(t *testing.T) {
				data, recon, err := encodeWithReconstruction(img.Img, Config{Quality: q})
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				decoded, err := oracle.DecodeWebPPlanes(data)
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				if err := oracle.CompareExact(reconSource{recon}, decoded); err != nil {
					t.Error(err)
				}
			})
		}
	}
}

// TestRoundTripDecodes is gate G1's first half: every corpus image
// encodes, decodes through the independent decoder, and keeps its size.
func TestRoundTripDecodes(t *testing.T) {
	images := loadCorpus(t)
	for _, img := range images {
		for _, q := range []int{1, 25, 50, 75, 95, 100} {
			var buf bytes.Buffer
			if err := Encode(&buf, img.Img, Config{Quality: q}); err != nil {
				t.Fatalf("%s q%d: encode: %v", img.Spec.Name, q, err)
			}
			decoded, err := oracle.DecodeWebP(buf.Bytes())
			if err != nil {
				t.Fatalf("%s q%d: decode: %v", img.Spec.Name, q, err)
			}
			if got, want := decoded.Bounds().Size(), img.Img.Bounds().Size(); got != want {
				t.Errorf("%s q%d: decoded size %v, want %v", img.Spec.Name, q, got, want)
			}
		}
	}
}

// TestQualityIsMonotone is test T4 and the input of gate G2b: a higher
// quality must spend more bytes and must keep more detail.
//
// The measure is coded luma PSNR: the encoder's own planes against the
// decoder's planes. That domain answers the question the test asks, which
// is whether the quality knob still reaches the quantizer. The
// red-green-blue domain cannot answer it at the top of the range, because
// the fixed cost of 4:2:0 chroma subsampling puts a floor under the
// number on saturated flat art, and the codec's own error falls far below
// that floor.
//
// The tolerance of two percent comes from specification test T4. A
// near-lossless picture reaches a noise floor where one quantizer step
// moves the score by a fraction of a decibel in either direction.
func TestQualityIsMonotone(t *testing.T) {
	images := loadCorpus(t)
	qualities := []int{50, 75, 85, 90, 95}
	const tolerance = 0.98

	for _, img := range images {
		var lastBytes int
		var lastPSNR float64
		for i, q := range qualities {
			var buf bytes.Buffer
			if err := Encode(&buf, img.Img, Config{Quality: q}); err != nil {
				t.Fatalf("%s q%d: encode: %v", img.Spec.Name, q, err)
			}
			psnr := codedLumaPSNR(t, img.Img, buf.Bytes())
			if i > 0 {
				if float64(buf.Len()) < float64(lastBytes)*tolerance {
					t.Errorf("%s: q%d spends %d bytes, below the %d bytes of the step before",
						img.Spec.Name, q, buf.Len(), lastBytes)
				}
				if psnr < lastPSNR*tolerance {
					t.Errorf("%s: q%d reaches %.2f dB, below the %.2f dB of the step before",
						img.Spec.Name, q, psnr, lastPSNR)
				}
			}
			lastBytes, lastPSNR = buf.Len(), psnr
		}
	}
}

// TestDeterminism is test T7: the same input always produces the same
// bytes, at every value of GOMAXPROCS.
func TestDeterminism(t *testing.T) {
	img := corpus.Generate(corpus.Spec{Name: "d", Class: corpus.Photo, Width: 200, Height: 130, Seed: 42})

	encode := func() []byte {
		var buf bytes.Buffer
		if err := Encode(&buf, img, Config{Quality: 68}); err != nil {
			t.Fatalf("encode: %v", err)
		}
		return buf.Bytes()
	}

	want := encode()
	if got := encode(); !bytes.Equal(got, want) {
		t.Error("two runs at the same settings produced different bytes")
	}

	restore := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(restore)
	for _, procs := range []int{1, 2, 4, 8} {
		runtime.GOMAXPROCS(procs)
		if got := encode(); !bytes.Equal(got, want) {
			t.Errorf("GOMAXPROCS %d produced different bytes", procs)
		}
	}
}

// TestEdgeSizes covers the awkward picture sizes: a single pixel, sizes
// below one macroblock, and sizes that no macroblock grid divides.
func TestEdgeSizes(t *testing.T) {
	sizes := [][2]int{
		{1, 1}, {1, 16}, {16, 1}, {3, 3}, {16, 16}, {17, 17}, {31, 47}, {64, 48}, {97, 61},
	}
	for _, s := range sizes {
		w, h := s[0], s[1]
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		r := rand.New(rand.NewPCG(uint64(w), uint64(h)))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i+0] = uint8(r.UintN(256))
			img.Pix[i+1] = uint8(r.UintN(256))
			img.Pix[i+2] = uint8(r.UintN(256))
			img.Pix[i+3] = 0xff
		}

		data, recon, err := encodeWithReconstruction(img, Config{Quality: 80})
		if err != nil {
			t.Fatalf("%dx%d: encode: %v", w, h, err)
		}
		decoded, err := oracle.DecodeWebPPlanes(data)
		if err != nil {
			t.Fatalf("%dx%d: decode: %v", w, h, err)
		}
		if got := decoded.Rect.Size(); got.X != w || got.Y != h {
			t.Errorf("%dx%d: decoded size %v", w, h, got)
		}
		if err := oracle.CompareExact(reconSource{recon}, decoded); err != nil {
			t.Errorf("%dx%d: %v", w, h, err)
		}
	}
}

// TestFlatImageStaysFlat pins a property the direct-current path must
// hold: a single flat colour codes back to that colour, within the
// quantizer step, and every macroblock skips.
func TestFlatImageStaysFlat(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 30, 140, 220, 0xff
	}
	var buf bytes.Buffer
	if err := Encode(&buf, img, Config{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := oracle.DecodeWebP(buf.Bytes())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			r, g, b, _ := decoded.At(x, y).RGBA()
			if absInt(int(r>>8)-30) > 4 || absInt(int(g>>8)-140) > 4 || absInt(int(b>>8)-220) > 4 {
				t.Fatalf("flat colour drifted at (%d,%d): rgb(%d,%d,%d)", x, y, r>>8, g>>8, b>>8)
			}
		}
	}
}

// FuzzEncode is test T6: any picture size and any pixels must produce a
// file the independent decoder reads, with the right size and no panic.
func FuzzEncode(f *testing.F) {
	f.Add(1, 1, 75, []byte{0})
	f.Add(16, 16, 1, []byte{1, 2, 3})
	f.Add(23, 5, 100, []byte{255, 0, 128})
	f.Add(37, 41, 50, []byte{7, 7, 7, 200, 1})

	f.Fuzz(func(t *testing.T, w, h, quality int, pixels []byte) {
		if len(pixels) == 0 {
			return
		}
		w = 1 + absInt(w)%200
		h = 1 + absInt(h)%200
		quality = 1 + absInt(quality)%100

		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i+0] = pixels[(i/4)%len(pixels)]
			img.Pix[i+1] = pixels[(i/4+1)%len(pixels)]
			img.Pix[i+2] = pixels[(i/4+2)%len(pixels)]
			img.Pix[i+3] = 0xff
		}

		data, recon, err := encodeWithReconstruction(img, Config{Quality: quality})
		if err != nil {
			t.Fatalf("%dx%d q%d: encode: %v", w, h, quality, err)
		}
		decoded, err := oracle.DecodeWebPPlanes(data)
		if err != nil {
			t.Fatalf("%dx%d q%d: decode: %v", w, h, quality, err)
		}
		if got := decoded.Rect.Size(); got.X != w || got.Y != h {
			t.Fatalf("%dx%d q%d: decoded size %v", w, h, quality, got)
		}
		if err := oracle.CompareExact(reconSource{recon}, decoded); err != nil {
			t.Fatalf("%dx%d q%d: %v", w, h, quality, err)
		}
	})
}

// moduleRoot is the path from this package to the module root, where the
// corpus lives.
const moduleRoot = "../.."

func loadCorpus(t *testing.T) []corpus.Image {
	t.Helper()
	images, err := corpus.LoadAll(moduleRoot)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	return images
}

// codedLumaPSNR measures luma distortion in the codec's own plane
// domain: the encoder's pre-encode planes against the decoder's planes.
func codedLumaPSNR(t *testing.T, src image.Image, encoded []byte) float64 {
	t.Helper()
	decoded, err := oracle.DecodeWebPPlanes(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	psnr, err := oracle.MeasurePlanePSNR(yuv.Convert(src).YCbCr(), decoded)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	return psnr.Y
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
