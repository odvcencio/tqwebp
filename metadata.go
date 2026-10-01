package webp

import "m31labs.dev/tqwebp/container"

// Metadata carries opaque ICC, EXIF and XMP payloads. Use the container package
// for compressed-payload-preserving metadata edits; Encode remains pixel-only.
type Metadata = container.Metadata
