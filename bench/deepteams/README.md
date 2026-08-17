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

## Read the numbers with this caveat in mind

`oracle.RoundTrip` decodes every WebP file through
`golang.org/x/image/webp`, then reads pixels through that image's `At`
method. `image.YCbCr.At` applies the standard full-range (JFIF-style)
YCbCr-to-RGB formula. deepteams/webp's forward conversion, correctly,
targets the limited-range (studio-swing) BT.601 convention real WebP
decoders and browsers expect (see the parent spec, §7.2 and open question
Q8). Decoding limited-range-encoded planes with a full-range formula lifts
black levels and compresses white levels: it shows up here as Y-PSNR in
the low-to-mid 20s and 30s decibels even at quality 90-95, while SSIM
(less sensitive to a near-uniform luminance shift) stays high (0.83-0.99).

This is a real, previously-documented decode-convention gap between
`golang.org/x/image/webp` and libwebp-convention encoders, not an encoder
defect and not an oracle bug: a compliant browser or `dwebp` would not
show this shift. It affects every WebP-producing codec measured through
this exact oracle path, including tqwebp's own encoder once it exists, so
WP-1's exit gate needs a dev-only `dwebp` differential fixture (parent spec
§11.3) to separate true codec loss from this decode-convention gap. Treat
the deepteams/webp numbers here as bytes-at-quality (reliable) plus a
same-convention-gap PSNR/SSIM (comparable to any other codec measured the
same way), not as an absolute quality figure.

## Running it

```sh
cd bench/deepteams
go generate ./...          # regenerate testdata/golden/deepteams_baseline.txt
go test ./...               # regeneration-check: table matches committed
go run ./cmd/dtbench        # print the table without updating the golden file
```
