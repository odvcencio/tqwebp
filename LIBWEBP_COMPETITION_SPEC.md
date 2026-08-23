# TQWebP Libwebp Competition Specification

- Status: Draft for execution
- Baseline: `main` at `4579db9020f18c2331f6cf13ae83c9971d5a002a`
- Date: 2026-08-23
- Scope: A pure-Go WebP implementation that competes directly with libwebp

## 1. Objective

TQWebP MUST become a credible replacement for libwebp for applications that
want a memory-safe, deterministic, native Go implementation without cgo,
WebAssembly, an FFI, or an external codec process.

Competition has two horizons:

1. **Encoder competition:** match the useful `cwebp` still-image surface and
   compete on rate-distortion, throughput, memory, and compatibility.
2. **WebP toolkit competition:** add lossless, alpha, decode, incremental
   decode, mux/demux, metadata, and animation so applications no longer need
   libwebp for a supported WebP workflow.

Encoder competition is the first release milestone. Decoder, mux, and
animation work MUST NOT delay finishing a competitive lossy encoder.

## 2. Normative language

`MUST`, `MUST NOT`, `SHOULD`, and `MAY` are requirements at their usual RFC
2119 strength.

- A **slice** is one bounded implementation unit with its own acceptance gate.
- A **work package** is an ordered collection of slices.
- **Dormant** means code is unreachable from the public production path.
- **Graduated** means the feature passed its gates and is reachable through an
  explicit public option.
- **Default-on** means the feature also passed the default-output regression
  gate and is enabled by the zero-value configuration.

## 3. Non-negotiable properties

TQWebP MUST preserve these properties throughout the program:

1. The runtime implementation is Go. It has no cgo, WebAssembly, FFI, dynamic
   library, or external-process dependency.
2. The same source image and configuration produce identical bytes across
   repeated runs, supported operating systems, CPU architectures, and
   `GOMAXPROCS` values unless the caller explicitly selects a documented
   nondeterministic fast mode.
3. Unsupported image features fail explicitly. TQWebP never silently drops
   alpha, metadata, color profiles, frames, or invisible RGB values.
4. Every emitted file is accepted by independent decoders. TQWebP's own
   decoder never serves as its sole correctness oracle.
5. Every quality or compression claim is generated from a pinned corpus and a
   replayable benchmark manifest.
6. Every output-changing slice is independently reviewable and revertible.
7. The existing `Encode(io.Writer, image.Image, *Options)` entry point remains
   source-compatible through the first stable release.

## 4. Current baseline

At the baseline revision, TQWebP has a real lossy encoder with:

- VP8 key-frame encoding in a simple RIFF WebP container.
- Opaque RGB input converted to BT.601 limited-range YUV 4:2:0.
- Quality values 1 through 100 and method values 0 through 6.
- Whole-block prediction for methods 0 through 4.
- Reconstructed-neighbor rate-distortion luma selection, including B_PRED,
  at methods 5 and 6.
- Bounded coefficient candidate search, trellis refinement, and token
  probability reconsideration at method 6.
- Deterministic output and independent decode/reconstruction gates.
- Segmentation headers, features, ID coding, map costs, and control costs as
  tested primitives.

The baseline does not yet have:

- Production segment assignment or segment activation.
- A nonzero tuned loop filter.
- Alpha, VP8X still-image assembly, lossless VP8L, or near-lossless mode.
- ICC, EXIF, or XMP preservation.
- Animation, mux/demux, or a public decoder.
- Incremental I/O, bounded-memory output, or parallel encoding.
- A tagged stable release or a libwebp-scale real-image benchmark corpus.

The README's method description predates several mainline work-package 2
slices. Documentation truth is part of Work Package 0.

## 5. Reference implementation and evidence

### 5.1 Pinned libwebp

Work Package 0 MUST pin an official libwebp release, initially version 1.6.0,
by all of:

- Release version and upstream commit.
- Source archive SHA-256.
- `cwebp`, `dwebp`, `webpmux`, and library version output.
- OCI image digest for the benchmark toolchain.
- Compiler, flags, CPU governor, operating system, and architecture.

Moving upstream `main` is informative only and MUST NOT be a release oracle.
The pinned release is updated deliberately in a dedicated pull request.

### 5.2 Corpus

Synthetic images remain useful for exactness and edge cases, but competitive
claims MUST use a licensed, immutable real-image corpus with provenance.

The corpus MUST contain at least:

| Class | Minimum | Required characteristics |
|---|---:|---|
| Photographs | 250 | portraits, landscapes, low light, texture, noise |
| Screenshots and text | 200 | light/dark UI, small fonts, diagrams, code |
| Flat art and icons | 150 | few-color art, gradients, sharp boundaries |
| Transparent stills | 150 | binary and smooth alpha, invisible RGB cases |
| Lossless sources | 150 | palette, photographic, synthetic, high entropy |
| Animations | 100 | sparse motion, full motion, alpha, disposal/blend |
| Adversarial dimensions | 100 | tiny, prime, odd, maximum-near, narrow/tall |

Every item MUST record origin, license, content hash, dimensions, class, and
whether redistribution is permitted. Nonredistributable inputs MAY be used in
private evaluation but MUST NOT influence a public claim that others cannot
replay.

### 5.3 Metrics

Lossy evaluation MUST report complete curves, not a single quality point:

- File size.
- Display-domain luma and RGB PSNR.
- SSIM and MS-SSIM.
- At least one perceptual metric selected and pinned in Work Package 0.
- BD-rate and BD-quality over the overlapping curve interval.
- Encode wall time, CPU time, allocations, allocated bytes, and peak RSS.
- Decode wall time and peak RSS once the decoder exists.

Lossless evaluation MUST report exact pixel equality, file size, encode time,
decode time, allocations, and peak RSS.

Measurements MUST include Linux amd64 and Linux arm64. Release candidates MUST
also pass compatibility on Windows amd64 and macOS arm64.

Each performance point MUST use a warm-up followed by at least ten measured
runs. Reports contain median, p10, p90, and median absolute deviation. A run is
invalid if thermal throttling, background load, or toolchain identity violates
the manifest.

## 6. Definition of competitive

TQWebP does not claim direct competition merely because it emits valid files.
The following gates define the claim.

### 6.1 Compatibility gate

For every supported feature and corpus item:

- `dwebp` from the pinned libwebp release MUST decode the output.
- Current stable Chrome, Firefox, and Safari/WebKit MUST decode the output in
  an automated browser fixture.
- Decoded dimensions, alpha, frame timing, blend/disposal behavior, and color
  profile behavior MUST match the source contract.
- Lossless output MUST be pixel exact.
- Lossy output MUST match TQWebP's reconstruction after applying the signaled
  loop filter.
- Malformed-input tests MUST never panic, hang, allocate without a configured
  bound, or read outside an input buffer.

The gate is 100%. There is no permitted invalid-file rate.

### 6.2 Lossy rate-distortion gate

Compare TQWebP and `cwebp` at equivalent method/preset levels over complete
quality curves.

| Level | Weighted median BD-rate | Worst content-class BD-rate | Requirement |
|---|---:|---:|---|
| Competitive preview | no worse than +5% | no worse than +10% | at least one class at or below 0% |
| Release candidate | no worse than +2% | no worse than +6% | no individual image worse than +20% |
| Competitive claim | at or below 0% | no worse than +3% | at least two classes better than libwebp |

Positive BD-rate means TQWebP needs more bytes at the same quality. The primary
score is computed independently for display luma, RGB MS-SSIM, and the selected
perceptual metric. A release MUST pass all three; it cannot win one metric by
catastrophically regressing another.

### 6.3 Lossy performance gate

At matched quality and on the pinned hardware:

| Level | Default effort | Highest effort | Peak RSS |
|---|---:|---:|---:|
| Competitive preview | at most 2.0x libwebp time | at most 3.0x | at most 1.75x |
| Release candidate | at most 1.5x | at most 2.0x | at most 1.40x |
| Competitive claim | at most 1.25x | at most 1.5x | at most 1.25x |

TQWebP MAY also earn a competitive claim by being at least 10% better in
weighted BD-rate while remaining within 1.5x libwebp's time and 1.4x its peak
RSS. This prevents the score from reducing codec competition to speed alone.

### 6.4 Lossless gate

For lossless method/effort equivalents:

- Every decoded pixel, including alpha and invisible RGB when `Exact` is set,
  MUST match the source.
- Competitive preview median size MUST be no worse than 1.10x libwebp, with no
  content class worse than 1.20x.
- Competitive claim median size MUST be no worse than 1.03x libwebp, with no
  class worse than 1.10x.
- Encode time MUST be within 2.0x libwebp for preview and 1.5x for the
  competitive claim.
- Decode time MUST be within 1.5x libwebp for preview and 1.25x for the
  competitive claim.

### 6.5 Operational gate

- The public API has a stable, documented zero value.
- Cancellation is observable and bounded.
- Caller-supplied output errors propagate without corruption or leaks.
- Maximum memory can be estimated before encode and bounded by configuration.
- The module passes `go test`, race tests, fuzz smoke, vet, formatting,
  supported cross-builds, browser compatibility, and the pinned differential
  suite.
- A release tag, changelog, reproducible benchmark report, and signed artifact
  manifest exist.

## 7. Public product surface

The simple API remains:

```go
func Encode(w io.Writer, src image.Image, opts *Options) error
```

The competitive encoder SHOULD add an advanced surface shaped like:

```go
type Encoder struct { /* private state */ }

type Config struct {
    Mode          Mode
    Quality       float32
    Method        int
    Preset        Preset
    TargetBytes   int64
    TargetPSNR    float64
    Segments      int
    Passes        int
    Filter        FilterConfig
    Alpha         AlphaConfig
    NearLossless  int
    Exact         bool
    Concurrency   int
    MemoryLimit   int64
}

func NewEncoder(Config) (*Encoder, error)
func (e *Encoder) Encode(context.Context, io.Writer, image.Image) (Stats, error)
```

This shape is directional, not frozen API. Its required semantics are:

- `Mode` distinguishes lossy, lossless, and near-lossless operation.
- `Preset` expresses photo, picture, drawing, icon/text, and default intent.
- Target size or target PSNR triggers a bounded multi-pass search.
- `Stats` reports coded size, quality metrics available without decoding,
  block/mode counts, segment counts, header bytes, residual bytes, passes,
  allocations, and timing.
- Cancellation stops at deterministic bounded checkpoints.
- The zero value retains today's quality 75/method 4 behavior until a
  documented major-version change.

The toolkit horizon SHOULD expose:

```go
func Decode(io.Reader) (image.Image, error)
func DecodeConfig(io.Reader) (image.Config, error)
type Decoder struct { /* incremental state */ }
type Mux struct { /* ordered RIFF chunks */ }
type Demux struct { /* parsed immutable view */ }
type Animation struct { Frames []Frame; LoopCount uint16 }
```

Exact names are settled only when their work package begins.

## 8. Work packages

Work packages are ordered by dependency and user value. A later package MAY
start early only when it does not steal the critical path or weaken an earlier
gate.

### WP0: Truthful baseline and competitor harness

Goal: Make every later improvement measurable against a stable target.

Slices:

1. Update README and API comments to match the current method 5/6 behavior.
2. Pin libwebp and the benchmark environment as described in section 5.1.
3. Add the licensed real-image manifest and corpus loader.
4. Add curve generation, BD-rate, MS-SSIM, perceptual scoring, allocation, and
   peak-RSS reporting.
5. Add automated `cwebp`, `dwebp`, and stable-browser differential runners.
6. Record baseline method 0, 4, 5, and 6 reports without changing output.

Graduation:

- One command reproduces the complete comparison report.
- Reports name exact source, corpus, binary, toolchain, and hardware identities.
- CI runs a bounded smoke subset; the full corpus runs in a scheduled or
  manually approved release job.

### WP1: Competitive lossy VP8 encoder

Goal: Reach the encoder competitive claim before expanding into VP8L.

#### WP1.1 Segment assignment

- Implement deterministic assignment for one through four segments using the
  existing feature, segment-map, and control-cost primitives.
- Jointly price feature deltas, map signaling, header updates, and residual
  savings.
- Preserve exact legacy bytes when `Segments == 1` or the candidate does not
  win.
- Activate only through an explicit option until the corpus gate passes.

Graduation: no invalid files, deterministic maps, positive net coded-size win
on the licensed corpus, and no content class with a rate-distortion regression
above 1% when segmentation is selected.

#### WP1.2 Loop filter

- Implement decoder-equivalent simple and strong loop-filter kernels.
- Apply the selected filter to encoder reconstruction before later blocks use
  filtered neighbors where the VP8 specification requires it.
- Add level 0 through 63, sharpness 0 through 7, per-segment deltas, and
  automatic filter selection.
- Retain level zero as an explicit valid mode.

Graduation: encoder reconstruction equals independent post-filter decode on
every plane and corpus item; automatic filtering improves the weighted
rate-distortion score without exceeding 5% default-effort time overhead.

#### WP1.3 Global multi-pass analysis

- Generalize the current single probability reconsideration into a bounded
  analysis loop.
- Support one through ten passes, target bytes, target PSNR, and quantizer
  bounds.
- Own retry count in one layer; no nested retry loops.
- Record convergence, best candidate, and budget consumption in `Stats`.

Graduation: target-size median error at most 2%, p95 at most 5%; target-PSNR
median error at most 0.10 dB; every search terminates within its declared pass
and time bounds.

#### WP1.4 Presets and spatial tuning

- Add default, photo, picture, drawing, and icon/text presets.
- Implement spatial noise shaping and sharp RGB-to-YUV conversion where they
  win.
- Tune quantizer and lambda mapping from real-image curves rather than the
  synthetic corpus alone.

Graduation: each preset wins or ties the default on its declared content class
after charging both size and time; a losing preset is not shipped.

#### WP1.5 Partitions, parallelism, and memory

- Support one, two, four, and eight token partitions.
- Parallelize only independent work; preserve deterministic merge order.
- Replace whole-file duplication with bounded partition buffers and exact size
  accounting.
- Add explicit low-memory and concurrency settings.

Graduation: identical bytes across supported concurrency values in
deterministic mode; at least 1.7x throughput on four physical cores for images
of four megapixels or larger; peak RSS meets the release-candidate gate.

#### WP1.6 Encoder release

- Add a `tqwebp` CLI with encode, inspect, benchmark, and version commands.
- Stabilize advanced configuration and statistics.
- Publish the first tagged encoder release only after sections 6.1 through 6.3
  pass at release-candidate level.

### WP2: Extended still images and alpha

Goal: Encode transparent lossy WebP and preserve still-image metadata.

#### WP2.1 Extended RIFF writer

- Add VP8X feature flags and canonical chunk ordering.
- Preserve unknown chunks when reading and rewriting containers.
- Enforce RIFF size, padding, canvas, and mutual-exclusion constraints.

#### WP2.2 Raw alpha

- Add ALPH chunks with uncompressed alpha as the first correctness slice.
- Implement none, horizontal, vertical, and gradient alpha filters.
- Add alpha quality and preprocessing signals.
- Preserve RGB beneath transparent pixels when `Exact` is enabled.

#### WP2.3 Compressed alpha

- Reuse the VP8L core from WP3 for compressed alpha payloads.
- Select raw versus compressed alpha by exact total container cost.

#### WP2.4 Metadata and color profiles

- Add ICC, EXIF, and XMP set/get/remove/preserve operations.
- Validate feature flags and chunk order independently from payload contents.
- Never reinterpret or normalize caller metadata without an explicit option.

Graduation for WP2: 100% compatibility gate for opaque and alpha still images,
metadata byte preservation through a read/write cycle, and no silent feature
loss.

### WP3: VP8L lossless and near-lossless encoder

Goal: Compete with libwebp's lossless encoder.

Slices:

1. LSB-first bit writer, canonical Huffman construction, and prefix-code
   validation.
2. Literal and backward-reference image streams with bounded window search.
3. Subtract-green, predictor, color, and color-index transforms.
4. Meta-Huffman groups and entropy-image selection.
5. Color cache and deterministic LZ77 match finding.
6. Transform and block-size search across effort levels.
7. Lossless presets 0 through 9.
8. Near-lossless preprocessing 0 through 100.
9. Exact invisible-RGB preservation and compressed-alpha reuse.
10. Allocation, parallelism, and low-memory passes.

Every slice begins with an independently decoded bitstream oracle. Optimizer
work cannot begin until a deliberately simple valid encoder is pixel exact.

Graduation: section 6.4 passes at release-candidate level before lossless is
enabled by the stable API.

### WP4: Decoder and incremental decode

Goal: Remove the runtime dependency on another WebP decoder.

Slices:

1. Bounded RIFF/VP8X parser and `DecodeConfig`.
2. VP8 boolean decoder, headers, modes, tokens, reconstruction, and filtering.
3. VP8L prefix codes, transforms, backward references, color cache, and pixels.
4. ALPH merge and exact extended-still composition.
5. Caller-provided output buffers and RGB, RGBA, BGRA, and YUV outputs.
6. Cropped and scaled decode.
7. Incremental append/update state machine with bounded retained input.
8. Malformed-input differential fuzzing against the pinned libwebp decoder.

Graduation:

- Every supported corpus and official conformance vector decodes correctly.
- Cross-decoding succeeds in both directions: TQWebP output in libwebp and
  libwebp output in TQWebP.
- Fuzzing finds no panic, unbounded allocation, hang, or out-of-bounds access.
- Decoder performance meets section 6.4 for lossless and the corresponding
  1.5x preview/1.25x competitive thresholds for lossy decode.

### WP5: Mux, demux, metadata, and animation

Goal: Cover the extended-container workflows supplied by libwebp.

Slices:

1. Immutable demux view with ordered known and unknown chunks.
2. Mux creation, replacement, removal, extraction, and canonical assembly.
3. ANIM global parameters and ANMF frame parsing/writing.
4. Exact canvas composition for offsets, duration, blend, and disposal.
5. Animation encoder with full-frame input and deterministic delta rectangles.
6. Key-frame selection and minimize-size search.
7. Incremental animation decoder and timestamps.
8. `tqwebp mux`, `demux`, and `animate` CLI surfaces.

Graduation: frame-by-frame RGBA composition matches libwebp across the
animation corpus, metadata and unknown chunks survive round trips, and no
timestamp, loop-count, blend, or disposal ambiguity remains untested.

### WP6: Competitive hardening and stable release

Goal: Turn feature parity into an operationally trustworthy codec.

Slices:

- Long-running fuzz campaigns for each parser and entropy boundary.
- Allocation-failure and writer-failure injection.
- Maximum-dimension and near-4-GiB container boundary tests.
- Architecture-specific profiling and algorithmic optimization while retaining
  a portable Go implementation.
- Semver/API compatibility tests and migration documentation.
- Reproducible builds, SBOM, signed checksums, vulnerability reporting, and a
  security policy.
- A public dashboard generated from the pinned competitor harness.

Graduation: all competitive-claim gates pass and the repository publishes a
tagged stable release.

## 9. Critical path

The default execution order is:

```text
WP0 benchmark truth
  -> WP1.1 segmentation
  -> WP1.2 loop filter
  -> WP1.3 multipass
  -> WP1.4 presets/tuning
  -> WP1.5 parallelism/memory
  -> WP1.6 competitive lossy release
  -> WP2.1 extended RIFF
       -> WP2.2 raw alpha
       -> WP2.4 metadata
  -> WP3 VP8L
       -> WP2.3 compressed alpha
       -> WP4 decoder
  -> WP5 animation/mux
  -> WP6 stable toolkit release
```

WP4 parser work MAY begin after WP2.1, but it MUST NOT consume the resources
needed to finish WP1.

## 10. Slice execution contract

Every slice MUST have a task specification containing:

- Exact base commit and clean-worktree assertion.
- The production symbol or format boundary it changes.
- Explicit in-scope and out-of-scope behavior.
- Independent oracle, fixtures, and negative cases.
- Output compatibility expectation: unchanged, explicit-option-only, or
  intended default change.
- Correctness, determinism, performance, allocation, and memory budgets.
- A rollback condition.

The implementation workflow is:

1. Create a unique branch from exact current `main`.
2. Add or update the failing acceptance test.
3. Implement the smallest slice.
4. Run focused tests and benchmarks.
5. Commit through the governed commit runtime.
6. Immediately push the commit and open or update a draft pull request.
7. Verify the remote branch resolves to the exact local commit SHA.
8. Run independent review and broader tests against the remote commit.
9. Correct through additional pushed commits on the same pull request.
10. Merge only when all declared gates pass.

A local commit is not a durable checkpoint. Review controls merge; it MUST NOT
prevent remote preservation.

No slice may remain in read-only investigation for more than 30 minutes without
either producing a test/implementation action or recording a concrete blocker.
No new harness requirement may block codec work unless an observed run proves
the requirement is necessary.

## 11. Pull-request boundaries

Preferred pull requests are small and independently useful:

- One syntax primitive or parser boundary.
- One dormant optimizer plus exhaustive unit oracle.
- One production integration behind an explicit option.
- One graduation/default-on change with corpus evidence.

Mechanism, telemetry, and tests that determine whether the mechanism is safe to
activate SHOULD land together. Large format additions use a chain of stacked
draft pull requests rather than an unpushed local branch.

## 12. First executable queue

The next ten bounded slices are:

1. **TQ-LW-001:** update method documentation and pin the libwebp toolchain.
2. **TQ-LW-002:** add real-corpus manifest/provenance schema and loader.
3. **TQ-LW-003:** add curve/BD-rate/RSS competitor report without changing
   encoder output.
4. **TQ-LW-004:** implement the deterministic one-to-four segment assignment
   kernel described by draft PR #6.
5. **TQ-LW-005:** integrate assignment, map, feature deltas, and control cost
   behind `Segments`, preserving legacy bytes when disabled.
6. **TQ-LW-006:** implement decoder-equivalent simple loop filtering with an
   explicitly selected fixed level.
7. **TQ-LW-007:** add strong filtering, sharpness, and per-segment deltas.
8. **TQ-LW-008:** add automatic filter selection against exact
   rate-distortion cost.
9. **TQ-LW-009:** generalize probability reconsideration into a bounded
   multi-pass controller.
10. **TQ-LW-010:** add target-size and target-PSNR search with deterministic
    termination.

The queue is revised only from measured evidence. A new item includes the item
it displaces and the reason.

## 13. Release milestones

| Milestone | Required result |
|---|---|
| M0 Measurement truth | Reproducible real-corpus libwebp comparison |
| M1 Valid competitive lossy preview | WP1 features plus preview quality/speed gates |
| M2 Competitive lossy encoder | Lossy competitive-claim and operational gates |
| M3 Complete still-image encoder | Alpha, metadata, lossless, near-lossless |
| M4 WebP codec | Lossy/lossless/extended decoder and incremental decode |
| M5 WebP toolkit | Mux, demux, animation, metadata workflows |
| M6 Stable libwebp competitor | All competitive-claim gates and stable release |

Milestone labels describe measured capability, not calendar progress.

## 14. Explicit non-goals

- Bit-for-bit equality with libwebp output.
- Reproducing every historical libwebp implementation quirk.
- Matching libwebp's C ABI.
- Claiming first, only, or fastest without reproducible evidence.
- Trading deterministic correctness for benchmark-only shortcuts.
- Allowing the full toolkit horizon to delay the competitive lossy encoder.

## 15. Authoritative references

- [WebP container specification](https://developers.google.com/speed/webp/docs/riff_container)
- [WebP lossless bitstream specification](https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification)
- [libwebp encoder API](https://chromium.googlesource.com/webm/libwebp/+/refs/heads/main/src/webp/encode.h)
- [libwebp API documentation](https://chromium.googlesource.com/webm/libwebp/+/refs/heads/main/doc/api.md)
- [`cwebp` command documentation](https://developers.google.com/speed/webp/docs/cwebp)
- [`webpmux` command documentation](https://developers.google.com/speed/webp/docs/webpmux)

The pinned release artifacts from WP0, not these moving web pages, are the
normative differential implementation for a given TQWebP release.
