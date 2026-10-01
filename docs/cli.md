# tqwebp command

Build this candidate with `go build ./cmd/tqwebp`. Release-tag installation and
packaged CLI binaries remain release gates; a local candidate build is not proof
that an older public tag already contains this command. `--version` reports the
build value (development unless supplied by release ldflags).

## Common operations

```sh
tqwebp photo.jpg -o photo.webp --quality 82
tqwebp cutout.png -o cutout.webp --preset compact --method 4
tqwebp - -o - --json < input.png > output.webp
tqwebp input.png -o opaque.webp --background '#ffffff'
tqwebp inspect input.webp
tqwebp inspect input.webp --validate --json
tqwebp decode input.webp -o image.png
tqwebp decode animated.webp -o selected.png --frame 2
tqwebp decode animated.webp --out-dir extracted-frames
tqwebp encode extracted-frames/manifest.json -o animation.webp
tqwebp metadata input.webp
tqwebp metadata input.webp -o stripped.webp --strip exif,xmp
tqwebp metadata input.webp -o edited.webp --icc selected-profile.icc
```

Flags may follow the input path. `-` selects stdin or binary stdout. No remote URL
is fetched. Binary output has no text prefix and diagnostics go to stderr. Output
to character devices, including terminals, requires explicit `--force`.

Inputs are PNG, JPEG and WebP. Animated WebP conversion preserves every stored
frame, timing and loop count through DecodeAll/EncodeAll. Selecting one displayed
frame requires explicit zero-based `--frame N`. Plain decode to a single PNG
refuses animation without that choice; `--out-dir` extracts the whole sequence.
GIF and APNG conversion are explicitly unsupported, rather than silently keeping
only frame zero. There is no implicit resizing or cropping.

Quality defaults to75 and method4. `standard`, `compact` and `smallest` map to
methods4,5 and6; the latter two remain experimental recipes, not size guarantees.
Explicit `--quality`/`--method` override the recipe. Quality100 is still lossy.

## Metadata and orientation

Pixel conversion defaults to `--metadata=color`: retain a structurally checked
associated RGB ICC payload, strip EXIF/XMP, and apply supported EXIF orientation.
`--metadata=none` writes no metadata. `--metadata=all` explicitly selects ICC,
EXIF and XMP. These choices do not waive input parsing or resource checks.

All eight TIFF/EXIF orientations are supported with checked offsets and geometry.
When orientation changes pixels and retained EXIF/XMP cannot safely be reconciled,
`--metadata=all` returns ErrInvalidMetadata with a choice to use color-only metadata
or explicit `--ignore-orientation`. The command does not pretend to rewrite
arbitrary TIFF/XMP structures. Ignoring orientation leaves it unapplied; it does
not claim that the metadata is valid or that a later viewer will ignore it.

ICC checks cover declared length/signature, RGB channel association and bounded
tag ranges (at most4096 tags). They are not complete ICC conformance validation,
a profile interpreter or an ICC transform. Profiled CMYK input cannot silently
copy its profile onto converted RGB; select `--metadata=none` explicitly if raw
standard-library channel conversion is intended. Numeric channels are never
reported as converted to sRGB. `--background` composites an explicitly selected
opaque numeric RGB background using Go's channel-space image/draw behavior.

PNG iCCP and supported XMP iTXt decompression is bounded and cancellation-aware.
JPEG ICC fragments are assembled under the aggregate metadata cap; TIFF offsets
and JPEG/PNG framing are checked. PNG output includes selected iCCP/eXIf/iTXt.
PNG eXIf preserves the TIFF payload but removes an exact optional six-byte
`Exif\0\0` transport wrapper, as required by PNG; this is distinct from untouched
WebP remux. PNG chunk admission includes the iCCP/XMP framing prefixes and checks
the2^31−1 data-length ceiling before narrowing or prefix-copy allocation.
[PNG framing and eXIf specification](https://www.w3.org/TR/png/)

The `metadata` command is a separate raw WebP preservation path. Its default
canonical remux keeps bounded metadata duplicates, empty payloads, compressed
frames and ordered unknown chunks without interpreting profiles or decoding pixels.
It never applies the pixel command's default color-only strip policy. Use `--strip`
for explicit removal and `--icc`, `--exif`, `--xmp` for replacement file payloads;
replacement removes all prior instances of that category, then adds one supplied
chunk. No raw metadata contents appear in diagnostics. Whole-file byte layout is
not preserved by canonical Mux; compressed payload identity is.

## Frame manifest

Paths resolve relative to the manifest's directory, or the current directory for
stdin manifests. They must name local still files; URLs and stdin frame paths are
rejected. Explicit local paths may refer outside the manifest directory; output
filenames are independently generated and never derived from input paths.

```json
{
  "version": 1,
  "canvas": [640, 480],
  "animated": true,
  "loop_count": 0,
  "background": [0, 0, 0, 0],
  "frames": [
    {"path": "frame-000000.png", "duration_ms": 17},
    {"path": "frame-000001.png", "duration_ms": 33}
  ],
  "metadata": {"icc": "profile.icc"}
}
```

Canvas and background arrays have exact lengths2 and4. Channels are RGBA bytes.
Frame order and exact integer millisecond durations are retained; loop count0
means infinite playback intent and is never expanded into work. One-frame animation
is retained. A static manifest uses animated=false, exactly one frame, zero duration
and zero loop/background controls. Missing animated defaults to true; a missing
version defaults to1. Unknown/duplicate fields and fractional durations are rejected.

Frame count, geometry and timing are admitted before referenced frame decoding.
Every frame must match the displayed canvas after selected orientation. Per-frame
profiles must agree; conflicting associations cannot be hidden behind one global
profile. Per-frame EXIF/XMP retained with `all` must match explicitly supplied global
manifest metadata. Extracted manifests round-trip that selected metadata through
sidecar paths. No delta optimizer, arbitrary frame scaling or profile conversion
runs behind this interface.

Extraction names are `frame-000000.png`, etc., plus fixed sidecar names and
`manifest.json`. Caller-controlled frame paths cannot escape the output directory.
A new directory is required, even with `--force`; unrelated existing directories
are never replaced. Each file uses the safe commit operation below. The manifest
is committed last as the completion marker. Multi-file extraction is not one atomic
transaction. Failures attempt cleanup of created files, but an I/O/cleanup failure
can leave complete or partial results; no all-or-none promise is made.

## Bounds, timeouts and signals

Initial defaults:

| Bound | Default |
|---|---:|
| Width / height |8192 /8192|
| Per-frame pixels |16,000,000|
| Compressed input |64MiB|
| Complete output |32MiB|
| Stored frames |1000|
| Aggregate pixels |256,000,000|
| Metadata |4MiB|
| Stored duration |10 minutes|

Each is configurable with the matching `--max-*` flag in `--help`. Width/height0
mean no additional axis limit; other CLI caps must be positive. Format/platform
bounds still apply. Manifest input-byte admission includes referenced frame and
sidecar files; the JSON input_bytes statistic describes the primary manifest/input.
Extraction's output cap is aggregate across its PNGs, sidecars and manifest.
The pixel decoder also retains the public default working-memory bound; these
policies are not a whole-process RSS guarantee. Raising caps is the caller's choice
and requires deployment memory/concurrency planning.

Unknown-length input reads detect the extra byte above the cap, check cooperative
cancellation and reject100 consecutive empty reads or invalid reader counts.
PNG/JPEG dimensions are inspected before image allocation. Manifest frame counts
are checked while parsing, before growing an unlimited frame list. No network
fetch, subprocess encoder, background worker pool or hidden native dependency exists.

`--timeout=30s` is an active codec-operation budget;0 disables it. Input reads and
ordinary Go PNG/JPEG decoding are not made interruptible by that budget. WebP
operations, metadata traversal and writing have cooperative checkpoints. An
arbitrary blocked I/O method still needs caller deadlines or process supervision.
First interrupt cancels controlled work; a second forces exit130 even when a
callback remains blocked. Forced termination may preclude a final JSON diagnostic
or temporary cleanup. Real subprocess tests use readiness handshakes to qualify
both cooperative and blocked-reader paths without timing sleeps.

## Output commits and results

Output is prepared before opening a destination temporary. Same-path and existing
same-inode input/output aliases are rejected, including with force. The temporary
is created in the destination directory; write and close errors are checked.
Without force, hard-link commit enforces no-clobber even if a competing file appears
after validation. Unsupported filesystem link operations fail rather than falling
back to unsafe overwrite. `--force` explicitly selects replacement through platform
Rename behavior; universal atomic replacement is not claimed.

A successful no-clobber link followed by temporary unlink failure can leave a
committed output while returning an error. Diagnostics explicitly warn about that
case. Cleanup never intentionally deletes a pre-existing destination. Stdout write
failure can leave partial bytes and cannot be rolled back.

`--json` writes one result to stderr on ordinary completion; stdout remains clean
for selected binary data. It includes resolved quality/method/preset, metadata,
background/orientation policy, source and output dimensions, frame/loop facts,
retained/removed categories, byte counts, elapsed time, version and stable error
category. Manifest source dimensions mean its declared logical canvas. Unknown
fields are absent; output_bytes is null on failure because partial output may exist.
Plain inspect leaves frame count unknown and validated=false; --validate consumes
all frames and trailer chunks in one declared RIFF extent and checks pixels. Raw
metadata operations establish structure only and do not claim entropy validation.

Exit codes:0 success;2 usage/options;3 invalid/unsupported input or unsafe metadata
policy;4 resource refusal;5 I/O failure;6 codec timeout;130 interrupt.

## Qualification boundaries

This command uses the existing public APIs rather than a second WebP implementation.
Unit/integration coverage includes file races, close/cleanup failures, metadata
bombs/offsets, orientation geometry, exact manifest bounds, JSON/stdout separation,
animation retention and real subprocess interruption. Production libraries remain
native Go. Cross-builds alone do not establish non-Linux runtime behavior. Browser,
profile-rendering, sustained fuzz, release-tag installation, broad holdout/performance
and independent formal security qualification remain separate release gates.
