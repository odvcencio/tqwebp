package yuv

import "image"

// RampPatchSize is the edge length in pixels of one colour patch of
// ColorRamp. It equals one macroblock, so every patch codes as flat
// blocks and no patch shares a chroma box with its neighbour.
const RampPatchSize = 16

// RampLevels are the four values each colour channel of ColorRamp takes.
var RampLevels = [4]uint8{0, 85, 170, 255}

// ColorRamp returns the 128x128 colour-ramp image the BT.601 golden
// fixture uses. The image holds the 64 corners and mid points of the
// red-green-blue cube, one flat 16x16 patch each, in a fixed order:
// patch index i takes red RampLevels[i/16], green RampLevels[(i/4)%4],
// and blue RampLevels[i%4], and it sits at column i%8, row i/8.
//
// The picture exists to pin the colour convention, not to look like
// anything. Flat patches remove every averaging and prediction question
// from the comparison, so a difference against libwebp's own planes can
// only come from the conversion coefficients.
func ColorRamp() *image.RGBA {
	const patches = 64
	const cols = 8
	w := cols * RampPatchSize
	h := (patches / cols) * RampPatchSize
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < patches; i++ {
		r := RampLevels[i/16]
		g := RampLevels[(i/4)%4]
		b := RampLevels[i%4]
		x0 := (i % cols) * RampPatchSize
		y0 := (i / cols) * RampPatchSize
		for y := y0; y < y0+RampPatchSize; y++ {
			o := m.PixOffset(x0, y)
			for x := 0; x < RampPatchSize; x++ {
				m.Pix[o+4*x+0] = r
				m.Pix[o+4*x+1] = g
				m.Pix[o+4*x+2] = b
				m.Pix[o+4*x+3] = 0xff
			}
		}
	}
	return m
}

// RampPatchCenter returns the pixel coordinate at the middle of patch i
// of ColorRamp, and the patch colour. Tests sample there because the
// middle of a flat patch carries no edge effects from the codec.
func RampPatchCenter(i int) (x, y int, r, g, b uint8) {
	const cols = 8
	x = (i%cols)*RampPatchSize + RampPatchSize/2
	y = (i/cols)*RampPatchSize + RampPatchSize/2
	return x, y, RampLevels[i/16], RampLevels[(i/4)%4], RampLevels[i%4]
}
