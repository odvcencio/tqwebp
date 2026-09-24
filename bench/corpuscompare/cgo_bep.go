//go:build bep

package main

import (
	bep "github.com/bep/gowebp/libwebp"
	"github.com/bep/gowebp/libwebp/webpoptions"
	"image"
	"io"
)

func cgoEncoder(name string, m image.Image, q int) func(io.Writer) error {
	if name != "bep" {
		panic("build without -tags bep")
	}
	return func(w io.Writer) error { return bep.Encode(w, m, webpoptions.EncodingOptions{Quality: q}) }
}
