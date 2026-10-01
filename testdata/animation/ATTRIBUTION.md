# Procedural animation fixtures

All artwork is generated from literal RGBA constants and simple rectangular
sequences by tools/generate-animation-fixtures.py; no third-party image artwork
is included. Generator and procedural artwork use this repository's MIT license.

Independent assembly/oracle: libwebpmux and WebPAnimDecoder 1.5.0 (reported API
versions 0x10500), MODE_RGBA, threads disabled. Source/distribution license is
retained at third_party/libwebp-COPYING. No native library is required by Go tests.
Full-file and canvas SHA-256 values and all controls are in provenance.json.

VP8L payloads are independently constructed constant-symbol streams, preserving
hidden RGB. Mixed VP8 samples use libwebp RGBA encoding and neutral chroma;
per-frame reference decoding disables fancy upsampling. The full-animation oracle
uses WebPAnimDecoder, whose default chroma interpolation is immaterial to these
neutral samples. This is not a claim of general fancy VP8 parity.

Framing mutations after libwebpmux assembly add ordered U001/U002 nested unknowns,
early EXIF/TOPU between frames, and remove one frame to retain an explicit one-frame
animation. WebPAnimDecoder accepts and supplies references for the resulting files.
