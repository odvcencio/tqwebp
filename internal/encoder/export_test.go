package encoder

import "image"

// This file exposes encoder internals to the package's own tests. It
// compiles into the test binary only.

// encodeWithReconstruction encodes m and returns both the file bytes and
// the encoder's own reconstruction.
func encodeWithReconstruction(m image.Image, cfg Config) ([]byte, *image.YCbCr, error) {
	return EncodeWithReconstruction(m, cfg)
}

// reconSource adapts an encoder reconstruction to the oracle's
// ReconstructionSource interface.
type reconSource struct{ planes *image.YCbCr }

func (r reconSource) ReconstructionPlanes() (*image.YCbCr, error) { return r.planes, nil }

// writerBuffer is a minimal io.Writer that collects bytes.
type writerBuffer = byteWriter
