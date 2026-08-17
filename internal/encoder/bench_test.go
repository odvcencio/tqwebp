package encoder

import (
	"testing"

	"m31labs.dev/tqwebp/internal/corpus"
)

// BenchmarkEncodePhoto measures single-thread encode speed on a one
// megapixel photograph, which is the reading gate G3 reports.
func BenchmarkEncodePhoto(b *testing.B) {
	img := corpus.Generate(corpus.Spec{Name: "b", Class: corpus.Photo, Width: 1000, Height: 1000, Seed: 77})
	cfg := Config{Quality: 75, Method: 4}
	b.SetBytes(1000 * 1000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var buf byteWriter
		if err := Encode(&buf, img, cfg); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncodeScreenshot measures the same on hard-edge content, where
// more macroblocks carry coefficients.
func BenchmarkEncodeScreenshot(b *testing.B) {
	img := corpus.Generate(corpus.Spec{Name: "b", Class: corpus.Screenshot, Width: 1000, Height: 1000, Seed: 78})
	cfg := Config{Quality: 75, Method: 4}
	b.SetBytes(1000 * 1000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var buf byteWriter
		if err := Encode(&buf, img, cfg); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncodeCorpusPhoto encodes the committed corpus photograph, so
// the benchmark reading and the gate reading measure the same picture.
func BenchmarkEncodeCorpusPhoto(b *testing.B) {
	images, err := corpus.LoadAll(moduleRoot)
	if err != nil {
		b.Fatal(err)
	}
	var img = images[0].Img
	var px int
	for _, im := range images {
		if im.Spec.Name == "photo_gradient_wide" {
			img = im.Img
			px = im.Spec.Width * im.Spec.Height
		}
	}
	cfg := Config{Quality: 75, Method: 4}
	b.SetBytes(int64(px))
	for i := 0; i < b.N; i++ {
		var buf byteWriter
		if err := Encode(&buf, img, cfg); err != nil {
			b.Fatal(err)
		}
	}
}
