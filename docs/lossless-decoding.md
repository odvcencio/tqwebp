# VP8L and compressed-ALPH decoding adaptation

This candidate closes the two remaining still-pixel format gaps: VP8L and
lossless-compressed ALPH. The root Decode/DecodeContext API returns owned NRGBA;
VP8L channels are exact, including encoded hidden RGB under zero alpha. Raw and
compressed ALPH both support all four alpha filters. VP8 still retains the
separate documented nearest-chroma/limited-range BT.601 policy. No ICC transform
is performed. Animation remains explicitly refused, never flattened.

## Pinned native implementation

The three files under internal/vp8ldecode are adapted from
`golang.org/x/image v0.38.0/vp8l`. The existing module pin is unchanged. Original
BSD notices, LICENSE, PATENTS, exact upstream/adapted hashes and a unified
adaptation diff are retained. This is reuse of proven native Go primitives,
not a new entropy codec or a libwebp runtime wrapper. Production code has no
cgo, shared-library discovery, subprocess, network access or global registration.
Maintainers must track upstream/security updates and re-audit every allocation
and cancellation hook before adopting a newer revision.

Unlike a reader-only wrapper, the internal adapter owns every entropy/table,
auxiliary image and inverse-transform allocation. Its input is already bounded
bytes, never an arbitrary caller Reader. Root input handling still checks
progress/context on every bounded Read, preserves non-EOF errors on final
bytes, and refuses invalid Reader counts or 100 consecutive empty reads.

## Exhaustive backing-allocation ledger

The existing root preflight reserves compressed RIFF storage, retained Demux
payload copies, descriptor capacity and 64 KiB fixed state/slack before codec
entry. Each dynamic VP8L backing allocation then calls the same working-budget
reservation before make. No reservation is released. This deliberately
conservative cumulative reservation is an upper bound on live backing storage;
it may refuse a file whose peak-live footprint would fit. It is not a claim
about process RSS, garbage collection or a constant multiplier per pixel.

All allocation sites in the adapted files are covered:

- Color cache: 4 * 2^cacheBits bytes; cacheBits must be 1..11
- Huffman groups: 2680 * groupCount bytes, the audited 64-bit upper layout of
  five trees, including 128-entry uint32 lookup tables and slice descriptors
- Tree nodes: 8 * (2*nonzeroSymbols-1) bytes; simple trees require 1 or 2 symbols
- Code-length and canonical-code arrays: 4 * alphabetSize bytes each, including
  the temporary code-length-code tree; largest alphabet is 256+24+2048=2328
- Auxiliary predictor/cross-color/meta images and palettes: complete requested
  byte capacity, including the palette's 1024-byte minimum capacity
- Main pixel output: complete 4*width*height byte capacity
- Color-index expansion: additional 4*originalWidth*height bytes when packing
  needs a distinct expanded image. The old packed image remains reserved

There are no uncharged append-growing slices, pools or dependency allocations.
Fixed local arrays and decoder state fit within root slack; test assertions
pin node/group/state sizes. The backing storage of returned NRGBA is included.
A positive working cap is checked against both policy and platform int before
allocation. Dimensions are 1..16384, so 4*W*H is at most 2^30 and fits signed
32-bit intermediate arithmetic; all root aggregate arithmetic is checked int64.

For compressed ALPH, the existing VP8 reservation stays charged while the
lossless alpha decoder runs. This includes YUV, output and compressed planes,
even if the compiler could shorten some lifetimes. The green channel is read
directly into the existing output alpha while unfiltering; there is no separate
extracted-alpha backing array. Temporary lossless RGBA is fully charged.

LimitError/ErrLimitExceeded propagate unchanged from internal reservation;
they are never mislabeled ErrInvalidFormat. Tests fail each of 188 observed
allocation sites across three structurally different fixtures and assert exact
error identity/category separation. Public compressed-alpha tests verify the
exact final reservation passes and one byte less fails.

## Structural and work bounds

- At most four distinct transforms, rejected before decoding duplicate payloads
- Auxiliary entropy-image recursion explicitly limited to depth two; transform
  subimages never recursively carry transforms or another meta-image
- Huffman group indices limited to the format's 16 bits (65536 groups). Each
  group needs at least 20 encoded bits for its five trees; impossible counts
  are rejected before allocating group storage
- Alphabets and code lengths remain format-bounded; out-of-range simple
  symbols, oversubscribed/incomplete trees and unsafe node indices are refused.
  Valid duplicate simple symbols normalize to a zero-bit singleton. Normal-code
  singletons require the normative length1; libwebp tolerates malformed longer
  singleton lengths, but this adapter deliberately rejects them
- Back references validate source/destination ranges before overlapping copies
- Pixel work and auxiliary image volume are bounded by charged output capacity;
  code-table construction work is bounded by charged table/array capacity and
  maximum 15-bit codes. No loop expands animation or caller-controlled repeats

Context is checked during bit and Huffman-symbol decoding, allocation admission,
Huffman construction, every 4096 copied/cache bytes, each inverse-transform row,
and each 4096-byte subtract-green/palette traversal. Single fixed-format loops
remain bounded by one row (at most 16384 pixels), one alphabet (at most 2328
entries), one tree (at most 4655 nodes), or one 15-bit code. No cancellation
helper goroutine is created. Allocation/zeroing and a blocked caller Read are
not preemptible; no hard latency or process-memory guarantee is claimed.

## Compressed ALPH framing

ALPH's first byte carries compression/filter/preprocessing flags. For compression
method 1, the following bytes contain a lossless image stream **without** the
ordinary five-byte VP8L signature/dimension/version header. The adapter supplies
checked container/frame dimensions and decodes the real transform/cache/Huffman
stream, then takes its green channel as filtered alpha residuals. Alpha
unfiltering happens afterward, using the ALPH header's selected filter. It does
not treat the payload as an ordinary full WebP or assume green bytes are already
unfiltered. Reserved-bit/container policy is unchanged from P3/P4.

Primary format references:
https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification
https://developers.google.com/speed/webp/docs/riff_container#alpha

## Qualification and remaining work

Twenty-three new independent libwebp 1.5.0 RGBA fixtures cover VP8L palettes at all four
packing widths, predictor/cross-color/subtract-green/color-index transforms,
alpha-bearing images, compressed ALPH's four filters, narrow/odd dimensions and
explicit hidden RGB and duplicate-simple zero-bit alignment. Upstream artwork attribution and fixture hashes are kept
under testdata/lossless. The procedural generator's libwebp requirement is an
explicit development oracle only. Normal Go tests need no libwebp installation.

Tests additionally exercise every selected allocation-refusal site, cancellation
through decoding/transform phases, malformed/incomplete Huffman trees, duplicate
transforms, sparse huge table indices, impossible group lengths, overlapping and
out-of-bounds back references, truncation and tiny budgets. Fuzzing is bounded
by input/pixel/allocation limits.

This does not complete animation, transparent encoding, Document/Reader/DecodeAll,
CLI, browser comparisons, broad holdout coverage, sustained fuzzing, external
security review or adversarial wall-clock cancellation qualification. Those
remain mandatory work; a completed still decoder is not a completed toolkit.
