// Command go generate, run from the module root, regenerates every
// checked-in derived artifact this repository ships:
//
//   - testdata/corpus/*.png, from internal/corpus (see cmd/gencorpus).
//   - testdata/golden/jpeg_baseline.txt, the stdlib JPEG baseline table
//     (see cmd/tqbench).
//   - testdata/golden/bt601/ramp.png, the source image of the colour
//     conversion fixture (see cmd/genbt601). The libwebp-made files next
//     to it come from the commands recorded in that directory's
//     manifest.json, which no Go tool can run.
//
// The libwebp fixtures under testdata/golden are not on this list either.
// They need Pillow with WebP support; tools/libwebp_baseline.py makes
// them.
//
//go:generate go run ./cmd/gencorpus
//go:generate go run ./cmd/genbt601
//go:generate go run ./cmd/tqbench -update -out testdata/golden/jpeg_baseline.txt

package webp
