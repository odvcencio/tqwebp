package register_test

import (
	"bytes"
	"errors"
	"image"
	webp "m31labs.dev/tqwebp"
	_ "m31labs.dev/tqwebp/register"
	"os"
	"testing"
)

func TestRegistration(t *testing.T) {
	b, e := os.ReadFile("../testdata/decode/pattern-19x17.webp")
	if e != nil {
		t.Fatal(e)
	}
	m, format, e := image.Decode(bytes.NewReader(b))
	if e != nil || format != "webp" || m.Bounds().Dx() != 19 {
		t.Fatal(m, format, e)
	}
	c, format, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || format != "webp" || c.Height != 17 {
		t.Fatal(c, format, e)
	}
	b, e = os.ReadFile("../testdata/decode/alpha-filter-0.webp")
	if e != nil {
		t.Fatal(e)
	}
	b[20] |= 2
	if _, _, e = image.Decode(bytes.NewReader(b)); !errors.Is(e, webp.ErrAnimatedImage) {
		t.Fatal(e)
	}
}
