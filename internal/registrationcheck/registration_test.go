package registrationcheck_test

import (
	"bytes"
	"errors"
	"image"
	_ "m31labs.dev/tqwebp"
	"os"
	"testing"
)

func TestRootImportDoesNotRegister(t *testing.T) {
	b, e := os.ReadFile("../../testdata/decode/pattern-1x1.webp")
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = image.Decode(bytes.NewReader(b)); !errors.Is(e, image.ErrFormat) {
		t.Fatalf("root unexpectedly registered format: %v", e)
	}
}
