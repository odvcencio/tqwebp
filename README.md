# tqwebp

A pure-Go lossy WebP encoder, under construction. No cgo, no wasm runtime,
no foreign function interface (FFI): that policy is the founding rule of
this project, not a later constraint.

This stage of the project, work package 0, ships no encoder yet. It ships
the measurement harness that gates every later encoder work package: the
generated test corpus, the correctness oracle, and the baseline comparison
numbers. Every future work package proves itself against this harness
before it merges.

## Status

Pre-v0. There is no `Encode` function yet. What exists:

- `testdata/corpus/`: a deterministic, generated test corpus (no fetched
  images, so no licensing risk and no network dependency).
- `oracle/`: the correctness oracle, ready for WP-1's encoder.
- `internal/baseline/` and `cmd/tqbench`: the stdlib JPEG baseline.
- `bench/deepteams/`: an external black-box baseline, in its own Go
  module so its dependency never reaches the root module.

## The corpus

`internal/corpus` generates every image in `testdata/corpus/` from a
fixed, self-contained pseudo-random seed — not fetched, not vendored from
a third party. Run `go generate ./...` from the module root and the
corpus reproduces byte for byte. It covers three content classes plus two
correctness edge cases:

| Image | Class | Size | Note |
|---|---|---|---|
| `photo_gradient_wide` | photo | 1200x800 | smooth gradient + detail + noise |
| `photo_texture_detail` | photo | 1024x768 | denser structured detail |
| `photo_noise_detail` | photo | 1280x800 | third seed/palette variant |
| `screenshot_dashboard` | screenshot | 1200x800 | panels, hard edges, dash "text" |
| `screenshot_panel_grid` | screenshot | 1024x768 | denser panel grid |
| `flatart_quadrant` | flat | 1200x800 | large solid regions |
| `flatart_logo_blocks` | flat | 640x480 | smaller solid-region variant |
| `edge_small_64` | photo | 64x64 | below one macroblock row in most dimensions |
| `edge_prime_dims` | screenshot | 641x487 | prime width and height: no 16px macroblock alignment |

## The oracle

`oracle` decodes every encoder's output through
`golang.org/x/image/webp` — an independent, pure-Go decoder — and scores
it against the source image:

- `PSNR` / `MeasurePSNR`: per-channel PSNR (Y, Cb, Cr, R, G, B).
- `SSIM` / `MeasureSSIM`: windowed (8x8) luma structural similarity.
- `RoundTrip` / `RoundTripWith`: encode, decode, score, in one call. Any
  encoder shaped like `func(io.Writer, image.Image) error` works; bind
  quality or other options with a closure, since WP-0 predates the real
  encoder's own `Options` type.
- `ReconstructionSource` / `CompareExact`: the exact-match hook. WP-1
  wires a real VP8 encoder to it so `CompareExact` can assert the
  loop-filter-0 contract (encoder-side reconstruction equals an
  independent decode, byte for byte). WP-0 ships the interface, the
  comparator, and a stub so the contract and its tests exist before the
  encoder does.
- `Table`: a stable, sorted text-table report, used by every baseline.

`oracle` (and its `golang.org/x/image` dependency) is a development and
test dependency. It must never be reachable from tqwebp's own non-test,
non-`cmd` encoder source once that code exists.

## The baselines

`cmd/tqbench` measures stdlib `image/jpeg` at quality 75, 82, and 90
across the corpus, and writes the result to
`testdata/golden/jpeg_baseline.txt`. `baseline_test.go` regenerates the
same table on every test run and fails if it drifts from that file
uncommitted: the CI gate for "the golden table matches committed".

`bench/deepteams/` measures `github.com/deepteams/webp`, a third-party
pure-Go WebP encoder, the same way — but from its own Go module, so its
dependency never appears in this module's `go.mod`. See
`bench/deepteams/README.md` for how to run it and for an important
caveat about reading its PSNR numbers.

## Regenerating derived files

```sh
go generate ./...          # corpus PNGs + testdata/golden/jpeg_baseline.txt
cd bench/deepteams && go generate ./...   # testdata/golden/deepteams_baseline.txt
```

## Building and testing

```sh
go build ./...
go vet ./...
go test ./...
gofmt -l .

cd bench/deepteams
go build ./...
go test ./...
```

## License

MIT. See `LICENSE`.
