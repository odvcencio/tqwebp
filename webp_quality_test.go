package webp

import (
	"bytes"
	"fmt"
	"testing"

	"m31labs.dev/tqwebp/internal/corpus"
	"m31labs.dev/tqwebp/oracle"
)

// Exact decoder reconstruction does not detect an encoder that discards too
// much detail. These small, reproducible fixtures gate Method 6 against the
// retained-quantization Method 5 path. The external real-image acceptance
// sweep in bench/corpuscompare complements this fast CI regression test.
// Noisy generated content permits .01 SSIM / .75 dB; the preserved real
// corpus has a tighter .005 SSIM / .5 dB budget. These catch collapse,
// rather than promising identical pictures across effort levels.
func TestMethod6QualityRegression(t *testing.T) {
	for _, class := range []corpus.Class{corpus.Photo, corpus.Screenshot, corpus.Flat} {
		img := corpus.Generate(corpus.Spec{Name: "quality-regression", Class: class, Width: 128, Height: 96, Seed: 77})
		for _, q := range []int{25, 50, 75, 90, 100} {
			t.Run(fmt.Sprintf("%v/q%d", class, q), func(t *testing.T) {
				var scores [2]float64
				var psnr [2]float64
				for i, method := range []int{5, 6} {
					var b bytes.Buffer
					if err := Encode(&b, img, &Options{Quality: q, Method: method}); err != nil {
						t.Fatal(err)
					}
					dec, err := oracle.DecodeWebP(b.Bytes())
					if err != nil {
						t.Fatal(err)
					}
					scores[i], err = oracle.MeasureSSIM(img, dec)
					if err != nil {
						t.Fatal(err)
					}
					p, err := oracle.MeasurePSNR(img, dec)
					if err != nil {
						t.Fatal(err)
					}
					psnr[i] = p.Y
				}
				if scores[1] < scores[0]-0.01 || psnr[1] < psnr[0]-0.75 {
					t.Fatalf("method 6 lost detail: SSIM %v; luma PSNR %v", scores, psnr)
				}
			})
		}
	}
}
