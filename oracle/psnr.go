package oracle

import (
	"fmt"
	"image"
	"math"
)

// PSNRInf is the value PSNR functions return when two planes are
// pixel-identical: the mean squared error is zero, so the true PSNR is
// infinite. Callers that need a finite number for averaging should treat
// PSNRInf as a sentinel rather than including it in a mean.
const PSNRInf = math.MaxFloat64

// PSNR returns the peak signal-to-noise ratio, in decibels, between two
// equal-sized 8-bit planes. It returns an error if the planes differ in
// size.
func PSNR(a, b [][]uint8) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("oracle: PSNR height mismatch: %d vs %d", len(a), len(b))
	}
	var sumSq float64
	var n int
	for y := range a {
		if len(a[y]) != len(b[y]) {
			return 0, fmt.Errorf("oracle: PSNR width mismatch at row %d: %d vs %d", y, len(a[y]), len(b[y]))
		}
		for x := range a[y] {
			d := float64(a[y][x]) - float64(b[y][x])
			sumSq += d * d
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("oracle: PSNR on empty plane")
	}
	mse := sumSq / float64(n)
	if mse == 0 {
		return PSNRInf, nil
	}
	return 10 * math.Log10(255*255/mse), nil
}

// ChannelPSNR holds PSNR (in decibels) for every channel this package
// measures. YCbCr fields compare the luma/chroma planes image/color
// derives from each image; RGB fields compare the raw 8-bit channels.
type ChannelPSNR struct {
	Y, Cb, Cr float64
	R, G, B   float64
}

// MeasurePSNR computes ChannelPSNR between a and b. It returns an error if
// the two images differ in size.
func MeasurePSNR(a, b image.Image) (ChannelPSNR, error) {
	if a.Bounds().Dx() != b.Bounds().Dx() || a.Bounds().Dy() != b.Bounds().Dy() {
		return ChannelPSNR{}, fmt.Errorf("oracle: MeasurePSNR size mismatch: %dx%d vs %dx%d",
			a.Bounds().Dx(), a.Bounds().Dy(), b.Bounds().Dx(), b.Bounds().Dy())
	}

	var out ChannelPSNR
	var err error
	if out.Y, err = PSNR(planeY(a), planeY(b)); err != nil {
		return ChannelPSNR{}, err
	}
	if out.Cb, err = PSNR(planeCb(a), planeCb(b)); err != nil {
		return ChannelPSNR{}, err
	}
	if out.Cr, err = PSNR(planeCr(a), planeCr(b)); err != nil {
		return ChannelPSNR{}, err
	}
	if out.R, err = PSNR(planeR(a), planeR(b)); err != nil {
		return ChannelPSNR{}, err
	}
	if out.G, err = PSNR(planeG(a), planeG(b)); err != nil {
		return ChannelPSNR{}, err
	}
	if out.B, err = PSNR(planeB(a), planeB(b)); err != nil {
		return ChannelPSNR{}, err
	}
	return out, nil
}
