# bench/deepteams

A measurement rig, not a library and not a dependency of tqwebp.

This nested Go module measures
[deepteams/webp](https://github.com/deepteams/webp), a third-party pure-Go
WebP encoder, across the same corpus and through the same oracle the root
tqwebp module uses for its own stdlib JPEG baseline. It exists so tqwebp
can report an external pure-Go comparison point without ever adding
`github.com/deepteams/webp` to the root module's `go.mod`.

## Why a separate module

The root module's rule is strict: `go mod graph` for `m31labs.dev/tqwebp`
must show nothing beyond the Go standard library and
`golang.org/x/image`. Comparing against deepteams/webp needs its own
dependency, so the comparison lives in its own module, with its own
`go.mod`, excluded from the root module's `go build ./...` and
`go test ./...` by the normal Go module boundary. No `go.work` file joins
them.

## What it measures

`internal/dtbaseline` encodes every corpus image with
`deepteams/webp.Encode` at quality 75, 82, 90, and 95 (95 added to surface
the quality-plateau question directly), then scores the result through
`m31labs.dev/tqwebp/oracle.RoundTrip`: the same independent
`golang.org/x/image/webp` decode, the same PSNR/SSIM formulas, that every
other baseline in this project uses.

## The colour convention question, and its answer

Work package 0 measured this baseline through a decode path that read the
planes with `image.YCbCr.At`, which applies the full-range JFIF
conversion. A lossy WebP file carries limited-range BT.601 planes, so that
path lifted black, compressed white, and reported luma PSNR in the low
20s even at quality 95. The gap was a decoder convention, never an
encoder defect.

Work package 1 closed it. `oracle.DecodeWebP` now inverts the planes with
libwebp's own coefficients, and `oracle.RoundTrip` uses that path, so this
table reads the same quality a browser would see. The committed golden
table moved by more than 20 dB on some rows when the fix landed. The
numbers are now directly comparable with tqwebp's own.

One residual difference stays, and it favours nobody: libwebp upsamples
chroma with a bilinear filter and `golang.org/x/image` repeats each chroma
sample, so both codecs read up to 1.3 dB low on hard colour edges. See
`testdata/golden/libwebp_differential.json` in the root module.

## Running it

```sh
cd bench/deepteams
go generate ./...          # regenerate testdata/golden/deepteams_baseline.txt
go test ./...               # regeneration-check: table matches committed
go run ./cmd/dtbench        # print the table without updating the golden file
```
