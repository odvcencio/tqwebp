# Changelog

All notable changes to tqwebp are documented in this file.

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
