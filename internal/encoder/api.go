package encoder

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"

	"m31labs.dev/tqwebp/internal/container"
	"m31labs.dev/tqwebp/internal/yuv"
)

// Config carries the settings one encode needs. The module root
// validates the public options and fills this in.
type Config struct {
	// Quality runs from 1, the smallest file, to 100, the best picture.
	Quality int
	// Methods below 5 use whole-block prediction. Method 5 adds 4x4
	// prediction and rate-distortion mode selection. Method 6 also refines
	// coefficients with a separately calibrated weight and reconsiders
	// analysis under optimized token probabilities. Public Method 0 is
	// normalized to the default before this internal entry point.
	Method int
}

// Encode writes m to w as a lossy WebP file.
func Encode(w io.Writer, m image.Image, cfg Config) error {
	return EncodeContext(context.Background(), w, m, cfg, 0)
}

// OutputLimitError is the private-to-the-module bridge to the root LimitError.
// Actual is -1 when serialization stopped before the full size was available.
type OutputLimitError struct{ Limit, Actual int64 }

func (e *OutputLimitError) Error() string {
	return fmt.Sprintf("tqwebp: output exceeds limit %d", e.Limit)
}

// EncodeContext preserves the legacy file/write layout without a cap. With a
// positive cap, both serialized partitions share a complete-file budget and
// the whole file is committed in one write only after validation.
func EncodeContext(ctx context.Context, w io.Writer, m image.Image, cfg Config, maxOutputBytes int64) error {
	src, err := yuv.ConvertContext(ctx, m)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	enc := newEncoder(src, cfg)
	enc.ctx, enc.maxOutputBytes = ctx, maxOutputBytes
	enc.runFrame()
	if enc.err != nil {
		return enc.err
	}
	if maxOutputBytes == 0 {
		return enc.writeFile(w)
	}
	payload, err := enc.frameBytes()
	if err != nil {
		return err
	}
	size := container.Size(len(payload))
	if int64(size) > maxOutputBytes {
		return &OutputLimitError{Limit: maxOutputBytes, Actual: int64(size)}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Exact capacity avoids the second unbounded bytes.Buffer growth of the
	// legacy capped path. Source/reconstruction/macroblocks remain uncapped.
	encoded := bytes.NewBuffer(make([]byte, 0, size))
	if err := container.WriteSimpleLossyContext(ctx, encoded, payload); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data := encoded.Bytes()
	n, err := w.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return ctx.Err()
}

// EncodeWithReconstruction encodes m and returns the file bytes together
// with the encoder's own reconstruction. The exact-match gate compares
// that reconstruction with an independent decode of the same bytes, so
// the repository's gate harness and tests call this instead of Encode.
func EncodeWithReconstruction(m image.Image, cfg Config) ([]byte, *image.YCbCr, error) {
	enc := newEncoder(yuv.Convert(m), cfg)
	enc.runFrame()
	var buf byteWriter
	if err := enc.writeFile(&buf); err != nil {
		return nil, nil, err
	}
	return buf.data, enc.reconstruction(), nil
}

// byteWriter collects written bytes without pulling in the bytes package.
type byteWriter struct{ data []byte }

func (w *byteWriter) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}
