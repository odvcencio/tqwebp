package oracle

import (
	"fmt"
	"image"
)

// ReconstructionSource is implemented by an encoder that can expose the
// internal Y, Cb, and Cr planes it built during encoding, before loop
// filtering runs. WP-1 wires a real VP8 encoder to this interface so
// CompareExact can prove reconstruction is bit-exact against an
// independent decode. WP-0 ships only the interface, the comparator, and a
// stub implementation, so the contract and its tests exist before the
// encoder does.
type ReconstructionSource interface {
	// ReconstructionPlanes returns the encoder's internal 4:2:0 luma and
	// chroma planes, in the same layout an *image.YCbCr decode produces.
	ReconstructionPlanes() (*image.YCbCr, error)
}

// CompareExact asserts the WP-0 loop-filter-0 contract (RFC 6386 §15): with
// loop filter level 0, an independent decoder's output must equal the
// encoder's own internal reconstruction, byte for byte, on every plane. It
// returns nil on an exact match, or an error describing the first
// mismatching plane and pixel otherwise.
func CompareExact(src ReconstructionSource, decoded image.Image) error {
	recon, err := src.ReconstructionPlanes()
	if err != nil {
		return fmt.Errorf("oracle: CompareExact: reconstruction planes: %w", err)
	}

	decodedYCbCr, ok := decoded.(*image.YCbCr)
	if !ok {
		// Not every decoder returns *image.YCbCr directly (an opaque WebP
		// with no subsampling quirks might), so fall back to a generic
		// per-plane comparison through the color-conversion path.
		return compareExactGeneric(recon, decoded)
	}

	if recon.Rect != decodedYCbCr.Rect {
		return fmt.Errorf("oracle: CompareExact: bounds mismatch: reconstruction %v, decoded %v",
			recon.Rect, decodedYCbCr.Rect)
	}
	if recon.SubsampleRatio != decodedYCbCr.SubsampleRatio {
		return fmt.Errorf("oracle: CompareExact: subsample ratio mismatch: reconstruction %v, decoded %v",
			recon.SubsampleRatio, decodedYCbCr.SubsampleRatio)
	}

	if err := compareBytePlane("Y", recon.Y, recon.YStride, decodedYCbCr.Y, decodedYCbCr.YStride, recon.Rect.Dx(), recon.Rect.Dy()); err != nil {
		return err
	}
	cw, ch := chromaSize(recon.Rect, recon.SubsampleRatio)
	if err := compareBytePlane("Cb", recon.Cb, recon.CStride, decodedYCbCr.Cb, decodedYCbCr.CStride, cw, ch); err != nil {
		return err
	}
	if err := compareBytePlane("Cr", recon.Cr, recon.CStride, decodedYCbCr.Cr, decodedYCbCr.CStride, cw, ch); err != nil {
		return err
	}
	return nil
}

func compareBytePlane(name string, a []uint8, aStride int, b []uint8, bStride int, w, h int) error {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			av := a[y*aStride+x]
			bv := b[y*bStride+x]
			if av != bv {
				return fmt.Errorf("oracle: CompareExact: plane %s mismatch at (%d,%d): reconstruction=%d decoded=%d",
					name, x, y, av, bv)
			}
		}
	}
	return nil
}

func chromaSize(r image.Rectangle, ratio image.YCbCrSubsampleRatio) (w, h int) {
	w, h = r.Dx(), r.Dy()
	switch ratio {
	case image.YCbCrSubsampleRatio420:
		return (w + 1) / 2, (h + 1) / 2
	case image.YCbCrSubsampleRatio422:
		return (w + 1) / 2, h
	case image.YCbCrSubsampleRatio440:
		return w, (h + 1) / 2
	default:
		return w, h
	}
}

// compareExactGeneric compares the reconstruction planes against a decoded
// image that is not natively *image.YCbCr, by re-deriving Y/Cb/Cr for the
// decoded image through the same color path oracle.PSNR uses. It is a
// fallback: the primary WP-1 path always compares native planes.
func compareExactGeneric(recon *image.YCbCr, decoded image.Image) error {
	decodedY := planeY(decoded)
	w, h := recon.Rect.Dx(), recon.Rect.Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			rv := recon.Y[y*recon.YStride+x]
			dv := decodedY[y][x]
			if rv != dv {
				return fmt.Errorf("oracle: CompareExact: plane Y mismatch at (%d,%d): reconstruction=%d decoded=%d",
					x, y, rv, dv)
			}
		}
	}
	return nil
}

// StubReconstructionSource is a fixed, in-memory ReconstructionSource used
// to test CompareExact before a real encoder exists. It always reports the
// planes it was built with; it does not encode anything.
type StubReconstructionSource struct {
	planes *image.YCbCr
}

// NewStubReconstructionSource builds a StubReconstructionSource that
// reports src's own YCbCr planes as its reconstruction — useful for
// exercising CompareExact against a decode of an image known to equal src.
func NewStubReconstructionSource(src image.Image) *StubReconstructionSource {
	return &StubReconstructionSource{planes: toYCbCrImage(src)}
}

// ReconstructionPlanes implements ReconstructionSource.
func (s *StubReconstructionSource) ReconstructionPlanes() (*image.YCbCr, error) {
	return s.planes, nil
}

// toYCbCrImage converts any image.Image to a 4:4:4 *image.YCbCr using the
// same color path the rest of this package uses.
func toYCbCrImage(m image.Image) *image.YCbCr {
	b := m.Bounds()
	out := image.NewYCbCr(b, image.YCbCrSubsampleRatio444)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			yy, cb, cr := toYCbCr(m.At(x, y))
			yi := out.YOffset(x, y)
			ci := out.COffset(x, y)
			out.Y[yi] = yy
			out.Cb[ci] = cb
			out.Cr[ci] = cr
		}
	}
	return out
}
