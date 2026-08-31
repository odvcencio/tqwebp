package encoder

import (
	"image"
	"image/color"
	"math/rand"
	"testing"

	"m31labs.dev/tqwebp/internal/alpha"
	"m31labs.dev/tqwebp/oracle"
)

// TestEveryAlphaFilterDecodesByteExact runs all four ALPH filtering
// methods through golang.org/x/image/webp, an independent decoder, and
// proves each returns the alpha plane sample for sample.
//
// The shipped encoder selects filter 0. Compression method 0 stores one
// byte per sample whichever filter runs, so no filter can shrink a file
// today. The other three are pinned here because the decode side of a
// later compression method 1 payload uses the same rules, and a wrong
// filter would then be a silent corruption.
func TestEveryAlphaFilterDecodesByteExact(t *testing.T) {
	images := map[string]*image.NRGBA{
		"random":         alphaFixtureRandom(37, 23),
		"binary-mask":    alphaFixtureMask(48, 33),
		"single-row":     alphaFixtureRandom(40, 1),
		"single-column":  alphaFixtureRandom(1, 40),
		"one-pixel":      alphaFixtureOnePixel(),
		"vertical-ramp":  alphaFixtureVerticalRamp(24, 40),
		"horizontal-run": alphaFixtureHorizontalRamp(40, 24),
	}

	for name, img := range images {
		for filter := alpha.FilterNone; filter <= alpha.FilterGradient; filter++ {
			cfg := Config{Quality: 75, Method: 4, AlphaFilter: filter}
			data, _, err := EncodeWithReconstruction(img, cfg)
			if err != nil {
				t.Fatalf("%s filter %d: encode: %v", name, filter, err)
			}
			decoded, err := oracle.DecodeWebPFile(data)
			if err != nil {
				t.Fatalf("%s filter %d: independent decode: %v", name, filter, err)
			}
			if !decoded.HasAlpha() {
				t.Fatalf("%s filter %d: the decoded file carries no alpha plane", name, filter)
			}

			b := img.Bounds()
			for y := 0; y < b.Dy(); y++ {
				for x := 0; x < b.Dx(); x++ {
					want := img.NRGBAAt(x, y).A
					if got := decoded.AlphaAt(x, y); got != want {
						t.Fatalf("%s filter %d: alpha at (%d,%d) is %d, want %d",
							name, filter, x, y, got, want)
					}
				}
			}
		}
	}
}

// TestAlphaFilterKeepsTheFileSize proves what the filter costs and what
// it saves at compression method 0: nothing either way. The claim
// belongs in a test, because the README states it.
func TestAlphaFilterKeepsTheFileSize(t *testing.T) {
	img := alphaFixtureMask(64, 64)
	var sizes []int
	for filter := alpha.FilterNone; filter <= alpha.FilterGradient; filter++ {
		data, _, err := EncodeWithReconstruction(img, Config{Quality: 75, Method: 4, AlphaFilter: filter})
		if err != nil {
			t.Fatalf("filter %d: %v", filter, err)
		}
		sizes = append(sizes, len(data))
	}
	for i, size := range sizes {
		if size != sizes[0] {
			t.Errorf("filter %d wrote %d bytes, filter 0 wrote %d", i, size, sizes[0])
		}
	}
}

// TestAlphaFilterIsRejectedOutOfRange proves an invalid filter is an
// error, not a silent fallback to filter 0.
func TestAlphaFilterIsRejectedOutOfRange(t *testing.T) {
	img := alphaFixtureMask(16, 16)
	if _, _, err := EncodeWithReconstruction(img, Config{Quality: 75, AlphaFilter: 4}); err == nil {
		t.Error("filter 4 was accepted")
	}
}

func alphaFixtureRandom(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(int64(w*1000 + h)))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{
				R: uint8(rng.Intn(256)),
				G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)),
				A: uint8(rng.Intn(256)),
			})
		}
	}
	m.SetNRGBA(0, 0, color.NRGBA{A: 0})
	return m
}

func alphaFixtureMask(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(0)
			if (x-w/2)*(x-w/2)+(y-h/2)*(y-h/2) < (w/3)*(w/3) {
				a = 0xff
			}
			m.SetNRGBA(x, y, color.NRGBA{R: 0x80, G: 0x40, B: 0x20, A: a})
		}
	}
	return m
}

func alphaFixtureOnePixel() *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	m.SetNRGBA(0, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 137})
	return m
}

func alphaFixtureVerticalRamp(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: 0x30, G: 0x60, B: 0x90, A: uint8(y * 255 / h)})
		}
	}
	return m
}

func alphaFixtureHorizontalRamp(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: 0x30, G: 0x60, B: 0x90, A: uint8(x * 255 / w)})
		}
	}
	return m
}
