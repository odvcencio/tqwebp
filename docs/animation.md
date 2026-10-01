# Bounded animation reading and composition

`NewReader(ctx, input, ReadLimits{})` parses control information through the first
frame boundary. Repeated `Next()` calls decode stored frames once, returning a
complete NRGBA display canvas and its exact duration. It does not expand loops,
clamp zero durations, sleep, or flatten animation. `Decode`/`DecodeContext` still
return `ErrAnimatedImage`; `DecodeConfig` remains header-only inspection.

A Reader frame is borrowed and read-only until the next `Next` or `Close`.
Copy it before advancing if you need retention. `DecodeAll` returns a Document
with independently owned frames and metadata. A still VP8L image bypasses
composition, retaining exact hidden RGB even at alpha zero. All methods on one
Reader must be called sequentially. Close is idempotent and neither closes nor
drains the caller's I/O. Errors are sticky; incomplete trailers are errors even
when every display frame has already been returned. Validated EOF leaves bytes
beyond the declared RIFF extent unread.

`Info` contains canvas, animation flag, loop count and background available at
header parsing. `Declared` is the VP8X feature declaration, or nil without VP8X.
`Completion` is nil until Next returns validated EOF, when frame count, aggregate
decoded pixels, duration and observed features become available. Observed Alpha
means actual decoded frame transparency, not the advisory VP8L bit. The returned
Info pointers are copies. No guessed final frame count is exposed early.

`Metadata()` before validated EOF returns ErrNotComplete. After EOF it returns
owned copies of the first ICC/EXIF/XMP occurrences, including an empty first
occurrence. Copy allocation is checked against the current working budget while
library-owned. Once returned, caller-retained copies are outside later library
accounting; repeated Metadata calls do not promise a whole-process memory cap.
Close releases the underlying metadata. Use container.Demux/Mux for duplicate
metadata and unknown preservation; pixel Reader deliberately drains unknowns.

## Compositor policy and independent oracle

The compositor implements libwebp 1.5.0 `src/demux/anim_decode.c` MODE_RGBA
non-premultiplied fixed-point blending, keyframe selection and disposal. A fresh
canvas and disposed rectangles are transparent black. ANIM Background is retained
as a rendering hint; it is not implicitly painted behind transparent pixels.
Replacement, first/keyframe and freshly disposed regions copy exact raw channels.
Source alpha zero over a retained destination leaves it unchanged. Other blends
use libwebp's integer rounding, not image/draw's premultiplied color arithmetic.

Algorithm source: https://chromium.googlesource.com/webm/libwebp/+/refs/tags/v1.5.0/src/demux/anim_decode.c
Google BSD notice is retained at third_party/libwebp-COPYING. The adaptation uses
one canvas instead of libwebp's previous/current canvas pair because Reader
outputs are borrowed. Allocation admission, checked geometry and cooperative
checkpoints are local additions. No foreign runtime or subprocess is used.

The seven procedural fixture animations (81 displayed frames) are independently
assembled and decoded by explicit development-only libwebpmux/WebPAnimDecoder
1.5.0. They include 64 adversarial alpha/disposal/rounding frames, partial first
frames, keyframes after disposal, hidden RGB, zero/max durations, loop counts,
odd canvas/rectangle sizes, metadata and ordered nested unknowns. WebP offsets
are encoded in even pixel units; odd offsets are not representable. VP8L canvas
pixels must match exactly. The mixed VP8 fixtures use neutral chroma so they do
not conceal the existing nearest-chroma versus fancy-upsample distinction.
Ordinary Go tests read checked-in fixtures without libwebp installed.

## Resource and cancellation audit

- No full RIFF or enclosing ANMF spool: the shared cursor validates headers and
  retains only one compressed frame plus selected metadata
- One 64 KiB fixed-state allowance covers cursor scratch/descriptors/errors;
  retained payloads and descriptor growth are separately admitted before allocation
- Codec scratch, compressed payload, decoded subframe and existing canvas can
  coexist; their shared budget reservations overlap. Codec scratch is released
  after decoding, subframe storage after composition, and canvas on Close
- Canvas reservation is checked 4*width*height. Geometry/storage products are
  validated before indexing. Disposal clears and pixel work have row checkpoints;
  long clear/copy/blend loops have bounded inner checkpoints
- DecodeAll additionally charges each retained canvas and conservative frame-slice
  growth; output ownership transfers only on successful return
- Frame count, aggregate decoded pixels, duration, canvas/frame pixels, metadata,
  input extent and live storage are bounded. All outer/nested chunks share a
  fixed 4096 count ceiling. LoopCount never multiplies work or counters
- Working limits cover library-managed live backing, excluding runtime overhead,
  garbage awaiting collection, caller-owned inputs/returned outputs and process RSS
- Cooperative cancellation is checked at frame/row/read and codec checkpoints.
  It cannot interrupt an already blocked caller Read; use I/O deadlines. Invalid
  read counts are ErrInvalidReader; 100 empty reads are io.ErrNoProgress. Final
  bytes with EOF are accepted; non-EOF caller errors retain their identity

See internal/webpwire/README.md for the cursor allocation ledger. The scoped
source review and bounded gates do not constitute formal security approval.

## Remaining usability work

[Transparent and EncodeAll encoding](encoding-alpha-animation.md) is now supported.
[CLI workflows](cli.md) are now implemented; broad browser/holdout/release qualification remains required. Canonical Mux remuxes
existing compressed frames without recompression; it does not provide byte-exact
original RIFF layout preservation ; pixel animation encoding uses the separate EncodeAll API.
