package yuv

import (
	"image"
	"slices"
	"testing"

	"m31labs.dev/tqwebp/oracle"
)

func TestSharpInverseMatchesBrowserOracle(t *testing.T) {
	values := []uint8{0, 1, 15, 16, 31, 63, 127, 128, 191, 235, 240, 254, 255}
	for _, y := range values {
		for _, u := range values {
			for _, v := range values {
				got := sharpYUVToRGB(y, u, v)
				r, g, b := oracle.YUVToRGB(y, u, v)
				if got.r != r || got.g != g || got.b != b {
					t.Fatalf("YUV(%d,%d,%d): sharp inverse RGB(%d,%d,%d), oracle RGB(%d,%d,%d)",
						y, u, v, got.r, got.g, got.b, r, g, b)
				}
			}
		}
	}
}

func TestConvertSharpImprovesSaturatedEdgeRGB(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	colors := [4]sharpRGB{
		{r: 250, g: 250, b: 248}, {r: 60, g: 170, b: 100},
		{r: 250, g: 250, b: 248}, {r: 60, g: 170, b: 100},
	}
	for i, p := range colors {
		src.Pix[4*i+0], src.Pix[4*i+1], src.Pix[4*i+2], src.Pix[4*i+3] = p.r, p.g, p.b, 0xff
	}

	fast := Convert(src)
	uniformOnly := ConvertSharp(src, false)
	sharp := ConvertSharp(src, true)
	if fast.Y[0] != uniformOnly.Y[0] || fast.Y[1] != uniformOnly.Y[1] || fast.Y[fast.YStride] != uniformOnly.Y[uniformOnly.YStride] || fast.Y[fast.YStride+1] != uniformOnly.Y[uniformOnly.YStride+1] || fast.U[0] != uniformOnly.U[0] || fast.V[0] != uniformOnly.V[0] {
		t.Fatal("uniform-only sharp conversion changed a mixed hard-edge box")
	}
	fastErr := visibleRGBError(colors, fast)
	sharpErr := visibleRGBError(colors, sharp)
	if sharpErr >= fastErr {
		t.Fatalf("sharp RGB error %d is not below fast error %d", sharpErr, fastErr)
	}
	if sharpErr*2 >= fastErr {
		t.Fatalf("sharp RGB error %d did not cut the saturated-edge error by at least half from %d", sharpErr, fastErr)
	}

	repeat := ConvertSharp(src, true)
	if !slices.Equal(sharp.Y, repeat.Y) || !slices.Equal(sharp.U, repeat.U) || !slices.Equal(sharp.V, repeat.V) {
		t.Fatal("sharp conversion is not deterministic")
	}
}

func TestConvertSharpImprovesLumaWithoutMovingEqualRGBError(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i := 0; i < 4; i++ {
		src.Pix[4*i+0], src.Pix[4*i+1], src.Pix[4*i+2], src.Pix[4*i+3] = 60, 170, 100, 0xff
	}
	fast := Convert(src)
	sharp := ConvertSharp(src, false)
	want := [4]sharpRGB{{r: 60, g: 170, b: 100}, {r: 60, g: 170, b: 100}, {r: 60, g: 170, b: 100}, {r: 60, g: 170, b: 100}}
	if gotFast, gotSharp := visibleRGBError(want, fast), visibleRGBError(want, sharp); gotSharp > gotFast {
		t.Fatalf("sharp RGB error %d exceeds fast error %d", gotSharp, gotFast)
	}
	if gotFast, gotSharp := visibleLumaError(want, fast), visibleLumaError(want, sharp); gotSharp >= gotFast {
		t.Fatalf("sharp luma error %d is not below fast error %d", gotSharp, gotFast)
	}
}

func TestConvertSharpLeavesLowContrastMixedBoxOnFastPath(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i := 0; i < 4; i++ {
		src.Pix[4*i+0] = uint8(100 + i)
		src.Pix[4*i+1] = uint8(120 + i)
		src.Pix[4*i+2] = uint8(140 + i)
		src.Pix[4*i+3] = 0xff
	}
	fast := Convert(src)
	sharp := ConvertSharp(src, true)
	if fast.Y[0] != sharp.Y[0] || fast.Y[1] != sharp.Y[1] || fast.Y[fast.YStride] != sharp.Y[sharp.YStride] || fast.Y[fast.YStride+1] != sharp.Y[sharp.YStride+1] || fast.U[0] != sharp.U[0] || fast.V[0] != sharp.V[0] {
		t.Fatalf("low-contrast visible box changed: fast YUV=(%d,%d,%d,%d,%d,%d), sharp=(%d,%d,%d,%d,%d,%d)",
			fast.Y[0], fast.Y[1], fast.Y[fast.YStride], fast.Y[fast.YStride+1], fast.U[0], fast.V[0],
			sharp.Y[0], sharp.Y[1], sharp.Y[sharp.YStride], sharp.Y[sharp.YStride+1], sharp.U[0], sharp.V[0])
	}
}

func visibleRGBError(want [4]sharpRGB, p *Planes) int64 {
	var total int64
	for i, target := range want {
		x, y := i%2, i/2
		got := sharpYUVToRGB(p.Y[y*p.YStride+x], p.U[0], p.V[0])
		total += sharpRGBError(target, got)
	}
	return total
}

func visibleLumaError(want [4]sharpRGB, p *Planes) int64 {
	var total int64
	for i, target := range want {
		x, y := i%2, i/2
		got := sharpYUVToRGB(p.Y[y*p.YStride+x], p.U[0], p.V[0])
		total += sharpLumaError(target, got)
	}
	return total
}
