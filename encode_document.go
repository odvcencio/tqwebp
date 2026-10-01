package webp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"time"

	"m31labs.dev/tqwebp/container"
	"m31labs.dev/tqwebp/internal/encoder"
	"m31labs.dev/tqwebp/internal/webpwire"
	"m31labs.dev/tqwebp/internal/yuv"
)

var ErrInvalidDocument = errors.New("tqwebp: invalid document")
var ErrInvalidMetadata = container.ErrInvalidMetadata

// DocumentLimits bounds EncodeAll. Embedded legacy Limits fields retain zero
// as unlimited. New zero fields select DefaultDocumentLimits; negatives fail.
// These pixel/output budgets are not working-memory or hard CPU limits.
type DocumentLimits struct {
	Limits
	MaxFrames, MaxTotalPixels, MaxMetadataBytes int64
	MaxDuration                                 time.Duration
}

func DefaultDocumentLimits() DocumentLimits {
	return DocumentLimits{MaxFrames: 1000, MaxTotalPixels: 256_000_000, MaxMetadataBytes: 4 << 20, MaxDuration: 10 * time.Minute}
}
func normalizeDocumentLimits(l DocumentLimits) (DocumentLimits, error) {
	if e := validateLimits(l.Limits); e != nil {
		return l, e
	}
	d := DefaultDocumentLimits()
	p := []*int64{&l.MaxFrames, &l.MaxTotalPixels, &l.MaxMetadataBytes}
	v := []int64{d.MaxFrames, d.MaxTotalPixels, d.MaxMetadataBytes}
	for i, x := range p {
		if *x < 0 {
			return l, ErrInvalidLimits
		}
		if *x == 0 {
			*x = v[i]
		}
	}
	if l.MaxDuration < 0 {
		return l, ErrInvalidLimits
	}
	if l.MaxDuration == 0 {
		l.MaxDuration = d.MaxDuration
	}
	return l, nil
}

// EncodeAll encodes full displayed canvases using lossy VP8 color and exact
// raw 8-bit alpha. Animations use full-canvas replacement, no disposal, one
// stored pass and unchanged timing/loop intent. It never mutates caller storage.
// Metadata contains only explicitly supplied nonempty payloads, copied verbatim;
// no profile transform or orientation operation is performed. All output is
// prepared before writing; a writer failure can leave a partial destination.
func EncodeAll(ctx context.Context, w io.Writer, doc *Document, o *Options, limits DocumentLimits) error {
	if nilInterface(ctx) {
		return ErrInvalidContext
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	cfg, e := configFor(o)
	if e != nil {
		return e
	}
	l, e := normalizeDocumentLimits(limits)
	if e != nil {
		return e
	}
	if nilInterface(w) {
		return ErrInvalidWriter
	}
	if doc == nil {
		return ErrInvalidDocument
	}
	if len(doc.Frames) == 0 || doc.Canvas.X <= 0 || doc.Canvas.Y <= 0 {
		return ErrInvalidDocument
	}
	if int64(len(doc.Frames)) > l.MaxFrames {
		return &LimitError{"frames", l.MaxFrames, int64(len(doc.Frames))}
	}
	if !doc.Animated && (len(doc.Frames) != 1 || doc.LoopCount != 0 || doc.Background != (color.NRGBA{})) {
		return ErrInvalidDocument
	}
	if doc.Canvas.X > 16383 || doc.Canvas.Y > 16383 {
		return ErrTooLarge
	}
	pixels := int64(doc.Canvas.X) * int64(doc.Canvas.Y)
	if pixels > l.MaxTotalPixels/int64(len(doc.Frames)) {
		return &LimitError{"total_pixels", l.MaxTotalPixels, limitActual(uint64(pixels), uint64(len(doc.Frames)))}
	}
	var duration time.Duration
	// Validate every image and duration before touching any pixel/opacity callback.
	for i, f := range doc.Frames {
		if e := ctx.Err(); e != nil {
			return e
		}
		if nilInterface(f.Pixels) {
			return fmt.Errorf("%w: frame %d pixels", ErrInvalidDocument, i)
		}
		b := f.Pixels.Bounds()
		if b.Max.X <= b.Min.X || b.Max.Y <= b.Min.Y {
			return fmt.Errorf("%w: frame %d bounds", ErrInvalidDocument, i)
		}
		width, height := uint64(b.Max.X)-uint64(b.Min.X), uint64(b.Max.Y)-uint64(b.Min.Y)
		if width != uint64(doc.Canvas.X) || height != uint64(doc.Canvas.Y) {
			return fmt.Errorf("%w: frame %d canvas mismatch", ErrInvalidDocument, i)
		}
		if l.MaxWidth > 0 && width > uint64(l.MaxWidth) {
			return &LimitError{"width", int64(l.MaxWidth), int64(width)}
		}
		if l.MaxHeight > 0 && height > uint64(l.MaxHeight) {
			return &LimitError{"height", int64(l.MaxHeight), int64(height)}
		}
		if l.MaxPixels > 0 && pixels > l.MaxPixels {
			return &LimitError{"pixels", l.MaxPixels, pixels}
		}
		if f.Duration < 0 || f.Duration%time.Millisecond != 0 || f.Duration/time.Millisecond > 0xffffff || !doc.Animated && f.Duration != 0 {
			return fmt.Errorf("%w: frame %d duration", ErrInvalidDocument, i)
		}
		if f.Duration > l.MaxDuration-duration {
			return &LimitError{"duration", int64(l.MaxDuration), limitDurationActual(duration, f.Duration)}
		}
		duration += f.Duration
	}
	metadata := make([]container.Chunk, 0, 3)
	var metadataBytes int64
	for _, c := range []container.Chunk{{FourCC: "ICCP", Data: doc.Metadata.ICC}, {FourCC: "EXIF", Data: doc.Metadata.EXIF}, {FourCC: "XMP ", Data: doc.Metadata.XMP}} {
		if e := ctx.Err(); e != nil {
			return e
		}
		if len(c.Data) == 0 {
			continue
		}
		if int64(len(c.Data)) > l.MaxMetadataBytes-metadataBytes {
			return &LimitError{"metadata_bytes", l.MaxMetadataBytes, -1}
		}
		metadataBytes += int64(len(c.Data))
		metadata = append(metadata, c)
	}
	opaque := make([]bool, len(doc.Frames))
	hasAlpha := false
	for i, f := range doc.Frames {
		opaque[i], e = yuv.IsOpaqueContext(ctx, f.Pixels)
		if e != nil {
			return e
		}
		hasAlpha = hasAlpha || !opaque[i]
	}
	// All fixed chunks, including each VP8 chunk header, precede codec admission.
	cap := encodingOutputCap(l.MaxOutputBytes)
	fixed := int64(0)
	add := func(n int64) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if n < 0 || n > cap-fixed {
			return outputLimit(l.MaxOutputBytes, cap, -1)
		}
		fixed += n
		return nil
	}
	if e = add(12); e != nil {
		return e
	}
	if doc.Animated || hasAlpha || len(metadata) > 0 {
		if e = add(18); e != nil {
			return e
		}
	}
	if doc.Animated {
		if e = add(14); e != nil {
			return e
		}
	}
	for _, c := range metadata {
		if int64(len(c.Data)) > cap {
			return outputLimit(l.MaxOutputBytes, cap, -1)
		}
		if e = add(chunkSize(int64(len(c.Data)))); e != nil {
			return e
		}
	}
	for _, op := range opaque {
		if e = add(8); e != nil {
			return e
		}
		if doc.Animated {
			if e = add(24); e != nil {
				return e
			}
		}
		if !op {
			if e = add(chunkSize(1 + pixels)); e != nil {
				return e
			}
		}
	}
	f := &container.File{Canvas: doc.Canvas, Animated: doc.Animated, LoopCount: doc.LoopCount, Background: doc.Background, MetadataChunks: metadata, Frames: make([]container.EncodedFrame, 0, len(doc.Frames))}
	used := fixed
	for i, src := range doc.Frames {
		if e := ctx.Err(); e != nil {
			return e
		}
		ef, e := encodeFrame(ctx, src.Pixels, cfg, opaque[i], cap-used+20, l.MaxOutputBytes, cap)
		if e != nil {
			return e
		}
		used += int64(len(ef.VP8)) + (int64(len(ef.VP8)) & 1)
		ef.Duration = src.Duration
		if doc.Animated {
			ef.Blend = container.BlendReplace
		}
		f.Frames = append(f.Frames, ef)
	}
	return muxEncoded(ctx, w, f, l.MaxOutputBytes, cap)
}
func limitDurationActual(a, b time.Duration) int64 {
	if b > time.Duration(math.MaxInt64)-a {
		return -1
	}
	return int64(a + b)
}
func chunkSize(n int64) int64 { return 8 + n + (n & 1) }
func encodingOutputCap(requested int64) int64 {
	cap := webpwire.MaxRIFFSize + 8
	if int64(int(^uint(0)>>1)) < cap {
		cap = int64(int(^uint(0) >> 1))
	}
	if requested > 0 && requested < cap {
		cap = requested
	}
	return cap
}
func outputLimit(requested, effective, actual int64) error {
	if requested > 0 && requested <= effective {
		return &LimitError{"output_bytes", requested, actual}
	}
	return &LimitError{"output_bytes", effective, actual}
}

// singleCapture borrows the internal encoder's one committed buffer. It is not
// caller-visible, and the encoder does not mutate it after Write returns.
type singleCapture struct{ data []byte }

func (c *singleCapture) Write(p []byte) (int, error) {
	if c.data != nil {
		return 0, errors.New("tqwebp: unexpected multiple internal writes")
	}
	c.data = p
	return len(p), nil
}

func encodeFrame(ctx context.Context, m image.Image, cfg encoder.Config, opaque bool, simpleCap, requested, effective int64) (container.EncodedFrame, error) {
	out := container.EncodedFrame{}
	if simpleCap <= 20 {
		return out, outputLimit(requested, effective, -1)
	}
	source := m
	b := m.Bounds()
	width, height := b.Dx(), b.Dy()
	if !opaque {
		// Bounds were admitted by the public preflight. The 4-byte straight-color
		// copy and one-byte alpha plane are encoder memory, not output-cap claims.
		if int64(width)*int64(height) > int64(int(^uint(0)>>1))/4 {
			return out, ErrTooLarge
		}
		rgb := image.NewNRGBA(image.Rect(0, 0, width, height))
		alpha := make([]byte, 1+width*height)
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				if x&63 == 0 {
					if e := ctx.Err(); e != nil {
						return out, e
					}
				}
				c := straightNRGBA(m.At(b.Min.X+x, b.Min.Y+y))
				i := y*rgb.Stride + 4*x
				rgb.Pix[i], rgb.Pix[i+1], rgb.Pix[i+2], rgb.Pix[i+3] = c.R, c.G, c.B, 255
				alpha[1+y*width+x] = c.A
			}
		}
		out.ALPH = alpha
		source = rgb
	}
	capture := &singleCapture{}
	if e := encoder.EncodeContext(ctx, capture, source, cfg, simpleCap); e != nil {
		var limit *encoder.OutputLimitError
		if errors.As(e, &limit) {
			return out, outputLimit(requested, effective, -1)
		}
		return out, e
	}
	data := capture.data
	if len(data) < 20 || string(data[:4]) != "RIFF" || string(data[8:16]) != "WEBPVP8 " {
		return out, errors.New("tqwebp: invalid internal VP8 serialization")
	}
	size := int64(binary.LittleEndian.Uint32(data[16:20]))
	if size+20+(size&1) != int64(len(data)) {
		return out, errors.New("tqwebp: invalid internal VP8 length")
	}
	out.VP8 = data[20 : 20+int(size)]
	out.Rect = image.Rect(0, 0, width, height)
	return out, ctx.Err()
}
func muxEncoded(ctx context.Context, w io.Writer, f *container.File, requested, effective int64) error {
	// No hidden container defaults: the root already admitted its policy.
	limits := container.Limits{MaxInputBytes: math.MaxInt64, MaxOutputBytes: effective, MaxChunks: math.MaxInt64, MaxFrames: math.MaxInt64, MaxMetadataBytes: math.MaxInt64, MaxRetainedBytes: math.MaxInt64}
	tracker := &writerErrorTracker{writer: w}
	e := container.Mux(ctx, tracker, f, limits)
	if tracker.err != nil {
		return e
	}
	var limit *container.LimitError
	if errors.As(e, &limit) {
		if limit.Resource == "output_bytes" {
			return outputLimit(requested, effective, limit.Actual)
		}
		return &LimitError{limit.Resource, limit.Limit, limit.Actual}
	}
	return e
}
func encodeTransparentStill(ctx context.Context, w io.Writer, m image.Image, cfg encoder.Config, limits Limits) error {
	b := m.Bounds()
	pixels := int64(b.Dx()) * int64(b.Dy())
	overhead := 18 + chunkSize(1+pixels)
	cap := encodingOutputCap(limits.MaxOutputBytes)
	if cap <= overhead+20 {
		return outputLimit(limits.MaxOutputBytes, cap, -1)
	}
	f, e := encodeFrame(ctx, m, cfg, false, cap-overhead, limits.MaxOutputBytes, cap)
	if e != nil {
		return e
	}
	return muxEncoded(ctx, w, &container.File{Canvas: image.Pt(b.Dx(), b.Dy()), Frames: []container.EncodedFrame{f}}, limits.MaxOutputBytes, cap)
}

// Preserve straight NRGBA64 values before 8-bit reduction, avoiding a lossy
// premultiply/unpremultiply detour (especially at alpha zero).
func straightNRGBA(c color.Color) color.NRGBA {
	switch v := c.(type) {
	case color.NRGBA:
		return v
	case color.NRGBA64:
		return color.NRGBA{R: uint8(v.R >> 8), G: uint8(v.G >> 8), B: uint8(v.B >> 8), A: uint8(v.A >> 8)}
	default:
		return color.NRGBAModel.Convert(c).(color.NRGBA)
	}
}
