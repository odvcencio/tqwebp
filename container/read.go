package container

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
)

const maxRIFFSize int64 = 1<<32 - 10

// Demux reads one RIFF extent and retains owned copies of all bounded payloads,
// including unknown chunks. Bytes following the declared RIFF extent are left
// unread. Reserved VP8X bits and extension bytes are ignored per the format.
// Cancellation is cooperative and cannot interrupt a blocking Reader.Read.
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
	if err = check("input_bytes", 12, l.MaxInputBytes); err != nil {
		return nil, err
	}
	var header [12]byte
	if err = read(ctx, r, header[:]); err != nil {
		return nil, format(0, "", err)
	}
	if string(header[:4]) != "RIFF" || string(header[8:]) != "WEBP" {
		return nil, format(0, "", ErrInvalidFormat)
	}
	size := int64(binary.LittleEndian.Uint32(header[4:8]))
	if size < 4 || size > maxRIFFSize || size&1 != 0 {
		return nil, format(4, "RIFF", ErrInvalidFormat)
	}
	if err = check("input_bytes", size+8, l.MaxInputBytes); err != nil {
		return nil, err
	}
	f := &File{}
	var off int64 = 12
	var count, retained, metadata int64
	var flags byte
	var imageSeen, alphaSeen bool
	var alphaOffset int64
	for off < size+8 {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		count++
		if err = check("chunk_count", count, l.MaxChunks); err != nil {
			return nil, err
		}
		if size+8-off < 8 {
			return nil, format(off, "", errors.Join(ErrInvalidFormat, io.ErrUnexpectedEOF))
		}
		var h [8]byte
		if err = read(ctx, r, h[:]); err != nil {
			return nil, format(off, "", err)
		}
		id := string(h[:4])
		n := int64(binary.LittleEndian.Uint32(h[4:]))
		padded := n + n&1
		if padded > size+8-off-8 {
			return nil, format(off, id, errors.Join(ErrInvalidFormat, io.ErrUnexpectedEOF))
		}
		if err = check("retained_bytes", retained+n, l.MaxRetainedBytes); err != nil {
			return nil, err
		}
		if id == "ICCP" || id == "EXIF" || id == "XMP " {
			metadata += n
			if err = check("metadata_bytes", metadata, l.MaxMetadataBytes); err != nil {
				return nil, err
			}
		}
		if n > int64(int(^uint(0)>>1)) {
			return nil, &LimitError{"retained_bytes", int64(int(^uint(0) >> 1)), n}
		}
		data := make([]byte, int(n))
		if err = read(ctx, r, data); err != nil {
			return nil, format(off+8, id, err)
		}
		if n&1 != 0 {
			var pad [1]byte
			if err = read(ctx, r, pad[:]); err != nil {
				return nil, format(off+8+n, id, err)
			}
			if pad[0] != 0 {
				return nil, format(off+8+n, id, ErrInvalidFormat)
			}
		}
		bad := func() (*File, error) { return nil, format(off, id, ErrInvalidFormat) }
		switch id {
		case "VP8X":
			if off != 12 || f.Extended || n < 10 {
				return bad()
			}
			f.Extended = true
			flags = data[0] & 0x3e
			f.Alpha = flags&0x10 != 0
			if flags&2 != 0 {
				return nil, format(off, id, ErrUnsupportedFeature)
			}
			f.Canvas = image.Pt(int(u24(data[4:7]))+1, int(u24(data[7:10]))+1)
			if uint64(f.Canvas.X)*uint64(f.Canvas.Y) > 1<<32-1 {
				return bad()
			}
		case "VP8 ", "VP8L":
			if imageSeen {
				return bad()
			}
			if !f.Extended && off != 12 {
				return bad()
			}
			if id == "VP8L" && alphaSeen {
				return bad()
			}
			if err = check("frames", 1, l.MaxFrames); err != nil {
				return nil, err
			}
			dim, _, e := dimensions(id, data)
			if e != nil {
				return nil, format(off, id, e)
			}
			if f.Extended && dim != f.Canvas {
				return bad()
			}
			f.Canvas = dim
			if len(f.Frames) == 0 {
				f.Frames = []EncodedFrame{{}}
			}
			f.Frames[0].Rect = image.Rectangle{Max: dim}
			if id == "VP8 " {
				f.Frames[0].VP8 = data
			} else {
				f.Frames[0].VP8L = data
			}
			imageSeen = true
		case "ALPH":
			if !f.Extended || imageSeen || alphaSeen || flags&0x10 == 0 {
				return bad()
			}
			alphaSeen = true
			alphaOffset = off
			if len(f.Frames) == 0 {
				f.Frames = []EncodedFrame{{}}
			}
			f.Frames[0].ALPH = data
		case "ICCP", "EXIF", "XMP ":
			if !f.Extended {
				return bad()
			}
			if id == "ICCP" && (imageSeen || alphaSeen) {
				return bad()
			}
			f.MetadataChunks = append(f.MetadataChunks, Chunk{id, data})
		case "ANIM":
			// With animation unset, the specification requires readers to ignore ANIM.
			if !f.Extended {
				return bad()
			}
		case "ANMF":
			return nil, format(off, id, ErrUnsupportedFeature)
		default:
			f.UnknownChunks = append(f.UnknownChunks, Chunk{id, data})
		}
		retained += n
		off += 8 + padded
	}
	if !imageSeen {
		return nil, format(off, "", ErrInvalidFormat)
	}
	if alphaSeen {
		if err = validateAlpha(f.Frames[0].ALPH, f.Canvas, false); err != nil {
			return nil, format(alphaOffset, "ALPH", err)
		}
	}
	var observed byte
	for _, c := range f.MetadataChunks {
		switch c.FourCC {
		case "ICCP":
			observed |= 0x20
		case "EXIF":
			observed |= 8
		case "XMP ":
			observed |= 4
		}
	}
	if f.Extended && flags&0x2c != observed {
		return nil, format(12, "VP8X", ErrInvalidFormat)
	}
	if f.Extended && len(f.Frames[0].VP8) > 0 && (flags&0x10 != 0) != alphaSeen {
		return nil, format(12, "VP8X", ErrInvalidFormat)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return f, nil
}
func read(ctx context.Context, r io.Reader, p []byte) error {
	empty := 0
	for len(p) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := len(p)
		if want > 32768 {
			want = 32768
		}
		n, err := r.Read(p[:want])
		if n < 0 || n > want {
			return io.ErrNoProgress
		}
		p = p[n:]
		if len(p) == 0 {
			if err != nil && err != io.EOF {
				return err
			}
			return ctx.Err()
		}
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
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

func u24(p []byte) uint32   { return uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 }
func put24(p []byte, n int) { p[0] = byte(n); p[1] = byte(n >> 8); p[2] = byte(n >> 16) }
func dimensions(id string, p []byte) (image.Point, bool, error) {
	if id == "VP8L" {
		if len(p) < 5 || p[0] != 0x2f {
			return image.Point{}, false, ErrInvalidFormat
		}
		v := binary.LittleEndian.Uint32(p[1:5])
		if v>>29 != 0 {
			return image.Point{}, false, ErrUnsupportedFeature
		}
		return image.Pt(int(v&0x3fff)+1, int(v>>14&0x3fff)+1), v&(1<<28) != 0, nil
	}
	if len(p) < 10 || p[0]&1 != 0 || p[0]&0x10 == 0 || p[0]>>1&7 > 3 || string(p[3:6]) != "\x9d\x01\x2a" {
		return image.Point{}, false, ErrInvalidFormat
	}
	dim := image.Pt(int(binary.LittleEndian.Uint16(p[6:8])&0x3fff), int(binary.LittleEndian.Uint16(p[8:10])&0x3fff))
	if dim.X == 0 || dim.Y == 0 {
		return image.Point{}, false, ErrInvalidFormat
	}
	partition := int(p[0])>>5 | int(p[1])<<3 | int(p[2])<<11
	if partition > len(p)-10 {
		return image.Point{}, false, ErrInvalidFormat
	}
	return dim, false, nil
}
func validateAlpha(p []byte, dim image.Point, writing bool) error {
	if len(p) < 1 {
		return ErrInvalidFormat
	}
	if p[0]&3 > 1 || p[0]>>4&3 > 1 {
		return ErrUnsupportedFeature
	}
	if writing && p[0]&0xc0 != 0 {
		return ErrInvalidFormat
	}
	if p[0]&3 == 0 && uint64(len(p)-1) != uint64(dim.X)*uint64(dim.Y) {
		return ErrInvalidFormat
	}
	if p[0]&3 == 1 && len(p) < 2 {
		return ErrInvalidFormat
	}
	return nil
}
