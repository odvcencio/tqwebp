package predict

import (
	"slices"
	"testing"
)

func TestBModeValuesAndNames(t *testing.T) {
	want := []string{
		"B_DC_PRED", "B_TM_PRED", "B_VE_PRED", "B_HE_PRED", "B_RD_PRED",
		"B_VR_PRED", "B_LD_PRED", "B_VL_PRED", "B_HD_PRED", "B_HU_PRED",
	}
	if int(NumBModes) != len(want) {
		t.Fatalf("NumBModes = %d, want %d", NumBModes, len(want))
	}
	for i, name := range want {
		mode := BMode(i)
		if got := mode.String(); got != name {
			t.Errorf("BMode(%d).String() = %q, want %q", i, got, name)
		}
	}
	if got := NumBModes.String(); got != "invalid" {
		t.Fatalf("NumBModes.String() = %q, want invalid", got)
	}
}

func TestPredict4Golden(t *testing.T) {
	nb := BNeighbors{
		Top:    [8]uint8{20, 40, 70, 110, 160, 190, 210, 230},
		Left:   [4]uint8{30, 60, 100, 150},
		Corner: 10,
	}
	tests := []struct {
		mode BMode
		want [16]uint8
	}{
		{BDC, [16]uint8{73, 73, 73, 73, 73, 73, 73, 73, 73, 73, 73, 73, 73, 73, 73, 73}},
		{BTM, [16]uint8{40, 60, 90, 130, 70, 90, 120, 160, 110, 130, 160, 200, 160, 180, 210, 250}},
		{BVE, [16]uint8{23, 43, 73, 113, 23, 43, 73, 113, 23, 43, 73, 113, 23, 43, 73, 113}},
		{BHE, [16]uint8{33, 33, 33, 33, 63, 63, 63, 63, 103, 103, 103, 103, 138, 138, 138, 138}},
		{BRD, [16]uint8{18, 23, 43, 73, 33, 18, 23, 43, 63, 33, 18, 23, 103, 63, 33, 18}},
		{BVR, [16]uint8{15, 30, 55, 90, 18, 23, 43, 73, 33, 15, 30, 55, 63, 18, 23, 43}},
		{BLD, [16]uint8{43, 73, 113, 155, 73, 113, 155, 188, 113, 155, 188, 210, 155, 188, 210, 225}},
		{BVL, [16]uint8{30, 55, 90, 135, 43, 73, 113, 155, 55, 90, 135, 188, 73, 113, 155, 210}},
		{BHD, [16]uint8{20, 18, 23, 43, 45, 33, 20, 18, 80, 63, 45, 33, 125, 103, 80, 63}},
		{BHU, [16]uint8{45, 63, 80, 103, 80, 103, 125, 138, 125, 138, 150, 150, 150, 150, 150, 150}},
	}

	const stride = 7
	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			dst := make([]uint8, 4*stride)
			for i := range dst {
				dst[i] = 0xee
			}
			Predict4(dst, stride, tt.mode, &nb)

			var got [16]uint8
			for y := 0; y < 4; y++ {
				copy(got[y*4:y*4+4], dst[y*stride:y*stride+4])
				for x := 4; x < stride; x++ {
					if dst[y*stride+x] != 0xee {
						t.Fatalf("wrote stride padding at (%d,%d): got %d", x, y, dst[y*stride+x])
					}
				}
			}
			if got != tt.want {
				t.Fatalf("predictor =\n%v\nwant\n%v", got, tt.want)
			}
		})
	}
}

func TestPredict4TrueMotionClamps(t *testing.T) {
	nb := BNeighbors{
		Top:    [8]uint8{0, 255, 0, 255},
		Left:   [4]uint8{0, 255, 1, 254},
		Corner: 128,
	}
	var got [16]uint8
	Predict4(got[:], 4, BTM, &nb)
	want := [16]uint8{
		0, 127, 0, 127,
		127, 255, 127, 255,
		0, 128, 0, 128,
		126, 255, 126, 255,
	}
	if got != want {
		t.Fatalf("true-motion clamp = %v, want %v", got, want)
	}
}

func TestPredict4RejectsUnknownMode(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Predict4 accepted an unknown mode")
		}
	}()
	var dst [16]uint8
	Predict4(dst[:], 4, NumBModes, &BNeighbors{})
}

func TestBNeighborsForBlockBordersAndTopRight(t *testing.T) {
	const (
		stride = 40
		rows   = 32
		mbw    = 2
	)
	plane := make([]uint8, stride*rows)
	for y := 0; y < rows; y++ {
		for x := 0; x < stride; x++ {
			plane[y*stride+x] = uint8((y*73 + x*11 + 5) % 251)
		}
	}
	at := func(x, y int) uint8 { return plane[y*stride+x] }

	t.Run("frame top left", func(t *testing.T) {
		got := BNeighborsForBlock(plane, stride, mbw, 0, 0, 0)
		want := BNeighbors{
			Top:    [8]uint8{127, 127, 127, 127, 127, 127, 127, 127},
			Left:   [4]uint8{129, 129, 129, 129},
			Corner: 127,
		}
		checkBNeighbors(t, got, want)
	})

	t.Run("top-row right-edge subblock", func(t *testing.T) {
		got := BNeighborsForBlock(plane, stride, mbw, 0, 0, 7)
		want := BNeighbors{
			Top: [8]uint8{
				at(12, 3), at(13, 3), at(14, 3), at(15, 3),
				127, 127, 127, 127,
			},
			Left:   [4]uint8{at(11, 4), at(11, 5), at(11, 6), at(11, 7)},
			Corner: at(11, 3),
		}
		checkBNeighbors(t, got, want)
	})

	t.Run("next macroblock supplies top right", func(t *testing.T) {
		got := BNeighborsForBlock(plane, stride, mbw, 0, 1, 3)
		want := BNeighbors{
			Top: [8]uint8{
				at(12, 15), at(13, 15), at(14, 15), at(15, 15),
				at(16, 15), at(17, 15), at(18, 15), at(19, 15),
			},
			Left:   [4]uint8{at(11, 16), at(11, 17), at(11, 18), at(11, 19)},
			Corner: at(11, 15),
		}
		checkBNeighbors(t, got, want)
	})

	t.Run("right frame edge repeats macroblock top", func(t *testing.T) {
		got := BNeighborsForBlock(plane, stride, mbw, 1, 1, 7)
		right := at(31, 15)
		want := BNeighbors{
			Top: [8]uint8{
				at(28, 19), at(29, 19), at(30, 19), at(31, 19),
				right, right, right, right,
			},
			Left:   [4]uint8{at(27, 20), at(27, 21), at(27, 22), at(27, 23)},
			Corner: at(27, 19),
		}
		checkBNeighbors(t, got, want)
	})

	t.Run("internal subblock reads immediate reconstruction", func(t *testing.T) {
		got := BNeighborsForBlock(plane, stride, mbw, 0, 1, 5)
		want := BNeighbors{
			Top: [8]uint8{
				at(4, 19), at(5, 19), at(6, 19), at(7, 19),
				at(8, 19), at(9, 19), at(10, 19), at(11, 19),
			},
			Left:   [4]uint8{at(3, 20), at(3, 21), at(3, 22), at(3, 23)},
			Corner: at(3, 19),
		}
		checkBNeighbors(t, got, want)
	})

	t.Run("left frame edge", func(t *testing.T) {
		got := BNeighborsForBlock(plane, stride, mbw, 0, 1, 4)
		want := BNeighbors{
			Top: [8]uint8{
				at(0, 19), at(1, 19), at(2, 19), at(3, 19),
				at(4, 19), at(5, 19), at(6, 19), at(7, 19),
			},
			Left:   [4]uint8{129, 129, 129, 129},
			Corner: 129,
		}
		checkBNeighbors(t, got, want)
	})

	t.Run("all macroblock-right subblocks reuse cached extension", func(t *testing.T) {
		want := []uint8{at(16, 15), at(17, 15), at(18, 15), at(19, 15)}
		for _, block := range []int{3, 7, 11, 15} {
			got := BNeighborsForBlock(plane, stride, mbw, 0, 1, block)
			if !slices.Equal(got.Top[4:], want) {
				t.Errorf("block %d top-right = %v, want %v", block, got.Top[4:], want)
			}
		}
	})

	t.Run("all frame-right subblocks repeat cached top sample", func(t *testing.T) {
		right := at(31, 15)
		want := []uint8{right, right, right, right}
		for _, block := range []int{3, 7, 11, 15} {
			got := BNeighborsForBlock(plane, stride, mbw, 1, 1, block)
			if !slices.Equal(got.Top[4:], want) {
				t.Errorf("block %d top-right = %v, want %v", block, got.Top[4:], want)
			}
		}
	})

	t.Run("all top-row right subblocks extend with missing top", func(t *testing.T) {
		want := []uint8{127, 127, 127, 127}
		for _, block := range []int{3, 7, 11, 15} {
			got := BNeighborsForBlock(plane, stride, mbw, 0, 0, block)
			if !slices.Equal(got.Top[4:], want) {
				t.Errorf("block %d top-right = %v, want %v", block, got.Top[4:], want)
			}
		}
	})

	// This models a visible 19x17 image. Its reconstructed plane is padded to
	// 32x32, and VP8 predictions intentionally use the reconstructed padded
	// sample at x=31 rather than the visible sample at x=18.
	t.Run("odd visible boundary uses padded reconstruction", func(t *testing.T) {
		got := BNeighborsForBlock(plane, stride, mbw, 1, 1, 15)
		right := at(31, 15)
		want := BNeighbors{
			Top: [8]uint8{
				at(28, 27), at(29, 27), at(30, 27), at(31, 27),
				right, right, right, right,
			},
			Left:   [4]uint8{at(27, 28), at(27, 29), at(27, 30), at(27, 31)},
			Corner: at(27, 27),
		}
		checkBNeighbors(t, got, want)
	})
}

func TestBNeighborsForBlockRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{"zero macroblock width", func() { BNeighborsForBlock(make([]uint8, 256), 16, 0, 0, 0, 0) }},
		{"macroblock x outside plane", func() { BNeighborsForBlock(make([]uint8, 256), 16, 1, 1, 0, 0) }},
		{"subblock outside macroblock", func() { BNeighborsForBlock(make([]uint8, 256), 16, 1, 0, 0, 16) }},
		{"short stride", func() { BNeighborsForBlock(make([]uint8, 16*16), 15, 1, 0, 0, 0) }},
		{"ragged plane", func() { BNeighborsForBlock(make([]uint8, 16*16+1), 16, 1, 0, 0, 0) }},
		{"block below plane", func() { BNeighborsForBlock(make([]uint8, 16*16), 16, 1, 0, 1, 0) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("BNeighborsForBlock accepted invalid input")
				}
			}()
			tt.fn()
		})
	}
}

func checkBNeighbors(t *testing.T, got, want BNeighbors) {
	t.Helper()
	if got != want {
		t.Fatalf("neighbors = %+v, want %+v", got, want)
	}
}
