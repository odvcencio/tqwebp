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
//go:generate go run ./cmd/gencorpus
//go:generate go run ./cmd/genbt601
//go:generate go run ./cmd/tqbench -update -out testdata/golden/jpeg_baseline.txt

// Package tqwebp is the module root of tqwebp, a pure-Go lossy WebP
// encoder under construction. This stage of the project (work package 0)
// ships no encoder yet. It ships the measurement harness that gates every
// later encoder work package: the generated test corpus, the correctness
// oracle, and the baseline comparison numbers.
package tqwebp
