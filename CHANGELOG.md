# Changelog

All notable changes to tqwebp are documented in this file.

## Unreleased: work package 2 — better modes

### Added

- `Limits` and `EncodeWithLimits` at the module root. Applications that
  process untrusted images can cap width, height, visible pixels, and the
  complete RIFF output. Zero means no caller-specified cap; negative limits
  return `ErrInvalidLimits`. Dimension and pixel caps are checked before
  pixel access or plane allocation, and an output cap is checked before any
  caller-visible write (`ErrOutputTooLarge` / `ErrLimitExceeded`). The
  existing `Encode` entry point remains available.

- `internal/cost`: the rate primitives the reconstructed-neighbour search
  consumes. Q8 fixed-point boolean
  decision costs cover every codable probability in both branches; on
  top of them sit key-frame luma and chroma mode trees, contextual
  B_PRED sub-mode trees priced through the same tree path the writer
  codes, skip decisions, segment-id tree costs, coefficient tokens with
  their magnitude categories and extra bits, probability-update
  decisions, partition-zero byte accounting, and separate integer-only
  mode-selection and coefficient-trellis lambdas derived from the quality
  knob. A full-frame replay parser proves the model prices the emitted
  syntax decision for decision, including shipped probability updates.

- `internal/encoder`: reconstructed-neighbor rate-distortion selection of
  the B_PRED luma path at `Method` 5 and 6, with deterministic pruning,
  contextual sub-mode syntax, exact coefficient-token pricing, and
  canonical tie-breaking. Method 6 refines an admitted B_PRED candidate's
  coefficients only when the complete mode objective improves, preventing
  trellis-scaled coefficients from displacing a higher-quality macroblock
  mode.
- Deterministic coefficient-probability optimization at Method 5 and 6.
  Method 5 freezes one table from its final records. Method 6 runs one
  bounded reconsideration under first-pass prices and re-derives the table
  from the reconsidered records before writing it.

### Changed

- The default effort is now Method 5. On the committed gate corpus it uses a
  median 0.538 times the bytes of stdlib JPEG quality 82 at equal displayed-
  luma quality, a median 0.787 times libwebp's bytes at interpolated equal
  quality, and 61.4 ms per megapixel median encode time.
- Methods 0 through 4 retain the whole-macroblock path. Method 5 is the normal
  B_PRED/probability tier; Method 6 is the bounded coefficient and repeated-
  refinement tier.

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
