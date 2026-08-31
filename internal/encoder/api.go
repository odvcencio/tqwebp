package encoder

import (
	"image"
	"io"

	"m31labs.dev/tqwebp/internal/alpha"
	"m31labs.dev/tqwebp/internal/container"
	"m31labs.dev/tqwebp/internal/yuv"
)

// Config carries the settings one encode needs. The module root
// validates the public options and fills this in.
type Config struct {
	// Quality runs from 1, the smallest file, to 100, the best picture.
	Quality int
	// Method runs from 0 to 6. Methods 0 to 4 share one effort level:
	// every macroblock's luma uses a whole-block prediction mode. At
	// Method 5 and 6 the encoder additionally runs a conservative
	// detailed-block pass that may code a macroblock's luma as sixteen
	// 4x4 blocks when its sum-of-squares error is clearly below half
	// of the whole-block error. The rule prices squared error only,
	// not bits; it is not a rate-distortion search.
	Method int
	// AlphaFilter selects the ALPH filtering method, 0 to 3, for a
	// picture with a translucent pixel. Compression method 0 stores one
	// byte per sample whichever filter runs, so the filter cannot change
	// the file size today and the default of 0, no filter, is the plain
	// choice. The field exists because a later compression method 1
	// payload does shrink with a filter, and because the decode side of
	// all four methods is already pinned by test.
	AlphaFilter int
}

// Encode writes m to w as a lossy WebP file.
//
// An opaque picture takes the simple container: a RIFF header and a VP8
// key frame, byte for byte what every earlier release wrote. A picture
// with a translucent pixel takes the extended container: a VP8X chunk
// with the alpha flag set, an ALPH chunk, and then the same key frame.
func Encode(w io.Writer, m image.Image, cfg Config) error {
	data, err := EncodeToBytes(m, cfg)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return nil
}

// EncodeToBytes encodes m and returns the whole file. Encode buffers the
// file anyway, because the container size, the frame tag, and the
// partition length all precede the data they describe.
func EncodeToBytes(m image.Image, cfg Config) ([]byte, error) {
	data, _, err := EncodeWithReconstruction(m, cfg)
	return data, err
}

// EncodeWithReconstruction encodes m and returns the file bytes together
// with the encoder's own reconstruction. The exact-match gate compares
// that reconstruction with an independent decode of the same bytes, so
// the repository's gate harness and tests call this instead of Encode.
func EncodeWithReconstruction(m image.Image, cfg Config) ([]byte, *image.YCbCr, error) {
	enc := newEncoder(yuv.Convert(m), cfg)
	enc.runFrame()
	payload, err := enc.frameBytes()
	if err != nil {
		return nil, nil, err
	}

	var buf byteWriter
	if plane := alphaPlaneOf(m); plane != nil {
		chunk, err := alpha.Chunk(plane, cfg.AlphaFilter)
		if err != nil {
			return nil, nil, err
		}
		err = container.WriteExtendedLossyAlpha(&buf, chunk, payload, plane.Width, plane.Height)
		if err != nil {
			return nil, nil, err
		}
		return buf.data, enc.reconstruction(), nil
	}
	if err := container.WriteSimpleLossy(&buf, payload); err != nil {
		return nil, nil, err
	}
	return buf.data, enc.reconstruction(), nil
}

// alphaPlaneOf returns m's alpha plane, or nil when every pixel of m is
// opaque and the file therefore needs no ALPH chunk.
//
// The second test, on the extracted plane, costs one pass and closes the
// gap an image type could open by reporting a translucent pixel its own
// samples do not hold. An opaque picture must reach the simple container
// on every path, because that byte-for-byte promise is a release gate.
func alphaPlaneOf(m image.Image) *alpha.Plane {
	if yuv.IsOpaque(m) {
		return nil
	}
	plane := alpha.Extract(m)
	if plane.Opaque() {
		return nil
	}
	return plane
}

// byteWriter collects written bytes without pulling in the bytes package.
type byteWriter struct{ data []byte }

func (w *byteWriter) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}
