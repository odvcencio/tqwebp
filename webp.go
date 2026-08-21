// Package webp encodes images in the lossy WebP format, in pure Go.
//
// A lossy WebP file is a VP8 key frame inside a RIFF container. This
// package writes that file with no cgo, no WebAssembly runtime, and no
// foreign function interface. Its output decodes in every browser and in
// golang.org/x/image/webp, which this repository's tests use as an
// independent oracle.
//
// The interface mirrors image/jpeg, so callers who know the standard
// library know this package:
//
//	f, err := os.Create("photo.webp")
//	if err != nil {
//		return err
//	}
//	defer f.Close()
//	if err := webp.Encode(f, img, &webp.Options{Quality: 80}); err != nil {
//		return err
//	}
//
// # Determinism
//
// The same image and the same options always produce the same bytes, on
// every platform and at every value of GOMAXPROCS. Asset pipelines that
// hash their output can rely on that.
//
// # Scope
//
// This release codes opaque images only. Encode returns
// ErrAlphaUnsupported for an image with a translucent pixel, so no
// pipeline can lose a mask without noticing. The encoder searches the four
// whole-block luma modes and four chroma modes. At method 1 and above it also
// trials the ten 4x4 luma predictors on detailed macroblocks and retains only
// conservative reconstruction-and-sparsity wins. Exact rate-distortion costs
// and two-pass probability optimization arrive in later releases.
package webp

import (
	"errors"
	"fmt"
	"image"
	"io"

	"m31labs.dev/tqwebp/internal/encoder"
	"m31labs.dev/tqwebp/internal/frame"
	"m31labs.dev/tqwebp/internal/yuv"
)

// DefaultQuality is the quality Encode uses when Options is nil or when
// its Quality field is zero.
const DefaultQuality = 75

// DefaultMethod is the effort level Encode uses when Options is nil or
// when its Method field is zero.
const DefaultMethod = 4

// Options configures the encoder. A nil *Options, and the zero value,
// both mean quality DefaultQuality and method DefaultMethod.
type Options struct {
	// Quality selects the rate-distortion point, from 1, the smallest
	// file, to 100, the best picture. Higher quality always spends more
	// bytes and always keeps more detail. A zero Quality means
	// DefaultQuality, which makes the zero value of Options useful.
	Quality int
	// Method selects the effort level from 0 through 6. A zero value means
	// DefaultMethod for historical zero-value compatibility. Values 1 through
	// 6 currently enable the same bounded selective 4x4 luma search; later
	// work will assign additional observable search to the higher values.
	Method int
}

// Sentinel errors Encode returns. Callers can test them with errors.Is.
var (
	// ErrAlphaUnsupported reports an image with at least one translucent
	// pixel. This release codes opaque images only, and it refuses rather
	// than dropping the alpha channel in silence.
	ErrAlphaUnsupported = errors.New("tqwebp: alpha channel is not supported yet")

	// ErrInvalidOptions reports an option value outside its range.
	ErrInvalidOptions = errors.New("tqwebp: invalid options")

	// ErrTooLarge reports an image wider or taller than 16383 pixels,
	// which the VP8 picture size fields cannot carry.
	ErrTooLarge = frame.ErrTooLarge
)

// Encode writes m to w in the lossy WebP format. A nil o means the
// default options.
//
// Encode buffers the whole file before it writes, because the container
// size, the frame tag, and the partition length all precede the data they
// describe.
func Encode(w io.Writer, m image.Image, o *Options) error {
	cfg, err := configFor(o)
	if err != nil {
		return err
	}

	b := m.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return fmt.Errorf("tqwebp: image is %dx%d pixels", b.Dx(), b.Dy())
	}
	if b.Dx() > frame.MaxDimension || b.Dy() > frame.MaxDimension {
		return ErrTooLarge
	}
	if !yuv.IsOpaque(m) {
		return ErrAlphaUnsupported
	}
	return encoder.Encode(w, m, cfg)
}

// configFor validates o and fills its defaults in.
func configFor(o *Options) (encoder.Config, error) {
	cfg := encoder.Config{Quality: DefaultQuality, Method: DefaultMethod}
	if o == nil {
		return cfg, nil
	}
	if o.Quality < 0 || o.Quality > 100 {
		return cfg, fmt.Errorf("%w: quality %d is outside 0 to 100", ErrInvalidOptions, o.Quality)
	}
	if o.Method < 0 || o.Method > 6 {
		return cfg, fmt.Errorf("%w: method %d is outside 0 to 6", ErrInvalidOptions, o.Method)
	}
	if o.Quality != 0 {
		cfg.Quality = o.Quality
	}
	if o.Method != 0 {
		cfg.Method = o.Method
	}
	return cfg, nil
}
