package oracle

import (
	"image"
	"io"
	"os"
	"strings"
	"testing"
)

// TestRoundTrip_RejectsNonWebP proves RoundTrip really decodes WebP: it
// feeds bytes that are not a WebP file and requires a decode error that
// names the container.
func TestRoundTrip_RejectsNonWebP(t *testing.T) {
	invalid := []byte("not a WebP file: filler bytes for a decode-error probe")
	encInvalid := func(w io.Writer, m image.Image) error {
		_, err := w.Write(invalid)
		return err
	}

	_, err := RoundTrip(encInvalid, checkerboardImage(8, 8))
	if err == nil {
		t.Fatal("invalid bytes decoded without an error")
	}
	if !strings.Contains(err.Error(), "RIFF") {
		t.Errorf("error %q does not name the RIFF container", err)
	}
}

// TestRoundTrip_UsesTheLibwebpColourPath proves RoundTrip scores through
// DecodeWebP, the path that inverts planes the way libwebp does. The test
// replays a real libwebp file and compares RoundTrip's score with a
// direct measurement over the same path.
func TestRoundTrip_UsesTheLibwebpColourPath(t *testing.T) {
	data, err := os.ReadFile(bt601Dir + "/libwebp_q100.webp")
	if err != nil {
		t.Fatalf("read WebP fixture: %v", err)
	}
	decoded, err := DecodeWebP(data)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	// The source is the fixture's own decoded picture, so any difference
	// in the scoring path shows up as a finite PSNR instead of infinity.
	replay := func(w io.Writer, m image.Image) error {
		_, err := w.Write(data)
		return err
	}
	res, err := RoundTrip(replay, decoded)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if res.EncodedBytes != len(data) {
		t.Errorf("reported %d bytes, want %d", res.EncodedBytes, len(data))
	}
	if res.PSNR.Y != PSNRInf {
		t.Errorf("luma PSNR is %v, want infinity: the scoring path differs from DecodeWebP", res.PSNR.Y)
	}
	if res.Width != decoded.Bounds().Dx() || res.Height != decoded.Bounds().Dy() {
		t.Errorf("reported size %dx%d, want %dx%d", res.Width, res.Height, decoded.Bounds().Dx(), decoded.Bounds().Dy())
	}
}
