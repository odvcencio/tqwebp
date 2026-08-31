package webp

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"

	"m31labs.dev/tqwebp/internal/corpus"
)

// goldenOpaqueContainer is the table this test pins.
const goldenOpaqueContainer = "testdata/golden/opaque_container.txt"

// TestOpaqueContainerIsByteIdentical is the regression gate of the alpha
// work package. The table it reads was written by the release before
// alpha landed. Every opaque encode must still produce those exact
// bytes, which the SHA-256 of each file proves.
//
// Alpha changed three things an opaque encode touches: the colour
// sampler now reads the alpha byte of an *image.RGBA, the encoder now
// asks whether the picture is opaque, and the container writer gained a
// second layout. None of the three may move one byte of an opaque file.
//
// Regenerate the table only when a deliberate bitstream change lands,
// and say so in that commit.
func TestOpaqueContainerIsByteIdentical(t *testing.T) {
	text, err := os.ReadFile(goldenOpaqueContainer)
	if err != nil {
		t.Fatalf("read the golden table: %v", err)
	}

	images := map[string]*corpus.Image{}
	for _, img := range corpus.GenerateAll() {
		image := img
		images[img.Spec.Name] = &image
	}

	rows := 0
	for lineNumber, line := range strings.Split(string(text), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var name string
		var quality, method, wantBytes int
		var wantSum string
		_, err := fmt.Sscan(line, &name, &quality, &method, &wantBytes, &wantSum)
		if err != nil {
			t.Fatalf("line %d: %v", lineNumber+1, err)
		}
		img, ok := images[name]
		if !ok {
			t.Fatalf("line %d: the corpus holds no image named %q", lineNumber+1, name)
		}

		var buf bytes.Buffer
		if err := Encode(&buf, img.Img, &Options{Quality: quality, Method: method}); err != nil {
			t.Fatalf("%s q%d m%d: %v", name, quality, method, err)
		}
		if buf.Len() != wantBytes {
			t.Errorf("%s q%d m%d: the file is %d bytes, the pre-alpha release wrote %d",
				name, quality, method, buf.Len(), wantBytes)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(buf.Bytes())); got != wantSum {
			t.Errorf("%s q%d m%d: the file hashes to %s, the pre-alpha release wrote %s",
				name, quality, method, got, wantSum)
		}
		rows++
	}
	if rows == 0 {
		t.Fatal("the golden table holds no row")
	}
	t.Logf("%d opaque encodes match the pre-alpha release byte for byte", rows)
}
