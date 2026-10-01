# tqwebp

tqwebp encodes **opaque still images as lossy WebP in pure Go**. It needs no
cgo, native library, WebAssembly runtime, or external encoder process.
The API follows `image/jpeg`: `Encode(io.Writer, image.Image, *Options)`.
Go 1.26 or newer is required. The package name is `webp`.

```sh
go get m31labs.dev/tqwebp
```

For local evaluation, add `replace m31labs.dev/tqwebp => /path/to/checkout`
to a consumer module. See the changelog for unreleased changes.

## Supported scope

| Capability | Behavior |
|---|---|
| Opaque RGB, grayscale, YCbCr, paletted, CMYK, and other `image.Image` values | Converted to BT.601 limited-range 4:2:0; nonzero bounds and subimages supported |
| Encoding dimensions | 1–16383 pixels on each axis; optional smaller caller limits |
| Transparent encoding | Rejected with `ErrAlphaUnsupported`; flatten over an explicit background if that suits the application |
| ICC/EXIF/XMP container transport/editing | Experimental still/animated Demux/Mux; see [contract](docs/container.md) |
| Lossless/transparent pixel encoding, animation pixel encoding | Not implemented |
| Decoding | Native VP8/VP8L stills + raw/compressed ALPH; NRGBA, exact lossless channels, nearest chroma for VP8. Bounded Reader/DecodeAll animation composition; see [animation](docs/animation.md) and [decoding limits](docs/decoding.md) |
| Determinism | Integer coding, fixed search order; repeated encodes and GOMAXPROCS tests check identical bytes |

Animation composition and owned document decoding are available through
Reader/DecodeAll. Transparent and animation pixel encoding, CLI workflows and
full decoder/browser qualification remain required milestones. The encoder is
still opaque/lossy; still decoding accepts both codec families and raw/compressed
alpha. See the
[API compatibility contract](docs/api.md) for the supported foundation.

Inputs must satisfy the `image.Image` contract, including valid pixel storage.
Quality 100 remains lossy, including chroma subsampling. Use a lossless codec
when exact pixels or small colored text matter.

## Using it

```go
import webp "m31labs.dev/tqwebp"

// img is a decoded, opaque image.Image.
f, err := os.Create("photo.webp")
if err != nil {
    return err
}
if err := webp.Encode(f, img, &webp.Options{Quality: 80}); err != nil {
    f.Close()
    return err
}
return f.Close() // surface a final filesystem write error too
```

Nil options and `Options{}` select quality 75 and method 4. Quality accepts
1–100; zero selects the default. Quality is an encoder setting, not a target
size or a promise to match cwebp's quality number. Method zero selects the
default, so it does not select a separate fastest mode.

| Method | Intended use and tradeoff |
|---|---|
| 1–4 | Stable, identical whole-block path; default 4 is suitable for online encoding when avoiding a native dependency matters |
| 5 | Experimental 4x4 prediction and mode search; smaller files, more CPU; evaluate on your own assets |
| 6 | Experimental coefficient search, trellis, and probability updates; improved compression, substantially more CPU and allocations |

The method 6 quality collapse present at `9c4604b` is corrected by separating
coefficient refinement's rate weight from mode selection's weight. A higher
method still need not improve every image's quality at an equal quality setting.
Loop filtering and production adaptive quantization remain disabled.

See the [compiled examples](example_test.go), including deliberate alpha
flattening and resource limits.

## Resource limits and errors

```go
err := webp.EncodeWithLimits(w, img, nil, webp.Limits{
    MaxWidth:       4096,
    MaxHeight:      4096,
    MaxPixels:      16 << 20,
    MaxOutputBytes: 8 << 20,
})
```

Zero limit fields mean no caller limit; negative fields return
`ErrInvalidLimits`. Width, height, and visible pixel count are checked before
reading pixels or allocating padded planes. Choose a pixel limit appropriate
to your deployment: `Encode` alone permits very large, expensive images.

`MaxOutputBytes` limits the complete file, including RIFF framing and padding.
Positive caps bound the serialized partition buffers and stop serialization
when the cap cannot be met, before writing anything to the caller. It is not
a memory or CPU cap. The encoder holds padded source/reconstruction planes,
macroblock records, and encoded data; method 6 runs a second analysis pass.
An output-cap refusal writes nothing and matches both `ErrOutputTooLarge`
and `ErrLimitExceeded` through `errors.Is`. Zero limits produce the same
bytes as `Encode`.

Both entry points report nil images, typed nil images, empty or inverted
bounds as `ErrInvalidImage`; nil writers as `ErrInvalidWriter`; out-of-range
options as `ErrInvalidOptions`; and VP8 dimension overflow as `ErrTooLarge`.
Unsupported alpha and input/limit validation fail before any output writes.
Writer errors are returned unchanged, and short writes return
`io.ErrShortWrite`. A writer failure can leave a partial destination file;
write to a temporary file and rename after success when atomic output matters.

For cancellation, use the same options and limits with `EncodeContext`:

```go
ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
defer cancel()
err := webp.EncodeContext(ctx, w, img, nil, limits)
```

A nil context returns `ErrInvalidContext`; a pre-cancelled context returns
`ctx.Err()` before image/writer callbacks. Cancellation is cooperative during
traversal, analysis, both method 6 passes and serialization. It cannot interrupt
a blocking custom image method or `Write`; the caller must supply I/O deadlines
or process supervision for a hard whole-job deadline. Cancellation after an
external write starts can leave partial output.

Limit refusals expose `*LimitError` through `errors.As`, with `Resource`, `Limit`
and `Actual`. `Actual == -1` means the full amount was unavailable safely, such
as when bounded serialization stopped early. `errors.Is` behavior stays the
same; output refusals match both limit sentinels. Negative limits remain invalid,
and the established `Options` and `Limits` fields/defaults are unchanged.

## Measured quality and size

The [September 30 validation](bench/corpuscompare/results/2026-09-30.md)
uses the same hash-checked 12-image corpus and independent libwebp decoder
as the [earlier comparison](bench/corpuscompare/results/2026-09-23.md).
At cwebp quality 75's luma SSIM, interpolated median file-size ratios are:

| Content | Method 4 | Method 5 | Corrected method 6 |
|---|---:|---:|---:|
| Six Kodak photos | 1.687× | 1.211× | 1.095× |
| Three graphics | 2.606× | 1.235× | 1.124× |
| Three screenshots | 2.408× | 1.430× | 1.092× |

Graphics and screenshots have only two matched images for methods 5 and 6.
All twelve corrected method 6 curves overlap cwebp over part of the q50–90
sweep. This is a small, three-point diagnostic comparison, not universal
parity evidence. Method 6 took about five times method 5's photo CPU time
on this shared host. Timings and allocation volume are descriptive; see the
raw measurements and method notes. Default method 4's files are larger.

All 144 outputs in that sweep decoded through `dwebp`, including all 108
tqwebp outputs. Automated tests also check exact reconstruction through
`golang.org/x/image/vp8`. Current browser validation was unavailable because
the installed Chrome executable could not be run. Browser decoding on an
earlier revision is recorded in the historical report.

## Color convention

WebP uses BT.601 limited-range YUV. tqwebp's forward conversion is pinned
against libwebp fixtures in `testdata/golden/bt601/`. The current
`golang.org/x/image/webp` decoder converts those planes with full-range JFIF
coefficients, which can shift displayed RGB values. The test-only
`oracle.DecodeWebP` uses the matching BT.601 inverse for cross-codec scoring.
Do not infer a color-conversion defect from comparing those two RGB decodes.

## Development and validation

```sh
go build ./...
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
GOMAXPROCS=2 go test . -run '^$' -fuzz '^FuzzEncodeAPI$' -fuzztime=30s
GOMAXPROCS=2 go test ./internal/encoder -run '^$' -fuzz '^FuzzEncode$' -fuzztime=30s
go run ./cmd/tqbench -gates
```

CI checks correctness, bounded quality regressions, examples, race tests,
short fuzz runs, and six target builds: Linux amd64/arm64, macOS arm64,
Windows amd64, js/wasm, and wasip1/wasm. See
[bench/corpuscompare](bench/corpuscompare/README.md) for the real-image
measurement and acceptance commands. External corpus images are supplied
locally; CI's generated fixtures do not establish real-photo parity.

The production import graph includes the standard library and
`m31labs.dev/turboquant/blockdsp`. `oracle`, `golang.org/x/image`, CGo
competitors, and Python measurement tools are development dependencies.
CI checks this separation. `go generate ./...` regenerates the deterministic
corpus, color fixture source, and JPEG baseline; external libwebp fixtures
use `tools/libwebp_baseline.py`.

## License

MIT for tqwebp-authored code. See [LICENSE](LICENSE). The adapted VP8/VP8L decoders
retain their Go Authors BSD license; image artwork attribution is recorded in
[testdata/lossless](testdata/lossless/ATTRIBUTION.md). Libwebp-derived
conversion constants retain the notice under [third_party](third_party/libwebp-COPYING).

## Container and metadata foundation

The separate [`container` package](docs/container.md) supports bounded still/animated-WebP
inspection and ICC/EXIF/XMP edits without recompressing payloads. It is structural
transport, not pixel decoding. Root native VP8/VP8L still decoding is now
available, including [Reader/DecodeAll composition](docs/animation.md); animation pixel encoding and full qualification remain required work. The existing pixel encoder and its defaults are unchanged.

## Intermediate still decoding

`Decode` / `DecodeContext` return real NRGBA pixels for VP8/VP8L and raw/compressed ALPH stills.
`DecodeConfig` is only bounded header inspection, including unsupported pixel
forms. Import `m31labs.dev/tqwebp/register` explicitly for `image.Decode`.
These still APIs reject animation, never flattening it; use Reader/DecodeAll
for composition. Nearest chroma is not libwebp default
fancy upsampling: [pixel policy, resource audit and remaining gates](docs/decoding.md).

VP8L and compressed alpha use an allocation-budgeted native adaptation; see
[allocation, framing and qualification audit](docs/lossless-decoding.md).
