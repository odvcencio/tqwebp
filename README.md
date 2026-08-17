# tqwebp

A lossy WebP encoder in pure Go. No cgo, no WebAssembly runtime, no
foreign function interface: that policy is the founding rule of this
project, not a later constraint.

tqwebp writes a VP8 key frame inside a RIFF container. Its output decodes
in every browser, in libwebp, and in `golang.org/x/image/webp`, which this
repository uses as an independent oracle on every test run.

Part of the TurboQuant codec family, next to mirage. The block transforms,
the block metrics, and the dead-zone quantizer come from
`m31labs.dev/turboquant/blockdsp`.

## Status

Work package 1 landed: a correct encoder. What that means, exactly:

- Every corpus image encodes, decodes, and keeps its size, at every
  quality.
- With the loop filter at level 0, an independent decoder reproduces the
  encoder's own picture byte for byte, on all three planes, at every
  quality tested.
- On the photo class, tqwebp spends 0.74 times the bytes stdlib JPEG
  quality 82 spends, at the same luma quality.
- Against libwebp at quality 75, tqwebp spends 1.17 times the bytes for
  the same luma quality.

What is missing, and lands next: the 4x4 prediction sub-modes, the
rate-distortion mode search, the two-pass probability optimization, and
tuned loop filter levels (work package 2). Those tools decide text and
hard-edge content, where tqwebp trails today.

## Using it

```go
import webp "m31labs.dev/tqwebp"

f, err := os.Create("photo.webp")
if err != nil {
	return err
}
defer f.Close()
if err := webp.Encode(f, img, &webp.Options{Quality: 80}); err != nil {
	return err
}
```

The interface mirrors `image/jpeg`. A nil `*Options`, and the zero value,
both mean quality 75 and method 4. Quality 75 lands within 0.02 dB of
libwebp's quality 75 on the photo corpus, so the knob means what a caller
who knows `cwebp` expects it to mean.

Encode refuses an image with a translucent pixel and returns
`ErrAlphaUnsupported`. Alpha arrives with work package 5, and refusing is
the only way a pipeline cannot lose a mask in silence.

Output is deterministic: the same image and the same options always
produce the same bytes, at every value of GOMAXPROCS.

## Measured gates

Run every gate with one command:

```sh
go run ./cmd/tqbench -gates                  # verdicts and numbers
go run ./cmd/tqbench -gates -json out.json   # the full measurements
```

| Gate | Bar | Measured | Verdict |
|---|---|---|---|
| G1 correctness | every image round-trips, and the decode equals the encoder's own picture | 63 of 63 encodes exact | PASS |
| G2 rate against stdlib JPEG q82 | median bytes at most 0.90x, no image over 1.10x | median 0.741, worst 0.760 | PASS |
| G2b quality curve | median gain 2.5 dB from q75 to q90, for at most 2.2x the bytes | gain 5.00 dB, bytes 9.60x | see below |
| G3 speed | reported, no bar | 23 ms per megapixel, single thread, no assembly | reported |
| G3 rate against libwebp q75 | informative, at most 1.35x | median 1.170x | reported |
| G4b against deepteams/webp | gated at WP-2 | photos -0.14 dB at the same file size | reported |

G2b's decibel clause passes with margin. Its byte-ratio clause fails, and
the run reports it rather than gating on it. The reason is the corpus, not
the encoder: the generated photo images carry per-pixel noise, which puts
a rate wall between quality 75 and quality 90 that no encoder can cross
cheaply. libwebp, measured on the same images, needs 7.06 times the bytes
for its own 4.42 dB. Vendoring the real photo corpus of specification
section 10.1 re-arms the clause; `-strict` gates it that day.

## The colour convention

A lossy WebP file carries BT.601 limited-range planes, and libwebp,
therefore every browser, inverts them with the matching coefficients.
`golang.org/x/image` inverts them with the full-range JFIF coefficients
instead, which moves red, green, and blue by up to 20 per channel.

tqwebp converts forward with libwebp's coefficients, and this repository
measures through `oracle.DecodeWebP`, which inverts with them too. Both
directions are pinned against libwebp's own files under
`testdata/golden/bt601/`, and `testdata/golden/libwebp_differential.json`
records what libwebp reports for every file tqwebp writes.

## The corpus

`internal/corpus` generates every image in `testdata/corpus/` from a
fixed, self-contained pseudo-random seed. Run `go generate ./...` from the
module root and the corpus reproduces byte for byte. It covers three
content classes plus two correctness edge cases:

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

`oracle` decodes every encoder's output through `golang.org/x/image/webp`
— an independent, pure-Go decoder — and scores it against the source:

- `DecodeWebP` / `WebPPlanesToRGBA`: decode with libwebp's colour
  convention. Every cross-codec measurement goes through it.
- `MeasurePSNR`: per-channel PSNR after that decode. Use it to compare
  two codecs.
- `MeasurePlanePSNR`: PSNR in the codec's own plane domain. Use it to ask
  about the codec alone, such as the shape of its quality curve. Do not
  use it across codecs with different sample ranges.
- `MeasureSSIM`: windowed (8x8) luma structural similarity.
- `ReconstructionSource` / `CompareExact`: the exact-match gate.
- `Table`: a stable, sorted text-table report.

`oracle`, and its `golang.org/x/image` dependency, are development and
test code. The encoder's own import graph carries neither; CI proves that
on every run with `go list -deps`.

## The baselines

`cmd/tqbench` measures stdlib `image/jpeg` at quality 75, 82, and 90 and
writes `testdata/golden/jpeg_baseline.txt`; `baseline_test.go` fails when
that table drifts.

`tools/libwebp_baseline.py` measures libwebp itself through Pillow and
writes `testdata/golden/libwebp_baseline.json`. It also decodes tqwebp's
own files with libwebp, which is the differential check of specification
section 11.3.

`bench/deepteams/` measures `github.com/deepteams/webp` from its own Go
module, so that dependency never reaches this module's `go.mod`.

## Regenerating derived files

```sh
go generate ./...                          # corpus, colour fixture source, JPEG table
cd bench/deepteams && go generate ./...    # deepteams table
python3 tools/libwebp_baseline.py          # libwebp fixture (needs Pillow with WebP)
```

## Building and testing

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./...
gofmt -l .
```

## Claims this project makes, and does not make

Permitted, because they are true and checked: lossy WebP encoder in pure
Go; zero cgo and zero WebAssembly runtime; deterministic output; every
release gate decodes each frame through `golang.org/x/image/webp`
in-process; a drop-in `image/jpeg`-shaped interface.

Not permitted: "first", "only", or "fastest". `deepteams/webp` exists, it
works, and at the same file size it still leads tqwebp on text and flat
art.

## License

MIT. See `LICENSE`.
