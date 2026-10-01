package container

import (
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"io"
)

// Mux validates and buffers a canonical still container before the first write.
// Invalid input, cancellation during preparation and policy refusals write no
// caller bytes. Writer errors are unchanged; short writes return io.ErrShortWrite.
// Cancellation or I/O failure after output begins may leave partial output.
// Payloads and duplicate metadata survive unchanged; order/padding may normalize.
func Mux(ctx context.Context, w io.Writer, f *File, limits Limits) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	l, err := normalize(limits)
	if err != nil {
		return err
	}
	if nilValue(w) {
		return ErrInvalidWriter
	}
	if f == nil {
		return ErrInvalidFormat
	}
	if err = check("frames", int64(len(f.Frames)), l.MaxFrames); err != nil {
		return err
	}
	if f.Animated || f.LoopCount != 0 || f.Background != (color.NRGBA{}) {
		return format(0, "ANIM", ErrUnsupportedFeature)
	}
	if len(f.Frames) != 1 {
		return format(0, "", ErrUnsupportedFeature)
	}
	frame := f.Frames[0]
	if frame.Blend > BlendReplace || frame.Dispose > DisposeBackground {
		return format(0, "ANMF", ErrInvalidFormat)
	}
	if frame.Duration != 0 || frame.Blend != BlendOver || frame.Dispose != DisposeNone {
		return format(0, "ANMF", ErrUnsupportedFeature)
	}
	id := "VP8 "
	payload := frame.VP8
	if (frame.VP8 == nil) == (frame.VP8L == nil) {
		return format(0, "", ErrInvalidFormat)
	}
	if frame.VP8L != nil {
		id = "VP8L"
		payload = frame.VP8L
		if frame.ALPH != nil {
			return format(0, "ALPH", ErrInvalidFormat)
		}
	}
	dim, alpha, err := dimensions(id, payload)
	if err != nil {
		return format(0, id, err)
	}
	if dim != f.Canvas || frame.Rect != (image.Rectangle{Max: dim}) {
		return format(0, id, ErrInvalidFormat)
	}
	if frame.ALPH != nil {
		if err = validateAlpha(frame.ALPH, dim, true); err != nil {
			return format(0, "ALPH", err)
		}
		alpha = true
	}
	if id == "VP8 " && f.Alpha && frame.ALPH == nil {
		return format(0, "ALPH", ErrInvalidFormat)
	}
	// Count caller-provided entries before constructing an internal chunk list.
	count := int64(len(f.MetadataChunks)) + int64(len(f.UnknownChunks)) + 1
	extended := f.Extended || len(f.MetadataChunks) > 0 || frame.ALPH != nil || f.Alpha || len(f.UnknownChunks) > 0
	if extended {
		count++
	}
	if frame.ALPH != nil {
		count++
	}
	if err = check("chunk_count", count, l.MaxChunks); err != nil {
		return err
	}
	if count > (maxRIFFSize-4)/8 {
		return format(0, "RIFF", ErrInvalidFormat)
	}
	if err = check("output_bytes", 12+count*8, l.MaxOutputBytes); err != nil {
		return err
	}
	var metadata, retained int64
	var flags byte
	if alpha || f.Alpha {
		flags |= 0x10
	}
	for _, c := range f.MetadataChunks {
		if err = ctx.Err(); err != nil {
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
	size := int64(12)
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
	if extended {
		if err = add("VP8X", 10); err != nil {
			return err
		}
	}
	if err = add(id, int64(len(payload))); err != nil {
		return err
	}
	if frame.ALPH != nil {
		if err = add("ALPH", int64(len(frame.ALPH))); err != nil {
			return err
		}
	}
	for _, c := range f.MetadataChunks {
		if err = add(c.FourCC, int64(len(c.Data))); err != nil {
			return err
		}
	}
	for _, c := range f.UnknownChunks {
		if len(c.FourCC) != 4 || known(c.FourCC) {
			return format(0, c.FourCC, ErrInvalidFormat)
		}
		if err = add(c.FourCC, int64(len(c.Data))); err != nil {
			return err
		}
	}
	if size > int64(int(^uint(0)>>1)) {
		return &LimitError{"output_bytes", int64(int(^uint(0) >> 1)), size}
	}
	chunks := make([]Chunk, 0, int(count))
	if extended {
		vp8x := make([]byte, 10)
		vp8x[0] = flags
		put24(vp8x[4:7], dim.X-1)
		put24(vp8x[7:10], dim.Y-1)
		chunks = append(chunks, Chunk{"VP8X", vp8x})
	}
	for _, c := range f.MetadataChunks {
		if err = ctx.Err(); err != nil {
			return err
		}
		if c.FourCC == "ICCP" {
			chunks = append(chunks, c)
		}
	}
	if frame.ALPH != nil {
		chunks = append(chunks, Chunk{"ALPH", frame.ALPH})
	}
	chunks = append(chunks, Chunk{id, payload})
	for _, c := range f.MetadataChunks {
		if err = ctx.Err(); err != nil {
			return err
		}
		if c.FourCC != "ICCP" {
			chunks = append(chunks, c)
		}
	}
	for _, c := range f.UnknownChunks {
		if err = ctx.Err(); err != nil {
			return err
		}
		if len(c.FourCC) != 4 || known(c.FourCC) {
			return format(0, c.FourCC, ErrInvalidFormat)
		}
		chunks = append(chunks, c)
	}
	out := make([]byte, int(size))
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(size-8))
	copy(out[8:12], "WEBP")
	off := 12
	for _, c := range chunks {
		if err = ctx.Err(); err != nil {
			return err
		}
		copy(out[off:off+4], c.FourCC)
		binary.LittleEndian.PutUint32(out[off+4:off+8], uint32(len(c.Data)))
		off += 8
		p := c.Data
		for len(p) > 0 {
			if err = ctx.Err(); err != nil {
				return err
			}
			n := len(p)
			if n > 32768 {
				n = 32768
			}
			copy(out[off:off+n], p[:n])
			p = p[n:]
			off += n
		}
		off += len(c.Data) & 1
	}
	for len(out) > 0 {
		if err = ctx.Err(); err != nil {
			return err
		}
		n := len(out)
		if n > 32768 {
			n = 32768
		}
		written, e := w.Write(out[:n])
		if e != nil {
			return e
		}
		if written != n {
			return io.ErrShortWrite
		}
		out = out[n:]
	}
	return ctx.Err()
}
