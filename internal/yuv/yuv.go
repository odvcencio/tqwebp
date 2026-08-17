// Package yuv converts an image.Image to the padded 4:2:0 luma and chroma
// planes a VP8 keyframe encoder codes.
//
// # Colour convention
//
// The package uses the BT.601 limited-range ("studio swing") fixed-point
// coefficients that libwebp uses. Luma runs from 16 to 235 and chroma runs
// from 16 to 240. Every browser inverts a WebP file with that same
// convention, so an encoder that wrote full-range JFIF values would shift
// the colour of every decoded image. The tqwebp specification section 7.2
// fixes this choice.
//
//	Y  = 16  + (16839*R + 33059*G +  6420*B + 32768) >> 16
//	U  = 128 + (-9719*R - 19081*G + 28800*B + 32768) >> 16
//	V  = 128 + (28800*R - 24116*G -  4684*B + 32768) >> 16
//
// The chroma planes take a 2x2 box average. The package sums the red,
// green, and blue values of the four pixels first and converts once, at
// two extra bits of precision, which is what libwebp does.
//
// Note on the specification text: section 7.2 prints these constants with
// a shift of 15 and a rounding term of 16384. That pair cannot be right,
// because the three luma coefficients sum to 56318, and 56318*255 >> 15
// is 438. The coefficients are libwebp's 16-bit fixed-point values, so the
// shift is 16 and the rounding term is 32768. Test TestGoldenLibwebpPlanes
// pins the result against planes that libwebp itself produced.
//
// # Padding
//
// VP8 codes whole 16x16 macroblocks. The planes therefore cover
// ceil(width/16)*16 by ceil(height/16)*16 pixels. The padded region
// repeats the nearest edge pixel, which keeps the padded blocks cheap to
// code.
package yuv

import (
	"image"
	"image/color"
)

// Fixed-point coefficients of the BT.601 limited-range forward
// conversion. See the package comment for the formula they belong to.
const (
	yR, yG, yB = 16839, 33059, 6420
	uR, uG, uB = -9719, -19081, 28800
	vR, vG, vB = 28800, -24116, -4684

	shift    = 16
	rounding = 1 << (shift - 1)

	// Chroma sums four pixels before it converts, so it shifts two bits
	// further and rounds two bits later.
	chromaShift    = shift + 2
	chromaRounding = 1 << (chromaShift - 1)
)

// Planes holds one frame as padded 4:2:0 planes. The luma plane is
// MBW*16 by MBH*16 samples. Each chroma plane is MBW*8 by MBH*8 samples.
// Width and Height record the visible size the frame header will carry.
type Planes struct {
	Y       []uint8
	U       []uint8
	V       []uint8
	YStride int
	CStride int

	Width, Height int
	MBW, MBH      int
}

// NewPlanes allocates zeroed planes for a width by height frame.
func NewPlanes(width, height int) *Planes {
	mbw := (width + 15) / 16
	mbh := (height + 15) / 16
	return &Planes{
		Y:       make([]uint8, mbw*16*mbh*16),
		U:       make([]uint8, mbw*8*mbh*8),
		V:       make([]uint8, mbw*8*mbh*8),
		YStride: mbw * 16,
		CStride: mbw * 8,
		Width:   width,
		Height:  height,
		MBW:     mbw,
		MBH:     mbh,
	}
}

// RGBToY converts one pixel to a limited-range luma sample.
func RGBToY(r, g, b uint8) uint8 {
	y := (yR*int32(r) + yG*int32(g) + yB*int32(b) + rounding) >> shift
	return clamp8(y + 16)
}

// RGBToU converts one pixel to a limited-range blue-difference sample.
func RGBToU(r, g, b uint8) uint8 {
	u := (uR*int32(r) + uG*int32(g) + uB*int32(b) + rounding) >> shift
	return clamp8(u + 128)
}

// RGBToV converts one pixel to a limited-range red-difference sample.
func RGBToV(r, g, b uint8) uint8 {
	v := (vR*int32(r) + vG*int32(g) + vB*int32(b) + rounding) >> shift
	return clamp8(v + 128)
}

// boxToU converts the summed red, green, and blue values of a 2x2 pixel
// box to one blue-difference sample.
func boxToU(r, g, b int32) uint8 {
	u := (uR*r + uG*g + uB*b + chromaRounding) >> chromaShift
	return clamp8(u + 128)
}

// boxToV converts the summed red, green, and blue values of a 2x2 pixel
// box to one red-difference sample.
func boxToV(r, g, b int32) uint8 {
	v := (vR*r + vG*g + vB*b + chromaRounding) >> chromaShift
	return clamp8(v + 128)
}

func clamp8(v int32) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// Convert reads m and returns its padded 4:2:0 planes. It ignores any
// alpha channel; callers reject non-opaque images before they call it
// (see IsOpaque).
func Convert(m image.Image) *Planes {
	b := m.Bounds()
	p := NewPlanes(b.Dx(), b.Dy())
	src := newSampler(m)

	// Luma covers every padded position. Padded positions repeat the
	// nearest edge pixel, which the sampler does through its clamp.
	for y := 0; y < p.MBH*16; y++ {
		row := p.Y[y*p.YStride:]
		for x := 0; x < p.YStride; x++ {
			r, g, bb := src.at(x, y)
			row[x] = RGBToY(r, g, bb)
		}
	}

	// Chroma averages each 2x2 box in the red, green, and blue domain,
	// then converts the sum once.
	for cy := 0; cy < p.MBH*8; cy++ {
		for cx := 0; cx < p.CStride; cx++ {
			var sr, sg, sb int32
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					r, g, bb := src.at(2*cx+dx, 2*cy+dy)
					sr += int32(r)
					sg += int32(g)
					sb += int32(bb)
				}
			}
			i := cy*p.CStride + cx
			p.U[i] = boxToU(sr, sg, sb)
			p.V[i] = boxToV(sr, sg, sb)
		}
	}
	return p
}

// IsOpaque reports whether every pixel of m is fully opaque. It uses the
// image's own Opaque method when the type has one, and it reads the alpha
// channel of every pixel otherwise.
func IsOpaque(m image.Image) bool {
	if o, ok := m.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	b := m.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := m.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

// sampler reads red, green, and blue values at padded coordinates. It
// clamps every coordinate into the source rectangle, which produces the
// edge replication the padded macroblock columns and rows need.
type sampler struct {
	generic image.Image
	rgba    *image.RGBA
	nrgba   *image.NRGBA
	gray    *image.Gray
	ycbcr   *image.YCbCr
	rect    image.Rectangle
}

func newSampler(m image.Image) *sampler {
	s := &sampler{rect: m.Bounds()}
	switch t := m.(type) {
	case *image.RGBA:
		s.rgba = t
	case *image.NRGBA:
		s.nrgba = t
	case *image.Gray:
		s.gray = t
	case *image.YCbCr:
		s.ycbcr = t
	default:
		s.generic = m
	}
	return s
}

func (s *sampler) at(x, y int) (r, g, b uint8) {
	px := s.rect.Min.X + x
	py := s.rect.Min.Y + y
	if px >= s.rect.Max.X {
		px = s.rect.Max.X - 1
	}
	if py >= s.rect.Max.Y {
		py = s.rect.Max.Y - 1
	}

	switch {
	case s.rgba != nil:
		// An opaque *image.RGBA carries un-premultiplied values already,
		// because premultiplying by an alpha of 255 changes nothing.
		i := s.rgba.PixOffset(px, py)
		p := s.rgba.Pix[i : i+3 : i+3]
		return p[0], p[1], p[2]
	case s.nrgba != nil:
		i := s.nrgba.PixOffset(px, py)
		p := s.nrgba.Pix[i : i+3 : i+3]
		return p[0], p[1], p[2]
	case s.gray != nil:
		v := s.gray.Pix[s.gray.PixOffset(px, py)]
		return v, v, v
	case s.ycbcr != nil:
		c := s.ycbcr.YCbCrAt(px, py)
		return color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
	default:
		cr, cg, cb, _ := s.generic.At(px, py).RGBA()
		return uint8(cr >> 8), uint8(cg >> 8), uint8(cb >> 8)
	}
}
