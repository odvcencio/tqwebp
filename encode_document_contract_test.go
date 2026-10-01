package webp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"reflect"
	"testing"
	"time"

	xwebp "golang.org/x/image/webp"
	webp "m31labs.dev/tqwebp"
	"m31labs.dev/tqwebp/container"
)

// These tests compare the actual VP8 payload with a separately built opaque
// straight-color image. Lossy RGB output itself is deliberately not asserted
// equal to input RGB; raw alpha must remain exact.
type contractAlphaFixture struct {
	name   string
	source image.Image
	rgb    *image.NRGBA
	alpha  []byte
}

type contractPremulColor struct{ r, g, b, a uint32 }

func (c contractPremulColor) RGBA() (uint32, uint32, uint32, uint32) {
	return c.r, c.g, c.b, c.a
}

type contractGenericImage struct {
	rect   image.Rectangle
	colors []color.Color
}

func (m *contractGenericImage) ColorModel() color.Model { return color.RGBA64Model }
func (m *contractGenericImage) Bounds() image.Rectangle { return m.rect }
func (m *contractGenericImage) At(x, y int) color.Color {
	if !image.Pt(x, y).In(m.rect) {
		panic("encoder sampled outside visible bounds")
	}
	return m.colors[(y-m.rect.Min.Y)*m.rect.Dx()+x-m.rect.Min.X]
}

// The expected unpremultiplication is integer arithmetic on the fixture's
// stored samples, independent of both the production helper and color models.
func contractUnpremul(r, g, b, a uint32) color.NRGBA {
	if a == 0 {
		return color.NRGBA{}
	}
	return color.NRGBA{
		R: uint8((uint64(r) * 65535 / uint64(a)) >> 8),
		G: uint8((uint64(g) * 65535 / uint64(a)) >> 8),
		B: uint8((uint64(b) * 65535 / uint64(a)) >> 8),
		A: uint8(a >> 8),
	}
}

func contractAlphaFixtures() []contractAlphaFixture {
	// Every source has odd dimensions and a nonzero/negative origin. Standard
	// image sources are subimages with a larger stride than their visible rows.
	r := image.Rect(3, -2, 8, 1)
	outer := image.Rect(1, -4, 11, 4)
	n := image.NewNRGBA(outer).SubImage(r).(*image.NRGBA)
	p := image.NewRGBA(outer).SubImage(r).(*image.RGBA)
	n64 := image.NewNRGBA64(outer).SubImage(r).(*image.NRGBA64)
	p64 := image.NewRGBA64(outer).SubImage(r).(*image.RGBA64)
	generic := &contractGenericImage{rect: r, colors: make([]color.Color, r.Dx()*r.Dy())}
	palette := color.Palette{
		color.NRGBA{R: 241, G: 37, B: 199, A: 0},
		color.NRGBA{R: 233, G: 53, B: 117, A: 1},
		color.NRGBA{R: 83, G: 179, B: 229, A: 127},
		color.NRGBA64{R: 0xfefe, G: 0x817f, B: 0x017f, A: 0xfeff},
		color.NRGBA{R: 51, G: 119, B: 201, A: 255},
	}
	pal := image.NewPaletted(outer, palette).SubImage(r).(*image.Paletted)
	fixtures := []contractAlphaFixture{
		{name: "NRGBA", source: n}, {name: "RGBA", source: p},
		{name: "NRGBA64", source: n64}, {name: "RGBA64", source: p64},
		{name: "generic", source: generic}, {name: "palette", source: pal},
	}
	for i := range fixtures {
		fixtures[i].rgb = image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		fixtures[i].alpha = make([]byte, r.Dx()*r.Dy())
	}
	a8s := []uint8{0, 1, 127, 254, 255}
	a16s := []uint32{0, 1, 0xff, 0x100, 0x7fff, 0xfeff, 0xfffe, 0xffff}
	for y := 0; y < r.Dy(); y++ {
		for x := 0; x < r.Dx(); x++ {
			i, sx, sy := y*r.Dx()+x, r.Min.X+x, r.Min.Y+y
			a8 := a8s[i%len(a8s)]
			straight := color.NRGBA{R: uint8(241 - i*7), G: uint8(37 + i*11), B: uint8(199 - i*5), A: a8}
			n.SetNRGBA(sx, sy, straight)
			premul := color.RGBA{R: uint8(uint16(a8) * 3 / 4), G: a8 / 3, B: a8 / 2, A: a8}
			p.SetRGBA(sx, sy, premul)
			a16 := a16s[i%len(a16s)]
			straight16 := color.NRGBA64{R: uint16(0xfeff - i*0x103), G: uint16(0x817f + i*0x81), B: uint16(0x01ff + i*0x301), A: uint16(a16)}
			n64.SetNRGBA64(sx, sy, straight16)
			premul16 := color.RGBA64{R: uint16(a16 * 3 / 4), G: uint16(a16 / 3), B: uint16(a16 / 2), A: uint16(a16)}
			p64.SetRGBA64(sx, sy, premul16)
			generic.colors[i] = contractPremulColor{a16 * 5 / 7, a16 / 7, a16 * 4 / 5, a16}
			pal.SetColorIndex(sx, sy, uint8(i%len(palette)))
			palExpected := []color.NRGBA{
				{R: 241, G: 37, B: 199, A: 0}, {R: 233, G: 53, B: 117, A: 1},
				{R: 83, G: 179, B: 229, A: 127}, {R: 254, G: 129, B: 1, A: 254},
				{R: 51, G: 119, B: 201, A: 255},
			}
			expected := []color.NRGBA{
				straight,
				contractUnpremul(uint32(premul.R)*257, uint32(premul.G)*257, uint32(premul.B)*257, uint32(a8)*257),
				{R: uint8(straight16.R >> 8), G: uint8(straight16.G >> 8), B: uint8(straight16.B >> 8), A: uint8(a16 >> 8)},
				contractUnpremul(uint32(premul16.R), uint32(premul16.G), uint32(premul16.B), a16),
				contractUnpremul(a16*5/7, a16/7, a16*4/5, a16),
				palExpected[i%len(palette)],
			}
			for j, c := range expected {
				fixtures[j].alpha[i] = c.A
				c.A = 255
				fixtures[j].rgb.SetNRGBA(x, y, c)
			}
		}
	}
	return fixtures
}

func contractDemux(t *testing.T, data []byte) *container.File {
	t.Helper()
	f, err := container.Demux(context.Background(), bytes.NewReader(data), container.Limits{})
	if err != nil {
		t.Fatalf("demux encoded output: %v", err)
	}
	return f
}

func contractCheckIndependentAlpha(t *testing.T, data, alpha []byte, canvas image.Point) {
	t.Helper()
	decoded, err := xwebp.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("independent x/image/webp decode: %v", err)
	}
	if decoded.Bounds() != image.Rect(0, 0, canvas.X, canvas.Y) {
		t.Fatalf("decoded bounds = %v", decoded.Bounds())
	}
	for y := 0; y < canvas.Y; y++ {
		for x := 0; x < canvas.X; x++ {
			_, _, _, got := decoded.At(x, y).RGBA()
			if want := uint32(alpha[y*canvas.X+x]) * 257; got != want {
				t.Fatalf("independent alpha at (%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}
}

func TestEncodeAlphaContractStraightRGBAndExactAlpha(t *testing.T) {
	for _, fixture := range contractAlphaFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			options := &webp.Options{Quality: 80, Method: 1}
			var opaque bytes.Buffer
			if err := webp.EncodeContext(context.Background(), &opaque, fixture.rgb, options, webp.Limits{}); err != nil {
				t.Fatal(err)
			}
			wantVP8 := contractDemux(t, opaque.Bytes()).Frames[0].VP8
			before := fmt.Sprintf("%#v", fixture.source)
			var previous []byte
			for _, api := range []string{"EncodeContext", "EncodeAll"} {
				t.Run(api, func(t *testing.T) {
					var out bytes.Buffer
					var err error
					canvas := fixture.source.Bounds().Size()
					if api == "EncodeContext" {
						err = webp.EncodeContext(context.Background(), &out, fixture.source, options, webp.Limits{})
					} else {
						err = webp.EncodeAll(context.Background(), &out, &webp.Document{Canvas: canvas, Frames: []webp.Frame{{Pixels: fixture.source}}}, options, webp.DocumentLimits{})
					}
					if err != nil {
						t.Fatal(err)
					}
					f := contractDemux(t, out.Bytes())
					if f.Animated || !f.Extended || !f.Alpha || f.Canvas != canvas || len(f.Frames) != 1 {
						t.Fatalf("incorrect alpha still controls: %+v", f)
					}
					frame := f.Frames[0]
					if !bytes.Equal(frame.VP8, wantVP8) {
						t.Fatal("VP8 payload differs from independently constructed opaque straight RGB")
					}
					if want := append([]byte{0}, fixture.alpha...); !bytes.Equal(frame.ALPH, want) {
						t.Fatalf("raw ALPH = %v, want %v", frame.ALPH, want)
					}
					contractCheckIndependentAlpha(t, out.Bytes(), fixture.alpha, canvas)
					if previous != nil && !bytes.Equal(previous, out.Bytes()) {
						t.Fatal("EncodeAll still differs from EncodeContext")
					}
					previous = bytes.Clone(out.Bytes())
				})
			}
			if got := fmt.Sprintf("%#v", fixture.source); got != before {
				t.Fatal("encoding changed caller image storage or controls")
			}
		})
	}
}

func TestEncodeDocumentContractAnimationControlsAndOwnership(t *testing.T) {
	fixtures := contractAlphaFixtures()
	first, second := fixtures[0], fixtures[2]
	frames := []webp.Frame{{Pixels: first.source}, {Pixels: second.source, Duration: 37 * time.Millisecond}, {Pixels: first.rgb, Duration: 123 * time.Millisecond}}
	metadata := webp.Metadata{ICC: []byte{0, 1, 0xff}, EXIF: []byte("Exif\x00\x00opaque"), XMP: []byte("<opaque/>")}
	for _, loops := range []uint16{0, 1, 65535} {
		t.Run(fmt.Sprint(loops), func(t *testing.T) {
			doc := &webp.Document{Canvas: image.Pt(5, 3), Animated: true, Frames: frames, LoopCount: loops, Background: color.NRGBA{R: 33, G: 71, B: 129, A: 3}, Metadata: metadata}
			before := fmt.Sprintf("%#v/%#v/%#v/%#v", doc, doc.Frames, first.source, second.source)
			var out bytes.Buffer
			if err := webp.EncodeAll(context.Background(), &out, doc, &webp.Options{Method: 1}, webp.DocumentLimits{}); err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%#v/%#v/%#v/%#v", doc, doc.Frames, first.source, second.source); got != before {
				t.Fatal("EncodeAll changed caller document, frames, pixels, or metadata")
			}
			f := contractDemux(t, out.Bytes())
			if !f.Animated || !f.Extended || !f.Alpha || f.Canvas != doc.Canvas || f.LoopCount != loops || f.Background != doc.Background || len(f.Frames) != len(frames) {
				t.Fatalf("animation controls changed: %+v", f)
			}
			if !reflect.DeepEqual(f.Metadata(), metadata) {
				t.Fatalf("metadata = %#v, want %#v", f.Metadata(), metadata)
			}
			for i, frame := range f.Frames {
				if frame.Rect != image.Rect(0, 0, 5, 3) || frame.Blend != container.BlendReplace || frame.Dispose != container.DisposeNone || frame.Duration != frames[i].Duration {
					t.Fatalf("frame %d is not full-canvas replacement with unchanged duration: %+v", i, frame)
				}
				var still bytes.Buffer
				if err := webp.EncodeContext(context.Background(), &still, frames[i].Pixels, &webp.Options{Method: 1}, webp.Limits{}); err != nil {
					t.Fatal(err)
				}
				want := contractDemux(t, still.Bytes()).Frames[0]
				if !bytes.Equal(frame.VP8, want.VP8) || !bytes.Equal(frame.ALPH, want.ALPH) {
					t.Fatalf("frame %d color/alpha differs from independent still encoding", i)
				}
			}
			decoded, err := webp.DecodeAll(context.Background(), bytes.NewReader(out.Bytes()), webp.ReadLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if decoded.LoopCount != loops || len(decoded.Frames) != 3 {
				t.Fatal("stored frames were expanded by loop count")
			}
			for i, alpha := range [][]byte{first.alpha, second.alpha, bytes.Repeat([]byte{255}, 15)} {
				for y := 0; y < 3; y++ {
					for x := 0; x < 5; x++ {
						_, _, _, a := decoded.Frames[i].Pixels.At(x, y).RGBA()
						if a != uint32(alpha[y*5+x])*257 {
							t.Fatalf("displayed frame %d alpha at (%d,%d) = %d", i, x, y, a)
						}
					}
				}
			}
			// No returned decoded frame may alias another displayed canvas.
			beforeSecond := bytes.Clone(decoded.Frames[1].Pixels.(*image.NRGBA).Pix)
			decoded.Frames[0].Pixels.(*image.NRGBA).Pix[0] ^= 0xff
			if !bytes.Equal(decoded.Frames[1].Pixels.(*image.NRGBA).Pix, beforeSecond) {
				t.Fatal("decoded output frames alias")
			}
		})
	}
}

type contractCountingWriter struct {
	bytes.Buffer
	calls int
	err   error
	short bool
}

func (w *contractCountingWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.err != nil {
		return 0, w.err
	}
	if w.short {
		return len(p) - 1, nil
	}
	return w.Buffer.Write(p)
}

type contractPreflightImage struct{ rect image.Rectangle }

func (m contractPreflightImage) Bounds() image.Rectangle { return m.rect }
func (contractPreflightImage) ColorModel() color.Model   { panic("preflight called ColorModel") }
func (contractPreflightImage) At(int, int) color.Color   { panic("preflight called At") }
func (contractPreflightImage) Opaque() bool              { panic("preflight called Opaque") }

func TestEncodeDocumentContractPreflightBeforeAnyPixels(t *testing.T) {
	guard := contractPreflightImage{image.Rect(9, -3, 14, 0)}
	base := func() *webp.Document {
		return &webp.Document{Canvas: image.Pt(5, 3), Animated: true, Frames: []webp.Frame{{Pixels: guard, Duration: 7 * time.Millisecond}, {Pixels: guard, Duration: 9 * time.Millisecond}}}
	}
	cases := []struct {
		name     string
		change   func(*webp.Document)
		limits   webp.DocumentLimits
		want     error
		resource string
	}{
		{name: "late nil image", change: func(d *webp.Document) { d.Frames[1].Pixels = nil }, want: webp.ErrInvalidDocument},
		{name: "late typed nil image", change: func(d *webp.Document) { d.Frames[1].Pixels = (*image.NRGBA)(nil) }, want: webp.ErrInvalidDocument},
		{name: "late empty bounds", change: func(d *webp.Document) { d.Frames[1].Pixels = contractPreflightImage{image.Rect(0, 0, 0, 3)} }, want: webp.ErrInvalidDocument},
		{name: "late inverted bounds", change: func(d *webp.Document) {
			d.Frames[1].Pixels = contractPreflightImage{image.Rectangle{Min: image.Pt(1, 0), Max: image.Pt(0, 3)}}
		}, want: webp.ErrInvalidDocument},
		{name: "late canvas mismatch", change: func(d *webp.Document) { d.Frames[1].Pixels = contractPreflightImage{image.Rect(0, 0, 4, 3)} }, want: webp.ErrInvalidDocument},
		{name: "late negative duration", change: func(d *webp.Document) { d.Frames[1].Duration = -time.Millisecond }, want: webp.ErrInvalidDocument},
		{name: "late fractional duration", change: func(d *webp.Document) { d.Frames[1].Duration = time.Millisecond + time.Nanosecond }, want: webp.ErrInvalidDocument},
		{name: "late unrepresentable duration", change: func(d *webp.Document) { d.Frames[1].Duration = 0x1000000 * time.Millisecond }, want: webp.ErrInvalidDocument},
		{name: "too many frames", limits: webp.DocumentLimits{MaxFrames: 1}, want: webp.ErrLimitExceeded, resource: "frames"},
		{name: "total pixels", limits: webp.DocumentLimits{MaxTotalPixels: 29}, want: webp.ErrLimitExceeded, resource: "total_pixels"},
		{name: "width", limits: webp.DocumentLimits{Limits: webp.Limits{MaxWidth: 4}}, want: webp.ErrLimitExceeded, resource: "width"},
		{name: "height", limits: webp.DocumentLimits{Limits: webp.Limits{MaxHeight: 2}}, want: webp.ErrLimitExceeded, resource: "height"},
		{name: "pixels", limits: webp.DocumentLimits{Limits: webp.Limits{MaxPixels: 14}}, want: webp.ErrLimitExceeded, resource: "pixels"},
		{name: "total duration", limits: webp.DocumentLimits{MaxDuration: 15 * time.Millisecond}, want: webp.ErrLimitExceeded, resource: "duration"},
		{name: "metadata bytes", change: func(d *webp.Document) { d.Metadata = webp.Metadata{ICC: []byte{1, 2}, EXIF: []byte{3, 4}} }, limits: webp.DocumentLimits{MaxMetadataBytes: 3}, want: webp.ErrLimitExceeded, resource: "metadata_bytes"},
		{name: "still has multiple frames", change: func(d *webp.Document) { d.Animated = false }, want: webp.ErrInvalidDocument},
		{name: "still duration", change: func(d *webp.Document) { d.Animated = false; d.Frames = d.Frames[:1] }, want: webp.ErrInvalidDocument},
		{name: "still loops", change: func(d *webp.Document) {
			d.Animated = false
			d.Frames = d.Frames[:1]
			d.Frames[0].Duration = 0
			d.LoopCount = 1
		}, want: webp.ErrInvalidDocument},
		{name: "still background", change: func(d *webp.Document) {
			d.Animated = false
			d.Frames = d.Frames[:1]
			d.Frames[0].Duration = 0
			d.Background.R = 1
		}, want: webp.ErrInvalidDocument},
		{name: "empty frames", change: func(d *webp.Document) { d.Frames = nil }, want: webp.ErrInvalidDocument},
		{name: "zero canvas", change: func(d *webp.Document) { d.Canvas.X = 0 }, want: webp.ErrInvalidDocument},
		{name: "negative canvas", change: func(d *webp.Document) { d.Canvas.Y = -1 }, want: webp.ErrInvalidDocument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := base()
			if tc.change != nil {
				tc.change(doc)
			}
			w := new(contractCountingWriter)
			err := webp.EncodeAll(context.Background(), w, doc, nil, tc.limits)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.resource != "" {
				var detail *webp.LimitError
				if !errors.As(err, &detail) || detail.Resource != tc.resource || errors.Is(err, webp.ErrOutputTooLarge) {
					t.Fatalf("limit classification = %#v, want %s", err, tc.resource)
				}
			}
			if w.calls != 0 || w.Len() != 0 {
				t.Fatalf("preflight failure performed %d writes / %d bytes", w.calls, w.Len())
			}
		})
	}
}

func TestEncodeDocumentContractMetadataOmissionAndExactBudgets(t *testing.T) {
	m := contractAlphaFixtures()[0].rgb
	for _, tc := range []struct {
		name     string
		metadata webp.Metadata
		wantIDs  []string
	}{
		{name: "nil"},
		{name: "all empty", metadata: webp.Metadata{ICC: []byte{}, EXIF: []byte{}, XMP: []byte{}}},
		{name: "ICC only", metadata: webp.Metadata{ICC: []byte{1}, EXIF: []byte{}, XMP: nil}, wantIDs: []string{"ICCP"}},
		{name: "EXIF only", metadata: webp.Metadata{ICC: []byte{}, EXIF: []byte{2, 0, 3}, XMP: []byte{}}, wantIDs: []string{"EXIF"}},
		{name: "XMP only", metadata: webp.Metadata{XMP: []byte{0xff, 0}}, wantIDs: []string{"XMP "}},
		{name: "all nonempty", metadata: webp.Metadata{ICC: []byte{1}, EXIF: []byte{2, 3}, XMP: []byte{4, 5, 6}}, wantIDs: []string{"ICCP", "EXIF", "XMP "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := &webp.Document{Canvas: image.Pt(5, 3), Frames: []webp.Frame{{Pixels: m}}, Metadata: tc.metadata}
			metadataBytes := int64(len(tc.metadata.ICC) + len(tc.metadata.EXIF) + len(tc.metadata.XMP))
			limits := webp.DocumentLimits{Limits: webp.Limits{MaxWidth: 5, MaxHeight: 3, MaxPixels: 15}, MaxFrames: 1, MaxTotalPixels: 15, MaxMetadataBytes: metadataBytes, MaxDuration: time.Nanosecond}
			var out bytes.Buffer
			if err := webp.EncodeAll(context.Background(), &out, doc, &webp.Options{Method: 1}, limits); err != nil {
				t.Fatal(err)
			}
			f := contractDemux(t, out.Bytes())
			var ids []string
			for _, c := range f.MetadataChunks {
				ids = append(ids, c.FourCC)
				if len(c.Data) == 0 {
					t.Fatal("EncodeAll emitted an empty supplied metadata category")
				}
			}
			if !reflect.DeepEqual(ids, tc.wantIDs) || f.Extended != (len(tc.wantIDs) != 0) || f.Alpha {
				t.Fatalf("metadata IDs/flags = %v/%v/%v, want %v", ids, f.Extended, f.Alpha, tc.wantIDs)
			}
			got := f.Metadata()
			if !bytes.Equal(got.ICC, tc.metadata.ICC) || !bytes.Equal(got.EXIF, tc.metadata.EXIF) || !bytes.Equal(got.XMP, tc.metadata.XMP) {
				t.Fatal("metadata bytes changed")
			}
			if metadataBytes > 1 {
				limits.MaxMetadataBytes = metadataBytes - 1
				w := new(contractCountingWriter)
				if err := webp.EncodeAll(context.Background(), w, doc, nil, limits); !errors.Is(err, webp.ErrLimitExceeded) || w.calls != 0 {
					t.Fatalf("one-byte-short metadata budget = %v, writes %d", err, w.calls)
				}
			}
		})
	}
	// All frame and cumulative timing budgets admit their exact boundary.
	doc := &webp.Document{Canvas: image.Pt(5, 3), Animated: true, Frames: []webp.Frame{{Pixels: m, Duration: 7 * time.Millisecond}, {Pixels: m, Duration: 9 * time.Millisecond}}}
	var exact bytes.Buffer
	if err := webp.EncodeAll(context.Background(), &exact, doc, &webp.Options{Method: 1}, webp.DocumentLimits{MaxFrames: 2, MaxTotalPixels: 30, MaxDuration: 16 * time.Millisecond}); err != nil {
		t.Fatalf("exact document budgets: %v", err)
	}
}

func TestEncodeDocumentContractOutputCapBoundaries(t *testing.T) {
	fixture := contractAlphaFixtures()[0]
	for _, mode := range []string{"transparent still", "document still", "document animation"} {
		t.Run(mode, func(t *testing.T) {
			encode := func(w io.Writer, cap int64) error {
				options := &webp.Options{Method: 1}
				if mode == "transparent still" {
					return webp.EncodeContext(context.Background(), w, fixture.source, options, webp.Limits{MaxOutputBytes: cap})
				}
				doc := &webp.Document{Canvas: image.Pt(5, 3), Frames: []webp.Frame{{Pixels: fixture.source}}, Metadata: webp.Metadata{ICC: []byte{1, 2, 3}, XMP: []byte{4, 5}}}
				if mode == "document animation" {
					doc.Animated = true
					doc.Frames = append(doc.Frames, webp.Frame{Pixels: fixture.rgb, Duration: time.Millisecond})
				}
				return webp.EncodeAll(context.Background(), w, doc, options, webp.DocumentLimits{Limits: webp.Limits{MaxOutputBytes: cap}})
			}
			baseline := new(contractCountingWriter)
			if err := encode(baseline, 0); err != nil {
				t.Fatal(err)
			}
			size := int64(baseline.Len())
			for _, cap := range []int64{1, 20, 30, 31, size - 2, size - 1, size, size + 1, 1 << 62} {
				w := new(contractCountingWriter)
				err := encode(w, cap)
				if cap < size {
					var detail *webp.LimitError
					if !errors.As(err, &detail) || detail.Resource != "output_bytes" || detail.Limit != cap || !errors.Is(err, webp.ErrOutputTooLarge) || !errors.Is(err, webp.ErrLimitExceeded) || w.calls != 0 || w.Len() != 0 {
						t.Fatalf("cap %d / size %d = %v, writes %d, bytes %d", cap, size, err, w.calls, w.Len())
					}
				} else if err != nil || !bytes.Equal(w.Bytes(), baseline.Bytes()) {
					t.Fatalf("admitted cap %d changed bytes or returned %v", cap, err)
				}
			}
		})
	}
}

func TestEncodeDocumentContractWriterErrorsRemainIdentical(t *testing.T) {
	fixture := contractAlphaFixtures()[0]
	for _, mode := range []string{"transparent still", "document still", "document animation"} {
		t.Run(mode, func(t *testing.T) {
			for _, supplied := range []error{
				errors.New("caller writer failed"),
				&container.LimitError{Resource: "output_bytes", Limit: 1, Actual: 2},
				&container.LimitError{Resource: "frames", Limit: 1, Actual: 2},
				&webp.LimitError{Resource: "output_bytes", Limit: 7, Actual: 9},
				nil,
			} {
				w := &contractCountingWriter{err: supplied, short: supplied == nil}
				var err error
				if mode == "transparent still" {
					err = webp.EncodeContext(context.Background(), w, fixture.source, &webp.Options{Method: 1}, webp.Limits{MaxOutputBytes: 1 << 20})
				} else {
					doc := &webp.Document{Canvas: image.Pt(5, 3), Frames: []webp.Frame{{Pixels: fixture.source}}, Animated: mode == "document animation"}
					err = webp.EncodeAll(context.Background(), w, doc, &webp.Options{Method: 1}, webp.DocumentLimits{Limits: webp.Limits{MaxOutputBytes: 1 << 20}})
				}
				want := supplied
				if want == nil {
					want = io.ErrShortWrite
				}
				if err != want || w.calls != 1 {
					t.Fatalf("writer error identity = %v (%T), want %v (%T); calls %d", err, err, want, want, w.calls)
				}
			}
		})
	}
}

type contractCancelImage struct {
	image.Image
	cancel   context.CancelFunc
	calls    int
	cancelAt int
	opaque   bool
}

func (m *contractCancelImage) Opaque() bool { return m.opaque }
func (m *contractCancelImage) At(x, y int) color.Color {
	m.calls++
	if m.calls == m.cancelAt {
		m.cancel()
	}
	return m.Image.At(x, y)
}

type contractCancelWriter struct {
	cancel context.CancelFunc
	err    error
	calls  int
}

func (w *contractCancelWriter) Write(p []byte) (int, error) {
	w.calls++
	w.cancel()
	return len(p), w.err
}

func TestEncodeDocumentContractCancellationBeforeCommit(t *testing.T) {
	for _, api := range []string{"EncodeContext", "EncodeAll"} {
		t.Run(api, func(t *testing.T) {
			encode := func(ctx context.Context, w io.Writer, m image.Image) error {
				if api == "EncodeContext" {
					return webp.EncodeContext(ctx, w, m, &webp.Options{Method: 1}, webp.Limits{})
				}
				return webp.EncodeAll(ctx, w, &webp.Document{Canvas: m.Bounds().Size(), Frames: []webp.Frame{{Pixels: m}}}, &webp.Options{Method: 1}, webp.DocumentLimits{})
			}
			// Force the transparent-color extraction path and cancel inside At.
			// No timers, goroutines, or scheduler-dependent completion races.
			ctx, cancel := context.WithCancel(context.Background())
			m := &contractCancelImage{Image: image.NewNRGBA(image.Rect(-3, 7, 126, 10)), cancel: cancel, cancelAt: 5}
			w := new(contractCountingWriter)
			err := encode(ctx, w, m)
			cancel()
			if err != context.Canceled || w.calls != 0 || m.calls > 64 || m.calls < 5 {
				t.Fatalf("extraction cancellation = %v, writes %d, At calls %d", err, w.calls, m.calls)
			}
			for _, callerErr := range []error{nil, errors.New("caller wins after cancellation")} {
				ctx, cancel := context.WithCancel(context.Background())
				w := &contractCancelWriter{cancel: cancel, err: callerErr}
				err := encode(ctx, w, contractAlphaFixtures()[0].source)
				cancel()
				want := callerErr
				if want == nil {
					want = context.Canceled
				}
				if err != want || w.calls != 1 {
					t.Fatalf("commit cancellation = %v, want %v, writes %d", err, want, w.calls)
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := new(contractCountingWriter)
	err := webp.EncodeAll(ctx, w, &webp.Document{Canvas: image.Pt(5, 3), Frames: []webp.Frame{{Pixels: contractPreflightImage{image.Rect(0, 0, 5, 3)}}}}, &webp.Options{Quality: -1}, webp.DocumentLimits{MaxFrames: -1})
	if err != context.Canceled || w.calls != 0 {
		t.Fatalf("pre-cancelled context = %v, writes %d", err, w.calls)
	}
}

func TestEncodeDocumentContractDefaultAndNegativeLimits(t *testing.T) {
	defaults := webp.DefaultDocumentLimits()
	if defaults.Limits != (webp.Limits{}) || defaults.MaxFrames <= 0 || defaults.MaxTotalPixels <= 0 || defaults.MaxMetadataBytes <= 0 || defaults.MaxDuration <= 0 {
		t.Fatalf("invalid document defaults: %+v", defaults)
	}
	fixture := contractAlphaFixtures()[0]
	doc := &webp.Document{Canvas: image.Pt(5, 3), Frames: []webp.Frame{{Pixels: fixture.source}}}
	var zero, explicit bytes.Buffer
	if err := webp.EncodeAll(context.Background(), &zero, doc, &webp.Options{Method: 1}, webp.DocumentLimits{}); err != nil {
		t.Fatal(err)
	}
	if err := webp.EncodeAll(context.Background(), &explicit, doc, &webp.Options{Method: 1}, defaults); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(zero.Bytes(), explicit.Bytes()) {
		t.Fatal("zero document limits differ from explicit defaults")
	}
	for _, limits := range []webp.DocumentLimits{
		{Limits: webp.Limits{MaxWidth: -1}}, {Limits: webp.Limits{MaxHeight: -1}},
		{Limits: webp.Limits{MaxPixels: -1}}, {Limits: webp.Limits{MaxOutputBytes: -1}},
		{MaxFrames: -1}, {MaxTotalPixels: -1}, {MaxMetadataBytes: -1}, {MaxDuration: -1},
	} {
		w := new(contractCountingWriter)
		if err := webp.EncodeAll(context.Background(), w, doc, nil, limits); !errors.Is(err, webp.ErrInvalidLimits) || w.calls != 0 {
			t.Fatalf("negative limits %+v = %v, writes %d", limits, err, w.calls)
		}
	}
}

type contractNilContext struct{ context.Context }

func TestEncodeDocumentContractValidationPrecedence(t *testing.T) {
	var nilContext *contractNilContext
	var nilWriter *contractCountingWriter
	guard := contractPreflightImage{image.Rect(0, 0, 5, 3)}
	doc := &webp.Document{Canvas: image.Pt(5, 3), Frames: []webp.Frame{{Pixels: guard}}}
	cases := []struct {
		name    string
		ctx     context.Context
		writer  io.Writer
		doc     *webp.Document
		options *webp.Options
		limits  webp.DocumentLimits
		want    error
	}{
		{name: "nil context", doc: doc, options: &webp.Options{Quality: -1}, limits: webp.DocumentLimits{MaxFrames: -1}, want: webp.ErrInvalidContext},
		{name: "typed nil context", ctx: nilContext, doc: doc, want: webp.ErrInvalidContext},
		{name: "options before limits", ctx: context.Background(), doc: doc, options: &webp.Options{Method: 7}, limits: webp.DocumentLimits{MaxFrames: -1}, want: webp.ErrInvalidOptions},
		{name: "limits before writer", ctx: context.Background(), doc: doc, limits: webp.DocumentLimits{MaxDuration: -1}, want: webp.ErrInvalidLimits},
		{name: "nil writer", ctx: context.Background(), want: webp.ErrInvalidWriter},
		{name: "typed nil writer", ctx: context.Background(), writer: nilWriter, want: webp.ErrInvalidWriter},
		{name: "nil document", ctx: context.Background(), writer: io.Discard, want: webp.ErrInvalidDocument},
		{name: "format dimension before pixels", ctx: context.Background(), writer: io.Discard, doc: &webp.Document{Canvas: image.Pt(16384, 1), Frames: []webp.Frame{{Pixels: contractPreflightImage{image.Rect(0, 0, 16384, 1)}}}}, want: webp.ErrTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := webp.EncodeAll(tc.ctx, tc.writer, tc.doc, tc.options, tc.limits); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEncodeDocumentContractOpaqueCompatibilityAndSingleFrameAnimation(t *testing.T) {
	m := contractAlphaFixtures()[0].rgb
	options := &webp.Options{Quality: 81, Method: 1}
	var legacy, still bytes.Buffer
	if err := webp.EncodeContext(context.Background(), &legacy, m, options, webp.Limits{}); err != nil {
		t.Fatal(err)
	}
	doc := &webp.Document{Canvas: image.Pt(5, 3), Frames: []webp.Frame{{Pixels: m}}}
	if err := webp.EncodeAll(context.Background(), &still, doc, options, webp.DocumentLimits{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(still.Bytes(), legacy.Bytes()) {
		t.Fatal("opaque document still changed the existing simple VP8 bytes")
	}
	f := contractDemux(t, still.Bytes())
	if f.Extended || f.Alpha || len(f.Frames[0].ALPH) != 0 {
		t.Fatal("opaque still acquired an extended header or alpha plane")
	}
	for _, duration := range []time.Duration{0, time.Millisecond, 0xffffff * time.Millisecond} {
		doc.Animated = true
		doc.LoopCount = 65535
		doc.Frames[0].Duration = duration
		var animation bytes.Buffer
		limit := duration
		if limit == 0 {
			limit = time.Nanosecond
		}
		if err := webp.EncodeAll(context.Background(), &animation, doc, options, webp.DocumentLimits{MaxFrames: 1, MaxDuration: limit}); err != nil {
			t.Fatalf("single frame duration %s: %v", duration, err)
		}
		f := contractDemux(t, animation.Bytes())
		if !f.Animated || !f.Extended || f.Alpha || f.LoopCount != 65535 || len(f.Frames) != 1 || f.Frames[0].Duration != duration || f.Frames[0].Blend != container.BlendReplace || f.Frames[0].Dispose != container.DisposeNone {
			t.Fatalf("single-frame animation controls = %+v", f)
		}
		if !bytes.Equal(f.Frames[0].VP8, contractDemux(t, legacy.Bytes()).Frames[0].VP8) {
			t.Fatal("single-frame animation changed opaque VP8 payload")
		}
	}
}
