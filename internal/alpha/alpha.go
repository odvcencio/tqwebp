// Package alpha reads the straight alpha channel of an image and builds
// the ALPH chunk payload a lossy WebP file carries next to its VP8 key
// frame.
//
// # Straight, not premultiplied
//
// WebP stores straight (non-premultiplied) colour next to the alpha
// channel. The alpha samples themselves are the same in both
// conventions, so this package copies them without arithmetic. Package
// yuv holds the matching un-premultiply step for the colour planes.
//
// # Chunk layout
//
// The ALPH payload is one header byte and then the alpha plane, width by
// height samples, in raster order, with no macroblock padding:
//
//	bit 7..6  reserved, zero
//	bit 5..4  pre-processing method
//	bit 3..2  filtering method
//	bit 1..0  compression method
//
// Compression method 0 stores the plane as it is, so the payload is
// 1 + width*height bytes. Compression method 1 stores a VP8L stream and
// is not built yet.
//
// # Filters
//
// A filter predicts each sample from its already-decoded neighbours and
// stores the difference, modulo 256. The filter never changes the size
// of a compression method 0 payload, because that payload holds one byte
// per sample either way. Filters exist to lower the entropy a later
// compression method 1 payload must code.
//
//	FilterNone        store the samples
//	FilterHorizontal  predict from the left sample
//	FilterVertical    predict from the sample above
//	FilterGradient    predict from left + above - above-left, clamped
//
// The first sample of the first row has no neighbour, so every filter
// predicts it as zero. The rest of the first row uses the left sample,
// and the first column of every later row uses the sample above. Those
// two rules match libwebp's decoder and golang.org/x/image/webp's
// decoder, which the tests of this package read back through.
package alpha

import (
	"fmt"
	"image"
)

// Filtering methods of the ALPH header byte.
const (
	FilterNone       = 0
	FilterHorizontal = 1
	FilterVertical   = 2
	FilterGradient   = 3
)

// Compression methods of the ALPH header byte.
const (
	// CompressionNone stores the alpha plane sample for sample.
	CompressionNone = 0
	// CompressionVP8L stores the alpha plane as a VP8L stream. This
	// package does not build one yet.
	CompressionVP8L = 1
)

// Plane holds one frame's alpha channel: Width by Height samples in
// raster order, with no macroblock padding, because the ALPH chunk
// carries the visible picture only.
type Plane struct {
	A      []uint8
	Stride int

	Width, Height int
}

// NewPlane allocates a fully transparent plane of the given size.
func NewPlane(width, height int) *Plane {
	return &Plane{
		A:      make([]uint8, width*height),
		Stride: width,
		Width:  width,
		Height: height,
	}
}

// At returns the alpha sample at column x of row y.
func (p *Plane) At(x, y int) uint8 { return p.A[y*p.Stride+x] }

// Opaque reports whether every sample is 255.
func (p *Plane) Opaque() bool {
	for _, v := range p.A {
		if v != 0xff {
			return false
		}
	}
	return true
}

// Extract reads the alpha channel of m. The result holds one sample per
// visible pixel, in raster order.
func Extract(m image.Image) *Plane {
	b := m.Bounds()
	p := NewPlane(b.Dx(), b.Dy())

	switch t := m.(type) {
	case *image.NRGBA:
		extractInterleaved(p, t.Pix, t.PixOffset(b.Min.X, b.Min.Y), t.Stride, 4)
		return p
	case *image.RGBA:
		// Alpha is the same sample in both conventions, so the
		// premultiplied type needs no arithmetic here.
		extractInterleaved(p, t.Pix, t.PixOffset(b.Min.X, b.Min.Y), t.Stride, 4)
		return p
	case *image.NYCbCrA:
		for y := 0; y < p.Height; y++ {
			row := p.A[y*p.Stride:][:p.Width]
			for x := 0; x < p.Width; x++ {
				row[x] = t.A[t.AOffset(b.Min.X+x, b.Min.Y+y)]
			}
		}
		return p
	case *image.Alpha:
		for y := 0; y < p.Height; y++ {
			row := p.A[y*p.Stride:][:p.Width]
			for x := 0; x < p.Width; x++ {
				row[x] = t.Pix[t.PixOffset(b.Min.X+x, b.Min.Y+y)]
			}
		}
		return p
	}

	for y := 0; y < p.Height; y++ {
		row := p.A[y*p.Stride:][:p.Width]
		for x := 0; x < p.Width; x++ {
			_, _, _, a := m.At(b.Min.X+x, b.Min.Y+y).RGBA()
			row[x] = uint8(a >> 8)
		}
	}
	return p
}

// extractInterleaved copies the fourth byte of every pixel out of an
// interleaved buffer.
func extractInterleaved(p *Plane, pix []uint8, offset, stride, bytesPerPixel int) {
	for y := 0; y < p.Height; y++ {
		src := pix[offset+y*stride:]
		row := p.A[y*p.Stride:][:p.Width]
		for x := 0; x < p.Width; x++ {
			row[x] = src[x*bytesPerPixel+3]
		}
	}
}

// HeaderByte builds the ALPH header byte from its four fields.
func HeaderByte(preprocessing, filter, compression int) byte {
	return byte(preprocessing&0x03)<<4 | byte(filter&0x03)<<2 | byte(compression&0x03)
}

// Chunk builds the ALPH chunk payload for p with the given filtering
// method and compression method 0. The result is the header byte and
// then the filtered plane.
func Chunk(p *Plane, filter int) ([]byte, error) {
	if filter < FilterNone || filter > FilterGradient {
		return nil, fmt.Errorf("tqwebp: alpha filter %d is outside 0 to 3", filter)
	}
	out := make([]byte, 1+len(p.A))
	out[0] = HeaderByte(0, filter, CompressionNone)
	Filter(out[1:], p, filter)
	return out, nil
}

// Filter writes the filtered form of p into dst, which must hold
// Width*Height bytes.
func Filter(dst []uint8, p *Plane, filter int) {
	w, h := p.Width, p.Height
	if w == 0 || h == 0 {
		return
	}
	switch filter {
	case FilterNone:
		for y := 0; y < h; y++ {
			copy(dst[y*w:][:w], p.A[y*p.Stride:][:w])
		}
		return
	case FilterHorizontal, FilterVertical, FilterGradient:
	default:
		return
	}

	// Every filter codes the first row against the left neighbour, and
	// the first sample of that row against zero.
	first := p.A[0:][:w]
	dst[0] = first[0]
	for x := 1; x < w; x++ {
		dst[x] = first[x] - first[x-1]
	}

	for y := 1; y < h; y++ {
		cur := p.A[y*p.Stride:][:w]
		up := p.A[(y-1)*p.Stride:][:w]
		row := dst[y*w:][:w]

		// The first sample of every later row codes against the sample
		// above it, whichever filter is in force.
		row[0] = cur[0] - up[0]

		switch filter {
		case FilterHorizontal:
			for x := 1; x < w; x++ {
				row[x] = cur[x] - cur[x-1]
			}
		case FilterVertical:
			for x := 1; x < w; x++ {
				row[x] = cur[x] - up[x]
			}
		case FilterGradient:
			for x := 1; x < w; x++ {
				row[x] = cur[x] - gradient(cur[x-1], up[x], up[x-1])
			}
		}
	}
}

// Unfilter inverts Filter in place. Only the tests of this package need
// it; the shipped encoder never reads a filtered plane back.
func Unfilter(dst []uint8, width, height, filter int) {
	if width == 0 || height == 0 {
		return
	}
	switch filter {
	case FilterNone:
		return
	case FilterHorizontal, FilterVertical, FilterGradient:
	default:
		return
	}

	for x := 1; x < width; x++ {
		dst[x] += dst[x-1]
	}
	for y := 1; y < height; y++ {
		row := dst[y*width:][:width]
		up := dst[(y-1)*width:][:width]
		row[0] += up[0]
		switch filter {
		case FilterHorizontal:
			for x := 1; x < width; x++ {
				row[x] += row[x-1]
			}
		case FilterVertical:
			for x := 1; x < width; x++ {
				row[x] += up[x]
			}
		case FilterGradient:
			for x := 1; x < width; x++ {
				row[x] += gradient(row[x-1], up[x], up[x-1])
			}
		}
	}
}

// gradient predicts one sample from its left, above, and above-left
// neighbours, clamped to a byte.
func gradient(left, above, aboveLeft uint8) uint8 {
	v := int(left) + int(above) - int(aboveLeft)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
