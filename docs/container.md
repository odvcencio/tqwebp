# Container and metadata transport

The experimental container API provides bounded structural inspection and canonical compressed-payload-preserving remux for still and animated WebP. VP8, VP8L, ALPH, ANIM/ANMF controls, opaque ICC/EXIF/XMP, ordered file-level unknown chunks and ordered per-frame unknown chunks are supported. It does not decode entropy-coded pixels, validate profiles, apply orientation, parse TIFF/XML or compose pixels; use the root [Reader/DecodeAll APIs](animation.md) for composition. Invalid controls, rectangles, framing and enum values are rejected.

The existing root encoder, Options, Limits, defaults, error precedence and
opaque output bytes are unchanged. Root `Metadata` aliases `container.Metadata`;
root Encode still has no metadata inference or retention behavior.

## Inspect and explicitly edit

```go
ctx := context.Background()
f, err := container.Demux(ctx, src, container.Limits{})
if err != nil { return err }
fmt.Println(f.Canvas, len(f.Frames), len(f.MetadataChunks))

// The typed view chooses the first occurrence, including an empty occurrence.
metadata := f.Metadata()
_ = metadata.ICC // opaque bytes, not proof of profile validity

// Explicit privacy policy: remove all EXIF and XMP, including duplicates.
keep := make([]container.Chunk, 0, len(f.MetadataChunks))
for _, chunk := range f.MetadataChunks {
    if chunk.FourCC == "ICCP" { keep = append(keep, chunk) }
}
f.MetadataChunks = keep
// Drop unknown chunks only if that is the caller's selected policy.
f.UnknownChunks = nil
return container.Mux(ctx, dst, f, container.Limits{})
```

To replace a category, remove **all** chunks of that FourCC and append the
caller-selected replacement. `Metadata()` is a borrowed view into File storage;
changing the returned slice contents changes the file. Replacing a view field
alone does not update `MetadataChunks`. Demux owns payload storage independently
of input; callers must not concurrently mutate a File or its slices during Mux.
Metadata payload contents never appear in library diagnostics.

## Structural policy and fidelity

- RIFF lengths, chunk lengths, overflow, zero padding, reconstruction ordering,
  duplicate critical chunks and codec-header/canvas dimensions are checked
- VP8X reserved fields and future extension bytes are accepted and ignored;
  canonical writers emit zero reserved fields and the current ten-byte header
- VP8L alpha hints are not entropy validation. `File.Alpha` preserves the VP8X
  declaration independently, so a false codec hint cannot clear that signal
- ICC precedes image data. EXIF/XMP may be read before or after image data;
  canonical output places them after image data, preserving their relative order
- Metadata duplicates, including empty payloads, are retained. Typed access uses
  the first category occurrence. Mux never silently deduplicates
- Choosing Demux explicitly selects unknown-chunk preservation. Their relative
  order and payloads survive; canonical Mux places them after known chunks
- An ANIM chunk with animation unset is ignored as required by the specification
- Reserved ALPH bits are accepted by Demux but refused by Mux's strict writer
  policy. Callers must explicitly normalize that header before remux; the library
  never edits compressed payload bytes behind the caller's back
- Only one declared RIFF extent is consumed. Trailing bytes remain unread, as
  allowed by the format. Demux is not a trailing-data or entire-stream validator

Unchanged remux preserves compressed image, alpha and retained metadata bytes,
not whole-file byte identity. Canonicalization can change ordering, padding,
reserved header data or simple-versus-extended framing. Retaining a pixel
payload is not a claim that its entropy stream is valid. ALPH compressed streams
are transported without decompression; raw ALPH length is checked.

## Limits and errors

Zero-valued `container.Limits` fields select these defaults:

| Field | Default |
| --- | ---: |
| MaxInputBytes | 64 MiB |
| MaxOutputBytes | 64 MiB |
| MaxChunks | 4096 |
| MaxFrames | 1000 |
| MaxMetadataBytes | 4 MiB |
| MaxRetainedBytes | 64 MiB |

Positive values override each default; negative values return ErrInvalidLimits.
Demux and Mux bound the stored animation frame count. A still contains one
frame; playback loop counts never expand the stored frames or parsing work.
Input/output counts include all RIFF framing/padding. Metadata counts every
occurrence. Retained bytes is an aggregate payload budget, including unknown
and ignored chunk payloads; it excludes object overhead and caller storage.
It is not a process-memory/RSS limit. Mux retains caller payloads while allocating
one exact complete-output buffer, so output and retained budgets are separate.
Demux allocates per chunk after declared input, retained and metadata checks;
chunk count is checked before each chunk. No canvas-sized pixel buffer is used.

Context validation comes first, then cancellation, limits and reader/writer.
Nil and typed-nil contexts/readers/writers are refused. Context is checked between
caller callbacks and bounded copies; repeated empty reads terminate with
io.ErrNoProgress. A blocked caller callback cannot be interrupted. Supply I/O
deadlines for hard I/O bounds; no goroutines are launched to race callbacks.

Use container's ErrInvalidFormat, ErrUnsupportedFeature, ErrInvalidMetadata,
ErrLimitExceeded and typed FormatError/LimitError. FormatError reports an offset,
chunk FourCC and a zero-based frame index when identified; file-level errors
use frame=-1. Truncation retains
io.ErrUnexpectedEOF and malformed classification. Other reader errors remain
available through errors.Is. Writer errors return unchanged; short writes return
io.ErrShortWrite. Error text is diagnostic, not a stable parsing interface.

Mux completes structural validation, budgets and private serialization before
writing. Refusals and pre-commit cancellation write zero bytes. Cancellation or
an I/O failure after the first external write may leave a partial destination.

## Evidence boundary

Unit tests include truncation at every byte, forged lengths, padding, flags,
order, duplicate/empty metadata, unknown retention, limits, cancellation and
writer identity. The repository's independent libwebp-generated VP8 fixture is
remuxed with metadata and checked with the existing independent Go pixel oracle.
Structural fuzz smoke is separate from sustained fuzzing, browser interoperability,
full decoder conformance or an independent security audit. Those release gates
remain open.

Primary format references:
- https://developers.google.com/speed/webp/docs/riff_container
- https://datatracker.ietf.org/doc/html/rfc9649
- https://datatracker.ietf.org/doc/html/rfc6386#section-19.1

## Animation canonical layout

Animation Mux writes VP8X, ICC metadata, ANIM, ANMF frames, non-ICC metadata, then file-level unknown chunks. Within each ANMF, ALPH precedes its VP8 payload (or VP8L stands alone), followed by ordered per-frame unknown chunks. Unknown payloads and relative order within their list survive, as do duplicate metadata payloads. Original interleaving, padding layout and extension bytes are not preserved byte-for-byte. No raw-layout mode is provided. Mux transports already encoded frame payloads; it is not a pixel animation encoder.

## Experimental API compatibility

Animation added EncodedFrame.UnknownChunks. This extends an exported experimental
struct and breaks positional EncodedFrame literals from the still-only candidate.
Use keyed literals for this container API. Root Options and Limits retain their
original field order and positional consumer compatibility.
