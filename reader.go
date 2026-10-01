package webp

import (
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"time"

	"m31labs.dev/tqwebp/internal/composite"
	"m31labs.dev/tqwebp/internal/webpwire"
)

var ErrNotComplete = errors.New("tqwebp: stream is not complete")

// Frame is one complete displayed canvas. Reader frames are borrowed/read-only
// until the next Next or Close. DecodeAll frames are independently owned.
type Frame struct {
	Pixels   image.Image
	Duration time.Duration
}

// Document is a single still or one stored animation pass. LoopCount is metadata,
// never expanded into repeated frames. Background is a stored rendering hint.
type Document struct {
	Canvas     image.Point
	Animated   bool
	Frames     []Frame
	LoopCount  uint16
	Background color.NRGBA
	Metadata   Metadata
}

// Features describes declared or fully observed features, not an ICC transform.
type Features struct{ Alpha, ICC, EXIF, XMP bool }

// ReadSummary is available only after all frames and RIFF trailers validate.
// Alpha reports actual decoded frame transparency, not the advisory VP8L hint.
type ReadSummary struct {
	Frames, DecodedPixels int64
	Duration              time.Duration
	Observed              Features
}

// Info contains parsed header controls. Declared is nil without VP8X; Completion
// is nil until Next returns validated EOF. Returned pointers are defensive copies.
type Info struct {
	Canvas     image.Point
	Animated   bool
	LoopCount  uint16
	Background color.NRGBA
	Declared   *Features
	Completion *ReadSummary
}

// Reader consumes one bounded compressed frame at a time, without spooling the
// entire input. It is sequential, not safe for concurrent calls. Close releases
// its storage but never closes or drains the caller's reader.
type Reader struct {
	ctx                context.Context
	cursor             *webpwire.Cursor
	working            *webpwire.Working
	header             webpwire.Header
	canvas             *composite.Canvas
	still              *image.NRGBA
	stillBytes         int64
	fixed              bool
	closed, eof, alpha bool
	terminal           error
	summary            *ReadSummary
}

// NewReader reads control information through the first frame boundary only.
// Cancellation cannot interrupt a blocked caller Read; supply I/O deadlines.
func NewReader(ctx context.Context, input io.Reader, limits ReadLimits) (*Reader, error) {
	if nilInterface(ctx) {
		return nil, ErrInvalidContext
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	l, e := normalizeReadLimits(limits)
	if e != nil {
		return nil, e
	}
	if nilInterface(input) {
		return nil, ErrInvalidReader
	}
	w := &webpwire.Working{Limit: l.MaxWorkingBytes}
	if e = w.Reserve(65536); e != nil {
		return nil, wireError(e)
	}
	policy := webpwire.Policy{Working: w, Limits: webpwire.Limits{MaxInputBytes: l.MaxInputBytes, MaxChunks: 4096, MaxFrames: l.MaxFrames, MaxMetadataBytes: l.MaxMetadataBytes, MaxCanvasPixels: l.MaxCanvasPixels, MaxFramePixels: l.MaxFramePixels, MaxDecodedPixels: l.MaxDecodedPixels, MaxDuration: l.MaxDuration}}
	cursor, e := webpwire.Open(ctx, input, policy)
	if e != nil {
		w.Release(65536)
		return nil, wireError(e)
	}
	h := cursor.Header()
	canvasBytes := int64(h.Canvas.X) * int64(h.Canvas.Y) * 4
	if h.Animated && canvasBytes > int64(int(^uint(0)>>1)) {
		cursor.Close()
		w.Release(65536)
		return nil, &LimitError{"working_bytes", int64(int(^uint(0) >> 1)), canvasBytes}
	}
	return &Reader{ctx: ctx, cursor: cursor, working: w, header: cursor.Header(), fixed: true}, nil
}
func featureBits(b byte) Features {
	return Features{Alpha: b&16 != 0, ICC: b&32 != 0, EXIF: b&8 != 0, XMP: b&4 != 0}
}
func (r *Reader) Info() Info {
	if r == nil {
		return Info{}
	}
	i := Info{Canvas: r.header.Canvas, Animated: r.header.Animated, LoopCount: r.header.LoopCount, Background: r.header.Background}
	if r.header.Extended {
		f := featureBits(r.header.Declared)
		i.Declared = &f
	}
	if r.summary != nil {
		s := *r.summary
		i.Completion = &s
	}
	return i
}
func (r *Reader) reserve(n int64) error {
	if e := r.ctx.Err(); e != nil {
		return e
	}
	if n < 0 || n > int64(int(^uint(0)>>1))-r.working.Used {
		return &LimitError{"working_bytes", r.working.Limit, -1}
	}
	return wireError(r.working.Reserve(n))
}
func (r *Reader) fail(e error) (Frame, error) { r.terminal = e; r.Close(); return Frame{}, e }

// Next returns one displayed canvas. It never expands loops, clamps durations,
// or sleeps. EOF validates the complete RIFF extent, including trailing metadata;
// bytes beyond that extent remain unread. After failure the same error is sticky.
func (r *Reader) Next() (Frame, error) {
	if r == nil {
		return Frame{}, ErrInvalidReader
	}
	if r.terminal != nil {
		return Frame{}, r.terminal
	}
	if r.closed {
		return Frame{}, ErrInvalidReader
	}
	if r.eof {
		return Frame{}, io.EOF
	}
	if e := r.ctx.Err(); e != nil {
		return r.fail(e)
	}
	r.still = nil
	if r.stillBytes != 0 {
		r.working.Release(r.stillBytes)
		r.stillBytes = 0
	}
	encoded, e := r.cursor.NextFrame()
	if e == io.EOF {
		done := r.cursor.Summary()
		if done == nil {
			return r.fail(ErrInvalidFormat)
		}
		observed := featureBits(done.Observed)
		observed.Alpha = r.alpha
		r.summary = &ReadSummary{Frames: done.Frames, DecodedPixels: done.DecodedPixels, Duration: time.Duration(done.DurationMilliseconds) * time.Millisecond, Observed: observed}
		r.eof = true
		return Frame{}, io.EOF
	}
	if e != nil {
		return r.fail(wireError(e))
	}
	owned := encoded.OwnedBytes
	defer func() { encoded = webpwire.Frame{}; r.working.Release(owned) }()
	before := r.working.Used
	pixels, e := decodePixels(r.ctx, encoded.Rect.Dx(), encoded.Rect.Dy(), encoded.VP8, encoded.VP8L, encoded.ALPH, r.reserve)
	lease := r.working.Used - before
	if e != nil {
		pixels = nil
		r.working.Release(lease)
		return r.fail(frameError(e, encoded))
	}
	// All codec scratch becomes unreachable at adapter return. Keep only output
	// backing, while the conservative reservation protected the decode's peak.
	keep := int64(cap(pixels.Pix))
	r.working.Release(lease - keep)
	if !r.header.Animated {
		r.still = pixels
		r.stillBytes = keep
		alpha, e := hasAlpha(r.ctx, pixels)
		if e != nil {
			return r.fail(e)
		}
		r.alpha = r.alpha || alpha
		return Frame{Pixels: pixels}, nil
	}
	defer func() { pixels = nil; r.working.Release(keep) }()
	if r.canvas == nil {
		c, e := composite.New(r.ctx, r.header.Canvas.X, r.header.Canvas.Y, r.reserve)
		if e != nil {
			return r.fail(e)
		}
		r.canvas = c
	}
	out, alpha, e := r.canvas.Apply(r.ctx, composite.Control{Rect: encoded.Rect, Replace: encoded.Blend == 1, Dispose: encoded.Dispose == 1}, pixels)
	if e != nil {
		return r.fail(e)
	}
	r.alpha = r.alpha || alpha
	return Frame{Pixels: out, Duration: encoded.Duration}, nil
}
func hasAlpha(ctx context.Context, m *image.NRGBA) (bool, error) {
	found := false
	for y := 0; y < m.Rect.Dy(); y++ {
		if e := ctx.Err(); e != nil {
			return false, e
		}
		for x := 0; x < m.Rect.Dx(); x++ {
			if m.Pix[y*m.Stride+4*x+3] != 255 {
				found = true
			}
		}
	}
	return found, nil
}

// Metadata returns defensive owned copies only after validated EOF. Copy
// allocation is admitted while library-owned; returned caller-retained storage
// is outside subsequent library-live accounting. Repeated calls do not impose
// a whole-process/RSS cap. After Close the metadata storage is unavailable.
func (r *Reader) Metadata() (Metadata, error) {
	if r == nil {
		return Metadata{}, ErrInvalidReader
	}
	if !r.eof {
		return Metadata{}, ErrNotComplete
	}
	if r.closed {
		return Metadata{}, ErrInvalidReader
	}
	if e := r.ctx.Err(); e != nil {
		return Metadata{}, e
	}
	chunks := r.cursor.MetadataChunks()
	var total int64
	for _, c := range chunks {
		total += int64(len(c.Data))
	}
	if e := r.reserve(total); e != nil {
		return Metadata{}, e
	}
	defer r.working.Release(total)
	var out Metadata
	for _, c := range chunks {
		b := make([]byte, len(c.Data))
		if e := copyContext(r.ctx, b, c.Data); e != nil {
			return Metadata{}, e
		}
		switch c.FourCC {
		case "ICCP":
			out.ICC = b
		case "EXIF":
			out.EXIF = b
		case "XMP ":
			out.XMP = b
		}
	}
	return out, nil
}
func copyContext(ctx context.Context, dst, src []byte) error {
	for len(src) > 0 {
		if e := ctx.Err(); e != nil {
			return e
		}
		n := len(src)
		if n > 4096 {
			n = 4096
		}
		copy(dst[:n], src[:n])
		dst, src = dst[n:], src[n:]
	}
	return ctx.Err()
}

// Close is idempotent. Info retains header/completion facts, but pixels and
// metadata views expire. Early Close is not validation of unread trailers.
func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	if r.closed {
		return nil
	}
	r.closed = true
	if r.cursor != nil {
		r.cursor.Close()
	}
	r.still = nil
	if r.stillBytes != 0 {
		r.working.Release(r.stillBytes)
		r.stillBytes = 0
	}
	if r.canvas != nil {
		n := r.canvas.Bytes()
		r.canvas.Close()
		r.canvas = nil
		r.working.Release(n)
	}
	if r.fixed {
		r.working.Release(65536)
		r.fixed = false
	}
	return nil
}

// DecodeAll retains independently owned displayed canvases and copied metadata
// under one shared working budget. Still VP8L hidden RGB is copied byte-for-byte.
func DecodeAll(ctx context.Context, input io.Reader, limits ReadLimits) (*Document, error) {
	r, e := NewReader(ctx, input, limits)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	i := r.Info()
	doc := &Document{Canvas: i.Canvas, Animated: i.Animated, LoopCount: i.LoopCount, Background: i.Background}
	var owned int64
	defer func() { r.working.Release(owned) }()
	for {
		f, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		pixels := f.Pixels.(*image.NRGBA)
		n := int64(len(pixels.Pix))
		// 128 bytes per retained frame conservatively covers slice growth overlap
		// (Frame <=32 bytes on supported targets), separately from pixel backing.
		if e = r.reserve(n + 128); e != nil {
			return nil, e
		}
		owned += n + 128
		cp := &image.NRGBA{Pix: make([]byte, len(pixels.Pix)), Stride: pixels.Stride, Rect: pixels.Rect}
		if e = copyContext(ctx, cp.Pix, pixels.Pix); e != nil {
			return nil, e
		}
		doc.Frames = append(doc.Frames, Frame{Pixels: cp, Duration: f.Duration})
	}
	m, e := r.Metadata()
	if e != nil {
		return nil, e
	}
	n := int64(len(m.ICC) + len(m.EXIF) + len(m.XMP))
	if e = r.reserve(n); e != nil {
		return nil, e
	}
	owned += n
	doc.Metadata = m
	return doc, nil
}

func wireError(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, webpwire.ErrInvalidReader) {
		return ErrInvalidReader
	}
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		return e
	}
	var resource *webpwire.ResourceError
	if errors.As(e, &resource) {
		return &LimitError{resource.Resource, resource.Limit, resource.Actual}
	}
	var fault *webpwire.Fault
	if errors.As(e, &fault) {
		reason := fault.Err
		switch fault.Kind {
		case webpwire.Invalid:
			reason = errors.Join(ErrInvalidFormat, reason)
		case webpwire.Unsupported:
			reason = errors.Join(ErrUnsupportedFeature, reason)
		}
		return &FormatError{Offset: fault.Offset, Chunk: fault.Chunk, Frame: fault.Frame, Err: reason}
	}
	return e
}
func frameError(e error, f webpwire.Frame) error {
	var fe *FormatError
	if !errors.As(e, &fe) {
		return e
	}
	out := *fe
	out.Frame = f.Index
	switch out.Chunk {
	case "VP8 ":
		out.Offset = f.VP8Offset
	case "VP8L":
		out.Offset = f.VP8LOffset
	case "ALPH":
		out.Offset = f.ALPHOffset
	default:
		out.Offset = f.Offset
	}
	return &out
}
