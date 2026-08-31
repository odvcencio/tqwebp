# Changelog

All notable changes to tqwebp are documented in this file.

## Unreleased: work package 2 — better modes

### Added

- Alpha for lossy frames, work package 2 slices 2.1 and 2.2. An image
  with one translucent pixel now writes the extended container: a `VP8X`
  chunk with the alpha flag and the 24-bit minus-one canvas fields, an
  `ALPH` chunk, then the VP8 key frame. The `ALPH` chunk uses
  compression method 0, which stores the alpha plane one byte per pixel.
  All four filtering methods — none, horizontal, vertical, gradient —
  are implemented and each decodes byte for byte through
  `golang.org/x/image/webp`; the encoder ships filter 0, because
  compression method 0 costs the same whichever filter runs.

  An opaque image keeps the simple container and its exact bytes. A
  committed table of 135 SHA-256 hashes, written by the release before
  alpha, pins that on every test run.

  Colour is straight, never premultiplied, which is what WebP stores. A
  translucent `*image.RGBA` is un-premultiplied with the arithmetic of
  `color.NRGBAModel`. A pixel of alpha zero in an `*image.RGBA` encodes
  as black; an `*image.NRGBA` keeps its own stored colour there.

  Raw alpha costs width times height bytes, so a translucent asset is
  larger than its PNG today. The README carries the measured table.
  Compression method 1, a VP8L alpha payload, is the follow-up that
  changes the number, and it is not built.

- `internal/alpha`: read the straight alpha channel of an image, build
  the `ALPH` payload, and filter the plane.

- `oracle.DecodeWebPFile` and `oracle.DecodeWebPNRGBA`: decode the
  colour planes and the alpha plane of an extended WebP file.
  `DecodeWebPPlanes` and `DecodeWebP` keep their signatures and their
  results.

- `Limits` and `EncodeWithLimits` at the module root. Applications that
  process untrusted images can cap width, height, visible pixels, and the
  complete RIFF output. Zero means no caller-specified cap; negative limits
  return `ErrInvalidLimits`. Dimension and pixel caps are checked before
  pixel access or plane allocation, and an output cap is checked before any
  caller-visible write (`ErrOutputTooLarge` / `ErrLimitExceeded`). The
  existing `Encode` entry point and its output remain unchanged.

- `internal/cost`: the exact rate primitives the coming
  reconstructed-neighbour search will consume. Q8 fixed-point boolean
  decision costs cover every codable probability in both branches; on
  top of them sit key-frame luma and chroma mode trees, contextual
  B_PRED sub-mode trees priced through the same tree path the writer
  codes, skip decisions, segment-id tree costs, coefficient tokens with
  their magnitude categories and extra bits, probability-update
  decisions, partition-zero byte accounting, and an integer-only lambda
  derived from the quality knob. Nothing in the production encode path
  consumes these yet, so every encoded file stays byte for byte; a
  full-frame replay parser proves the model prices the emitted syntax
  decision for decision at both effort paths.

- `internal/encoder`: a conservative detailed-block selector for the
  B_PRED luma path, available only at `Method` 5 and 6. A macroblock
  whose whole-block prediction fits poorly may be coded as sixteen
  independent 4x4 blocks, but only when the candidate's sum-of-squares
  error is strictly below half of the whole-block error; otherwise the
  whole-block record and reconstruction stand unchanged. The rule prices
  squared error only -- it is not a rate-distortion search -- and
  methods below 5 never run it, so their output is byte-identical to the
  previous release.

### Deprecated

- `ErrAlphaUnsupported`. `Encode` used to return it for an image with a
  translucent pixel. It now writes the alpha channel instead, so no
  encode returns this error. The variable stays declared, so code that
  tests for it still compiles.

## Unreleased: work package 1 — a correct encoder

The encoder exists. It writes a VP8 key frame inside a RIFF container,
and an independent decoder reproduces its picture byte for byte.

### Added

- `Encode`, `Options`, and the sentinel errors, at the module root. The
  interface mirrors `image/jpeg`.
- `internal/boolenc`: the boolean entropy coder of RFC 6386 section 7.3,
  with explicit carry propagation, a paired decoder for tests, a golden
  state trace, carry-chain cases, and a fuzz target.
- `internal/yuv`: red-green-blue to 4:2:0 conversion with libwebp's
  BT.601 limited-range coefficients, 2x2 box chroma, and macroblock
  padding by edge replication.
- `internal/predict`, `internal/quantize`, `internal/token`,
  `internal/frame`, `internal/container`: whole-block prediction, the
  quantizer tables and the quality map, the coefficient token writer with
  the default probabilities, the key-frame header, and the RIFF writer.
- `internal/encoder`: the pipeline, including the decoder-equivalent
  reconstruction the exact-match gate compares against.
- `internal/gate` and `cmd/tqbench -gates`: one command runs every
  release gate and writes a JSON summary.
- `testdata/golden/bt601/`: the colour convention fixture, made by
  libwebp, pinning the forward and the inverse conversion.
- `testdata/golden/libwebp_baseline.json` and
  `testdata/golden/libwebp_differential.json`, with
  `tools/libwebp_baseline.py` that produces them.

### Changed

- `oracle.RoundTrip` decodes with libwebp's colour convention through the
  new `oracle.DecodeWebP`. The previous path read the planes with the
  full-range JFIF conversion and charged every WebP encoder for a
  decoder's convention: the committed deepteams/webp table moved by more
  than 20 dB when the fix landed.
- The root package is `webp`, as the specification's interface section
  requires. It was `tqwebp`.
- CI proves the encoder's import graph carries no oracle and no
  `golang.org/x/image`, and builds for six target platforms.

### Fixed

- The Y2 alternating-current factor overflowed a 16-bit multiply at
  quantizer indexes above 117, which broke the lowest quality settings.
  Test T9 caught it.

### Dependencies

- `m31labs.dev/turboquant/blockdsp` supplies the 4x4 transforms, the
  block metrics, the scan order, and the dead-zone quantizer.

## Unreleased: work package 0 — scaffold, corpus, oracle, baselines

The first shipped stage of tqwebp: no encoder yet, but the full
measurement harness every later encoder work package is gated on.

### Added

- Module scaffold: `go.mod` (module `m31labs.dev/tqwebp`), MIT `LICENSE`,
  `README.md`, `.gitignore`, and a GitHub Actions CI workflow that builds,
  vets, gofmt-checks, and tests the module and separately builds and
  tests the `bench/deepteams` nested module.
- `internal/corpus`: a deterministic, self-contained generator for the
  test corpus (photo, screenshot, and flat content classes, plus a small
  and a prime-dimension edge case), driven by `cmd/gencorpus` and
  `go:generate`. No fetched or vendored third-party images.
- `oracle`: the correctness oracle. Per-channel PSNR, windowed SSIM, a
  round-trip harness that decodes any encoder's output through
  `golang.org/x/image/webp`, the `ReconstructionSource` /
  `CompareExact` exact-match hook (API and stub tests now; wired to the
  real encoder in WP-1), and a stable text-table reporter.
- `internal/baseline` and `cmd/tqbench`: the stdlib JPEG baseline
  (quality 75, 82, 90) across the corpus, with a committed golden table
  and a regeneration-check test.
- `bench/deepteams`: a separate Go module measuring
  `github.com/deepteams/webp` as a black-box external baseline, so that
  dependency never reaches the root module's `go.mod`.
