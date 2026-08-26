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
	// Method runs from 0 to 6. Methods 0 to 4 share one effort level:
	// every macroblock's luma uses a whole-block prediction mode. At
	// Method 5 and 6 reconstructed-neighbor rate-distortion search may
	// instead code sixteen B_PRED blocks. They also use inverse-aware sharp
	// YUV conversion, with extended hard-edge refinement at quality 85 and
	// above. Method 5 derives token probabilities once; Method 6 also refines
	// coefficients and runs one bounded reconsideration under the derived
	// entropy prices.
	Method int
}

// Encode writes m to w as a lossy WebP file.
func Encode(w io.Writer, m image.Image, cfg Config) error {
	enc := newEncoder(convertInput(m, cfg), cfg)
	enc.runFrame()
	return enc.writeFile(w)
}

// EncodeWithReconstruction encodes m and returns the file bytes together
// with the encoder's own reconstruction. The exact-match gate compares
// that reconstruction with an independent decode of the same bytes, so
// the repository's gate harness and tests call this instead of Encode.
func EncodeWithReconstruction(m image.Image, cfg Config) ([]byte, *image.YCbCr, error) {
	enc := newEncoder(convertInput(m, cfg), cfg)
	enc.runFrame()
	var buf byteWriter
	if err := enc.writeFile(&buf); err != nil {
		return nil, nil, err
	}
	return buf.data, enc.reconstruction(), nil
}

func convertInput(m image.Image, cfg Config) *yuv.Planes {
	if cfg.Method >= 5 {
		return yuv.ConvertSharp(m, cfg.Quality >= 85)
	}
	return yuv.Convert(m)
}

// byteWriter collects written bytes without pulling in the bytes package.
type byteWriter struct{ data []byte }

func (w *byteWriter) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}
