package oracle

import (
	"bytes"
	"fmt"
	"image"
	"io"
)

// EncodeFunc is the shape every encoder this oracle measures must expose.
// It mirrors image/jpeg's Encode signature (D9a of the tqwebp spec), minus
// the concrete Options type: WP-0 predates the real encoder's own Options,
// so callers bind quality, method, and any other knob by closing over it.
// A stdlib JPEG baseline at quality 82 is therefore
// "func(w io.Writer, m image.Image) error { return jpeg.Encode(w, m,
// &jpeg.Options{Quality: 82}) }", and a future tqwebp.Encode call, or
// deepteams/webp's Encode call, wraps the same way.
type EncodeFunc func(w io.Writer, m image.Image) error

// Result is the outcome of one RoundTrip call: the encoded size and the
// oracle's distortion measurements against the source image.
type Result struct {
	// EncodedBytes is the length of the encoder's output.
	EncodedBytes int
	// PSNR holds per-channel PSNR (decibels) between the source image and
	// the independently decoded output.
	PSNR ChannelPSNR
	// SSIM is luma SSIM (§11.1 convention: 8x8 windows) between the source
	// image and the independently decoded output.
	SSIM float64
	// Width and Height are the source image's dimensions, carried through
	// for reporting.
	Width, Height int
}

// RoundTrip encodes src with enc, decodes the result through
// golang.org/x/image/webp — an independent, pure-Go decoder — and scores
// the decoded image against src. This is the WP-0 backbone gate: every
// later encoder work package proves itself through this same path.
//
// The decode goes through DecodeWebP, which reads the planes with
// golang.org/x/image and then inverts them with libwebp's own BT.601
// limited-range coefficients. The inversion matters: x/image applies the
// full-range JFIF coefficients instead, which moves red, green, and blue
// by up to 20 per channel and charges a WebP encoder several decibels for
// a decoder's convention. See webpcolor.go.
//
// RoundTrip does not care what enc's target format is, as long as the
// output is a lossy WebP file. Baseline encoders that emit a different
// container (for example, stdlib JPEG) use RoundTripWith and supply their
// own decoder instead.
func RoundTrip(enc EncodeFunc, src image.Image) (*Result, error) {
	return RoundTripWith(enc, src, decodeWebPReader)
}

// decodeWebPReader adapts DecodeWebP to the DecodeFunc shape.
func decodeWebPReader(r io.Reader) (image.Image, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return DecodeWebP(data)
}

// DecodeFunc is the shape of an independent decoder RoundTripWith uses to
// close the loop on enc's output. golang.org/x/image/webp.Decode and
// image/jpeg.Decode both already have this shape.
type DecodeFunc func(r io.Reader) (image.Image, error)

// RoundTripWith is RoundTrip generalized over the decoder, so the same
// oracle machinery scores non-WebP baselines (stdlib JPEG, in WP-0's
// baseline harness) with their own matching decoder.
func RoundTripWith(enc EncodeFunc, src image.Image, dec DecodeFunc) (*Result, error) {
	var buf bytes.Buffer
	if err := enc(&buf, src); err != nil {
		return nil, fmt.Errorf("oracle: encode: %w", err)
	}

	decoded, err := dec(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("oracle: independent decode: %w", err)
	}

	b := src.Bounds()
	db := decoded.Bounds()
	if b.Dx() != db.Dx() || b.Dy() != db.Dy() {
		return nil, fmt.Errorf("oracle: dimension mismatch after round trip: source %dx%d, decoded %dx%d",
			b.Dx(), b.Dy(), db.Dx(), db.Dy())
	}

	psnr, err := MeasurePSNR(src, decoded)
	if err != nil {
		return nil, fmt.Errorf("oracle: PSNR: %w", err)
	}
	ssim, err := MeasureSSIM(src, decoded)
	if err != nil {
		return nil, fmt.Errorf("oracle: SSIM: %w", err)
	}

	return &Result{
		EncodedBytes: buf.Len(),
		PSNR:         psnr,
		SSIM:         ssim,
		Width:        b.Dx(),
		Height:       b.Dy(),
	}, nil
}
