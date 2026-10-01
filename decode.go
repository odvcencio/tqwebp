package webp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"time"

	"m31labs.dev/tqwebp/container"
	vp8 "m31labs.dev/tqwebp/internal/vp8decode"
)

var (
	ErrInvalidReader      = container.ErrInvalidReader
	ErrInvalidFormat      = container.ErrInvalidFormat
	ErrUnsupportedFeature = container.ErrUnsupportedFeature
	ErrAnimatedImage      = errors.New("tqwebp: animated image; composed animation decoding is not yet implemented")
)

// FormatError locates a malformed or unsupported encoded structure.
type FormatError = container.FormatError

// ReadLimits bounds decoding. Zero selects DefaultReadLimits; negatives are
// invalid. MaxWorkingBytes covers library-managed live backing allocations,
// including compressed data and output, not caller storage, runtime overhead or
// process RSS. No limit interrupts blocked caller I/O. Animation-related fields
// are reserved for the required future animation decoder.
type ReadLimits struct {
	MaxInputBytes, MaxCanvasPixels, MaxFramePixels, MaxFrames int64
	MaxDecodedPixels, MaxMetadataBytes, MaxWorkingBytes       int64
	MaxDuration                                               time.Duration
}

func DefaultReadLimits() ReadLimits {
	return ReadLimits{64 << 20, 16_000_000, 16_000_000, 1000, 256_000_000, 4 << 20, 256 << 20, 10 * time.Minute}
}
func normalizeReadLimits(l ReadLimits) (ReadLimits, error) {
	d := DefaultReadLimits()
	p := []*int64{&l.MaxInputBytes, &l.MaxCanvasPixels, &l.MaxFramePixels, &l.MaxFrames, &l.MaxDecodedPixels, &l.MaxMetadataBytes, &l.MaxWorkingBytes}
	v := []int64{d.MaxInputBytes, d.MaxCanvasPixels, d.MaxFramePixels, d.MaxFrames, d.MaxDecodedPixels, d.MaxMetadataBytes, d.MaxWorkingBytes}
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
func readCheck(resource string, n, limit int64) error {
	if n > limit {
		return &LimitError{resource, limit, n}
	}
	return nil
}
func decodeError(off int64, id string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &FormatError{Offset: off, Chunk: id, Frame: -1, Err: errors.Join(ErrInvalidFormat, err)}
}
func readFailure(off int64, id string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return decodeError(off, id, err)
	}
	return &FormatError{Offset: off, Chunk: id, Frame: -1, Err: err}
}
func readContext(ctx context.Context, r io.Reader, p []byte) error {
	empty, total := 0, 0
	for len(p) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := len(p)
		if size > 4096 {
			size = 4096
		}
		n, err := r.Read(p[:size])
		if n < 0 || n > size {
			return ErrInvalidReader
		}
		if cancel := ctx.Err(); cancel != nil {
			return cancel
		}
		p = p[n:]
		total += n
		if err != nil && err != io.EOF {
			return err
		}
		if len(p) == 0 {
			return nil
		}
		if err != nil {
			if err == io.EOF && total > 0 {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		if n == 0 {
			empty++
			if empty >= 100 {
				return io.ErrNoProgress
			}
		} else {
			empty = 0
		}
	}
	return ctx.Err()
}

func riffHeader(ctx context.Context, r io.Reader, l ReadLimits) ([12]byte, int64, error) {
	var h [12]byte
	if err := readCheck("input_bytes", 12, l.MaxInputBytes); err != nil {
		return h, 0, err
	}
	if err := readContext(ctx, r, h[:]); err != nil {
		return h, 0, readFailure(0, "", err)
	}
	n := int64(binary.LittleEndian.Uint32(h[4:8])) + 8
	if string(h[:4]) != "RIFF" || string(h[8:]) != "WEBP" || n < 12 || n > 1<<32-2 || n&1 != 0 {
		return h, 0, decodeError(0, "RIFF", ErrInvalidFormat)
	}
	if err := readCheck("input_bytes", n, l.MaxInputBytes); err != nil {
		return h, 0, err
	}
	return h, n, nil
}

// Decode decodes a VP8 still, including raw ALPH with all four filters, to
// independently owned NRGBA pixels. It uses limited-range BT.601 and nearest
// 4:2:0 chroma, not libwebp's default fancy upsampling. VP8L and compressed ALPH
// are not yet supported. Animation returns ErrAnimatedImage, never frame zero.
// Metadata is discarded; container.Demux provides byte-preserving metadata.
func Decode(r io.Reader) (image.Image, error) {
	return DecodeContext(context.Background(), r, ReadLimits{})
}

// DecodeContext is Decode with cooperative cancellation and explicit limits.
// It buffers one bounded RIFF extent and leaves trailing bytes unread. CPU
// cancellation is checked per VP8 macroblock and conversion/alpha row. A
// blocking Reader.Read cannot be interrupted; supply deadline-aware I/O.
func DecodeContext(ctx context.Context, r io.Reader, limits ReadLimits) (image.Image, error) {
	if nilInterface(ctx) {
		return nil, ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, err := normalizeReadLimits(limits)
	if err != nil {
		return nil, err
	}
	if nilInterface(r) {
		return nil, ErrInvalidReader
	}
	h, n, err := riffHeader(ctx, r, l)
	if err != nil {
		return nil, err
	}
	// Fixed slack covers decoder state, descriptors for the header phase and
	// bounded I/O scratch. The complete allocation audit is in docs/decoding.md.
	if err = readCheck("working_bytes", n+65536, l.MaxWorkingBytes); err != nil {
		return nil, err
	}
	if n > int64(int(^uint(0)>>1)) {
		return nil, &LimitError{"working_bytes", int64(int(^uint(0) >> 1)), n}
	}
	data := make([]byte, int(n))
	copy(data, h[:])
	if err = readContext(ctx, r, data[12:]); err != nil {
		return nil, readFailure(12, "", err)
	}
	// Preflight every chunk without allocating. Count both retained payload and
	// conservative descriptor capacity before invoking container.Demux.
	var chunks, payload int64
	for off := int64(12); off < n; {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if n-off < 8 {
			return nil, decodeError(off, "", io.ErrUnexpectedEOF)
		}
		id := string(data[off : off+4])
		size := int64(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		end := off + 8 + size + (size & 1)
		if end > n {
			return nil, decodeError(off, id, io.ErrUnexpectedEOF)
		}
		chunks++
		payload += size
		if chunks > 4096 {
			return nil, &LimitError{"chunk_count", 4096, chunks}
		}
		if id == "VP8X" && size >= 10 && data[off+8]&2 != 0 {
			return nil, ErrAnimatedImage
		}
		off = end
	}
	base := n + payload + 256*(chunks+1) + 65536
	if err = readCheck("working_bytes", base, l.MaxWorkingBytes); err != nil {
		return nil, err
	}
	f, err := container.Demux(ctx, bytes.NewReader(data), container.Limits{MaxInputBytes: l.MaxInputBytes, MaxMetadataBytes: l.MaxMetadataBytes, MaxRetainedBytes: payload + 1})
	if err != nil {
		var le *container.LimitError
		if errors.As(err, &le) {
			return nil, &LimitError{le.Resource, le.Limit, le.Actual}
		}
		return nil, err
	}
	w, ht := int64(f.Canvas.X), int64(f.Canvas.Y)
	pixels := w * ht
	for _, v := range []struct {
		key string
		max int64
	}{{"canvas_pixels", l.MaxCanvasPixels}, {"frame_pixels", l.MaxFramePixels}, {"decoded_pixels", l.MaxDecodedPixels}} {
		if err = readCheck(v.key, pixels, v.max); err != nil {
			return nil, err
		}
	}
	frame := f.Frames[0]
	if len(frame.VP8L) > 0 {
		return nil, ErrUnsupportedFeature
	}
	if len(frame.ALPH) > 0 && frame.ALPH[0]&3 != 0 {
		return nil, ErrUnsupportedFeature
	}
	// 384 bytes/MB for padded YUV, four filter bytes/MB, six predictor
	// bytes/column, <= compressed payload bytes for partition copies, output.
	mw, mh := (w+15)/16, (ht+15)/16
	working := base + 388*mw*mh + 6*mw + int64(len(frame.VP8)) + 4*pixels
	if err = readCheck("working_bytes", working, l.MaxWorkingBytes); err != nil {
		return nil, err
	}
	if working > int64(int(^uint(0)>>1)) {
		return nil, &LimitError{"working_bytes", int64(int(^uint(0) >> 1)), working}
	}
	d := vp8.NewDecoder()
	d.Init(bytes.NewReader(frame.VP8), len(frame.VP8))
	fh, err := d.DecodeFrameHeader()
	if err != nil {
		return nil, decodeError(-1, "VP8 ", err)
	}
	if !fh.KeyFrame || !fh.ShowFrame || int64(fh.Width) != w || int64(fh.Height) != ht {
		return nil, decodeError(-1, "VP8 ", ErrInvalidFormat)
	}
	yuv, err := d.DecodeFrame(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, decodeError(-1, "VP8 ", err)
	}
	out := image.NewNRGBA(image.Rect(0, 0, int(w), int(ht)))
	for y := 0; y < int(ht); y++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < int(w); x++ {
			yy := int(yuv.Y[y*yuv.YStride+x])
			uv := (y/2)*yuv.CStride + x/2
			rr, gg, bb := limitedRGB(yy, int(yuv.Cb[uv]), int(yuv.Cr[uv]))
			i := y*out.Stride + 4*x
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = rr, gg, bb, 255
		}
	}
	if len(frame.ALPH) > 0 {
		if err = decodeRawAlpha(ctx, out, frame.ALPH); err != nil {
			return nil, err
		}
	}
	return out, ctx.Err()
}

// Copyright 2010 Google Inc. All Rights Reserved.
// This conversion follows libwebp src/dsp/yuv.h, BSD-3-Clause; the retained
// copyright/license is in third_party/libwebp-COPYING.
// libwebp's limited-range BT.601 rounding: truncate each product to six
// fractional bits, then clip and shift. See third_party/libwebp-COPYING.
func limitedRGB(y, u, v int) (uint8, uint8, uint8) {
	clip := func(x int) uint8 {
		if x < 0 {
			return 0
		}
		if x > 255 {
			return 255
		}
		return uint8(x)
	}
	return clip(((19077 * y >> 8) + (26149 * v >> 8) - 14234) >> 6), clip(((19077 * y >> 8) - (6419 * u >> 8) - (13320 * v >> 8) + 8708) >> 6), clip(((19077 * y >> 8) + (33050 * u >> 8) - 17685) >> 6)
}
func decodeRawAlpha(ctx context.Context, out *image.NRGBA, a []byte) error {
	w, h := out.Rect.Dx(), out.Rect.Dy()
	filter := (a[0] >> 2) & 3
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := 0; x < w; x++ {
			pred := 0
			if y == 0 {
				if x > 0 && filter != 0 {
					pred = int(out.Pix[y*out.Stride+4*(x-1)+3])
				}
			} else if x == 0 {
				if filter != 0 {
					pred = int(out.Pix[(y-1)*out.Stride+3])
				}
			} else {
				left, top, tl := int(out.Pix[y*out.Stride+4*(x-1)+3]), int(out.Pix[(y-1)*out.Stride+4*x+3]), int(out.Pix[(y-1)*out.Stride+4*(x-1)+3])
				switch filter {
				case 1:
					pred = left
				case 2:
					pred = top
				case 3:
					pred = left + top - tl
					if pred < 0 {
						pred = 0
					}
					if pred > 255 {
						pred = 255
					}
				}
			}
			out.Pix[y*out.Stride+4*x+3] = byte(int(a[1+y*w+x]) + pred)
		}
	}
	return nil
}

// DecodeConfig inspects bounded headers, not entropy data or whole-file validity.
// It can report VP8L and animated canvas dimensions even though Decode rejects
// these forms in this tranche. Its color model is NRGBA, with no ICC transform.
func DecodeConfig(r io.Reader) (image.Config, error) {
	ctx := context.Background()
	l := DefaultReadLimits()
	var result image.Config
	if nilInterface(r) {
		return result, ErrInvalidReader
	}
	_, n, err := riffHeader(ctx, r, l)
	if err != nil {
		return result, err
	}
	var ch [8]byte
	var b [10]byte
	if n < 20 {
		return result, decodeError(12, "", io.ErrUnexpectedEOF)
	}
	if err = readContext(ctx, r, ch[:]); err != nil {
		return result, readFailure(12, "", err)
	}
	id := string(ch[:4])
	size := int64(binary.LittleEndian.Uint32(ch[4:]))
	need := 0
	switch id {
	case "VP8X", "VP8 ":
		need = 10
	case "VP8L":
		need = 5
	default:
		return result, decodeError(12, id, ErrInvalidFormat)
	}
	if size < int64(need) || 20+size+(size&1) > n {
		return result, decodeError(12, id, io.ErrUnexpectedEOF)
	}
	if err = readContext(ctx, r, b[:need]); err != nil {
		return result, readFailure(20, id, err)
	}
	var w, h int64
	switch id {
	case "VP8X":
		w = 1 + int64(b[4]) + (int64(b[5]) << 8) + (int64(b[6]) << 16)
		h = 1 + int64(b[7]) + (int64(b[8]) << 8) + (int64(b[9]) << 16)
	case "VP8 ":
		if b[0]&1 != 0 || b[0]&0x10 == 0 || b[0]>>1&7 > 3 || string(b[3:6]) != "\x9d\x01\x2a" {
			return result, decodeError(20, id, ErrInvalidFormat)
		}
		w = int64(binary.LittleEndian.Uint16(b[6:8]) & 0x3fff)
		h = int64(binary.LittleEndian.Uint16(b[8:10]) & 0x3fff)
	case "VP8L":
		if b[0] != 0x2f || b[4]>>5 != 0 {
			return result, decodeError(20, id, ErrInvalidFormat)
		}
		v := binary.LittleEndian.Uint32(b[1:5])
		w = int64(v&0x3fff) + 1
		h = int64((v>>14)&0x3fff) + 1
	}
	if w == 0 || h == 0 || w*h > 1<<32-1 {
		return result, decodeError(20, id, ErrInvalidFormat)
	}
	if err = readCheck("canvas_pixels", w*h, l.MaxCanvasPixels); err != nil {
		return result, err
	}
	return image.Config{ColorModel: color.NRGBAModel, Width: int(w), Height: int(h)}, nil
}
