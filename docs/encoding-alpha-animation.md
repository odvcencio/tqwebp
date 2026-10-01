# Transparent stills and animation encoding

The existing Encode, EncodeWithLimits and EncodeContext calls now accept
transparent images. They encode lossy VP8 color with exact uncompressed 8-bit
ALPH. No flattening or external encoder is selected. An input previously accepted
as opaque follows the unchanged native path, preserving its encoded bytes,
Options/Limit layout, defaults and validation/writer behavior.

Raw alpha costs about one byte per visible pixel plus container framing. This
slice does not implement compressed ALPH or lossless VP8L color encoding. Quality
100 remains lossy 4:2:0; it does not make RGB lossless. Compressed ALPH/VP8L decoding
remain supported independently of encoding choices.

## Pixel semantics

Transparent input is materialized as straight opaque NRGBA color plus a separate
alpha plane. Stored NRGBA/NRGBA64 channels are read directly; 16-bit straight
channels and alpha reduce by taking their high byte. RGBA/RGBA64 and generic
premultiplied colors use Go's NRGBA color conversion (unpremultiply before
8-bit reduction). RGB at alpha zero cannot be recovered from premultiplied input.
No premultiplied RGB is treated as straight color. Input nonzero origins and
subimages retain their visible dimensions.

Hidden RGB in a straight input is fed to the lossy color encoder but is not an
exact-pixel preservation promise. Near-transparent RGB can have premultiplication
quantization error already present in the source. Independent alpha equality and
straight-color VP8 payload tests distinguish those facts from accidental darkening.
No ICC transform or orientation application occurs. Profile metadata remains opaque.

ErrAlphaUnsupported remains declared for source compatibility; normal Encode no
longer returns it. Applications requiring opaque-only input must explicitly check
opacity, as the compiled example demonstrates. Existing explicit image/draw
flattening remains available when a caller selects an opaque background.

## Document convenience

EncodeAll(ctx,w,doc,options,DocumentLimits{}) consumes the existing Document type
returned by DecodeAll. Each Frame is a full displayed canvas; it must match Canvas
and callers must not mutate pixels, slices, metadata or options during the call.
The library reads but never modifies or takes ownership of those inputs.

A still has one frame, zero duration, zero loop/background controls and
Animated=false. An animation has at least one frame and Animated=true, including
one-frame animation. Durations are exact nonnegative milliseconds up to 16777215;
fractional milliseconds are rejected. LoopCount and Background are preserved as
controls, and loop count never expands encoded work.

The initial encoder emits every animation frame at (0,0), full-canvas BlendReplace,
DisposeNone. Transparent holes replace earlier pixels rather than accidentally
retaining them. This preserves the displayed-frame sequence without a delta-frame
optimizer. Container Demux/Mux still supports arbitrary valid rectangles, blending
and disposal when working with already encoded payloads.

EncodeAll writes one chunk for each nonempty explicitly supplied ICC/EXIF/XMP
payload. Nil and empty categories are omitted. It does not infer, discover or
silently copy metadata. Canonical ICC placement precedes image data; EXIF/XMP
follow it. Use the container representation for duplicate/empty metadata and
compressed-payload-preserving remux.

## Limits and output ownership

DocumentLimits embeds the unchanged legacy Limits. Its embedded zero fields remain
unlimited. New zero fields use MaxFrames=1000, MaxTotalPixels=256 million,
MaxMetadataBytes=4MiB, MaxDuration=10 minutes. Negative fields are invalid. The
aggregate pixel resource is named total_pixels in LimitError; other resource names
retain the shared contract. Full-canvas encoding retains the VP8 limit of16383 per
axis; this does not lower the decoder/container limits.

Validate every frame's geometry/duration and metadata aggregate before reading
pixels. Compute all fixed framing, metadata and raw-alpha bytes before codec
serialization. Each internal VP8 encode receives only the remaining complete-file
allowance, adjusted for its temporary simple header. Padded payload lengths count,
including odd ALPH payloads. Internal smaller caps map back to the original public
output limit; format/platform ceilings remain identifiable by their effective cap.

Private source/color planes, reconstructions, macroblocks, raw alpha, prior encoded
frames and final container may coexist. MaxOutputBytes is an output rejection
limit, not a working-memory or process-RSS cap. No hard memory guarantee was added
to legacy encoding. Use pixel/frame/output bounds, concurrency admission and
supervision appropriate to deployment hardware.

All output is prepared before the first caller write. Invalid documents, encoding
failures, policy refusals and preparation cancellation leave the caller writer
untouched. A caller write failure or cancellation after writing starts can leave
partial output. External errors retain exact identity; short writes return
io.ErrShortWrite. No goroutine races a blocked image method or writer. Context
checks are cooperative across conversion, native encoder phases and mux.

## Evidence and remaining gates

The opaque 27-image/method byte manifest and existing quality gates remain intact.
New fixtures contain native tqwebp encodes checked by independent libwebp1.5 still
and animation decoders. Neutral chroma permits exact comparisons without hiding
the decoder's nearest-chroma versus fancy-upsample distinction. Raw alpha,
replacement holes, timing, loops and selected metadata are checked independently.
Fixtures/tools do not become runtime dependencies.

Browser compositing, sustained fuzzing, broad holdout/performance and platform
runtime gates remain separate release requirements. CLI is the next checkpoint.
A passing bounded test suite or scoped source review is not formal security approval.

Primary container specification: https://developers.google.com/speed/webp/docs/riff_container
