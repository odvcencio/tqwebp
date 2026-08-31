package gate

import (
	"fmt"
	"image"
	"image/color"

	"m31labs.dev/tqwebp/internal/corpus"
	"m31labs.dev/tqwebp/internal/encoder"
	"m31labs.dev/tqwebp/oracle"
)

// G5Result reports the alpha correctness gate.
//
// The gate has two clauses, and both block the run:
//
//  1. A translucent picture writes the extended container, and an
//     independent decode returns its alpha plane sample for sample.
//     Compression method 0 stores the plane raw, so nothing may move.
//  2. An opaque picture writes the simple container. No VP8X chunk and
//     no ALPH chunk may appear, so an opaque caller pays nothing.
type G5Result struct {
	Pass bool `json:"pass"`
	// Cases counts the translucent pictures the gate encoded.
	Cases int `json:"cases"`
	// ExactPlanes counts the ones whose alpha plane decoded unchanged.
	ExactPlanes int `json:"exact_planes"`
	// OpaqueSimple counts the opaque corpus images that kept the simple
	// container.
	OpaqueSimple int `json:"opaque_simple"`
	// OpaqueImages is how many opaque images the gate checked.
	OpaqueImages int `json:"opaque_images"`
	// Notes carries every failure, in a stable order.
	Notes []string `json:"notes,omitempty"`
	// Overhead reports what the ALPH chunk cost, per case.
	Overhead []G5Overhead `json:"overhead"`
}

// G5Overhead is one translucent picture's byte accounting.
type G5Overhead struct {
	Case          string `json:"case"`
	Width, Height int    `json:"-"`
	Bytes         int    `json:"bytes"`
	AlphaBytes    int    `json:"alpha_bytes"`
	// TranslucentPixels counts the pixels below full opacity.
	TranslucentPixels int `json:"translucent_pixels"`
}

// alphaQuality is the quality the alpha gate encodes at. Compression
// method 0 stores the alpha plane raw, so the quality changes the colour
// planes only; one setting is enough.
const alphaQuality = 75

// evaluateG5 runs the alpha correctness gate.
func (rep *Report) evaluateG5(images []corpus.Image, opts Options) {
	for _, c := range alphaCases(images) {
		rep.G5.Cases++
		data, _, err := encoder.EncodeWithReconstruction(c.img, encoder.Config{
			Quality: alphaQuality,
			Method:  4,
		})
		if err != nil {
			rep.G5.Notes = append(rep.G5.Notes, fmt.Sprintf("%s: encode: %v", c.name, err))
			continue
		}
		decoded, err := oracle.DecodeWebPFile(data)
		if err != nil {
			rep.G5.Notes = append(rep.G5.Notes, fmt.Sprintf("%s: independent decode: %v", c.name, err))
			continue
		}
		if !decoded.HasAlpha() {
			rep.G5.Notes = append(rep.G5.Notes, fmt.Sprintf("%s: the file carries no ALPH chunk", c.name))
			continue
		}

		b := c.img.Bounds()
		mismatch := ""
		translucent := 0
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				want := c.img.NRGBAAt(b.Min.X+x, b.Min.Y+y).A
				if want != 0xff {
					translucent++
				}
				if got := decoded.AlphaAt(x, y); got != want && mismatch == "" {
					mismatch = fmt.Sprintf("%s: alpha at (%d,%d) decoded as %d, want %d",
						c.name, x, y, got, want)
				}
			}
		}
		if mismatch != "" {
			rep.G5.Notes = append(rep.G5.Notes, mismatch)
			continue
		}
		if opts.WriteAlphaCase != nil {
			if err := opts.WriteAlphaCase(c.name, c.img, data); err != nil {
				rep.G5.Notes = append(rep.G5.Notes, fmt.Sprintf("%s: write the fixture: %v", c.name, err))
				continue
			}
		}
		rep.G5.ExactPlanes++
		rep.G5.Overhead = append(rep.G5.Overhead, G5Overhead{
			Case:              c.name,
			Width:             b.Dx(),
			Height:            b.Dy(),
			Bytes:             len(data),
			AlphaBytes:        b.Dx() * b.Dy(),
			TranslucentPixels: translucent,
		})
	}

	for _, img := range images {
		rep.G5.OpaqueImages++
		data, _, err := encoder.EncodeWithReconstruction(img.Img, encoder.Config{
			Quality: alphaQuality,
			Method:  4,
		})
		if err != nil {
			rep.G5.Notes = append(rep.G5.Notes, fmt.Sprintf("%s: encode: %v", img.Spec.Name, err))
			continue
		}
		if len(data) < 16 || string(data[12:16]) != "VP8 " {
			rep.G5.Notes = append(rep.G5.Notes,
				fmt.Sprintf("%s: an opaque picture did not write the simple container", img.Spec.Name))
			continue
		}
		rep.G5.OpaqueSimple++
	}

	rep.G5.Pass = len(rep.G5.Notes) == 0 &&
		rep.G5.Cases > 0 &&
		rep.G5.ExactPlanes == rep.G5.Cases &&
		rep.G5.OpaqueSimple == rep.G5.OpaqueImages
}

// alphaCase is one translucent picture the gate encodes.
type alphaCase struct {
	name string
	img  *image.NRGBA
}

// alphaShape names one rule that fills an alpha plane.
type alphaShape struct {
	name string
	at   func(x, y, w, h int) uint8
}

// smallPicture is the pixel count below which the gate runs every alpha
// shape. Above it the gate runs one shape, so the run stays quick.
const smallPicture = 400000

// alphaCases builds the gate's translucent fixtures. Each one takes its
// colour from a corpus image and its alpha from a fixed rule, so the set
// is deterministic and needs no new file in testdata.
func alphaCases(images []corpus.Image) []alphaCase {
	var cases []alphaCase
	for _, img := range images {
		b := img.Img.Bounds()
		shapes := []alphaShape{{"ramp", alphaRamp}}
		if b.Dx()*b.Dy() <= smallPicture {
			shapes = append(shapes,
				alphaShape{"disc", alphaDisc},
				alphaShape{"edges", alphaEdges},
			)
		}
		for _, shape := range shapes {
			cases = append(cases, alphaCase{
				name: img.Spec.Name + "/" + shape.name,
				img:  withAlpha(img.Img, shape.at),
			})
		}
	}
	return cases
}

// withAlpha copies src into an *image.NRGBA and sets each pixel's alpha
// from at.
func withAlpha(src image.Image, at func(x, y, w, h int) uint8) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bb, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			out.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r >> 8),
				G: uint8(g >> 8),
				B: uint8(bb >> 8),
				A: at(x, y, w, h),
			})
		}
	}
	return out
}

// alphaRamp walks the whole 0 to 255 range across the picture.
func alphaRamp(x, y, w, h int) uint8 {
	return uint8((x*256/w + y*256/h) % 256)
}

// alphaDisc is a hard-edged mask in a transparent field, the shape a
// badge carries.
func alphaDisc(x, y, w, h int) uint8 {
	dx, dy := x-w/2, y-h/2
	r := w / 3
	if dx*dx+dy*dy < r*r {
		return 0xff
	}
	return 0
}

// alphaEdges pins the two ends of the range at the picture boundary,
// where an off-by-one in a stride would show.
func alphaEdges(x, y, w, h int) uint8 {
	switch {
	case y == 0 || x == w-1:
		return 0
	case y == h-1 || x == 0:
		return 0xff
	default:
		return uint8((x*7 + y*11) % 256)
	}
}
