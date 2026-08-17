package yuv_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"os"
	"testing"

	"m31labs.dev/tqwebp/internal/yuv"
	"m31labs.dev/tqwebp/oracle"
)

const fixtureDir = "../../testdata/golden/bt601"

// TestGoldenLibwebpPlanes is the forward half of the BT.601 pin. It reads
// a WebP file libwebp encoded from the colour ramp, decodes its planes,
// and compares them with the planes package yuv builds from the same
// pixels. Flat 16x16 patches carry no averaging or prediction question,
// so a difference could only come from the conversion coefficients.
//
// Measured on libwebp 1.5.0: luma agrees exactly, chroma differs by at
// most one. The tolerance of two leaves room for quantizer noise and
// still separates the limited-range convention from the full-range one,
// which differs by 16 or more (TestConventionIsLimitedRange).
func TestGoldenLibwebpPlanes(t *testing.T) {
	data := readFixture(t, "libwebp_q100.webp")
	planes, err := oracle.DecodeWebPPlanes(data)
	if err != nil {
		t.Fatalf("decode libwebp fixture: %v", err)
	}

	ours := yuv.Convert(yuv.ColorRamp())
	const tolerance = 2
	for i := 0; i < 64; i++ {
		x, y, r, g, b := yuv.RampPatchCenter(i)
		wantY := int(planes.Y[planes.YOffset(x, y)])
		wantU := int(planes.Cb[planes.COffset(x, y)])
		wantV := int(planes.Cr[planes.COffset(x, y)])
		gotY := int(ours.Y[y*ours.YStride+x])
		gotU := int(ours.U[(y/2)*ours.CStride+x/2])
		gotV := int(ours.V[(y/2)*ours.CStride+x/2])

		if absDiff(gotY, wantY) > tolerance || absDiff(gotU, wantU) > tolerance || absDiff(gotV, wantV) > tolerance {
			t.Errorf("patch %d rgb(%d,%d,%d): tqwebp Y=%d U=%d V=%d, libwebp Y=%d U=%d V=%d",
				i, r, g, b, gotY, gotU, gotV, wantY, wantU, wantV)
		}
	}
}

// TestRampMatchesFixture proves that the Go generator still builds the
// pixels libwebp was fed. A drift here would silence the golden test
// above without anyone noticing.
func TestRampMatchesFixture(t *testing.T) {
	data := readFixture(t, "ramp.png")
	m, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode ramp.png: %v", err)
	}
	want := yuv.ColorRamp()
	if m.Bounds() != want.Bounds() {
		t.Fatalf("ramp.png bounds %v, generator bounds %v", m.Bounds(), want.Bounds())
	}
	for y := 0; y < want.Bounds().Dy(); y++ {
		for x := 0; x < want.Bounds().Dx(); x++ {
			if m.At(x, y) != want.At(x, y) {
				t.Fatalf("ramp.png differs from the generator at (%d,%d)", x, y)
			}
		}
	}
	if got := sha256File(data); got != "277271d79eae191d85ec53c1d841550b6a45c3982cda3468393200b3f635794f" {
		t.Errorf("ramp.png hash %s does not match testdata/golden/bt601/manifest.json", got)
	}
}

// TestConventionIsLimitedRange pins the range endpoints. Full-range JFIF
// conversion maps black to 0 and white to 255; the limited-range BT.601
// convention WebP needs maps them to 16 and 235.
func TestConventionIsLimitedRange(t *testing.T) {
	cases := []struct {
		r, g, b    uint8
		y, u, v    uint8
		whatItIs   string
		fullRangeY uint8
	}{
		{0, 0, 0, 16, 128, 128, "black", 0},
		{255, 255, 255, 235, 128, 128, "white", 255},
		{255, 0, 0, 82, 90, 240, "red", 76},
		{0, 255, 0, 145, 54, 34, "green", 150},
		{0, 0, 255, 41, 240, 110, "blue", 29},
	}
	for _, c := range cases {
		gotY := yuv.RGBToY(c.r, c.g, c.b)
		gotU := yuv.RGBToU(c.r, c.g, c.b)
		gotV := yuv.RGBToV(c.r, c.g, c.b)
		if gotY != c.y || gotU != c.u || gotV != c.v {
			t.Errorf("%s: got Y=%d U=%d V=%d, want Y=%d U=%d V=%d", c.whatItIs, gotY, gotU, gotV, c.y, c.u, c.v)
		}
		if gotY == c.fullRangeY {
			t.Errorf("%s: luma %d equals the full-range JFIF value, so the conversion lost its limited-range convention", c.whatItIs, gotY)
		}
	}
}

// TestBoxAverageMatchesFlatPixel pins the chroma path: over a flat 2x2
// box, the box average must equal the single-pixel conversion.
func TestBoxAverageMatchesFlatPixel(t *testing.T) {
	for r := 0; r < 256; r += 5 {
		for g := 0; g < 256; g += 5 {
			for b := 0; b < 256; b += 5 {
				m := image.NewRGBA(image.Rect(0, 0, 2, 2))
				for i := 0; i < 4; i++ {
					m.Pix[4*i+0] = uint8(r)
					m.Pix[4*i+1] = uint8(g)
					m.Pix[4*i+2] = uint8(b)
					m.Pix[4*i+3] = 0xff
				}
				p := yuv.Convert(m)
				if got, want := p.U[0], yuv.RGBToU(uint8(r), uint8(g), uint8(b)); got != want {
					t.Fatalf("rgb(%d,%d,%d): box U %d, pixel U %d", r, g, b, got, want)
				}
				if got, want := p.V[0], yuv.RGBToV(uint8(r), uint8(g), uint8(b)); got != want {
					t.Fatalf("rgb(%d,%d,%d): box V %d, pixel V %d", r, g, b, got, want)
				}
			}
		}
	}
}

// TestPaddingReplicatesEdges pins the macroblock padding rule: padded
// columns and rows repeat the nearest visible pixel.
func TestPaddingReplicatesEdges(t *testing.T) {
	m := image.NewRGBA(image.Rect(0, 0, 3, 5))
	for y := 0; y < 5; y++ {
		for x := 0; x < 3; x++ {
			o := m.PixOffset(x, y)
			m.Pix[o+0] = uint8(40 * x)
			m.Pix[o+1] = uint8(30 * y)
			m.Pix[o+2] = 200
			m.Pix[o+3] = 0xff
		}
	}
	p := yuv.Convert(m)
	if p.MBW != 1 || p.MBH != 1 || p.YStride != 16 {
		t.Fatalf("planes are %dx%d macroblocks with luma stride %d, want 1x1 and 16", p.MBW, p.MBH, p.YStride)
	}
	for y := 0; y < 16; y++ {
		sy := min(y, 4)
		for x := 0; x < 16; x++ {
			sx := min(x, 2)
			want := yuv.RGBToY(uint8(40*sx), uint8(30*sy), 200)
			if got := p.Y[y*p.YStride+x]; got != want {
				t.Fatalf("luma at (%d,%d) is %d, want the replicated edge value %d", x, y, got, want)
			}
		}
	}
}

// TestConvertHonoursSourceOrigin pins that a sub-image with a non-zero
// origin converts from its own pixels, not from the wrong offset.
func TestConvertHonoursSourceOrigin(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for i := range full.Pix {
		full.Pix[i] = 0
	}
	sub := full.SubImage(image.Rect(8, 8, 24, 24)).(*image.RGBA)
	for y := 8; y < 24; y++ {
		for x := 8; x < 24; x++ {
			o := full.PixOffset(x, y)
			full.Pix[o+0], full.Pix[o+1], full.Pix[o+2], full.Pix[o+3] = 200, 100, 50, 255
		}
	}
	p := yuv.Convert(sub)
	want := yuv.RGBToY(200, 100, 50)
	for i := 0; i < 16*16; i++ {
		if p.Y[i] != want {
			t.Fatalf("luma sample %d is %d, want %d", i, p.Y[i], want)
		}
	}
}

// TestIsOpaque pins the alpha gate the public Encode function depends on.
func TestIsOpaque(t *testing.T) {
	opaque := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i := range opaque.Pix {
		opaque.Pix[i] = 0xff
	}
	if !yuv.IsOpaque(opaque) {
		t.Error("a fully opaque image reported alpha")
	}
	transparent := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	transparent.Pix[3] = 0x80
	if yuv.IsOpaque(transparent) {
		t.Error("an image with a translucent pixel reported opaque")
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(fixtureDir + "/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func sha256File(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
