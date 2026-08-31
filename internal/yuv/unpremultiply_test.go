package yuv

import (
	"bytes"
	"image"
	"image/color"
	"math/rand"
	"testing"
)

// TestOpaqueConversionIsUnchanged proves the un-premultiply step cannot
// move an opaque picture's planes. The opaque path is a release gate:
// every file an earlier release wrote must still come out byte for byte.
func TestOpaqueConversionIsUnchanged(t *testing.T) {
	const w, h = 37, 21
	rng := rand.New(rand.NewSource(4))
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{
				R: uint8(rng.Intn(256)),
				G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)),
				A: 0xff,
			}
			nrgba.SetNRGBA(x, y, c)
			rgba.SetRGBA(x, y, color.RGBA{R: c.R, G: c.G, B: c.B, A: 0xff})
		}
	}

	from := Convert(rgba)
	to := Convert(nrgba)
	comparePlanes(t, "opaque RGBA against NRGBA", from, to)
}

// TestPremultipliedAndStraightAgree proves the un-premultiply step
// recovers the colour the straight type carries. The two types cannot
// agree to the last bit, because the premultiply already threw
// resolution away, so the test bounds the difference instead.
func TestPremultipliedAndStraightAgree(t *testing.T) {
	const w, h = 32, 32
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(17))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Keep the alpha high enough that the premultiply still
			// carries the colour: at a low alpha the round trip loses
			// most of the colour resolution, which is a property of the
			// premultiplied type, not of this package.
			c := color.NRGBA{
				R: uint8(rng.Intn(256)),
				G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)),
				A: uint8(128 + rng.Intn(128)),
			}
			nrgba.SetNRGBA(x, y, c)
			rgba.Set(x, y, c)
		}
	}

	straight := Convert(nrgba)
	recovered := Convert(rgba)
	const tolerance = 3
	for i := range straight.Y {
		if diff(straight.Y[i], recovered.Y[i]) > tolerance {
			t.Fatalf("luma sample %d is %d after un-premultiply, want %d within %d",
				i, recovered.Y[i], straight.Y[i], tolerance)
		}
	}
}

// TestUnpremultiplyMatchesTheStandardLibrary walks every legal
// (colour, alpha) byte pair and proves the package's arithmetic is
// color.NRGBAModel's arithmetic, sample for sample. That agreement is
// what lets an *image.RGBA and the *image.NRGBA the standard library
// converts it to produce the same planes.
func TestUnpremultiplyMatchesTheStandardLibrary(t *testing.T) {
	for a := 0; a < 256; a++ {
		for c := 0; c <= a; c++ {
			src := color.RGBA{R: uint8(c), G: uint8(c), B: uint8(c), A: uint8(a)}
			want := color.NRGBAModel.Convert(src).(color.NRGBA).R
			if got := unpremultiply8(uint8(c), uint8(a)); got != want {
				t.Fatalf("unpremultiply8(%d, %d) = %d, want %d", c, a, got, want)
			}
			r16, _, _, a16 := src.RGBA()
			if got := unpremultiply16(r16, a16); got != want {
				t.Fatalf("unpremultiply16(%d, %d) = %d, want %d", r16, a16, got, want)
			}
		}
	}
}

// TestUnpremultiplyClampsAnImpossibleSample proves a colour above its
// own alpha, which no valid premultiplied pixel holds, clamps instead of
// wrapping around.
func TestUnpremultiplyClampsAnImpossibleSample(t *testing.T) {
	if got := unpremultiply8(200, 100); got != 0xff {
		t.Errorf("unpremultiply8(200, 100) = %d, want 255", got)
	}
	if got := unpremultiply16(0xc000, 0x4000); got != 0xff {
		t.Errorf("unpremultiply16(0xc000, 0x4000) = %d, want 255", got)
	}
}

// TestAlphaZeroConvertsAsBlackForRGBA pins the documented choice: an
// *image.RGBA pixel of alpha zero holds no colour to recover, so it
// converts as black.
func TestAlphaZeroConvertsAsBlackForRGBA(t *testing.T) {
	rgba := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := range rgba.Pix {
		rgba.Pix[i] = 0
	}
	p := Convert(rgba)
	wantY := RGBToY(0, 0, 0)
	for i, v := range p.Y {
		if v != wantY {
			t.Fatalf("luma sample %d is %d, want %d for black", i, v, wantY)
		}
	}
}

// TestAlphaZeroKeepsColourForNRGBA pins the other side of the same
// choice: an *image.NRGBA never lost its colour, so alpha zero keeps it.
func TestAlphaZeroKeepsColourForNRGBA(t *testing.T) {
	nrgba := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			nrgba.SetNRGBA(x, y, color.NRGBA{R: 200, G: 100, B: 50, A: 0})
		}
	}
	p := Convert(nrgba)
	wantY := RGBToY(200, 100, 50)
	for i, v := range p.Y {
		if v != wantY {
			t.Fatalf("luma sample %d is %d, want %d", i, v, wantY)
		}
	}
}

// TestNYCbCrAReadsAsStraightColour proves the type an alpha WebP decode
// returns converts through the same planes its opaque part carries.
func TestNYCbCrAReadsAsStraightColour(t *testing.T) {
	const w, h = 24, 18
	rng := rand.New(rand.NewSource(29))
	withAlpha := image.NewNYCbCrA(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	for i := range withAlpha.Y {
		withAlpha.Y[i] = uint8(rng.Intn(256))
	}
	for i := range withAlpha.Cb {
		withAlpha.Cb[i] = uint8(rng.Intn(256))
		withAlpha.Cr[i] = uint8(rng.Intn(256))
	}
	for i := range withAlpha.A {
		withAlpha.A[i] = uint8(rng.Intn(256))
	}

	plain := &image.YCbCr{
		Y:              withAlpha.Y,
		Cb:             withAlpha.Cb,
		Cr:             withAlpha.Cr,
		YStride:        withAlpha.YStride,
		CStride:        withAlpha.CStride,
		SubsampleRatio: withAlpha.SubsampleRatio,
		Rect:           withAlpha.Rect,
	}
	comparePlanes(t, "NYCbCrA against its own YCbCr", Convert(withAlpha), Convert(plain))
}

func comparePlanes(t *testing.T, what string, got, want *Planes) {
	t.Helper()
	if !bytes.Equal(got.Y, want.Y) {
		t.Errorf("%s: the luma planes differ", what)
	}
	if !bytes.Equal(got.U, want.U) {
		t.Errorf("%s: the blue-difference planes differ", what)
	}
	if !bytes.Equal(got.V, want.V) {
		t.Errorf("%s: the red-difference planes differ", what)
	}
}

func diff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}
