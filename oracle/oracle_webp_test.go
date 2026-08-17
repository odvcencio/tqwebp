package oracle

import (
	"image"
	"io"
	"testing"

	"golang.org/x/image/webp"
)

// TestRoundTrip_UsesWebPDecodeByDefault proves RoundTrip's decoder is
// golang.org/x/image/webp.Decode, the same decoder RoundTripWith accepts
// explicitly. WP-0 ships no WebP encoder (and no fetched or vendored
// WebP fixture: the corpus and test data in this repository are all
// generated from source), so this test proves the wiring without a real
// bitstream: it feeds bytes that are not a valid WebP file, and checks
// that RoundTrip fails exactly the way RoundTripWith(enc, src,
// webp.Decode) fails. If RoundTrip used a different, more permissive
// decoder, the two error strings would not match.
func TestRoundTrip_UsesWebPDecodeByDefault(t *testing.T) {
	invalid := []byte("not a WebP file: filler bytes for a decode-error probe")
	encInvalid := func(w io.Writer, m image.Image) error {
		_, err := w.Write(invalid)
		return err
	}
	src := checkerboardImage(8, 8)

	_, errDefault := RoundTrip(encInvalid, src)
	_, errExplicit := RoundTripWith(encInvalid, src, webp.Decode)

	if errDefault == nil || errExplicit == nil {
		t.Fatalf("want decode errors from invalid WebP bytes, got default=%v explicit=%v", errDefault, errExplicit)
	}
	if errDefault.Error() != errExplicit.Error() {
		t.Fatalf("RoundTrip's decode error does not match RoundTripWith(..., webp.Decode):\n  RoundTrip:     %q\n  RoundTripWith: %q", errDefault, errExplicit)
	}
}
