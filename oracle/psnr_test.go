package oracle

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func solidImage(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestPSNR_IdenticalPlanesIsInf(t *testing.T) {
	a := [][]uint8{{10, 20}, {30, 40}}
	b := [][]uint8{{10, 20}, {30, 40}}
	got, err := PSNR(a, b)
	if err != nil {
		t.Fatalf("PSNR: %v", err)
	}
	if got != PSNRInf {
		t.Fatalf("PSNR(identical) = %v, want PSNRInf", got)
	}
}

func TestPSNR_KnownMSE(t *testing.T) {
	// Single-pixel planes differing by 10 everywhere: MSE = 100,
	// PSNR = 10*log10(65025/100) = 28.129...
	a := [][]uint8{{100}}
	b := [][]uint8{{110}}
	got, err := PSNR(a, b)
	if err != nil {
		t.Fatalf("PSNR: %v", err)
	}
	want := 10 * math.Log10(255*255/100.0)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("PSNR = %v, want %v", got, want)
	}
}

func TestPSNR_SizeMismatch(t *testing.T) {
	a := [][]uint8{{1, 2}}
	b := [][]uint8{{1}}
	if _, err := PSNR(a, b); err == nil {
		t.Fatal("PSNR: want error on width mismatch, got nil")
	}
}

func TestPSNR_MoreNoiseLowersScore(t *testing.T) {
	a := [][]uint8{{100, 100, 100, 100}}
	small := [][]uint8{{102, 98, 103, 99}}
	large := [][]uint8{{140, 60, 150, 50}}

	psnrSmall, err := PSNR(a, small)
	if err != nil {
		t.Fatalf("PSNR: %v", err)
	}
	psnrLarge, err := PSNR(a, large)
	if err != nil {
		t.Fatalf("PSNR: %v", err)
	}
	if psnrSmall <= psnrLarge {
		t.Fatalf("PSNR did not fall with more distortion: small-noise=%v large-noise=%v", psnrSmall, psnrLarge)
	}
}

func TestMeasurePSNR_IdenticalImages(t *testing.T) {
	img := solidImage(4, 4, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	got, err := MeasurePSNR(img, img)
	if err != nil {
		t.Fatalf("MeasurePSNR: %v", err)
	}
	for name, v := range map[string]float64{
		"Y": got.Y, "Cb": got.Cb, "Cr": got.Cr, "R": got.R, "G": got.G, "B": got.B,
	} {
		if v != PSNRInf {
			t.Errorf("MeasurePSNR(identical).%s = %v, want PSNRInf", name, v)
		}
	}
}

func TestMeasurePSNR_SizeMismatch(t *testing.T) {
	a := solidImage(4, 4, color.RGBA{A: 255})
	b := solidImage(5, 4, color.RGBA{A: 255})
	if _, err := MeasurePSNR(a, b); err == nil {
		t.Fatal("MeasurePSNR: want error on size mismatch, got nil")
	}
}
