package oracle

import (
	"fmt"
	"image"
	"math"
)

// ssimWindow is the side length, in pixels, of the non-overlapping window
// SSIM averages over. RFC-style SSIM implementations commonly use 8x8
// windows on the luma plane; this package follows that convention.
const ssimWindow = 8

const (
	ssimC1 = (0.01 * 255) * (0.01 * 255)
	ssimC2 = (0.03 * 255) * (0.03 * 255)
)

// SSIM computes the mean structural similarity index between two
// equal-sized 8-bit planes, using non-overlapping ssimWindow x ssimWindow
// windows. Windows that run past the plane edge are clipped rather than
// dropped, so every pixel contributes to exactly one window. The result is
// in [-1, 1]; 1 means identical planes.
func SSIM(a, b [][]uint8) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("oracle: SSIM height mismatch: %d vs %d", len(a), len(b))
	}
	h := len(a)
	if h == 0 {
		return 0, fmt.Errorf("oracle: SSIM on empty plane")
	}
	w := len(a[0])
	if w == 0 {
		return 0, fmt.Errorf("oracle: SSIM on empty plane")
	}
	for y := range a {
		if len(a[y]) != w || len(b[y]) != w {
			return 0, fmt.Errorf("oracle: SSIM width mismatch at row %d", y)
		}
	}

	var sum float64
	var windows int
	for y0 := 0; y0 < h; y0 += ssimWindow {
		y1 := y0 + ssimWindow
		if y1 > h {
			y1 = h
		}
		for x0 := 0; x0 < w; x0 += ssimWindow {
			x1 := x0 + ssimWindow
			if x1 > w {
				x1 = w
			}
			sum += ssimBlock(a, b, x0, y0, x1, y1)
			windows++
		}
	}
	return sum / float64(windows), nil
}

// ssimBlock computes the SSIM value for one window [x0,x1) x [y0,y1),
// following the standard mean/variance/covariance formulation:
//
//	SSIM = (2*muA*muB + C1)(2*cov + C2) / ((muA^2+muB^2+C1)(varA+varB+C2))
func ssimBlock(a, b [][]uint8, x0, y0, x1, y1 int) float64 {
	n := float64((x1 - x0) * (y1 - y0))

	var sumA, sumB float64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			sumA += float64(a[y][x])
			sumB += float64(b[y][x])
		}
	}
	muA, muB := sumA/n, sumB/n

	var varA, varB, cov float64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			da := float64(a[y][x]) - muA
			db := float64(b[y][x]) - muB
			varA += da * da
			varB += db * db
			cov += da * db
		}
	}
	varA /= n
	varB /= n
	cov /= n

	numerator := (2*muA*muB + ssimC1) * (2*cov + ssimC2)
	denominator := (muA*muA + muB*muB + ssimC1) * (varA + varB + ssimC2)
	return clampSSIM(numerator / denominator)
}

// MeasureSSIM computes luma SSIM between a and b using SSIM's 8x8-window
// convention. It returns an error if the two images differ in size.
func MeasureSSIM(a, b image.Image) (float64, error) {
	if a.Bounds().Dx() != b.Bounds().Dx() || a.Bounds().Dy() != b.Bounds().Dy() {
		return 0, fmt.Errorf("oracle: MeasureSSIM size mismatch: %dx%d vs %dx%d",
			a.Bounds().Dx(), a.Bounds().Dy(), b.Bounds().Dx(), b.Bounds().Dy())
	}
	return SSIM(planeY(a), planeY(b))
}

// clampSSIM keeps a computed SSIM value inside its defined range, guarding
// against floating-point overshoot on near-degenerate (flat) windows.
func clampSSIM(v float64) float64 {
	return math.Max(-1, math.Min(1, v))
}
