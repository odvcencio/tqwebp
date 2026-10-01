package cli

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	webp "m31labs.dev/tqwebp"
	"strconv"
)

func invalidMetadata(why string) error {
	return fmt.Errorf("%w: %s; choose --metadata=none or --ignore-orientation where appropriate", webp.ErrInvalidMetadata, why)
}
func orientation(exif []byte) (int, error) {
	if len(exif) == 0 {
		return 1, nil
	}
	if bytes.HasPrefix(exif, []byte("Exif\x00\x00")) {
		exif = exif[6:]
	}
	if len(exif) < 8 {
		return 0, invalidMetadata("short TIFF header")
	}
	var order binary.ByteOrder
	switch string(exif[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, invalidMetadata("invalid TIFF byte order")
	}
	if order.Uint16(exif[2:4]) != 42 {
		return 0, invalidMetadata("invalid TIFF marker")
	}
	offset := uint64(order.Uint32(exif[4:8]))
	if offset == 0 {
		return 1, nil
	}
	if offset < 8 {
		return 0, invalidMetadata("TIFF IFD overlaps header")
	}
	if offset > uint64(len(exif))-2 {
		return 0, invalidMetadata("TIFF IFD offset out of range")
	}
	count := uint64(order.Uint16(exif[offset : offset+2]))
	if count > (uint64(len(exif))-offset-2)/12 || offset+2+count*12+4 > uint64(len(exif)) {
		return 0, invalidMetadata("TIFF IFD entries out of range")
	}
	found := false
	value := 1
	for i := uint64(0); i < count; i++ {
		entry := exif[offset+2+i*12 : offset+14+i*12]
		if order.Uint16(entry[:2]) != 0x112 {
			continue
		}
		if found || order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			return 0, invalidMetadata("unsupported or duplicate orientation field")
		}
		value = int(order.Uint16(entry[8:10]))
		if value < 1 || value > 8 {
			return 0, invalidMetadata("orientation outside1..8")
		}
		found = true
	}
	return value, nil
}
func validRGBProfile(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if len(b) < 132 || uint64(binary.BigEndian.Uint32(b[:4])) != uint64(len(b)) || string(b[36:40]) != "acsp" {
		return invalidMetadata("invalid ICC structure")
	}
	if string(b[16:20]) != "RGB " {
		return invalidMetadata("ICC is not associated with output RGB channels; profile conversion is unavailable")
	}
	count := uint64(binary.BigEndian.Uint32(b[128:132]))
	if count > 4096 {
		return &webp.LimitError{Resource: "metadata_tags", Limit: 4096, Actual: int64(count)}
	}
	if count > (uint64(len(b))-132)/12 {
		return invalidMetadata("ICC tag table exceeds payload")
	}
	for i := uint64(0); i < count; i++ {
		p := b[132+i*12 : 144+i*12]
		off, n := uint64(binary.BigEndian.Uint32(p[4:8])), uint64(binary.BigEndian.Uint32(p[8:12]))
		if off > uint64(len(b)) || n > uint64(len(b))-off {
			return invalidMetadata("ICC tag range exceeds payload")
		}
	}
	return nil
}
func parseBackground(s string) (color.NRGBA, error) {
	if len(s) != 7 || s[0] != '#' {
		return color.NRGBA{}, errors.New("background must be #RRGGBB")
	}
	n, e := strconv.ParseUint(s[1:], 16, 24)
	if e != nil {
		return color.NRGBA{}, errors.New("background must be #RRGGBB")
	}
	return color.NRGBA{R: byte(n >> 16), G: byte(n >> 8), B: byte(n), A: 255}, nil
}
func straight(c color.Color) color.NRGBA {
	switch v := c.(type) {
	case color.NRGBA:
		return v
	case color.NRGBA64:
		return color.NRGBA{R: byte(v.R >> 8), G: byte(v.G >> 8), B: byte(v.B >> 8), A: byte(v.A >> 8)}
	}
	return color.NRGBAModel.Convert(c).(color.NRGBA)
}
func orient(ctx context.Context, src image.Image, orientation int) (image.Image, error) {
	if orientation == 1 {
		return src, nil
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	out := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x&255 == 0 {
				if e := ctx.Err(); e != nil {
					return nil, e
				}
			}
			dx, dy := x, y
			switch orientation {
			case 2:
				dx = w - 1 - x
			case 3:
				dx = w - 1 - x
				dy = h - 1 - y
			case 4:
				dy = h - 1 - y
			case 5:
				dx = y
				dy = x
			case 6:
				dx = h - 1 - y
				dy = x
			case 7:
				dx = h - 1 - y
				dy = w - 1 - x
			case 8:
				dx = y
				dy = w - 1 - x
			}
			out.SetNRGBA(dx, dy, straight(src.At(b.Min.X+x, b.Min.Y+y)))
		}
	}
	return out, nil
}
func transform(ctx context.Context, doc *webp.Document, o options) ([]string, []string, error) {
	var retained, removed []string
	orientValue := 1
	if !o.ignoreOrientation {
		var e error
		orientValue, e = orientation(doc.Metadata.EXIF)
		if e != nil {
			return nil, nil, e
		}
	}
	if orientValue != 1 && o.metadata == "all" && (len(doc.Metadata.EXIF) > 0 || len(doc.Metadata.XMP) > 0) {
		return nil, nil, invalidMetadata("orientation changes geometry and retained EXIF/XMP cannot be safely rewritten; use --metadata=color or --ignore-orientation")
	}
	for _, v := range []struct {
		name string
		data *[]byte
		keep bool
	}{{"icc", &doc.Metadata.ICC, o.metadata != "none"}, {"exif", &doc.Metadata.EXIF, o.metadata == "all"}, {"xmp", &doc.Metadata.XMP, o.metadata == "all"}} {
		if len(*v.data) == 0 {
			continue
		}
		if v.keep {
			retained = append(retained, v.name)
		} else {
			removed = append(removed, v.name)
			*v.data = nil
		}
	}
	if e := validRGBProfile(doc.Metadata.ICC); e != nil {
		return nil, nil, e
	}
	for i := range doc.Frames {
		m, e := orient(ctx, doc.Frames[i].Pixels, orientValue)
		if e != nil {
			return nil, nil, e
		}
		if o.background != "" {
			bg, _ := parseBackground(o.background)
			b := m.Bounds()
			out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
			// Row-wise draws provide cooperative checkpoints without changing Go's
			// premultiplied compositing semantics.
			for y := 0; y < b.Dy(); y++ {
				if e := ctx.Err(); e != nil {
					return nil, nil, e
				}
				row := image.Rect(0, y, b.Dx(), y+1)
				draw.Draw(out, row, image.NewUniform(bg), image.Point{}, draw.Src)
				draw.Draw(out, row, m, image.Pt(b.Min.X, b.Min.Y+y), draw.Over)
			}
			m = out
		}
		doc.Frames[i].Pixels = m
	}
	if orientValue >= 5 {
		doc.Canvas = image.Pt(doc.Canvas.Y, doc.Canvas.X)
	}
	if e := checkDimensions(doc.Canvas.X, doc.Canvas.Y, o); e != nil {
		return nil, nil, e
	}
	return retained, removed, nil
}
func extractMetadata(ctx context.Context, b []byte, kind string, limit int64) (webp.Metadata, error) {
	var m webp.Metadata
	total := int64(0)
	add := func(dst *[]byte, p []byte) error {
		if int64(len(p)) > limit-total {
			return &webp.LimitError{Resource: "metadata_bytes", Limit: limit, Actual: -1}
		}
		total += int64(len(p))
		if *dst == nil {
			*dst = append([]byte{}, p...)
		}
		return nil
	}
	inflate := func(p []byte) ([]byte, error) {
		r, e := zlib.NewReader(&contextReader{ctx: ctx, r: bytes.NewReader(p)})
		if e != nil {
			return nil, invalidMetadata("invalid compressed PNG metadata")
		}
		v, e := readBounded(ctx, r, limit-total)
		ce := r.Close()
		if e != nil {
			if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
				return nil, e
			}
			var le *webp.LimitError
			if errors.As(e, &le) {
				return nil, &webp.LimitError{Resource: "metadata_bytes", Limit: limit, Actual: -1}
			}
			return nil, invalidMetadata("invalid compressed PNG metadata")
		}
		if ce != nil {
			return nil, invalidMetadata("compressed PNG metadata close failure")
		}
		return v, nil
	}
	switch kind {
	case "png":
		if len(b) < 8 {
			return m, invalidMetadata("short PNG")
		}
		count := 0
		for off := 8; off < len(b); {
			if e := ctx.Err(); e != nil {
				return m, e
			}
			count++
			if count > 4096 {
				return m, &webp.LimitError{Resource: "chunk_count", Limit: 4096, Actual: int64(count)}
			}
			if len(b)-off < 12 {
				return m, invalidMetadata("short PNG chunk")
			}
			n := uint64(binary.BigEndian.Uint32(b[off : off+4]))
			if n > uint64(len(b)-off-12) {
				return m, invalidMetadata("PNG chunk exceeds input")
			}
			tag := string(b[off+4 : off+8])
			p := b[off+8 : off+8+int(n)]
			off += 12 + int(n)
			switch tag {
			case "acTL":
				return m, fail(3, "unsupported", errors.New("APNG input requires an explicit frame manifest; refusing to flatten animation"))
			case "iCCP":
				end := len(p)
				if end > 80 {
					end = 80
				}
				zero := bytes.IndexByte(p[:end], 0)
				if zero < 1 {
					return m, invalidMetadata("invalid PNG ICC name")
				}
				name, rest := p[:zero], p[zero+1:]
				if len(name) > 79 || len(rest) < 2 || rest[0] != 0 {
					return m, invalidMetadata("invalid PNG ICC framing")
				}
				raw, e := inflate(rest[1:])
				if e != nil {
					return m, e
				}
				if e = add(&m.ICC, raw); e != nil {
					return m, e
				}
			case "eXIf":
				if e := add(&m.EXIF, p); e != nil {
					return m, e
				}
			case "iTXt":
				keyword, rest, ok := bytes.Cut(p, []byte{0})
				if !ok || string(keyword) != "XML:com.adobe.xmp" {
					continue
				}
				if len(rest) < 2 || rest[0] > 1 || rest[1] != 0 {
					return m, invalidMetadata("invalid PNG XMP framing")
				}
				compressed := rest[0] == 1
				rest = rest[2:]
				_, rest, ok = bytes.Cut(rest, []byte{0})
				if !ok {
					return m, invalidMetadata("invalid PNG XMP language")
				}
				_, rest, ok = bytes.Cut(rest, []byte{0})
				if !ok {
					return m, invalidMetadata("invalid PNG XMP translation")
				}
				if compressed {
					var e error
					rest, e = inflate(rest)
					if e != nil {
						return m, e
					}
				}
				if e := add(&m.XMP, rest); e != nil {
					return m, e
				}
			}
		}
	case "jpeg":
		parts := map[int][]byte{}
		expected := 0
		off := 2
		scan := false
		for off < len(b) {
			if e := ctx.Err(); e != nil {
				return m, e
			}
			if scan {
				for off < len(b) && b[off] != 0xff {
					if off&4095 == 0 {
						if e := ctx.Err(); e != nil {
							return m, e
						}
					}
					off++
				}
				if off == len(b) {
					break
				}
			}
			if b[off] != 0xff {
				return m, invalidMetadata("invalid JPEG marker")
			}
			for off < len(b) && b[off] == 0xff {
				if off&4095 == 0 {
					if e := ctx.Err(); e != nil {
						return m, e
					}
				}
				off++
			}
			if off == len(b) {
				break
			}
			tag := b[off]
			off++
			if scan && (tag == 0 || tag >= 0xd0 && tag <= 0xd7) {
				continue
			}
			scan = false
			if tag == 0xd9 {
				break
			}
			if tag == 0xd8 || tag == 0x01 {
				continue
			}
			if len(b)-off < 2 {
				return m, invalidMetadata("short JPEG segment")
			}
			n := int(binary.BigEndian.Uint16(b[off : off+2]))
			if n < 2 || n > len(b)-off {
				return m, invalidMetadata("JPEG segment exceeds input")
			}
			p := b[off+2 : off+n]
			off += n
			switch tag {
			case 0xda:
				scan = true
			case 0xe1:
				if bytes.HasPrefix(p, []byte("Exif\x00\x00")) {
					if e := add(&m.EXIF, p[6:]); e != nil {
						return m, e
					}
				} else if prefix := []byte("http://ns.adobe.com/xap/1.0/\x00"); bytes.HasPrefix(p, prefix) {
					if e := add(&m.XMP, p[len(prefix):]); e != nil {
						return m, e
					}
				}
			case 0xe2:
				if !bytes.HasPrefix(p, []byte("ICC_PROFILE\x00")) {
					continue
				}
				if len(p) < 14 {
					return m, invalidMetadata("short JPEG ICC fragment")
				}
				seq, num := int(p[12]), int(p[13])
				if seq == 0 || num == 0 || seq > num || expected != 0 && expected != num || parts[seq] != nil {
					return m, invalidMetadata("conflicting JPEG ICC fragments")
				}
				if int64(len(p)-14) > limit-total {
					return m, &webp.LimitError{Resource: "metadata_bytes", Limit: limit, Actual: -1}
				}
				total += int64(len(p) - 14)
				expected = num
				parts[seq] = p[14:]
			}
		}
		if expected > 0 {
			for i := 1; i <= expected; i++ {
				p, ok := parts[i]
				if !ok {
					return m, invalidMetadata("missing JPEG ICC fragment")
				}
				m.ICC = append(m.ICC, p...)
			}
		}
	}
	return m, nil
}
