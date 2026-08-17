package encoder

import (
	"image"
	"io"

	"m31labs.dev/tqwebp/internal/yuv"
)

// Config carries the settings one encode needs. The module root
// validates the public options and fills this in.
type Config struct {
	// Quality runs from 1, the smallest file, to 100, the best picture.
	Quality int
	// Method runs from 0 to 6. This release implements one effort level.
	Method int
}

// Encode writes m to w as a lossy WebP file.
func Encode(w io.Writer, m image.Image, cfg Config) error {
	enc := newEncoder(yuv.Convert(m), cfg)
	enc.run()
	return enc.writeFile(w)
}

// EncodeWithReconstruction encodes m and returns the file bytes together
// with the encoder's own reconstruction. The exact-match gate compares
// that reconstruction with an independent decode of the same bytes, so
// the repository's gate harness and tests call this instead of Encode.
func EncodeWithReconstruction(m image.Image, cfg Config) ([]byte, *image.YCbCr, error) {
	enc := newEncoder(yuv.Convert(m), cfg)
	enc.run()
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
