package oracle

import (
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"testing"
)

// jpegEncodeFunc closes JPEG's Options into the oracle's EncodeFunc shape,
// exactly as WP-1's real encoder will do once it has its own Options type.
func jpegEncodeFunc(quality int) EncodeFunc {
	return func(w io.Writer, m image.Image) error {
		return jpeg.Encode(w, m, &jpeg.Options{Quality: quality})
	}
}

func checkerboardImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x/4+y/4)%2 == 0 {
				img.SetRGBA(x, y, color.RGBA{R: 220, G: 60, B: 40, A: 255})
			} else {
				img.SetRGBA(x, y, color.RGBA{R: 40, G: 90, B: 210, A: 255})
			}
		}
	}
	return img
}

func TestRoundTripWith_JPEGBaseline(t *testing.T) {
	src := checkerboardImage(64, 64)
	res, err := RoundTripWith(jpegEncodeFunc(85), src, jpeg.Decode)
	if err != nil {
		t.Fatalf("RoundTripWith: %v", err)
	}
	if res.EncodedBytes == 0 {
		t.Fatal("RoundTripWith: EncodedBytes = 0")
	}
	if res.Width != 64 || res.Height != 64 {
		t.Fatalf("RoundTripWith: dims = %dx%d, want 64x64", res.Width, res.Height)
	}
	if res.PSNR.Y < 20 {
		t.Fatalf("RoundTripWith: Y-PSNR = %v, suspiciously low for quality 85", res.PSNR.Y)
	}
	if res.SSIM < 0.5 {
		t.Fatalf("RoundTripWith: SSIM = %v, suspiciously low for quality 85", res.SSIM)
	}
}

func TestRoundTripWith_QualityMonotonicity(t *testing.T) {
	src := checkerboardImage(96, 96)

	low, err := RoundTripWith(jpegEncodeFunc(20), src, jpeg.Decode)
	if err != nil {
		t.Fatalf("RoundTripWith(q20): %v", err)
	}
	high, err := RoundTripWith(jpegEncodeFunc(95), src, jpeg.Decode)
	if err != nil {
		t.Fatalf("RoundTripWith(q95): %v", err)
	}

	if high.EncodedBytes <= low.EncodedBytes {
		t.Fatalf("higher quality did not grow bytes: q20=%d q95=%d", low.EncodedBytes, high.EncodedBytes)
	}
	if high.PSNR.Y <= low.PSNR.Y {
		t.Fatalf("higher quality did not raise Y-PSNR: q20=%v q95=%v", low.PSNR.Y, high.PSNR.Y)
	}
}

func TestRoundTripWith_EncodeError(t *testing.T) {
	failing := func(w io.Writer, m image.Image) error {
		return io.ErrClosedPipe
	}
	src := checkerboardImage(8, 8)
	if _, err := RoundTripWith(failing, src, jpeg.Decode); err == nil {
		t.Fatal("RoundTripWith: want error from a failing encoder, got nil")
	}
}

func TestRoundTripWith_DecodeError(t *testing.T) {
	failing := func(r io.Reader) (image.Image, error) {
		return nil, io.ErrUnexpectedEOF
	}
	src := checkerboardImage(8, 8)
	if _, err := RoundTripWith(jpegEncodeFunc(75), src, failing); err == nil {
		t.Fatal("RoundTripWith: want error from a failing decoder, got nil")
	}
}
