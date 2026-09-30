package webp

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"io"
	"testing"

	decoder "golang.org/x/image/webp"
)

func TestEncodeInvalidInputs(t *testing.T) {
	var nilImage *image.RGBA
	var nilWriter *bytes.Buffer
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name string
		m    image.Image
		w    io.Writer
		want error
	}{
		{"nil image", nil, io.Discard, ErrInvalidImage},
		{"typed nil image", nilImage, io.Discard, ErrInvalidImage},
		{"nil writer", opaqueImage(1, 1), nil, ErrInvalidWriter},
		{"typed nil writer", opaqueImage(1, 1), nilWriter, ErrInvalidWriter},
		{"empty", panicImage{image.Rectangle{}}, io.Discard, ErrInvalidImage},
		{"inverted", panicImage{image.Rectangle{Min: image.Pt(2, 3), Max: image.Pt(1, 1)}}, io.Discard, ErrInvalidImage},
		{"overflow", panicImage{image.Rectangle{Min: image.Pt(-maxInt-1, 0), Max: image.Pt(maxInt, 1)}}, io.Discard, ErrTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, bounded := range []bool{false, true} {
				var err error
				if bounded {
					err = EncodeWithLimits(test.w, test.m, nil, Limits{})
				} else {
					err = Encode(test.w, test.m, nil)
				}
				if !errors.Is(err, test.want) {
					t.Fatalf("bounded=%v: %v, want %v", bounded, err, test.want)
				}
			}
		})
	}
}

type failingWriter struct {
	calls  int
	failAt int
	err    error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return len(p) - 1, w.err
	}
	return len(p), nil
}

func TestEncodeWriterFailures(t *testing.T) {
	sentinel := errors.New("destination failed")
	for _, limits := range []Limits{{}, {MaxOutputBytes: 1 << 20}} {
		for _, failAt := range []int{1, 2} {
			if limits.MaxOutputBytes > 0 && failAt == 2 {
				continue
			} // capped output has one write
			for _, cause := range []error{nil, sentinel} {
				w := &failingWriter{failAt: failAt, err: cause}
				err := EncodeWithLimits(w, opaqueImage(16, 16), nil, limits)
				want := cause
				if want == nil {
					want = io.ErrShortWrite
				}
				if !errors.Is(err, want) {
					t.Fatalf("limits=%+v write=%d: %v, want %v", limits, failAt, err, want)
				}
				if w.calls != failAt {
					t.Fatalf("continued after failure: %d calls", w.calls)
				}
			}
		}
	}
}

// The API accepts the image.Image contract, including nonzero origins and
// concrete layouts without fast paths. Compare each subimage with its own
// zero-origin copy so sampling and colour conversion stay independent.
func TestEncodeImageModesAndSubimages(t *testing.T) {
	rect := image.Rect(-7, 11, 18, 34)
	rgba := image.NewRGBA(rect)
	nrgba := image.NewNRGBA(rect)
	gray := image.NewGray(rect)
	rgba64 := image.NewRGBA64(rect)
	nrgba64 := image.NewNRGBA64(rect)
	cmyk := image.NewCMYK(rect)
	paletted := image.NewPaletted(rect, color.Palette{color.Black, color.White, color.RGBA{R: 255, A: 255}})
	ycbcr := image.NewYCbCr(rect, image.YCbCrSubsampleRatio420)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			c := color.RGBA{R: uint8(x * 17), G: uint8(y * 13), B: uint8(x + y), A: 255}
			rgba.Set(x, y, c)
			nrgba.Set(x, y, c)
			gray.Set(x, y, c)
			rgba64.Set(x, y, c)
			nrgba64.Set(x, y, c)
			cmyk.Set(x, y, c)
			paletted.Set(x, y, c)
			yy, cb, cr := color.RGBToYCbCr(c.R, c.G, c.B)
			ycbcr.Y[ycbcr.YOffset(x, y)] = yy
			i := ycbcr.COffset(x, y)
			ycbcr.Cb[i] = cb
			ycbcr.Cr[i] = cr
		}
	}
	sub := image.Rect(-3, 13, 14, 32)
	modes := []image.Image{rgba.SubImage(sub), nrgba.SubImage(sub), gray.SubImage(sub), rgba64.SubImage(sub), nrgba64.SubImage(sub), cmyk.SubImage(sub), paletted.SubImage(sub), ycbcr.SubImage(sub), genericImage{rgba.SubImage(sub)}}
	for _, m := range modes {
		copyImage := image.NewRGBA(image.Rect(0, 0, sub.Dx(), sub.Dy()))
		for y := 0; y < sub.Dy(); y++ {
			for x := 0; x < sub.Dx(); x++ {
				copyImage.Set(x, y, m.At(sub.Min.X+x, sub.Min.Y+y))
			}
		}
		var got, want bytes.Buffer
		if err := Encode(&got, m, nil); err != nil {
			t.Fatalf("%T: %v", m, err)
		}
		if err := Encode(&want, copyImage, nil); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("%T subimage differs from its zero-origin copy", m)
		}
		config, err := decoder.DecodeConfig(bytes.NewReader(got.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if config.Width != sub.Dx() || config.Height != sub.Dy() {
			t.Fatalf("%T: wrong decoded size", m)
		}
	}
}

type genericImage struct{ image.Image }

func FuzzEncodeAPI(f *testing.F) {
	f.Add([]byte{1, 2, 3, 255}, uint8(17), uint8(19), int8(-3), uint8(75), uint8(6))
	f.Add([]byte{255, 0, 0, 128}, uint8(1), uint8(1), int8(2), uint8(100), uint8(4))
	f.Fuzz(func(t *testing.T, pixels []byte, w, h uint8, origin int8, q, method uint8) {
		if len(pixels) == 0 {
			return
		}
		width, height := int(w%32)+1, int(h%32)+1
		rect := image.Rect(int(origin), int(origin), int(origin)+width, int(origin)+height)
		m := image.NewNRGBA(rect)
		for i := range m.Pix {
			m.Pix[i] = pixels[i%len(pixels)]
			if i%4 == 3 && method&128 == 0 {
				m.Pix[i] = 255
			}
		}
		options := &Options{Quality: int(q % 102), Method: int(method % 8)}
		var out bytes.Buffer
		err := EncodeWithLimits(&out, m, options, Limits{MaxPixels: 1024, MaxOutputBytes: 1 << 20})
		if options.Quality > 100 || options.Method > 6 {
			if !errors.Is(err, ErrInvalidOptions) {
				t.Fatalf("options: %v", err)
			}
			return
		}
		if !m.Opaque() {
			if !errors.Is(err, ErrAlphaUnsupported) {
				t.Fatalf("alpha: %v", err)
			}
			if out.Len() != 0 {
				t.Fatal("wrote refused input")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decoder.Decode(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Bounds().Dx() != width || decoded.Bounds().Dy() != height {
			t.Fatal("wrong dimensions")
		}
		var again bytes.Buffer
		if err := Encode(&again, m, options); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), again.Bytes()) {
			t.Fatal("nondeterministic or bounded bytes differ")
		}
	})
}
