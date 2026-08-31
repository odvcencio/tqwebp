package alpha

import (
	"bytes"
	"image"
	"image/color"
	"math/rand"
	"testing"
)

// TestHeaderByteBitLayout pins the ALPH header byte against the field
// order of the WebP container specification: two reserved bits, then
// pre-processing, then filtering, then compression.
func TestHeaderByteBitLayout(t *testing.T) {
	cases := []struct {
		preprocessing, filter, compression int
		want                               byte
	}{
		{0, FilterNone, CompressionNone, 0x00},
		{0, FilterHorizontal, CompressionNone, 0x04},
		{0, FilterVertical, CompressionNone, 0x08},
		{0, FilterGradient, CompressionNone, 0x0c},
		{0, FilterNone, CompressionVP8L, 0x01},
		{1, FilterGradient, CompressionVP8L, 0x1d},
		{3, FilterGradient, CompressionVP8L, 0x3d},
	}
	for _, c := range cases {
		got := HeaderByte(c.preprocessing, c.filter, c.compression)
		if got != c.want {
			t.Errorf("HeaderByte(%d,%d,%d) = %#02x, want %#02x",
				c.preprocessing, c.filter, c.compression, got, c.want)
		}
		// The decoder of golang.org/x/image/webp reads the two fields
		// back with these two masks, so read them back the same way.
		if int(got&0x03) != c.compression {
			t.Errorf("compression reads back as %d, want %d", got&0x03, c.compression)
		}
		if int((got>>2)&0x03) != c.filter {
			t.Errorf("filter reads back as %d, want %d", (got>>2)&0x03, c.filter)
		}
	}
}

// TestFilterRoundTrip proves each filter is exactly invertible by the
// unfilter rule libwebp and golang.org/x/image/webp both implement.
func TestFilterRoundTrip(t *testing.T) {
	planes := map[string]*Plane{
		"random":      randomPlane(37, 23, 1),
		"flat":        constantPlane(19, 11, 200),
		"transparent": constantPlane(16, 16, 0),
		"opaque":      constantPlane(16, 16, 255),
		"one-pixel":   randomPlane(1, 1, 2),
		"one-row":     randomPlane(64, 1, 3),
		"one-column":  randomPlane(1, 64, 4),
		"binary":      binaryPlane(40, 30),
	}
	filters := []int{FilterNone, FilterHorizontal, FilterVertical, FilterGradient}

	for name, p := range planes {
		for _, f := range filters {
			filtered := make([]uint8, p.Width*p.Height)
			Filter(filtered, p, f)
			Unfilter(filtered, p.Width, p.Height, f)
			if !bytes.Equal(filtered, p.A) {
				t.Errorf("%s filter %d did not invert", name, f)
			}
		}
	}
}

// TestChunkLayout pins the payload Chunk builds: one header byte, then
// exactly one byte per visible pixel.
func TestChunkLayout(t *testing.T) {
	p := randomPlane(13, 7, 5)
	for _, f := range []int{FilterNone, FilterHorizontal, FilterVertical, FilterGradient} {
		chunk, err := Chunk(p, f)
		if err != nil {
			t.Fatalf("filter %d: %v", f, err)
		}
		if want := 1 + 13*7; len(chunk) != want {
			t.Errorf("filter %d: chunk is %d bytes, want %d", f, len(chunk), want)
		}
		if want := HeaderByte(0, f, CompressionNone); chunk[0] != want {
			t.Errorf("filter %d: header byte is %#02x, want %#02x", f, chunk[0], want)
		}
	}

	// A filter method outside 0 to 3 is a programming error, not a
	// silent fallback.
	if _, err := Chunk(p, 4); err == nil {
		t.Error("filter 4 was accepted")
	}
}

// TestChunkFilterNoneIsThePlane proves the plain payload holds the alpha
// samples in raster order, which is what a raw decode reads back.
func TestChunkFilterNoneIsThePlane(t *testing.T) {
	p := randomPlane(9, 5, 6)
	chunk, err := Chunk(p, FilterNone)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(chunk[1:], p.A) {
		t.Error("the payload after the header byte is not the plane")
	}
}

// TestExtractAgreesAcrossImageTypes proves every fast path reads the
// same alpha samples the generic path reads.
func TestExtractAgreesAcrossImageTypes(t *testing.T) {
	const w, h = 11, 9
	rng := rand.New(rand.NewSource(21))
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			nrgba.SetNRGBA(x, y, color.NRGBA{
				R: uint8(rng.Intn(256)),
				G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)),
				A: uint8(rng.Intn(256)),
			})
		}
	}
	want := Extract(nrgba)

	rgba := image.NewRGBA(nrgba.Bounds())
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			rgba.Set(x, y, nrgba.At(x, y))
		}
	}
	if got := Extract(rgba); !bytes.Equal(got.A, want.A) {
		t.Error("*image.RGBA gives different alpha than *image.NRGBA")
	}

	if got := Extract(generic{nrgba}); !bytes.Equal(got.A, want.A) {
		t.Error("the generic path gives different alpha than *image.NRGBA")
	}

	alphaOnly := image.NewAlpha(nrgba.Bounds())
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			alphaOnly.SetAlpha(x, y, color.Alpha{A: want.At(x, y)})
		}
	}
	if got := Extract(alphaOnly); !bytes.Equal(got.A, want.A) {
		t.Error("*image.Alpha gives different alpha than *image.NRGBA")
	}
}

// TestExtractHonoursANonZeroOrigin proves Extract reads from the
// picture's own rectangle, not from the buffer's first pixel.
func TestExtractHonoursANonZeroOrigin(t *testing.T) {
	full := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := range full.Pix {
		full.Pix[i] = 0xff
	}
	full.SetNRGBA(5, 6, color.NRGBA{A: 3})

	sub := full.SubImage(image.Rect(4, 4, 8, 8)).(*image.NRGBA)
	p := Extract(sub)
	if p.Width != 4 || p.Height != 4 {
		t.Fatalf("plane is %dx%d, want 4x4", p.Width, p.Height)
	}
	if got := p.At(1, 2); got != 3 {
		t.Errorf("alpha at (1,2) of the sub-image is %d, want 3", got)
	}
	if got := p.At(0, 0); got != 0xff {
		t.Errorf("alpha at (0,0) of the sub-image is %d, want 255", got)
	}
}

// TestPlaneOpaque pins the test the encoder uses to keep an opaque
// picture in the simple container.
func TestPlaneOpaque(t *testing.T) {
	if !constantPlane(4, 4, 255).Opaque() {
		t.Error("a plane of 255 is not opaque")
	}
	p := constantPlane(4, 4, 255)
	p.A[7] = 254
	if p.Opaque() {
		t.Error("a plane with a sample of 254 reports opaque")
	}
}

type generic struct{ m image.Image }

func (g generic) ColorModel() color.Model { return g.m.ColorModel() }
func (g generic) Bounds() image.Rectangle { return g.m.Bounds() }
func (g generic) At(x, y int) color.Color { return g.m.At(x, y) }

func randomPlane(w, h int, seed int64) *Plane {
	p := NewPlane(w, h)
	rng := rand.New(rand.NewSource(seed))
	for i := range p.A {
		p.A[i] = uint8(rng.Intn(256))
	}
	return p
}

func constantPlane(w, h int, v uint8) *Plane {
	p := NewPlane(w, h)
	for i := range p.A {
		p.A[i] = v
	}
	return p
}

// binaryPlane is the shape a badge mask has: a hard-edged region of
// opaque samples in a transparent field.
func binaryPlane(w, h int) *Plane {
	p := NewPlane(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x-w/2)*(x-w/2)+(y-h/2)*(y-h/2) < (w/3)*(w/3) {
				p.A[y*p.Stride+x] = 0xff
			}
		}
	}
	return p
}
