// Package oracle is the WP-0 correctness oracle: the ground truth every
// later tqwebp encoder work package is gated on. It decodes encoded output
// through golang.org/x/image/webp (an independent, pure-Go decoder), scores
// the result against the source image with PSNR and SSIM, and defines the
// exact-match hook a real encoder wires into once it exists.
//
// oracle is a development and test dependency. It, and its
// golang.org/x/image import, must never be reachable from tqwebp's
// non-test, non-cmd source once the encoder itself lands.
package oracle

import (
	"image"
	"image/color"
)

// planeY extracts the luma (Y) channel of m as a rectangular byte grid,
// using the standard BT.601-ish conversion image/color applies when it
// converts to color.YCbCr. Every image.Image, regardless of its native
// color model, produces a plane through this path.
func planeY(m image.Image) [][]uint8 {
	return extractPlane(m, func(c color.Color) uint8 {
		y, _, _ := toYCbCr(c)
		return y
	})
}

func planeCb(m image.Image) [][]uint8 {
	return extractPlane(m, func(c color.Color) uint8 {
		_, cb, _ := toYCbCr(c)
		return cb
	})
}

func planeCr(m image.Image) [][]uint8 {
	return extractPlane(m, func(c color.Color) uint8 {
		_, _, cr := toYCbCr(c)
		return cr
	})
}

func planeR(m image.Image) [][]uint8 {
	return extractPlane(m, func(c color.Color) uint8 {
		r, _, _, _ := toRGBA8(c)
		return r
	})
}

func planeG(m image.Image) [][]uint8 {
	return extractPlane(m, func(c color.Color) uint8 {
		_, g, _, _ := toRGBA8(c)
		return g
	})
}

func planeB(m image.Image) [][]uint8 {
	return extractPlane(m, func(c color.Color) uint8 {
		_, _, b, _ := toRGBA8(c)
		return b
	})
}

func toYCbCr(c color.Color) (y, cb, cr uint8) {
	r, g, b, _ := toRGBA8(c)
	return color.RGBToYCbCr(r, g, b)
}

func toRGBA8(c color.Color) (r, g, b, a uint8) {
	rr, gg, bb, aa := c.RGBA()
	return uint8(rr >> 8), uint8(gg >> 8), uint8(bb >> 8), uint8(aa >> 8)
}

func extractPlane(m image.Image, f func(color.Color) uint8) [][]uint8 {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	plane := make([][]uint8, h)
	for y := 0; y < h; y++ {
		row := make([]uint8, w)
		for x := 0; x < w; x++ {
			row[x] = f(m.At(b.Min.X+x, b.Min.Y+y))
		}
		plane[y] = row
	}
	return plane
}
