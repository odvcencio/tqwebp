package oracle

import (
	"image"
	"image/color"
	"testing"
)

func TestCompareExact_StubMatchesItself(t *testing.T) {
	src := checkerboardImage(32, 32)
	stub := NewStubReconstructionSource(src)

	planes, err := stub.ReconstructionPlanes()
	if err != nil {
		t.Fatalf("ReconstructionPlanes: %v", err)
	}

	// Compare the stub's own planes against themselves through the
	// *image.YCbCr fast path: this is the loop-filter-0 contract's exact
	// success case (RFC 6386 §15), reproduced without a real encoder.
	if err := CompareExact(stub, planes); err != nil {
		t.Fatalf("CompareExact(identical planes) = %v, want nil", err)
	}
}

func TestCompareExact_DetectsMismatch(t *testing.T) {
	src := checkerboardImage(32, 32)
	stub := NewStubReconstructionSource(src)

	planes, err := stub.ReconstructionPlanes()
	if err != nil {
		t.Fatalf("ReconstructionPlanes: %v", err)
	}
	mutated := *planes
	mutatedY := make([]uint8, len(planes.Y))
	copy(mutatedY, planes.Y)
	mutatedY[0] ^= 0xFF
	mutated.Y = mutatedY

	if err := CompareExact(stub, &mutated); err == nil {
		t.Fatal("CompareExact(mutated planes): want error, got nil")
	}
}

func TestCompareExact_GenericFallback(t *testing.T) {
	src := checkerboardImage(16, 16)
	stub := NewStubReconstructionSource(src)

	// An RGBA decode result exercises compareExactGeneric instead of the
	// *image.YCbCr fast path.
	rgba := image.NewRGBA(src.Bounds())
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			rgba.Set(x, y, src.At(x, y))
		}
	}
	if err := CompareExact(stub, rgba); err != nil {
		t.Fatalf("CompareExact(generic fallback, identical source) = %v, want nil", err)
	}
}

func TestCompareExact_BoundsMismatch(t *testing.T) {
	src := checkerboardImage(16, 16)
	stub := NewStubReconstructionSource(src)
	other := image.NewYCbCr(image.Rect(0, 0, 8, 8), image.YCbCrSubsampleRatio444)
	if err := CompareExact(stub, other); err == nil {
		t.Fatal("CompareExact(bounds mismatch): want error, got nil")
	}
}

func TestReconstructionSource_InterfaceSatisfiedByStub(t *testing.T) {
	var _ ReconstructionSource = (*StubReconstructionSource)(nil)
}

func TestNewStubReconstructionSource_PreservesColor(t *testing.T) {
	src := solidImage(4, 4, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	stub := NewStubReconstructionSource(src)
	planes, err := stub.ReconstructionPlanes()
	if err != nil {
		t.Fatalf("ReconstructionPlanes: %v", err)
	}
	wantY, wantCb, wantCr := color.RGBToYCbCr(10, 20, 30)
	gotY := planes.Y[planes.YOffset(0, 0)]
	gotCb := planes.Cb[planes.COffset(0, 0)]
	gotCr := planes.Cr[planes.COffset(0, 0)]
	if gotY != wantY || gotCb != wantCb || gotCr != wantCr {
		t.Fatalf("stub planes = (%d,%d,%d), want (%d,%d,%d)", gotY, gotCb, gotCr, wantY, wantCb, wantCr)
	}
}
