// Package corpus generates the deterministic WP-0 test corpus: three
// content classes (photo, screenshot, flat) at fixed sizes, built from a
// seeded pseudo-random generator instead of fetched images. Every image in
// testdata/corpus/ reproduces byte-for-byte from this package, so the
// corpus carries no licensing risk and needs no network access.
package corpus

import (
	"image"
	"image/color"
)

func clampByte(v float64) uint8 {
	switch {
	case v < 0:
		return 0
	case v > 255:
		return 255
	default:
		return uint8(v)
	}
}

// Generate renders spec into an in-memory RGBA image using the generator
// for spec.Class. The output is fully determined by spec: the same Spec
// always yields the same pixels.
func Generate(spec Spec) *image.RGBA {
	switch spec.Class {
	case Screenshot:
		return generateScreenshot(spec)
	case Flat:
		return generateFlat(spec)
	default:
		return generatePhoto(spec)
	}
}

// generatePhoto renders a smooth low-frequency gradient, three octaves of
// value noise for structured detail, and mild per-pixel noise on top: a
// stand-in for photographic content free of hard edges.
func generatePhoto(spec Spec) *image.RGBA {
	w, h := spec.Width, spec.Height
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := newRNG(spec.Seed)

	// Gradient palette: two endpoint colors and a phase, all seeded.
	r0, g0, b0 := 40+r.intn(120), 60+r.intn(120), 90+r.intn(120)
	r1, g1, b1 := 40+r.intn(120), 60+r.intn(120), 90+r.intn(120)
	phase := r.float64() * 6.283185307

	// Three octaves of value noise, coarse to fine, for structured detail.
	octaves := []struct {
		cells int
		amp   float64
	}{
		{cells: 6, amp: 40},
		{cells: 13, amp: 22},
		{cells: 29, amp: 10},
	}
	grids := make([][][]float64, len(octaves))
	for oi, oct := range octaves {
		gw, gh := oct.cells+1, oct.cells+1
		grid := make([][]float64, gh)
		for y := 0; y < gh; y++ {
			grid[y] = make([]float64, gw)
			for x := 0; x < gw; x++ {
				grid[y][x] = r.signedNoise(oct.amp)
			}
		}
		grids[oi] = grid
	}

	for y := 0; y < h; y++ {
		fy := float64(y) / float64(h-1+boolToInt(h == 1))
		for x := 0; x < w; x++ {
			fx := float64(x) / float64(w-1+boolToInt(w == 1))

			// Smooth gradient base, modulated by a slow sinusoid so flat
			// diagonal banding does not dominate the frame.
			mix := 0.5 + 0.5*sin(phase+fx*3.14159265+fy*1.5707963)
			cr := lerp(float64(r0), float64(r1), mix)
			cg := lerp(float64(g0), float64(g1), mix)
			cb := lerp(float64(b0), float64(b1), mix)

			var detail float64
			for oi, oct := range octaves {
				detail += bilinear(grids[oi], fx, fy, oct.cells)
			}

			jitter := r.signedNoise(6)

			img.SetRGBA(x, y, color.RGBA{
				R: clampByte(cr + detail + jitter),
				G: clampByte(cg + detail*0.9 + jitter),
				B: clampByte(cb + detail*0.8 + jitter),
				A: 255,
			})
		}
	}
	return img
}

// generateScreenshot renders a flat background, a grid of solid panel
// rectangles with hard 1px borders, and dense dash blocks that stand in
// for rendered text: a stand-in for captured user-interface content.
func generateScreenshot(spec Spec) *image.RGBA {
	w, h := spec.Width, spec.Height
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := newRNG(spec.Seed)

	bg := color.RGBA{R: 246, G: 247, B: 249, A: 255}
	fillRect(img, 0, 0, w, h, bg)

	border := color.RGBA{R: 210, G: 213, B: 218, A: 255}
	palette := []color.RGBA{
		{R: 255, G: 255, B: 255, A: 255},
		{R: 33, G: 100, B: 220, A: 255},
		{R: 235, G: 238, B: 242, A: 255},
		{R: 40, G: 44, B: 52, A: 255},
		{R: 220, G: 60, B: 60, A: 255},
	}
	textInk := color.RGBA{R: 60, G: 64, B: 72, A: 255}

	panels := 4 + r.intn(4)
	for i := 0; i < panels; i++ {
		pw := 60 + r.intn(maxInt(w/3, 1))
		ph := 30 + r.intn(maxInt(h/3, 1))
		px := r.intn(maxInt(w-pw, 1))
		py := r.intn(maxInt(h-ph, 1))
		fill := palette[r.intn(len(palette))]

		fillRect(img, px, py, pw, ph, fill)
		strokeRect(img, px, py, pw, ph, border)

		// Dash blocks: short horizontal bars simulating lines of text.
		lines := 2 + r.intn(6)
		for l := 0; l < lines; l++ {
			ly := py + 6 + l*9
			if ly+2 >= py+ph {
				break
			}
			lx := px + 6
			remaining := pw - 12
			for remaining > 4 {
				dashLen := 4 + r.intn(maxInt(minInt(remaining, 30), 1))
				fillRect(img, lx, ly, dashLen, 2, textInk)
				gap := 3 + r.intn(6)
				lx += dashLen + gap
				remaining -= dashLen + gap
			}
		}
	}
	return img
}

// generateFlat renders a handful of large solid regions from a small fixed
// palette, split by straight cuts: a stand-in for logos and line art.
func generateFlat(spec Spec) *image.RGBA {
	w, h := spec.Width, spec.Height
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := newRNG(spec.Seed)

	palette := []color.RGBA{
		{R: 250, G: 250, B: 248, A: 255},
		{R: 20, G: 30, B: 40, A: 255},
		{R: 230, G: 90, B: 40, A: 255},
		{R: 40, G: 140, B: 200, A: 255},
		{R: 250, G: 200, B: 40, A: 255},
		{R: 60, G: 170, B: 100, A: 255},
	}

	fillRect(img, 0, 0, w, h, palette[0])

	regions := 4 + r.intn(4)
	for i := 0; i < regions; i++ {
		rw := w/3 + r.intn(maxInt(w/2, 1))
		rh := h/3 + r.intn(maxInt(h/2, 1))
		rx := r.intn(maxInt(w-1, 1))
		ry := r.intn(maxInt(h-1, 1))
		c := palette[1+r.intn(len(palette)-1)]
		fillRect(img, rx, ry, rw, rh, c)
	}
	return img
}

func fillRect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	bounds := img.Bounds()
	x1, y1 := clampInt(x+w, bounds.Min.X, bounds.Max.X), clampInt(y+h, bounds.Min.Y, bounds.Max.Y)
	x0, y0 := clampInt(x, bounds.Min.X, bounds.Max.X), clampInt(y, bounds.Min.Y, bounds.Max.Y)
	for py := y0; py < y1; py++ {
		for px := x0; px < x1; px++ {
			img.SetRGBA(px, py, c)
		}
	}
}

func strokeRect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	fillRect(img, x, y, w, 1, c)
	fillRect(img, x, y+h-1, w, 1, c)
	fillRect(img, x, y, 1, h, c)
	fillRect(img, x+w-1, y, 1, h, c)
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

// bilinear samples a (cells+1)x(cells+1) value grid at fractional
// coordinates (fx, fy) in [0, 1], interpolating between the four nearest
// grid points.
func bilinear(grid [][]float64, fx, fy float64, cells int) float64 {
	gx := fx * float64(cells)
	gy := fy * float64(cells)
	x0 := int(gx)
	y0 := int(gy)
	x1, y1 := x0+1, y0+1
	if x1 > cells {
		x1 = cells
	}
	if y1 > cells {
		y1 = cells
	}
	tx := gx - float64(x0)
	ty := gy - float64(y0)

	top := lerp(grid[y0][x0], grid[y0][x1], tx)
	bottom := lerp(grid[y1][x0], grid[y1][x1], tx)
	return lerp(top, bottom, ty)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// sin is a small Taylor-series sine, accurate enough for a smooth gradient
// phase. It stays local instead of calling math.Sin so the generator never
// depends on a platform-specific FPU intrinsic: the corpus must render to
// the same bytes on every architecture that builds this module.
func sin(x float64) float64 {
	// Reduce to [-pi, pi].
	const twoPi = 6.283185307179586
	for x > 3.141592653589793 {
		x -= twoPi
	}
	for x < -3.141592653589793 {
		x += twoPi
	}
	x2 := x * x
	// 7-term Taylor series, more than accurate enough for pixel-scale use.
	return x * (1 - x2/6*(1-x2/20*(1-x2/42*(1-x2/72))))
}
