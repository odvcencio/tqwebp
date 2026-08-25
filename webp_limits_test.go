package webp

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"testing"

	"m31labs.dev/tqwebp/internal/corpus"
)

func TestEncodeWithLimitsRejectsDimensionsBeforePixels(t *testing.T) {
	for name, test := range map[string]struct {
		img    image.Image
		limits Limits
	}{
		"width": {
			img:    panicImage{bounds: image.Rect(0, 0, 9, 4)},
			limits: Limits{MaxWidth: 8},
		},
		"height": {
			img:    panicImage{bounds: image.Rect(0, 0, 4, 9)},
			limits: Limits{MaxHeight: 8},
		},
		"pixels": {
			img:    panicImage{bounds: image.Rect(0, 0, 4, 4)},
			limits: Limits{MaxPixels: 15},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out recordingWriter
			err := EncodeWithLimits(&out, test.img, nil, test.limits)
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("error = %v, want ErrLimitExceeded", err)
			}
			if out.writes != 0 {
				t.Fatalf("writer received %d writes on preflight refusal", out.writes)
			}
		})
	}
}

func TestEncodeWithLimitsOutputCapDoesNotPartiallyWrite(t *testing.T) {
	img := opaqueImage(48, 32)
	var want bytes.Buffer
	if err := Encode(&want, img, nil); err != nil {
		t.Fatalf("reference encode: %v", err)
	}

	var got recordingWriter
	err := EncodeWithLimits(&got, img, nil, Limits{MaxOutputBytes: int64(want.Len() - 1)})
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("error = %v, want ErrOutputTooLarge", err)
	}
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("error = %v, want ErrLimitExceeded as well", err)
	}
	if got.writes != 0 || got.data.Len() != 0 {
		t.Fatalf("output was partially written: writes=%d bytes=%d", got.writes, got.data.Len())
	}
}

func TestEncodeWithLimitsAtBoundaryMatchesEncode(t *testing.T) {
	img := corpus.Generate(corpus.Spec{Name: "bounded", Class: corpus.Screenshot, Width: 48, Height: 32, Seed: 17})
	var want bytes.Buffer
	if err := Encode(&want, img, &Options{Quality: 62, Method: 5}); err != nil {
		t.Fatalf("reference encode: %v", err)
	}

	limits := Limits{
		MaxWidth:       img.Bounds().Dx(),
		MaxHeight:      img.Bounds().Dy(),
		MaxPixels:      int64(img.Bounds().Dx()) * int64(img.Bounds().Dy()),
		MaxOutputBytes: int64(want.Len()),
	}
	var got bytes.Buffer
	if err := EncodeWithLimits(&got, img, &Options{Quality: 62, Method: 5}, limits); err != nil {
		t.Fatalf("bounded encode: %v", err)
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatal("bounded output differs from Encode at every limit boundary")
	}
}

func TestEncodeWithLimitsZeroIsUnboundedAndDeterministic(t *testing.T) {
	img := corpus.Generate(corpus.Spec{Name: "bounded-zero", Class: corpus.Flat, Width: 40, Height: 24, Seed: 19})
	encode := func() []byte {
		var out bytes.Buffer
		if err := EncodeWithLimits(&out, img, &Options{Quality: 70}, Limits{}); err != nil {
			t.Fatalf("bounded encode: %v", err)
		}
		return append([]byte(nil), out.Bytes()...)
	}

	want := encode()
	for i := 0; i < 3; i++ {
		if got := encode(); !bytes.Equal(got, want) {
			t.Fatalf("run %d produced different bytes", i)
		}
	}

	var legacy bytes.Buffer
	if err := Encode(&legacy, img, &Options{Quality: 70}); err != nil {
		t.Fatalf("legacy encode: %v", err)
	}
	if !bytes.Equal(want, legacy.Bytes()) {
		t.Fatal("zero Limits changed Encode output")
	}
}

func TestEncodeWithLimitsRejectsNegativeLimits(t *testing.T) {
	img := opaqueImage(8, 8)
	tests := []struct {
		name   string
		limits Limits
	}{
		{name: "width", limits: Limits{MaxWidth: -1}},
		{name: "height", limits: Limits{MaxHeight: -1}},
		{name: "pixels", limits: Limits{MaxPixels: -1}},
		{name: "output", limits: Limits{MaxOutputBytes: -1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out recordingWriter
			err := EncodeWithLimits(&out, img, nil, test.limits)
			if !errors.Is(err, ErrInvalidLimits) {
				t.Fatalf("error = %v, want ErrInvalidLimits", err)
			}
			if out.writes != 0 {
				t.Fatalf("writer received %d writes for invalid limits", out.writes)
			}
		})
	}
}

type panicImage struct {
	bounds image.Rectangle
}

func (panicImage) ColorModel() color.Model   { return color.RGBAModel }
func (m panicImage) Bounds() image.Rectangle { return m.bounds }
func (panicImage) At(int, int) color.Color   { panic("At called during limit preflight") }
func (panicImage) Opaque() bool              { panic("Opaque called during limit preflight") }

type recordingWriter struct {
	data   bytes.Buffer
	writes int
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.data.Write(p)
}

func opaqueImage(width, height int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 3; i < len(m.Pix); i += 4 {
		m.Pix[i] = 0xff
	}
	return m
}
