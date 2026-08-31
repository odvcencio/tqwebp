package webp

import (
	"bytes"
	"image"
	"image/color"
	"math/rand"
	"runtime"
	"testing"

	"m31labs.dev/tqwebp/internal/corpus"
	"m31labs.dev/tqwebp/oracle"
)

// TestAlphaPlaneRoundTripsByteExact is the correctness gate of the alpha
// work package. Compression method 0 stores the alpha plane sample for
// sample, so an independent decode must return every sample unchanged.
// The test asserts per pixel, not on a summary.
func TestAlphaPlaneRoundTripsByteExact(t *testing.T) {
	for _, tc := range alphaCases() {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Encode(&buf, tc.img, &Options{Quality: 75}); err != nil {
				t.Fatalf("encode: %v", err)
			}
			decoded, err := oracle.DecodeWebPFile(buf.Bytes())
			if err != nil {
				t.Fatalf("independent decode: %v", err)
			}
			if !decoded.HasAlpha() {
				t.Fatal("the decoded file carries no alpha plane")
			}

			b := tc.img.Bounds()
			if got := decoded.Planes.Rect.Size(); got != b.Size() {
				t.Fatalf("decoded size %v, want %v", got, b.Size())
			}
			for y := 0; y < b.Dy(); y++ {
				for x := 0; x < b.Dx(); x++ {
					_, _, _, want := tc.img.At(b.Min.X+x, b.Min.Y+y).RGBA()
					if got := decoded.AlphaAt(x, y); got != uint8(want>>8) {
						t.Fatalf("alpha at (%d,%d) decoded as %d, want %d",
							x, y, got, uint8(want>>8))
					}
				}
			}
		})
	}
}

// TestAlphaFileUsesTheExtendedContainer proves the chunk layout on the
// wire: a VP8X chunk with the alpha flag, then ALPH, then VP8.
func TestAlphaFileUsesTheExtendedContainer(t *testing.T) {
	img := translucentNRGBA(21, 13, 7)
	var buf bytes.Buffer
	if err := Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	data := buf.Bytes()

	if got := string(data[0:4]); got != "RIFF" {
		t.Fatalf("the file starts with %q, want RIFF", got)
	}
	if got := string(data[8:12]); got != "WEBP" {
		t.Fatalf("the form type is %q, want WEBP", got)
	}
	if got := string(data[12:16]); got != "VP8X" {
		t.Fatalf("the first chunk is %q, want VP8X", got)
	}
	if data[20]&0x10 == 0 {
		t.Errorf("the VP8X flags byte is %#02x, and its alpha bit is clear", data[20])
	}
	gotW := int(data[24]) | int(data[25])<<8 | int(data[26])<<16
	gotH := int(data[27]) | int(data[28])<<8 | int(data[29])<<16
	if gotW != 20 || gotH != 12 {
		t.Errorf("the canvas fields are %d and %d, want 20 and 12", gotW, gotH)
	}
	if got := string(data[30:34]); got != "ALPH" {
		t.Fatalf("the second chunk is %q, want ALPH", got)
	}
	alphaLen := int(data[34]) | int(data[35])<<8 | int(data[36])<<16 | int(data[37])<<24
	if want := 1 + 21*13; alphaLen != want {
		t.Errorf("the ALPH chunk is %d bytes, want %d", alphaLen, want)
	}
	if got := data[38]; got != 0x00 {
		t.Errorf("the ALPH header byte is %#02x, want 0x00: no filter, no compression", got)
	}
	next := 38 + alphaLen + alphaLen&1
	if got := string(data[next : next+4]); got != "VP8 " {
		t.Errorf("the third chunk is %q, want \"VP8 \"", got)
	}
}

// TestOpaqueOutputStaysSimple proves an opaque picture keeps the simple
// container, with no VP8X chunk and no ALPH chunk. The whole file must
// match what the container writer produces for the frame alone, so the
// alpha work package costs an opaque caller nothing.
func TestOpaqueOutputStaysSimple(t *testing.T) {
	images := map[string]image.Image{
		"corpus-photo":      corpus.Generate(corpus.Spec{Name: "p", Class: corpus.Photo, Width: 96, Height: 70, Seed: 3}),
		"corpus-screenshot": corpus.Generate(corpus.Spec{Name: "s", Class: corpus.Screenshot, Width: 130, Height: 90, Seed: 11}),
		"rgba-all-255":      opaqueRGBA(41, 27, 13),
		"nrgba-all-255":     opaqueNRGBA(41, 27, 13),
	}
	for name, img := range images {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Encode(&buf, img, &Options{Quality: 70}); err != nil {
				t.Fatalf("encode: %v", err)
			}
			data := buf.Bytes()
			if got := string(data[12:16]); got != "VP8 " {
				t.Fatalf("the first chunk is %q, want \"VP8 \": an opaque picture takes no VP8X", got)
			}
			if bytes.Contains(data, []byte("ALPH")) {
				t.Error("an opaque picture wrote an ALPH chunk")
			}
			decoded, err := oracle.DecodeWebPFile(data)
			if err != nil {
				t.Fatalf("independent decode: %v", err)
			}
			if decoded.HasAlpha() {
				t.Error("the decoder found an alpha plane in an opaque file")
			}
		})
	}
}

// TestOpaqueBytesMatchTheSimpleContainerExactly proves the byte-for-byte
// promise directly: the file an opaque encode writes is the RIFF header
// and the VP8 chunk, and nothing else.
func TestOpaqueBytesMatchTheSimpleContainerExactly(t *testing.T) {
	img := corpus.Generate(corpus.Spec{Name: "f", Class: corpus.Flat, Width: 64, Height: 48, Seed: 5})
	var buf bytes.Buffer
	if err := Encode(&buf, img, &Options{Quality: 62}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	data := buf.Bytes()

	payloadLen := int(data[16]) | int(data[17])<<8 | int(data[18])<<16 | int(data[19])<<24
	want := 12 + 8 + payloadLen + payloadLen&1
	if len(data) != want {
		t.Errorf("the file is %d bytes; the header and the VP8 chunk alone need %d", len(data), want)
	}
	riffSize := int(data[4]) | int(data[5])<<8 | int(data[6])<<16 | int(data[7])<<24
	if riffSize != len(data)-8 {
		t.Errorf("the RIFF size field says %d, the file holds %d bytes after it", riffSize, len(data)-8)
	}
}

// TestAlphaColourDecodesWithinLossyTolerance proves the colour planes of
// an alpha file survive the round trip the way every other lossy file's
// planes do. The comparison runs where alpha is high: under a fully
// transparent pixel the colour is unconstrained, and both the encoder
// and every decoder are free with it.
func TestAlphaColourDecodesWithinLossyTolerance(t *testing.T) {
	img := smoothNRGBA(96, 64)
	var buf bytes.Buffer
	if err := Encode(&buf, img, &Options{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := oracle.DecodeWebPNRGBA(buf.Bytes())
	if err != nil {
		t.Fatalf("independent decode: %v", err)
	}

	var sum, count float64
	b := img.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			src := img.NRGBAAt(x, y)
			if src.A < 250 {
				continue
			}
			got := decoded.NRGBAAt(x, y)
			if got.A != src.A {
				t.Fatalf("alpha at (%d,%d) is %d, want %d", x, y, got.A, src.A)
			}
			for _, d := range []int{
				int(got.R) - int(src.R),
				int(got.G) - int(src.G),
				int(got.B) - int(src.B),
			} {
				sum += float64(d * d)
				count++
			}
		}
	}
	if count == 0 {
		t.Fatal("the fixture holds no opaque pixel to measure")
	}
	// A mean squared error of 100 is 28 dB of peak signal-to-noise
	// ratio, which quality 90 clears with room on this fixture.
	if mse := sum / count; mse > 100 {
		t.Errorf("mean squared colour error is %.1f under opaque pixels, want at most 100", mse)
	}
}

// TestAlphaEdgeRows covers the two ends of the alpha range at picture
// boundaries, where an off-by-one in a stride or a pad would show.
func TestAlphaEdgeRows(t *testing.T) {
	const w, h = 33, 19
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(0)
			switch {
			case y == 0: // a fully transparent first row
				a = 0
			case y == h-1: // a fully opaque last row
				a = 255
			case x == 0:
				a = 255
			case x == w-1:
				a = 0
			default:
				a = uint8((x*7 + y*11) % 256)
			}
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 3), G: uint8(y * 5), B: 0x40, A: a})
		}
	}

	var buf bytes.Buffer
	if err := Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := oracle.DecodeWebPFile(buf.Bytes())
	if err != nil {
		t.Fatalf("independent decode: %v", err)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if got, want := decoded.AlphaAt(x, y), img.NRGBAAt(x, y).A; got != want {
				t.Fatalf("alpha at (%d,%d) is %d, want %d", x, y, got, want)
			}
		}
	}
}

// TestAlphaDeterminism repeats the module's determinism gate on the
// alpha path: the same picture and the same options write the same
// bytes at every value of GOMAXPROCS.
func TestAlphaDeterminism(t *testing.T) {
	img := translucentRGBA(70, 50, 31)
	encode := func() []byte {
		var buf bytes.Buffer
		if err := Encode(&buf, img, &Options{Quality: 62}); err != nil {
			t.Fatalf("encode: %v", err)
		}
		return buf.Bytes()
	}

	want := encode()
	restore := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(restore)
	for _, procs := range []int{1, 2, 4, 8} {
		runtime.GOMAXPROCS(procs)
		if got := encode(); !bytes.Equal(got, want) {
			t.Errorf("GOMAXPROCS %d produced different bytes", procs)
		}
	}
}

// TestAlphaSizeIsHonest pins the cost of compression method 0: the file
// grows by exactly the ALPH chunk, which is one header byte, one byte
// per pixel, and any pad byte, plus the 18-byte VP8X chunk and the
// 8-byte ALPH chunk header.
func TestAlphaSizeIsHonest(t *testing.T) {
	const w, h = 64, 48
	opaque := opaqueNRGBA(w, h, 3)
	translucent := image.NewNRGBA(opaque.Bounds())
	copy(translucent.Pix, opaque.Pix)
	translucent.SetNRGBA(w/2, h/2, color.NRGBA{
		R: opaque.NRGBAAt(w/2, h/2).R,
		G: opaque.NRGBAAt(w/2, h/2).G,
		B: opaque.NRGBAAt(w/2, h/2).B,
		A: 254,
	})

	var withoutAlpha, withAlpha bytes.Buffer
	if err := Encode(&withoutAlpha, opaque, nil); err != nil {
		t.Fatalf("encode the opaque picture: %v", err)
	}
	if err := Encode(&withAlpha, translucent, nil); err != nil {
		t.Fatalf("encode the translucent picture: %v", err)
	}

	alphaChunk := 1 + w*h
	overhead := 18 + 8 + alphaChunk + alphaChunk&1
	got := withAlpha.Len() - withoutAlpha.Len()
	if got != overhead {
		t.Errorf("alpha added %d bytes, want exactly %d: 18 for VP8X, 8 for the ALPH header, %d for the plane",
			got, overhead, alphaChunk+alphaChunk&1)
	}
}

type alphaCase struct {
	name string
	img  image.Image
}

func alphaCases() []alphaCase {
	return []alphaCase{
		{"nrgba-random", translucentNRGBA(37, 23, 1)},
		{"rgba-random", translucentRGBA(37, 23, 1)},
		{"nrgba-odd-dimensions", translucentNRGBA(1, 1, 2)},
		{"nrgba-one-row", translucentNRGBA(64, 1, 3)},
		{"nrgba-one-column", translucentNRGBA(1, 64, 4)},
		{"nrgba-macroblock-aligned", translucentNRGBA(32, 32, 5)},
		{"nrgba-badge-mask", badgeNRGBA(48, 48)},
		{"rgba-badge-mask", toRGBA(badgeNRGBA(48, 48))},
		{"nrgba-all-transparent-but-one", oneOpaquePixel(24, 18)},
		{"nrgba-generic-type", genericImage{translucentNRGBA(29, 17, 6)}},
	}
}

func translucentNRGBA(w, h int, seed int64) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(seed))
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
	// Pin both ends of the range, whatever the seed drew. A picture of
	// one pixel has room for one end only, and it must be the
	// translucent one, or the file would take the simple container.
	if w*h == 1 {
		m.SetNRGBA(0, 0, color.NRGBA{R: 9, G: 8, B: 7, A: 128})
		return m
	}
	m.SetNRGBA(0, 0, color.NRGBA{A: 0})
	m.SetNRGBA(w-1, h-1, color.NRGBA{R: 9, G: 8, B: 7, A: 255})
	return m
}

// smoothNRGBA is a picture a lossy codec can actually code: a smooth
// colour gradient under a soft alpha disc. Per-pixel colour noise, which
// translucentNRGBA carries on purpose for the alpha gate, would measure
// the corpus and not the encoder.
func smoothNRGBA(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	cx, cy := float64(w)/2, float64(h)/2
	radius := float64(w) / 3
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			a := uint8(0)
			switch d := dx*dx + dy*dy; {
			case d < radius*radius:
				a = 255
			case d < (radius+3)*(radius+3):
				a = 90
			}
			m.SetNRGBA(x, y, color.NRGBA{
				R: uint8(40 + 200*x/w),
				G: uint8(30 + 200*y/h),
				B: uint8(60 + 120*(x+y)/(w+h)),
				A: a,
			})
		}
	}
	return m
}

func translucentRGBA(w, h int, seed int64) *image.RGBA {
	return toRGBA(translucentNRGBA(w, h, seed))
}

func toRGBA(src *image.NRGBA) *image.RGBA {
	b := src.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out.Set(x, y, src.At(x, y))
		}
	}
	return out
}

func opaqueNRGBA(w, h int, seed int64) *image.NRGBA {
	m := translucentNRGBA(w, h, seed)
	for i := 3; i < len(m.Pix); i += 4 {
		m.Pix[i] = 0xff
	}
	return m
}

func opaqueRGBA(w, h int, seed int64) *image.RGBA {
	return toRGBA(opaqueNRGBA(w, h, seed))
}

// badgeNRGBA is the shape a gridiron badge has: an opaque disc with a
// soft edge, in a fully transparent field.
func badgeNRGBA(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	cx, cy := float64(w)/2, float64(h)/2
	radius := float64(w) / 3
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			d := dx*dx + dy*dy
			a := uint8(0)
			switch {
			case d < radius*radius:
				a = 255
			case d < (radius+2)*(radius+2):
				a = 128
			}
			m.SetNRGBA(x, y, color.NRGBA{R: 0xd0, G: 0x40, B: 0x20, A: a})
		}
	}
	return m
}

func oneOpaquePixel(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	m.SetNRGBA(w/2, h/2, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})
	return m
}

// genericImage hides the concrete type, so the encoder must take the
// path that reads through At.
type genericImage struct{ m image.Image }

func (g genericImage) ColorModel() color.Model { return g.m.ColorModel() }
func (g genericImage) Bounds() image.Rectangle { return g.m.Bounds() }
func (g genericImage) At(x, y int) color.Color { return g.m.At(x, y) }
