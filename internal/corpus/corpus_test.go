package corpus

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

// encodePNG renders spec and re-encodes it with the same encoder WriteAll
// uses, so tests compare the same bytes a real "go generate" run would
// produce.
func encodePNG(t *testing.T, spec Spec) []byte {
	t.Helper()
	enc := &png.Encoder{CompressionLevel: png.BestCompression}
	var buf bytes.Buffer
	if err := enc.Encode(&buf, Generate(spec)); err != nil {
		t.Fatalf("encode %s: %v", spec.Name, err)
	}
	return buf.Bytes()
}

func TestGenerate_Deterministic(t *testing.T) {
	for _, spec := range Manifest {
		a := encodePNG(t, spec)
		b := encodePNG(t, spec)
		if !bytes.Equal(a, b) {
			t.Errorf("%s: two Generate+encode runs produced different bytes", spec.Name)
		}
	}
}

func TestGenerate_MatchesCommittedCorpus(t *testing.T) {
	for _, spec := range Manifest {
		regenerated := encodePNG(t, spec)
		committed, err := Load("../..", spec)
		if err != nil {
			t.Fatalf("%s: load committed PNG: %v (run `go generate` from the module root)", spec.Name, err)
		}
		var committedBuf bytes.Buffer
		enc := &png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&committedBuf, committed); err != nil {
			t.Fatalf("%s: re-encode committed PNG: %v", spec.Name, err)
		}
		if !bytes.Equal(regenerated, committedBuf.Bytes()) {
			t.Errorf("%s: regenerated pixels do not match testdata/corpus/%s.png (run `go generate` from the module root and commit the result)", spec.Name, spec.Name)
		}
	}
}

func TestManifest_Dimensions(t *testing.T) {
	seen := map[string]bool{}
	haveSmall, havePrime, count1200x800Class := false, false, 0
	for _, spec := range Manifest {
		if spec.Width <= 0 || spec.Height <= 0 {
			t.Errorf("%s: non-positive dimensions %dx%d", spec.Name, spec.Width, spec.Height)
		}
		if seen[spec.Name] {
			t.Errorf("duplicate corpus name %q", spec.Name)
		}
		seen[spec.Name] = true

		if spec.Width <= 64 && spec.Height <= 64 {
			haveSmall = true
		}
		if isPrime(spec.Width) && isPrime(spec.Height) {
			havePrime = true
		}
		if spec.Width >= 1000 && spec.Height >= 700 {
			count1200x800Class++
		}
	}
	if !haveSmall {
		t.Error("manifest has no small (<=64x64-class) edge case image")
	}
	if !havePrime {
		t.Error("manifest has no prime-width/prime-height edge case image")
	}
	if count1200x800Class < 2 {
		t.Errorf("manifest has %d images at the 1200x800 class, want at least 2 (\"a few\")", count1200x800Class)
	}
}

func TestManifest_CoversAllClasses(t *testing.T) {
	classes := map[Class]int{}
	for _, spec := range Manifest {
		classes[spec.Class]++
	}
	for _, c := range []Class{Photo, Screenshot, Flat} {
		if classes[c] == 0 {
			t.Errorf("manifest has no %s-class image", c)
		}
	}
	if classes[Photo] < 3 {
		t.Errorf("manifest has %d photo-class images, want >= 3 (\"several\")", classes[Photo])
	}
}

func TestGenerate_ProducesCorrectSize(t *testing.T) {
	for _, spec := range Manifest {
		img := Generate(spec)
		b := img.Bounds()
		if b.Dx() != spec.Width || b.Dy() != spec.Height {
			t.Errorf("%s: Generate produced %dx%d, want %dx%d", spec.Name, b.Dx(), b.Dy(), spec.Width, spec.Height)
		}
	}
}

func TestGenerate_DifferentSeedsDifferentPixels(t *testing.T) {
	specA := Spec{Name: "seed-a", Class: Photo, Width: 32, Height: 32, Seed: 1}
	specB := Spec{Name: "seed-b", Class: Photo, Width: 32, Height: 32, Seed: 2}
	a := Generate(specA)
	b := Generate(specB)
	if imagesEqual(a, b) {
		t.Error("Generate with different seeds produced identical pixels")
	}
}

func imagesEqual(a, b *image.RGBA) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	return bytes.Equal(a.Pix, b.Pix)
}

func isPrime(n int) bool {
	if n < 2 {
		return false
	}
	for i := 2; i*i <= n; i++ {
		if n%i == 0 {
			return false
		}
	}
	return true
}
