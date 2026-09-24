//go:build !bep

package main

import (
	chai "github.com/chai2010/webp"
	"image"
	"io"
)

func cgoEncoder(name string, m image.Image, q int) func(io.Writer) error {
	if name != "chai" {
		panic("build with -tags bep")
	}
	return func(w io.Writer) error { return chai.Encode(w, m, &chai.Options{Quality: float32(q)}) }
}
