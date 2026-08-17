package oracle

import (
	"bytes"
	"fmt"
	"image"

	"golang.org/x/image/webp"
)

// The WebP colour trap, and how this file closes it.
//
// A lossy WebP file carries BT.601 limited-range planes: luma from 16 to
// 235, chroma from 16 to 240. libwebp, and therefore every browser,
// inverts them with the matching limited-range coefficients.
// golang.org/x/image/webp hands its planes to image/color, which inverts
// with the full-range JFIF coefficients instead. The two disagree, so a
// PSNR measured on x/image's RGB output charges tqwebp for a decoder
// convention it does not use.
//
// Every function below inverts with libwebp's own coefficients. The
// fixture under testdata/golden/bt601 pins them: the RGB pixels these
// functions produce match the RGB pixels libwebp produced from the same
// file.
//
//	R = clip8((y*19077 >> 8) + (v*26149 >> 8) - 14234) >> 6
//	G = clip8((y*19077 >> 8) - (u*6419 >> 8) - (v*13320 >> 8) + 8708) >> 6
//	B = clip8((y*19077 >> 8) + (u*33050 >> 8) - 17685) >> 6

// YUVToRGB inverts one limited-range BT.601 sample triple with libwebp's
// fixed-point coefficients.
func YUVToRGB(y, u, v uint8) (r, g, b uint8) {
	yy := int32(y) * 19077 >> 8
	r = clipFix6(yy + int32(v)*26149>>8 - 14234)
	g = clipFix6(yy - int32(u)*6419>>8 - int32(v)*13320>>8 + 8708)
	b = clipFix6(yy + int32(u)*33050>>8 - 17685)
	return r, g, b
}

// clipFix6 clamps a six-bit fixed-point sample and returns the whole part.
func clipFix6(v int32) uint8 {
	if v < 0 {
		return 0
	}
	v >>= 6
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// WebPPlanesToRGBA converts decoded WebP planes to RGBA with libwebp's
// inverse conversion. Use it instead of drawing the *image.YCbCr through
// image/color, which would apply the wrong convention.
func WebPPlanesToRGBA(m *image.YCbCr) *image.RGBA {
	b := m.Rect
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		o := out.PixOffset(0, y)
		for x := 0; x < b.Dx(); x++ {
			yi := m.YOffset(b.Min.X+x, b.Min.Y+y)
			ci := m.COffset(b.Min.X+x, b.Min.Y+y)
			r, g, bb := YUVToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
			p := out.Pix[o+4*x : o+4*x+4 : o+4*x+4]
			p[0], p[1], p[2], p[3] = r, g, bb, 0xff
		}
	}
	return out
}

// DecodeWebPPlanes decodes a lossy WebP file and returns its planes
// untouched by any colour conversion. Plane comparisons, and the exact
// reconstruction gate, use this function.
func DecodeWebPPlanes(data []byte) (*image.YCbCr, error) {
	m, err := webp.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("oracle: decode WebP: %w", err)
	}
	planes, ok := m.(*image.YCbCr)
	if !ok {
		return nil, fmt.Errorf("oracle: decoded WebP is %T, want *image.YCbCr", m)
	}
	return planes, nil
}

// DecodeWebP decodes a lossy WebP file to RGBA the way libwebp and the
// browsers do. It is the decode side of every cross-codec measurement in
// this repository.
func DecodeWebP(data []byte) (*image.RGBA, error) {
	planes, err := DecodeWebPPlanes(data)
	if err != nil {
		return nil, err
	}
	return WebPPlanesToRGBA(planes), nil
}
