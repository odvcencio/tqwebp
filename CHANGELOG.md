# Changelog

All notable changes to tqwebp are documented in this file.

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
