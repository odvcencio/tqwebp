package oracle

import (
	"fmt"
	"image"
	"math"
)

// PlanePSNR holds the distortion of one coded picture measured in the
// codec's own plane domain: the encoder's pre-encode Y, Cb, and Cr planes
// against the planes an independent decoder produced.
//
// Use this measure for questions about the codec itself, such as the
// shape of its quality curve. The plane domain removes the decoder's
// colour convention from the answer, which matters because
// golang.org/x/image and libwebp disagree about it (see webpcolor.go).
//
// Do not use this measure to compare two codecs that carry different
// sample ranges. A limited-range WebP luma plane spans 16 to 235 and a
// full-range JPEG luma plane spans 0 to 255, so the same relative error
// scores about 1.3 dB higher in the limited-range plane. Cross-codec
// comparisons go through the red-green-blue domain instead
// (MeasurePSNR over DecodeWebP's output).
type PlanePSNR struct {
	Y, Cb, Cr float64
}

// MeasurePlanePSNR compares two sets of planes over the visible
// rectangle. Both arguments must carry the same picture size and the same
// subsample ratio.
func MeasurePlanePSNR(src, dec *image.YCbCr) (PlanePSNR, error) {
	if src.Rect.Dx() != dec.Rect.Dx() || src.Rect.Dy() != dec.Rect.Dy() {
		return PlanePSNR{}, fmt.Errorf("oracle: MeasurePlanePSNR size mismatch: %dx%d vs %dx%d",
			src.Rect.Dx(), src.Rect.Dy(), dec.Rect.Dx(), dec.Rect.Dy())
	}
	if src.SubsampleRatio != dec.SubsampleRatio {
		return PlanePSNR{}, fmt.Errorf("oracle: MeasurePlanePSNR subsample ratio mismatch: %v vs %v",
			src.SubsampleRatio, dec.SubsampleRatio)
	}

	w, h := src.Rect.Dx(), src.Rect.Dy()
	cw, ch := chromaSize(src.Rect, src.SubsampleRatio)
	return PlanePSNR{
		Y:  stridedPSNR(src.Y, src.YStride, dec.Y, dec.YStride, w, h),
		Cb: stridedPSNR(src.Cb, src.CStride, dec.Cb, dec.CStride, cw, ch),
		Cr: stridedPSNR(src.Cr, src.CStride, dec.Cr, dec.CStride, cw, ch),
	}, nil
}

// stridedPSNR measures one plane pair over a w by h window.
func stridedPSNR(a []uint8, aStride int, b []uint8, bStride int, w, h int) float64 {
	var sumSq float64
	for y := 0; y < h; y++ {
		rowA := a[y*aStride:]
		rowB := b[y*bStride:]
		for x := 0; x < w; x++ {
			d := float64(rowA[x]) - float64(rowB[x])
			sumSq += d * d
		}
	}
	if w == 0 || h == 0 {
		return 0
	}
	mse := sumSq / float64(w*h)
	if mse == 0 {
		return PSNRInf
	}
	return 10 * math.Log10(255*255/mse)
}
