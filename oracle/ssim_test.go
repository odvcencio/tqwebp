package oracle

import (
	"image/color"
	"math"
	"testing"
)

func TestSSIM_IdenticalPlanesIsOne(t *testing.T) {
	plane := make([][]uint8, 16)
	for y := range plane {
		row := make([]uint8, 16)
		for x := range row {
			row[x] = uint8((x*7 + y*13) % 256)
		}
		plane[y] = row
	}
	got, err := SSIM(plane, plane)
	if err != nil {
		t.Fatalf("SSIM: %v", err)
	}
	if math.Abs(got-1) > 1e-9 {
		t.Fatalf("SSIM(identical) = %v, want 1", got)
	}
}

func TestSSIM_DegradesWithNoise(t *testing.T) {
	const n = 32
	base := make([][]uint8, n)
	mild := make([][]uint8, n)
	severe := make([][]uint8, n)
	for y := 0; y < n; y++ {
		base[y] = make([]uint8, n)
		mild[y] = make([]uint8, n)
		severe[y] = make([]uint8, n)
		for x := 0; x < n; x++ {
			v := uint8((x*5 + y*3) % 200)
			base[y][x] = v

			mv := int(v) + ((x + y) % 5) - 2
			mild[y][x] = clampByteForTest(mv)

			sv := int(v) + ((x*37+y*19)%121 - 60)
			severe[y][x] = clampByteForTest(sv)
		}
	}

	ssimMild, err := SSIM(base, mild)
	if err != nil {
		t.Fatalf("SSIM(mild): %v", err)
	}
	ssimSevere, err := SSIM(base, severe)
	if err != nil {
		t.Fatalf("SSIM(severe): %v", err)
	}
	if ssimMild <= ssimSevere {
		t.Fatalf("SSIM did not fall with more distortion: mild=%v severe=%v", ssimMild, ssimSevere)
	}
	if ssimMild > 1 || ssimSevere < -1 {
		t.Fatalf("SSIM out of range: mild=%v severe=%v", ssimMild, ssimSevere)
	}
}

func clampByteForTest(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func TestSSIM_SizeMismatch(t *testing.T) {
	a := [][]uint8{{1, 2}, {3, 4}}
	b := [][]uint8{{1, 2}}
	if _, err := SSIM(a, b); err == nil {
		t.Fatal("SSIM: want error on height mismatch, got nil")
	}
}

func TestSSIM_NonMultipleOfWindow(t *testing.T) {
	// 10x10 plane does not divide evenly into 8x8 windows; SSIM must still
	// clip the trailing windows instead of erroring or panicking.
	plane := make([][]uint8, 10)
	for y := range plane {
		row := make([]uint8, 10)
		for x := range row {
			row[x] = uint8((x + y) * 10)
		}
		plane[y] = row
	}
	got, err := SSIM(plane, plane)
	if err != nil {
		t.Fatalf("SSIM: %v", err)
	}
	if math.Abs(got-1) > 1e-9 {
		t.Fatalf("SSIM(identical, non-multiple size) = %v, want 1", got)
	}
}

func TestMeasureSSIM_IdenticalImages(t *testing.T) {
	img := solidImage(9, 9, color.RGBA{R: 12, G: 200, B: 88, A: 255})
	got, err := MeasureSSIM(img, img)
	if err != nil {
		t.Fatalf("MeasureSSIM: %v", err)
	}
	if math.Abs(got-1) > 1e-9 {
		t.Fatalf("MeasureSSIM(identical) = %v, want 1", got)
	}
}

func TestMeasureSSIM_SizeMismatch(t *testing.T) {
	a := solidImage(4, 4, color.RGBA{A: 255})
	b := solidImage(4, 5, color.RGBA{A: 255})
	if _, err := MeasureSSIM(a, b); err == nil {
		t.Fatal("MeasureSSIM: want error on size mismatch, got nil")
	}
}
