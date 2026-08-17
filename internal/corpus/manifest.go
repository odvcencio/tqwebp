package corpus

// Class names one of the three content classes the WP-0 spec requires.
type Class string

const (
	// Photo covers smooth gradients, layered structured detail, and mild
	// per-pixel noise: a stand-in for photographic content.
	Photo Class = "photo"
	// Screenshot covers hard edges, flat runs, and text-like dash blocks:
	// a stand-in for captured user-interface content.
	Screenshot Class = "screenshot"
	// Flat covers large solid regions built from a small palette: a
	// stand-in for logos and line art.
	Flat Class = "flat"
)

// Spec describes one generated corpus image: its name, class, size, and
// the seed that makes its generation reproducible.
type Spec struct {
	// Name is the file stem. The generator writes testdata/corpus/<Name>.png.
	Name string
	// Class selects the generator algorithm.
	Class Class
	// Width and Height are the image dimensions in pixels.
	Width, Height int
	// Seed feeds the deterministic generator. Two runs with the same Spec
	// produce byte-identical PNG output.
	Seed uint64
	// Note explains why a Spec exists beyond its class, for edge cases.
	Note string
}

// Manifest is the fixed list of corpus images this package generates. It
// covers all three content classes, a few images at the 1200x800 class,
// one small 64x64-class edge case, and one prime-dimension edge case that
// exercises macroblock padding.
var Manifest = []Spec{
	{Name: "photo_gradient_wide", Class: Photo, Width: 1200, Height: 800, Seed: 0x1001},
	{Name: "photo_texture_detail", Class: Photo, Width: 1024, Height: 768, Seed: 0x1002},
	{Name: "photo_noise_detail", Class: Photo, Width: 1280, Height: 800, Seed: 0x1003},

	{Name: "screenshot_dashboard", Class: Screenshot, Width: 1200, Height: 800, Seed: 0x2001},
	{Name: "screenshot_panel_grid", Class: Screenshot, Width: 1024, Height: 768, Seed: 0x2002},

	{Name: "flatart_quadrant", Class: Flat, Width: 1200, Height: 800, Seed: 0x3001},
	{Name: "flatart_logo_blocks", Class: Flat, Width: 640, Height: 480, Seed: 0x3002},

	{
		Name: "edge_small_64", Class: Photo, Width: 64, Height: 64, Seed: 0x4001,
		Note: "small edge case: below one 16px macroblock row in every dimension but four",
	},
	{
		Name: "edge_prime_dims", Class: Screenshot, Width: 641, Height: 487, Seed: 0x4002,
		Note: "prime width and prime height: neither dimension is a multiple of the 16px macroblock size",
	},
}
