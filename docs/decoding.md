# Intermediate VP8 still decoder (P5/P6 slice)

This candidate adds **real decoded pixels**, not a DecodeConfig forwarding
alias. Decode and DecodeContext return independently owned `*image.NRGBA` for
VP8 stills, including extended still containers and uncompressed ALPH with
none/horizontal/vertical/gradient filters. Metadata is intentionally omitted
from these pixel-only calls; use container.Demux for byte-preserving metadata.

VP8L, compressed ALPH, composed animation, DecodeAll, Reader, Document and
transparent encoding are **required remaining work**. Decode returns
ErrUnsupportedFeature for the first two, ErrAnimatedImage for declared
animation, and never silently returns frame zero. This is not the complete
P5/P6 or usability release, and browser qualification remains open.

## Pixel policy

VP8 uses limited-range BT.601 inverse conversion with **nearest 4:2:0 chroma**
and libwebp 1.5.0 fixed-point rounding, with filtering enabled and dithering off.
It intentionally differs from libwebp's default fancy/bilinear chroma
upsampling. Color boundaries can look less smooth. Oracle comparisons use
`no_fancy_upsampling=1`, never default-mode results mislabeled as equivalent.
No ICC transform or orientation operation occurs. Do not label raw numeric
channels as converted to sRGB. ALPH bytes are exact; no premultiplication or
implicit background is applied. Hidden RGB is not guaranteed by lossy coding.

Future interpolation support must retain checked allocations and cooperative
cancellation. Versioned decoder output is not promised byte-stable forever.

## API

Use `Decode(r)` for bounded defaults or
`DecodeContext(ctx, r, ReadLimits{MaxWorkingBytes: 128 << 20})` to override
individual limits. Zero fields select published defaults; negative fields fail
with ErrInvalidLimits. A nil context is invalid, a pre-cancelled context returns
its error before reader methods, and nil/typed-nil readers are rejected.
The implementation consumes exactly one RIFF extent, leaving trailing bytes
unread. Decode validates the full container extent and codec entropy stream.

`DecodeConfig(r)` reads at most 30 bytes, checks the RIFF/header lengths and
canvas limit, and returns NRGBA dimensions. It does not read full image data or
validate entropy, metadata or trailing chunks. It can inspect VP8L and animated
headers even though this candidate cannot decode their pixels. Header success
must never be treated as decode success or whole-file validation.

Root import has no global image registration. Opt in with:

```go
import _ "m31labs.dev/tqwebp/register"
```

The registration package wires image.Decode and image.DecodeConfig. Importing
another WebP registration package is ambiguous: Go uses the first registered
matching decoder. Avoid that combination. Animation remains an error.

## Resource contract and allocation audit

Defaults: 64 MiB input, 16 million canvas pixels, 16 million frame pixels,
1000 frames, 256 million aggregate decoded pixels, 4 MiB metadata, 256 MiB
library-managed working allocations and ten minutes one-pass duration. Still
calls perform one frame; duration is zero. Animation fields are reserved for
future implementation. An additional fixed 4096-chunk maximum is enforced.

MaxWorkingBytes counts library-managed live backing storage, excluding caller
input/copies, Go object/allocator/runtime overhead, goroutine stacks and process
RSS. It is not a sandbox or process-memory limit. No unbounded pool is used.
An audited conservative reservation is checked before each allocating phase:

1. Before compressed input allocation: `N + 65536`, where N is the checked
   complete RIFF extent, also checked against input bytes and platform int.
2. Before Demux: `B = N + P + 256*(C+1) + 65536`, where P is total payload
   lengths and C is observed chunk count, both found without allocating in an
   overflow-safe preflight. P covers Demux's payload copies. The descriptor
   reserve covers backing capacities during append of Chunk slices (40 bytes
   each on 64-bit), their FourCC strings and frame storage. Fixed slack covers
   bounded temporary buffers and decoder state.
3. Before VP8 and output: `B + 388*MW*MH + 6*MW + V + 4*W*H`, where MW/MH
   are ceil(W/16), ceil(H/16), and V is the VP8 payload length. Padded YCbCr
   uses 384 bytes per macroblock; filter parameters four bytes per macroblock;
   row predictors six bytes per macroblock column. Partition backing allocations
   in total cannot exceed V, including partition length bytes. NRGBA is 4*W*H.
   Alpha unfilters directly into NRGBA and requires no second alpha plane.

`internal/vp8decode/adaptation_test.go` asserts the audited struct sizes. The
adapted decoder contains only these dynamic backing allocations. Header
partition lengths are checked before allocation. Every package update must
repeat this audit; a new Huffman/VP8L implementation cannot reuse this bound.
Reservation is conservative (including storage whose liveness may end sooner),
so an image may be refused even when a different decoder could fit the cap.
LimitError carries resource, limit and computed amount, and matches
ErrLimitExceeded. Malformed and unsupported errors remain distinct. Underlying
I/O and unexpected EOF identities are preserved through FormatError wrapping.

## Cancellation and provenance

The narrow x/image/vp8 adaptation adds context checks before each reconstruction
macroblock and each filtering macroblock, plus each 4096-byte partition read.
All reader loops check context after every Read and reject 100 consecutive
zero-progress returns with io.ErrNoProgress; invalid counts are rejected.
Conversion and alpha unfilter check each row. Header entropy loops and one
macroblock have fixed bounded work. Allocating/zeroing a pre-reserved backing
array is not interruptible, so no universal millisecond latency or hard real-time
claim is made. Worst-case latency remains a release qualification gate. A
blocked arbitrary Reader.Read cannot be interrupted; callers must supply I/O
deadlines or external process supervision. No leaked cancellation goroutines
are created.

The source is golang.org/x/image v0.38.0/vp8, already pinned by go.mod.
The implementation is copied only because its CPU loops lack cancellation
hooks; it is not a novel decoder or a libwebp runtime wrapper. Original BSD
notices and LICENSE are preserved in internal/vp8decode. See provenance.json
there for upstream hashes, adaptation scope and maintenance responsibility.
The independent fixtures come from x/image and libwebp 1.5.0; provenance.json
under testdata/decode pins inputs and raw oracle RGBA outputs. The optional
Python generator explicitly requires a selected development libwebp and is
never run by the Go package or normal tests. Production remains native Go,
without cgo, shared-library probing, external executable, or network access.
