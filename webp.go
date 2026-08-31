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
// # Alpha
//
// Encode keeps a translucent picture's alpha channel, and it decides
// without a knob. An opaque picture writes the simple container: a RIFF
// header and a VP8 key frame, byte for byte what every earlier release
// wrote. A picture with one translucent pixel writes the extended
// container instead: a VP8X chunk with the alpha flag set, an ALPH chunk
// that stores the alpha plane one byte per pixel, and then the same key
// frame.
//
// Storing the alpha plane raw costs width times height bytes. A VP8L
// compressed alpha plane, which the WebP container also allows, costs far
// less on the flat masks that badges and logos carry. This release does
// not build one; see the README for what that means for file size today.
//
// The colour under a translucent pixel is straight, not premultiplied,
// which is what the WebP format stores. An *image.RGBA holds
// premultiplied colour, so Encode divides the alpha back out of it. A
// pixel of alpha zero in an *image.RGBA carries no colour to recover and
// encodes as black. An *image.NRGBA holds straight colour already and
// keeps every sample, alpha zero included.
//
// # Scope
//
// This release codes lossy frames. It writes no lossless VP8L frame, no
// animation, and no metadata chunk. ErrAlphaUnsupported remains for
// callers who test it, and Encode no longer returns it.
//
// # Resource limits
//
// EncodeWithLimits adds optional bounds for width, height, visible pixel
// count, and complete output size. Every Limits field uses zero for no
// caller-specified limit; negative fields return ErrInvalidLimits. Bounds
// and the visible pixel count are checked before Opaque or At is called and
// before padded planes are allocated. The complete file is serialized in a
// private buffer before MaxOutputBytes is checked, so a refusal for that
// limit never writes a partial file. Encode remains the backwards-compatible
// unconstrained entry point.
//
// # Effort levels
//
// Method runs from 0 to 6. Methods 0 to 4 implement one effort level:
// every macroblock's luma uses one of the four whole-block prediction
// modes and one of the four chroma modes. Method 5 and 6 add a
// conservative detailed-block pass: a macroblock whose luma the whole-
// block modes fit poorly may be coded as sixteen independent 4x4 blocks
// instead, but only when the candidate's sum-of-squares error is
// strictly below half of the whole-block error. That margin is a fixed,
// bounded proxy chosen so the selector stays conservative without a bit
// model; the exact rate-distortion mode search and the two-pass
// probability optimization arrive in later releases.
package webp

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"

	"m31labs.dev/tqwebp/internal/encoder"
	"m31labs.dev/tqwebp/internal/frame"
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
	// Method selects the effort level, from 0, the fastest, to 6, the
	// slowest and best. Methods 0 to 4 share one effort level. At 5
	// and 6 a conservative detailed-block pass may code selected
	// macroblocks' luma as sixteen 4x4 blocks; see the Effort levels
	// section above.
	Method int
}

// Limits bounds the work and output of EncodeWithLimits. A zero field means
// that field has no caller-specified limit. Negative fields are invalid and
// return ErrInvalidLimits. The encoder's built-in VP8 dimension limit still
// applies when MaxWidth and MaxHeight are zero.
//
// MaxPixels counts the visible pixels in m.Bounds(), before any padded
// macroblocks are allocated. MaxOutputBytes counts the complete WebP file,
// including its RIFF and VP8 headers and any pad byte.
type Limits struct {
	MaxWidth       int
	MaxHeight      int
	MaxPixels      int64
	MaxOutputBytes int64
}

// Sentinel errors Encode returns. Callers can test them with errors.Is.
var (
	// ErrAlphaUnsupported is retired. Encode used to return it for an
	// image with a translucent pixel; it now writes the alpha channel
	// into an ALPH chunk instead, so no encode returns this error any
	// more. The variable stays so that code which tests for it still
	// compiles.
	//
	// Deprecated: Encode keeps the alpha channel. Nothing returns this
	// error.
	ErrAlphaUnsupported = errors.New("tqwebp: alpha channel is not supported yet")

	// ErrInvalidOptions reports an option value outside its range.
	ErrInvalidOptions = errors.New("tqwebp: invalid options")

	// ErrInvalidLimits reports a negative resource limit. Zero means
	// unlimited for every field in Limits.
	ErrInvalidLimits = errors.New("tqwebp: invalid limits")

	// ErrLimitExceeded reports an image or output that exceeds a caller's
	// configured resource limit.
	ErrLimitExceeded = errors.New("tqwebp: resource limit exceeded")

	// ErrOutputTooLarge reports an encoded file larger than MaxOutputBytes.
	// It also wraps ErrLimitExceeded, so callers can handle all limit
	// refusals through either sentinel.
	ErrOutputTooLarge = errors.New("tqwebp: output exceeds limit")

	// ErrTooLarge reports an image wider or taller than 16383 pixels,
	// which the VP8 picture size fields cannot carry.
	ErrTooLarge = frame.ErrTooLarge
)

// Encode writes m to w in the lossy WebP format. A nil o means the
// default options.
//
// An opaque m writes the simple container. An m with a translucent pixel
// writes the extended container, which carries the alpha channel; see the
// Alpha section above.
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
	return encoder.Encode(w, m, cfg)
}

// EncodeWithLimits writes m in the lossy WebP format subject to limits. A
// nil o means the default options. A zero Limits value imposes no additional
// limit, so the encoded bytes match Encode for the same image and options.
//
// Bounds and the visible pixel count are checked before the encoder calls
// Opaque or At and before it allocates padded image planes. The complete
// WebP file is serialized into a private buffer before MaxOutputBytes is
// checked, so an output-cap refusal never writes a partial file to w.
func EncodeWithLimits(w io.Writer, m image.Image, o *Options, limits Limits) error {
	cfg, err := configFor(o)
	if err != nil {
		return err
	}
	if err := validateLimits(limits); err != nil {
		return err
	}

	b := m.Bounds()
	width, height := b.Dx(), b.Dy()
	if width <= 0 || height <= 0 {
		return fmt.Errorf("tqwebp: image is %dx%d pixels", width, height)
	}
	if limits.MaxWidth > 0 && width > limits.MaxWidth {
		return fmt.Errorf("%w: width %d exceeds MaxWidth %d", ErrLimitExceeded, width, limits.MaxWidth)
	}
	if limits.MaxHeight > 0 && height > limits.MaxHeight {
		return fmt.Errorf("%w: height %d exceeds MaxHeight %d", ErrLimitExceeded, height, limits.MaxHeight)
	}
	if limits.MaxPixels > 0 && uint64(width) > uint64(limits.MaxPixels)/uint64(height) {
		return fmt.Errorf("%w: image has more than MaxPixels %d pixels", ErrLimitExceeded, limits.MaxPixels)
	}
	if width > frame.MaxDimension || height > frame.MaxDimension {
		return ErrTooLarge
	}

	var encoded bytes.Buffer
	if err := encoder.Encode(&encoded, m, cfg); err != nil {
		return err
	}
	if limits.MaxOutputBytes > 0 && int64(encoded.Len()) > limits.MaxOutputBytes {
		return fmt.Errorf("%w: %d bytes exceeds MaxOutputBytes %d: %w", ErrOutputTooLarge, encoded.Len(), limits.MaxOutputBytes, ErrLimitExceeded)
	}
	data := encoded.Bytes()
	n, err := w.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func validateLimits(limits Limits) error {
	if limits.MaxWidth < 0 {
		return fmt.Errorf("%w: MaxWidth must be non-negative", ErrInvalidLimits)
	}
	if limits.MaxHeight < 0 {
		return fmt.Errorf("%w: MaxHeight must be non-negative", ErrInvalidLimits)
	}
	if limits.MaxPixels < 0 {
		return fmt.Errorf("%w: MaxPixels must be non-negative", ErrInvalidLimits)
	}
	if limits.MaxOutputBytes < 0 {
		return fmt.Errorf("%w: MaxOutputBytes must be non-negative", ErrInvalidLimits)
	}
	return nil
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
