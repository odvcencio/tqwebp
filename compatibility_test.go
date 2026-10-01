package webp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	webp "m31labs.dev/tqwebp"
)

// These positional literals are deliberate external-consumer compile fixtures.
// P1/P2 must not extend either established struct or change its field order.
var positionalOptions = webp.Options{75, 4}
var positionalLimits = webp.Limits{64, 64, 4096, 1 << 20}

func TestLegacyPositionalConsumer(t *testing.T) {
	f, err := os.Open("testdata/corpus/edge_small_64.png")
	if err != nil {
		t.Fatal(err)
	}
	m, err := png.Decode(f)
	closeErr := f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	var plain, limited, advanced bytes.Buffer
	if err := webp.Encode(&plain, m, &positionalOptions); err != nil {
		t.Fatal(err)
	}
	if err := webp.EncodeWithLimits(&limited, m, &positionalOptions, positionalLimits); err != nil {
		t.Fatal(err)
	}
	if err := webp.EncodeContext(context.Background(), &advanced, m, &positionalOptions, positionalLimits); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain.Bytes(), limited.Bytes()) || !bytes.Equal(plain.Bytes(), advanced.Bytes()) {
		t.Fatal("legacy positional consumer changed bytes")
	}
}

// The manifest was generated from the hash-verified, untouched efac282e tree,
// rather than accepted from the new implementation. It covers all nine
// deterministic corpus images and the three distinct effort paths.
func TestUsabilityBaselineBytes(t *testing.T) {
	manifest, err := os.ReadFile("testdata/golden/usability_efac282e.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		var name, hash string
		var quality, method, size int
		if _, err := fmt.Sscanf(line, "%s %d %d %d %s", &name, &quality, &method, &size, &hash); err != nil {
			t.Fatal(err)
		}
		t.Run(fmt.Sprintf("%s/method%d", name, method), func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata/corpus", name))
			if err != nil {
				t.Fatal(err)
			}
			m, err := png.Decode(f)
			closeErr := f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			// A live cancellable context exercises the new checked path even
			// though this particular successful call never cancels.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var got bytes.Buffer
			if err := webp.EncodeContext(ctx, &got, m, &webp.Options{quality, method}, webp.Limits{MaxOutputBytes: int64(size)}); err != nil {
				t.Fatal(err)
			}
			if got.Len() != size || fmt.Sprintf("%x", sha256.Sum256(got.Bytes())) != hash {
				t.Fatal("output moved from efac282e baseline")
			}
		})
	}
}
