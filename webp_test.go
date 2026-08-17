package webp

import (
	"bytes"
	"image"
	"image/color"
	"runtime"
	"testing"

	"m31labs.dev/tqwebp/internal/corpus"
	"m31labs.dev/tqwebp/oracle"
)

// TestEncodeRoundTrip proves the public interface writes a file the
// independent decoder reads back at the right size.
func TestEncodeRoundTrip(t *testing.T) {
	img := corpus.Generate(corpus.Spec{Name: "p", Class: corpus.Photo, Width: 96, Height: 70, Seed: 3})
	var buf bytes.Buffer
	if err := Encode(&buf, img, &Options{Quality: 70}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := oracle.DecodeWebP(buf.Bytes())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, want := decoded.Bounds().Size(), img.Bounds().Size(); got != want {
		t.Errorf("decoded size %v, want %v", got, want)
	}
}

// TestDeterminismThroughPublicInterface is test T7 at the public seam:
// the same call always writes the same bytes, at every value of
// GOMAXPROCS.
func TestDeterminismThroughPublicInterface(t *testing.T) {
	img := corpus.Generate(corpus.Spec{Name: "d", Class: corpus.Screenshot, Width: 130, Height: 90, Seed: 11})
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

// TestErrors pins the refusals of the public interface.
func TestErrors(t *testing.T) {
	opaque := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range opaque.Pix {
		opaque.Pix[i] = 0xff
	}

	t.Run("alpha", func(t *testing.T) {
		m := image.NewNRGBA(image.Rect(0, 0, 8, 8))
		for i := range m.Pix {
			m.Pix[i] = 0xff
		}
		m.Set(3, 3, color.NRGBA{R: 1, G: 2, B: 3, A: 128})
		if err := Encode(&bytes.Buffer{}, m, nil); err != ErrAlphaUnsupported {
			t.Errorf("error is %v, want ErrAlphaUnsupported", err)
		}
	})

	t.Run("quality-range", func(t *testing.T) {
		for _, q := range []int{-1, 101} {
			if err := Encode(&bytes.Buffer{}, opaque, &Options{Quality: q}); err == nil {
				t.Errorf("quality %d was accepted", q)
			}
		}
	})

	t.Run("method-range", func(t *testing.T) {
		for _, m := range []int{-1, 7} {
			if err := Encode(&bytes.Buffer{}, opaque, &Options{Method: m}); err == nil {
				t.Errorf("method %d was accepted", m)
			}
		}
	})

	t.Run("empty", func(t *testing.T) {
		if err := Encode(&bytes.Buffer{}, image.NewRGBA(image.Rect(0, 0, 0, 0)), nil); err == nil {
			t.Error("an empty image was accepted")
		}
	})

	t.Run("too-large", func(t *testing.T) {
		if err := Encode(&bytes.Buffer{}, fakeHuge{}, nil); err != ErrTooLarge {
			t.Errorf("error is %v, want ErrTooLarge", err)
		}
	})
}

// fakeHuge reports a size past the 14-bit picture size fields without
// allocating anything.
type fakeHuge struct{}

func (fakeHuge) ColorModel() color.Model { return color.RGBAModel }
func (fakeHuge) Bounds() image.Rectangle { return image.Rect(0, 0, 16384, 8) }
func (fakeHuge) At(int, int) color.Color { return color.RGBA{A: 0xff} }
func (fakeHuge) Opaque() bool            { return true }

// TestNilOptionsMatchesDefaults pins the documented zero value.
func TestNilOptionsMatchesDefaults(t *testing.T) {
	img := corpus.Generate(corpus.Spec{Name: "n", Class: corpus.Flat, Width: 48, Height: 32, Seed: 9})
	var withNil, withDefaults, withZero bytes.Buffer
	if err := Encode(&withNil, img, nil); err != nil {
		t.Fatalf("encode with nil options: %v", err)
	}
	if err := Encode(&withDefaults, img, &Options{Quality: DefaultQuality, Method: DefaultMethod}); err != nil {
		t.Fatalf("encode with explicit defaults: %v", err)
	}
	if err := Encode(&withZero, img, &Options{}); err != nil {
		t.Fatalf("encode with the zero value: %v", err)
	}
	if !bytes.Equal(withNil.Bytes(), withDefaults.Bytes()) {
		t.Error("nil options and the explicit defaults produced different bytes")
	}
	if !bytes.Equal(withNil.Bytes(), withZero.Bytes()) {
		t.Error("the zero value and nil options produced different bytes")
	}
}
