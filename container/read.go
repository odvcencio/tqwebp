package container

import (
	"context"
	"errors"
	"image"
	"io"

	"m31labs.dev/tqwebp/internal/webpwire"
)

const maxRIFFSize int64 = webpwire.MaxRIFFSize

// Demux reads one RIFF extent and retains owned copies of bounded compressed
// frames, metadata and unknown chunks, including per-frame unknown subchunks.
// Bytes beyond that extent remain unread. It validates structure and codec
// headers, not entropy coding. Cancellation cannot interrupt blocked caller I/O.
func Demux(ctx context.Context, r io.Reader, limits Limits) (*File, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	l, err := normalize(limits)
	if err != nil {
		return nil, err
	}
	if nilValue(r) {
		return nil, ErrInvalidReader
	}
	c, err := webpwire.Open(ctx, r, webpwire.Policy{Preserve: true, Limits: webpwire.Limits{
		MaxInputBytes: l.MaxInputBytes, MaxChunks: l.MaxChunks, MaxFrames: l.MaxFrames,
		MaxMetadataBytes: l.MaxMetadataBytes, MaxRetainedBytes: l.MaxRetainedBytes,
	}})
	if err != nil {
		return nil, fromWire(err)
	}
	defer c.Close()
	h := c.Header()
	f := &File{Canvas: h.Canvas, Extended: h.Extended, Animated: h.Animated, Alpha: h.AlphaDeclared, LoopCount: h.LoopCount, Background: h.Background}
	for {
		p, e := c.NextFrame()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, fromWire(e)
		}
		frame := EncodedFrame{Duration: p.Duration, Blend: Blend(p.Blend), Dispose: Dispose(p.Dispose), Rect: p.Rect, VP8: p.VP8, VP8L: p.VP8L, ALPH: p.ALPH}
		for _, ch := range p.UnknownChunks {
			frame.UnknownChunks = append(frame.UnknownChunks, Chunk{ch.FourCC, ch.Data})
		}
		f.Frames = append(f.Frames, frame)
	}
	for _, ch := range c.MetadataChunks() {
		f.MetadataChunks = append(f.MetadataChunks, Chunk{ch.FourCC, ch.Data})
	}
	for _, ch := range c.UnknownChunks() {
		f.UnknownChunks = append(f.UnknownChunks, Chunk{ch.FourCC, ch.Data})
	}
	return f, nil
}

func fromWire(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, webpwire.ErrInvalidReader) {
		return ErrInvalidReader
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var limit *webpwire.ResourceError
	if errors.As(err, &limit) {
		return &LimitError{limit.Resource, limit.Limit, limit.Actual}
	}
	var fault *webpwire.Fault
	if errors.As(err, &fault) {
		cause := fault.Err
		switch fault.Kind {
		case webpwire.Invalid:
			cause = errors.Join(ErrInvalidFormat, cause)
		case webpwire.Unsupported:
			cause = errors.Join(ErrUnsupportedFeature, cause)
		}
		return &FormatError{fault.Offset, fault.Chunk, fault.Frame, cause}
	}
	return err
}

func u24(p []byte) uint32   { return webpwire.U24(p) }
func put24(p []byte, n int) { webpwire.Put24(p, n) }
func dimensions(id string, p []byte) (image.Point, bool, error) {
	dim, alpha, err := webpwire.Probe(id, p, int64(len(p)))
	return dim, alpha, fromWire(err)
}
func validateAlpha(p []byte, dim image.Point, writing bool) error {
	return fromWire(webpwire.ValidateAlpha(p, dim, writing))
}
