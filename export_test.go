package webp

import (
	"image"

	"m31labs.dev/tqwebp/internal/yuv"
)

// This file exposes encoder internals to the package's own tests. It
// compiles into the test binary only.

// encodeWithReconstruction encodes m and returns both the file bytes and
// the encoder's own reconstruction. The exact-match gate compares that
// reconstruction with an independent decode of the same bytes.
func encodeWithReconstruction(m image.Image, opts Options) ([]byte, *image.YCbCr, error) {
	enc := newEncoder(yuv.Convert(m), opts)
	enc.run()
	var buf writerBuffer
	if err := enc.writeFile(&buf); err != nil {
		return nil, nil, err
	}
	return buf.data, enc.reconstruction(), nil
}

// reconSource adapts an encoder reconstruction to the oracle's
// ReconstructionSource interface.
type reconSource struct{ planes *image.YCbCr }

func (r reconSource) ReconstructionPlanes() (*image.YCbCr, error) { return r.planes, nil }

// writerBuffer is a minimal io.Writer that collects bytes, so the test
// helper does not need the bytes package in this file.
type writerBuffer struct{ data []byte }

func (w *writerBuffer) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}
