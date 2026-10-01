// Package register opts into standard image decoding with tqwebp. The root
// package has no registration side effect. Avoid importing multiple WebP
// registration packages: image uses the first matching registered decoder.
// Animation is rejected, never silently flattened. VP8/VP8L stills and raw or
// compressed ALPH are supported; full animation remains required work.
package register

import (
	"image"
	webp "m31labs.dev/tqwebp"
)

func init() { image.RegisterFormat("webp", "RIFF????WEBP", webp.Decode, webp.DecodeConfig) }
