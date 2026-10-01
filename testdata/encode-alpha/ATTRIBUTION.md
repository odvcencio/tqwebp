# Public-encoder alpha/animation fixtures

Procedural neutral-gray artwork and native tqwebp outputs are produced by
`tools/generate-encode-fixtures.go`, using this repository's MIT license. No
third-party artwork is present. `inputs.json` defines exact source bytes,
canvas, frame order, alpha and durations. Encoding uses quality80/method4.

Independent oracle outputs are produced by `tools/verify-encode-fixtures.py`
using explicit libwebp1.5.0 and WebPAnimDecoder1.5.0 paths. Still references use
MODE_RGBA with no fancy upsampling; animations use WebPAnimDecoder MODE_RGBA.
Neutral chroma avoids conflating the existing nearest-chroma VP8 decoder policy
with libwebp's default fancy interpolation. These fixtures are not evidence of
general fancy interpolation parity. The libwebp BSD license is retained under
third_party/libwebp-COPYING. Go tests require no native library installation.

Exact decoded alpha and frame controls are compared against procedural inputs;
full independent decoded canvas hashes are recorded in oracle.json. Native
bitstreams are compared to checked-in outputs by public API regression tests.
The source pixels are not promised equal to lossy decoded RGB.

Metadata payloads are opaque test bytes, not validated ICC/EXIF/XMP documents.
This tests explicit transport and ordering without claiming profile processing.
