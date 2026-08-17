package corpus

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
)

// Image pairs a generated Spec with its rendered pixels, for callers that
// need both the manifest metadata and the image in one value.
type Image struct {
	Spec Spec
	Img  image.Image
}

// GenerateAll renders every Spec in Manifest.
func GenerateAll() []Image {
	images := make([]Image, len(Manifest))
	for i, spec := range Manifest {
		images[i] = Image{Spec: spec, Img: Generate(spec)}
	}
	return images
}

// Dir returns the directory tqwebp stores generated corpus PNGs in,
// relative to the module root.
const Dir = "testdata/corpus"

// PNGPath returns the path GenerateAll's PNG output for spec lives at,
// relative to the module root.
func PNGPath(spec Spec) string {
	return filepath.Join(Dir, spec.Name+".png")
}

// WriteAll renders every Spec in Manifest and writes each as a PNG under
// root/testdata/corpus. It is deterministic: image/png's encoder output is
// stable for a given input image and compression level, and Generate is
// itself deterministic, so repeated runs write identical bytes.
func WriteAll(root string) error {
	dir := filepath.Join(root, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("corpus: create %s: %w", dir, err)
	}
	enc := &png.Encoder{CompressionLevel: png.BestCompression}
	for _, spec := range Manifest {
		img := Generate(spec)
		var buf bytes.Buffer
		if err := enc.Encode(&buf, img); err != nil {
			return fmt.Errorf("corpus: encode %s: %w", spec.Name, err)
		}
		path := filepath.Join(root, PNGPath(spec))
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			return fmt.Errorf("corpus: write %s: %w", path, err)
		}
	}
	return nil
}

// Load reads the committed PNG for spec back from root/testdata/corpus.
func Load(root string, spec Spec) (image.Image, error) {
	path := filepath.Join(root, PNGPath(spec))
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("corpus: open %s: %w", path, err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("corpus: decode %s: %w", path, err)
	}
	return img, nil
}

// LoadAll reads every committed corpus PNG back from root/testdata/corpus,
// pairing each with its Spec.
func LoadAll(root string) ([]Image, error) {
	images := make([]Image, 0, len(Manifest))
	for _, spec := range Manifest {
		img, err := Load(root, spec)
		if err != nil {
			return nil, err
		}
		images = append(images, Image{Spec: spec, Img: img})
	}
	return images, nil
}
