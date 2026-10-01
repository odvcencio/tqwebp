package cli

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	webp "m31labs.dev/tqwebp"
	"math"
)

func webpData(b []byte) bool {
	return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
}
func codecContext(ctx context.Context, o options) (context.Context, context.CancelFunc) {
	if o.timeout == 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, o.timeout)
}
func readLimits(o options) webp.ReadLimits {
	return webp.ReadLimits{MaxInputBytes: o.inputBytes, MaxCanvasPixels: o.pixels, MaxFramePixels: o.pixels, MaxFrames: o.frames, MaxDecodedPixels: o.totalPixels, MaxMetadataBytes: o.metadataBytes, MaxDuration: o.durationLimit}
}
func documentLimits(o options) webp.DocumentLimits {
	return webp.DocumentLimits{Limits: webp.Limits{MaxWidth: o.width, MaxHeight: o.height, MaxPixels: o.pixels, MaxOutputBytes: o.outputBytes}, MaxFrames: o.frames, MaxTotalPixels: o.totalPixels, MaxMetadataBytes: o.metadataBytes, MaxDuration: o.durationLimit}
}
func checkDimensions(w, h int, o options) error {
	if w <= 0 || h <= 0 {
		return webp.ErrInvalidImage
	}
	if o.width > 0 && w > o.width {
		return &webp.LimitError{Resource: "width", Limit: int64(o.width), Actual: int64(w)}
	}
	if o.height > 0 && h > o.height {
		return &webp.LimitError{Resource: "height", Limit: int64(o.height), Actual: int64(h)}
	}
	if int64(w) > o.pixels/int64(h) {
		return &webp.LimitError{Resource: "pixels", Limit: o.pixels, Actual: -1}
	}
	if int64(w) > int64(int(^uint(0)>>1))/8/int64(h) {
		return &webp.LimitError{Resource: "pixels", Limit: int64(int(^uint(0)>>1)) / 8, Actual: -1}
	}
	return nil
}
func decodeDocument(ctx context.Context, b []byte, o options) (*webp.Document, string, error) {
	if webpData(b) {
		op, cancel := codecContext(ctx, o)
		defer cancel()
		r, e := webp.NewReader(op, bytes.NewReader(b), readLimits(o))
		if e != nil {
			return nil, "webp", e
		}
		info := r.Info()
		r.Close()
		if e = checkDimensions(info.Canvas.X, info.Canvas.Y, o); e != nil {
			return nil, "webp", e
		}
		d, e := webp.DecodeAll(op, bytes.NewReader(b), readLimits(o))
		return d, "webp", e
	}
	if bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")) {
		return nil, "gif", fail(3, "unsupported", webp.ErrUnsupportedFeature)
	}
	config, kind, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil {
		return nil, "", fail(3, "invalid_input", e)
	}
	if kind != "png" && kind != "jpeg" {
		return nil, kind, fail(3, "unsupported", webp.ErrUnsupportedFeature)
	}
	if e = checkDimensions(config.Width, config.Height, o); e != nil {
		return nil, kind, e
	}
	if e = ctx.Err(); e != nil {
		return nil, kind, e
	}
	metadata, e := extractMetadata(ctx, b, kind, o.metadataBytes)
	if e != nil {
		return nil, kind, e
	}
	m, _, e := image.Decode(bytes.NewReader(b))
	if e != nil {
		return nil, kind, fail(3, "invalid_input", e)
	}
	if e = ctx.Err(); e != nil {
		return nil, kind, e
	}
	if _, cmyk := m.(*image.CMYK); cmyk && len(metadata.ICC) > 0 && o.metadata != "none" {
		return nil, kind, invalidMetadata("profiled CMYK input needs profile conversion; choose --metadata=none explicitly")
	}
	return &webp.Document{Canvas: image.Pt(config.Width, config.Height), Frames: []webp.Frame{{Pixels: m}}, Metadata: metadata}, kind, nil
}
func encodeDocument(ctx context.Context, d *webp.Document, o options) ([]byte, error) {
	op, cancel := codecContext(ctx, o)
	defer cancel()
	out := &cappedBuffer{max: o.outputBytes, ctx: op}
	if e := webp.EncodeAll(op, out, d, &webp.Options{Quality: o.quality, Method: o.method}, documentLimits(o)); e != nil {
		return nil, e
	}
	return out.data, nil
}
func pngChunk(w io.Writer, tag string, data []byte) error {
	if e := admitPNGChunk(int64(len(data)), 0); e != nil {
		return e
	}
	var h [8]byte
	binary.BigEndian.PutUint32(h[:4], uint32(len(data)))
	copy(h[4:], tag)
	if _, e := w.Write(h[:]); e != nil {
		return e
	}
	if _, e := w.Write(data); e != nil {
		return e
	}
	digest := crc32.NewIEEE()
	digest.Write([]byte(tag))
	digest.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], digest.Sum32())
	_, e := w.Write(sum[:])
	return e
}
func encodePNG(ctx context.Context, m image.Image, metadata webp.Metadata, o options) ([]byte, error) {
	op, cancel := codecContext(ctx, o)
	defer cancel()
	base := &cappedBuffer{max: o.outputBytes, ctx: op}
	if e := png.Encode(base, m); e != nil {
		return nil, e
	}
	if len(metadata.ICC) == 0 && len(metadata.EXIF) == 0 && len(metadata.XMP) == 0 {
		return base.data, nil
	}
	// The standard encoder always writes PNG signature plus the 13-byte IHDR first.
	if len(base.data) < 33 {
		return nil, webp.ErrInvalidFormat
	}
	out := &cappedBuffer{max: o.outputBytes, ctx: op}
	if _, e := out.Write(base.data[:33]); e != nil {
		return nil, e
	}
	if len(metadata.ICC) > 0 {
		compressed := &cappedBuffer{max: o.outputBytes, ctx: op}
		z := zlib.NewWriter(compressed)
		_, e := z.Write(metadata.ICC)
		ce := z.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
		if e := admitPNGChunk(int64(len(compressed.data)), 8); e != nil {
			return nil, e
		}
		body := append([]byte("tqwebp\x00\x00"), compressed.data...)
		if e = pngChunk(out, "iCCP", body); e != nil {
			return nil, e
		}
	}
	if len(metadata.EXIF) > 0 {
		exif := metadata.EXIF
		if bytes.HasPrefix(exif, []byte("Exif\x00\x00")) {
			exif = exif[6:]
		}
		if e := pngChunk(out, "eXIf", exif); e != nil {
			return nil, e
		}
	}
	if len(metadata.XMP) > 0 {
		if e := admitPNGChunk(int64(len(metadata.XMP)), 22); e != nil {
			return nil, e
		}
		body := append([]byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00"), metadata.XMP...)
		if e := pngChunk(out, "iTXt", body); e != nil {
			return nil, e
		}
	}
	if _, e := out.Write(base.data[33:]); e != nil {
		return nil, e
	}
	return out.data, nil
}

// PNG specification5.3 limits chunk data to2^31-1 bytes, despite uint32 storage.
func admitPNGChunk(payload, prefix int64) error {
	const maximum int64 = 1<<31 - 1
	if payload < 0 || prefix < 0 {
		return webp.ErrInvalidMetadata
	}
	if prefix > maximum || payload > maximum-prefix {
		actual := int64(-1)
		if payload <= math.MaxInt64-prefix {
			actual = payload + prefix
		}
		return &webp.LimitError{Resource: "metadata_bytes", Limit: maximum, Actual: actual}
	}
	return nil
}
