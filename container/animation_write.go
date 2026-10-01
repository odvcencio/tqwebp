package container

import (
	"context"
	"encoding/binary"
	"image"
	"io"
	"time"
)

func frameFormat(index int, id string, err error) error {
	return &FormatError{Offset: 0, Chunk: id, Frame: index, Err: err}
}

// muxAnimated performs all structural and output-budget admission before
// allocating the final serialization buffer. ANMFs are written into that buffer
// directly, never into a second enclosing frame allocation.
func muxAnimated(ctx context.Context, w io.Writer, f *File, l Limits) error {
	if len(f.Frames) == 0 {
		return format(0, "ANMF", ErrInvalidFormat)
	}
	cw, ch := int64(f.Canvas.X), int64(f.Canvas.Y)
	if cw <= 0 || ch <= 0 || cw > 1<<24 || ch > 1<<24 || cw*ch > 1<<32-1 {
		return format(0, "VP8X", ErrInvalidFormat)
	}
	count := int64(2) + int64(len(f.MetadataChunks)) + int64(len(f.UnknownChunks))
	if err := check("chunk_count", count, l.MaxChunks); err != nil {
		return err
	}
	var flags byte = 2
	if f.Alpha {
		flags |= 0x10
	}
	var metadata int64
	for _, c := range f.MetadataChunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch c.FourCC {
		case "ICCP":
			flags |= 0x20
		case "EXIF":
			flags |= 8
		case "XMP ":
			flags |= 4
		default:
			return format(0, c.FourCC, ErrInvalidMetadata)
		}
		if int64(len(c.Data)) > l.MaxMetadataBytes-metadata {
			return &LimitError{"metadata_bytes", l.MaxMetadataBytes, metadata + int64(len(c.Data))}
		}
		metadata += int64(len(c.Data))
	}
	validUnknown := func(c Chunk) error {
		if len(c.FourCC) != 4 || known(c.FourCC) {
			return ErrInvalidFormat
		}
		return nil
	}
	sizes := make([]int64, len(f.Frames))
	var sawLossless, sawAlpha bool
	for i, frame := range f.Frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		if frame.Blend > BlendReplace || frame.Dispose > DisposeBackground {
			return frameFormat(i, "ANMF", ErrInvalidFormat)
		}
		r := frame.Rect
		if r.Min.X < 0 || r.Min.Y < 0 || r.Min.X&1 != 0 || r.Min.Y&1 != 0 || r.Max.X <= r.Min.X || r.Max.Y <= r.Min.Y || int64(r.Max.X) > cw || int64(r.Max.Y) > ch {
			return frameFormat(i, "ANMF", ErrInvalidFormat)
		}
		if frame.Duration < 0 || frame.Duration%time.Millisecond != 0 || frame.Duration/time.Millisecond > 0xffffff {
			return frameFormat(i, "ANMF", ErrInvalidFormat)
		}
		if (frame.VP8 == nil) == (frame.VP8L == nil) {
			return frameFormat(i, "ANMF", ErrInvalidFormat)
		}
		id, payload := "VP8 ", frame.VP8
		if frame.VP8L != nil {
			id, payload = "VP8L", frame.VP8L
			sawLossless = true
			if frame.ALPH != nil {
				return frameFormat(i, "ALPH", ErrInvalidFormat)
			}
		}
		dim, alpha, err := dimensions(id, payload)
		if err != nil {
			return frameFormat(i, id, err)
		}
		if dim != image.Pt(r.Dx(), r.Dy()) {
			return frameFormat(i, id, ErrInvalidFormat)
		}
		if alpha {
			flags |= 0x10
		}
		if frame.ALPH != nil {
			if err = validateAlpha(frame.ALPH, dim, true); err != nil {
				return frameFormat(i, "ALPH", err)
			}
			flags |= 0x10
			sawAlpha = true
		}
		count += 2 + int64(len(frame.UnknownChunks)) // outer ANMF and codec
		if frame.ALPH != nil {
			count++
		}
		if err = check("chunk_count", count, l.MaxChunks); err != nil {
			return err
		}
		n := int64(16)
		add := func(size int) error {
			v := int64(size)
			if v > maxRIFFSize || n > maxRIFFSize-8-v-(v&1) {
				return frameFormat(i, "ANMF", ErrInvalidFormat)
			}
			n += 8 + v + (v & 1)
			return nil
		}
		if err = add(len(payload)); err != nil {
			return err
		}
		if frame.ALPH != nil {
			if err = add(len(frame.ALPH)); err != nil {
				return err
			}
		}
		for _, c := range frame.UnknownChunks {
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = validUnknown(c); err != nil {
				return frameFormat(i, c.FourCC, err)
			}
			if err = add(len(c.Data)); err != nil {
				return err
			}
		}
		sizes[i] = n
	}
	if f.Alpha && !sawLossless && !sawAlpha {
		return format(0, "VP8X", ErrInvalidFormat)
	}
	size, retained := int64(12), int64(0)
	add := func(id string, n int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n > maxRIFFSize || size > maxRIFFSize+8-8-n-(n&1) {
			return format(0, id, ErrInvalidFormat)
		}
		size += 8 + n + (n & 1)
		if n > l.MaxRetainedBytes-retained {
			return &LimitError{"retained_bytes", l.MaxRetainedBytes, retained + n}
		}
		retained += n
		return check("output_bytes", size, l.MaxOutputBytes)
	}
	if err := add("VP8X", 10); err != nil {
		return err
	}
	if err := add("ANIM", 6); err != nil {
		return err
	}
	for _, n := range sizes {
		if err := add("ANMF", n); err != nil {
			return err
		}
	}
	for _, c := range f.MetadataChunks {
		if err := add(c.FourCC, int64(len(c.Data))); err != nil {
			return err
		}
	}
	for _, c := range f.UnknownChunks {
		if err := validUnknown(c); err != nil {
			return format(0, c.FourCC, err)
		}
		if err := add(c.FourCC, int64(len(c.Data))); err != nil {
			return err
		}
	}
	if size > int64(int(^uint(0)>>1)) {
		return &LimitError{"output_bytes", int64(int(^uint(0) >> 1)), size}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	out := make([]byte, int(size))
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(size-8))
	copy(out[8:12], "WEBP")
	off := 12
	header := func(id string, n int64) {
		copy(out[off:off+4], id)
		binary.LittleEndian.PutUint32(out[off+4:off+8], uint32(n))
		off += 8
	}
	emit := func(id string, p []byte) error {
		header(id, int64(len(p)))
		for len(p) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			n := len(p)
			if n > 32768 {
				n = 32768
			}
			copy(out[off:off+n], p[:n])
			off += n
			p = p[n:]
		}
		if off&1 != 0 {
			off++
		}
		return nil
	}
	var x [10]byte
	x[0] = flags
	put24(x[4:7], f.Canvas.X-1)
	put24(x[7:10], f.Canvas.Y-1)
	if err := emit("VP8X", x[:]); err != nil {
		return err
	}
	for _, c := range f.MetadataChunks {
		if c.FourCC == "ICCP" {
			if err := emit(c.FourCC, c.Data); err != nil {
				return err
			}
		}
	}
	var anim [6]byte
	anim[0], anim[1], anim[2], anim[3] = f.Background.B, f.Background.G, f.Background.R, f.Background.A
	binary.LittleEndian.PutUint16(anim[4:], f.LoopCount)
	if err := emit("ANIM", anim[:]); err != nil {
		return err
	}
	for i, frame := range f.Frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		header("ANMF", sizes[i])
		var a [16]byte
		put24(a[0:3], frame.Rect.Min.X/2)
		put24(a[3:6], frame.Rect.Min.Y/2)
		put24(a[6:9], frame.Rect.Dx()-1)
		put24(a[9:12], frame.Rect.Dy()-1)
		put24(a[12:15], int(frame.Duration/time.Millisecond))
		a[15] = byte(frame.Blend)<<1 | byte(frame.Dispose)
		copy(out[off:off+16], a[:])
		off += 16
		if frame.ALPH != nil {
			if err := emit("ALPH", frame.ALPH); err != nil {
				return err
			}
		}
		if frame.VP8L != nil {
			if err := emit("VP8L", frame.VP8L); err != nil {
				return err
			}
		} else {
			if err := emit("VP8 ", frame.VP8); err != nil {
				return err
			}
		}
		for _, c := range frame.UnknownChunks {
			if err := emit(c.FourCC, c.Data); err != nil {
				return err
			}
		}
	}
	for _, c := range f.MetadataChunks {
		if c.FourCC != "ICCP" {
			if err := emit(c.FourCC, c.Data); err != nil {
				return err
			}
		}
	}
	for _, c := range f.UnknownChunks {
		if err := emit(c.FourCC, c.Data); err != nil {
			return err
		}
	}
	for len(out) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := len(out)
		if n > 32768 {
			n = 32768
		}
		written, err := w.Write(out[:n])
		if err != nil {
			return err
		}
		if written != n {
			return io.ErrShortWrite
		}
		out = out[n:]
	}
	return ctx.Err()
}
