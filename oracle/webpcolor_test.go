package oracle

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

const bt601Dir = "../testdata/golden/bt601"

// TestGoldenLibwebpInverse is the inverse half of the BT.601 pin. It
// decodes the fixture file's planes, inverts them with this package's
// libwebp-shaped conversion, and compares the result with the RGB pixels
// libwebp itself produced from the same file.
//
// A match proves that a browser sees what this repository measures. The
// tolerance is one, which covers nothing more than a rounding step; the
// full-range JFIF conversion golang.org/x/image applies differs by 16 or
// more on dark pixels, and TestXImageConventionDiffers pins that gap.
func TestGoldenLibwebpInverse(t *testing.T) {
	data, err := os.ReadFile(bt601Dir + "/libwebp_q100.webp")
	if err != nil {
		t.Fatalf("read WebP fixture: %v", err)
	}
	wantBytes, err := os.ReadFile(bt601Dir + "/libwebp_rgb.png")
	if err != nil {
		t.Fatalf("read RGB fixture: %v", err)
	}
	want, err := png.Decode(bytes.NewReader(wantBytes))
	if err != nil {
		t.Fatalf("decode RGB fixture: %v", err)
	}

	got, err := DecodeWebP(data)
	if err != nil {
		t.Fatalf("decode WebP with the libwebp inverse: %v", err)
	}
	if got.Bounds() != want.Bounds() {
		t.Fatalf("bounds %v, want %v", got.Bounds(), want.Bounds())
	}

	// libwebp upsamples chroma with its "fancy" bilinear filter, and this
	// package repeats each chroma sample over its 2x2 box, as
	// golang.org/x/image does. The two agree wherever the neighbouring
	// chroma samples are equal, which inside the fixture means every
	// pixel that sits two or more pixels away from a patch edge. The
	// comparison skips the edge band, because an upsampling policy is not
	// what this test pins. The fixture's patches are 16 pixels wide.
	const (
		tolerance = 1
		patch     = 16
		margin    = 2
	)
	inside := func(v int) bool {
		p := v % patch
		return p >= margin && p < patch-margin
	}

	worst := 0
	for y := 0; y < want.Bounds().Dy(); y++ {
		for x := 0; x < want.Bounds().Dx(); x++ {
			if !inside(x) || !inside(y) {
				continue
			}
			gr, gg, gb, _ := got.At(x, y).RGBA()
			wr, wg, wb, _ := want.At(x, y).RGBA()
			for _, d := range []int{
				diff(int(gr>>8), int(wr>>8)),
				diff(int(gg>>8), int(wg>>8)),
				diff(int(gb>>8), int(wb>>8)),
			} {
				if d > worst {
					worst = d
				}
				if d > tolerance {
					t.Fatalf("pixel (%d,%d): tqwebp rgb(%d,%d,%d), libwebp rgb(%d,%d,%d)",
						x, y, gr>>8, gg>>8, gb>>8, wr>>8, wg>>8, wb>>8)
				}
			}
		}
	}
	t.Logf("largest channel difference against libwebp: %d", worst)
}

// TestXImageConventionDiffers documents the trap this file exists for. It
// decodes the same fixture through golang.org/x/image's own colour path
// and shows that the result is far from libwebp's. Measurements that use
// the wrong path charge the encoder for the decoder's convention.
func TestXImageConventionDiffers(t *testing.T) {
	data, err := os.ReadFile(bt601Dir + "/libwebp_q100.webp")
	if err != nil {
		t.Fatalf("read WebP fixture: %v", err)
	}
	planes, err := DecodeWebPPlanes(data)
	if err != nil {
		t.Fatalf("decode planes: %v", err)
	}
	correct := WebPPlanesToRGBA(planes)

	worst := 0
	b := planes.Rect
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			xr, xg, xb, _ := planes.At(b.Min.X+x, b.Min.Y+y).RGBA()
			cr, cg, cb, _ := correct.At(x, y).RGBA()
			for _, d := range []int{
				diff(int(xr>>8), int(cr>>8)),
				diff(int(xg>>8), int(cg>>8)),
				diff(int(xb>>8), int(cb>>8)),
			} {
				if d > worst {
					worst = d
				}
			}
		}
	}
	if worst < 8 {
		t.Errorf("largest difference between the two conventions is %d; the trap this file guards against would be harmless at that size", worst)
	}
	t.Logf("golang.org/x/image differs from libwebp by up to %d per channel on the fixture", worst)
}

func diff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
