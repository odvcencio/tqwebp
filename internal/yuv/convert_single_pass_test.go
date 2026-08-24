package yuv

import (
	"image"
	"image/color"
	"testing"
)

type countingImage struct {
	image.Image
	calls int
}

func (m *countingImage) At(x, y int) color.Color {
	m.calls++
	return m.Image.At(x, y)
}

func TestConvertSamplesEachPaddedPixelOnce(t *testing.T) {
	src := image.NewRGBA(image.Rect(3, 5, 20, 24))
	m := &countingImage{Image: src}
	p := Convert(m)
	want := p.YStride * p.MBH * 16
	if m.calls != want {
		t.Fatalf("source samples = %d, want %d (one per padded luma position)", m.calls, want)
	}
}

func BenchmarkProbeConvertRGBA512(b *testing.B) {
	src := image.NewRGBA(image.Rect(0, 0, 512, 512))
	for i := range src.Pix {
		src.Pix[i] = uint8(i * 7)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Convert(src)
	}
}
