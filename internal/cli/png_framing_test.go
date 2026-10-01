package cli

import (
	"bytes"
	"context"
	"errors"
	webp "m31labs.dev/tqwebp"
	"math"
	"testing"
)

func TestPNGMetadataChunkBoundariesWithoutAllocation(t *testing.T) {
	for _, prefix := range []int64{0, 8, 22} {
		if e := admitPNGChunk((1<<31)-1-prefix, prefix); e != nil {
			t.Fatal(e)
		}
		for _, size := range []int64{(1 << 31) - prefix, math.MaxInt64} {
			e := admitPNGChunk(size, prefix)
			var limit *webp.LimitError
			if !errors.As(e, &limit) || limit.Limit != (1<<31)-1 || !errors.Is(e, webp.ErrLimitExceeded) || errors.Is(e, webp.ErrInvalidFormat) {
				t.Fatal(e)
			}
		}
	}
	if e := admitPNGChunk(-1, 0); !errors.Is(e, webp.ErrInvalidMetadata) {
		t.Fatal(e)
	}
}
func TestPNGExifStripsOnlyTransportWrapper(t *testing.T) {
	tiff := exifOrientation(1)
	wrapped := append([]byte("Exif\x00\x00"), tiff...)
	o := testOptions(t)
	o.metadata = "all"
	o.ignoreOrientation = true
	data, e := encodePNG(context.Background(), tinyImage(), webp.Metadata{EXIF: wrapped}, o)
	if e != nil {
		t.Fatal(e)
	}
	m, e := extractMetadata(context.Background(), data, "png", 1024)
	if e != nil || !bytes.Equal(m.EXIF, tiff) {
		t.Fatal(e, m.EXIF)
	}
	// Canonical WebP metadata remux retains the complete original opaque payload.
	input := makeAnimation(t, webp.Metadata{EXIF: wrapped})
	code, r, out := runJSON(t, []string{"metadata", "-", "-o", "-"}, input)
	if code != 0 {
		t.Fatal(code, r)
	}
	d, e := webp.DecodeAll(context.Background(), bytes.NewReader(out), webp.ReadLimits{})
	if e != nil || !bytes.Equal(d.Metadata.EXIF, wrapped) {
		t.Fatal(e)
	}
	if !bytes.Equal(wrapped, append([]byte("Exif\x00\x00"), tiff...)) {
		t.Fatal("source metadata mutated")
	}
}
