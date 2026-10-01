package webp_test

import (
	"fmt"
	webp "m31labs.dev/tqwebp"
	"os"
)

func ExampleDecode() {
	f, err := os.Open("testdata/decode/alpha-filter-3.webp")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	// Real still pixels, with nearest chroma and no ICC transform. VP8L,
	// compressed ALPH and animation are explicitly rejected in this candidate.
	img, err := webp.Decode(f)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%T %v\n", img, img.Bounds())
	// Output: *image.NRGBA (0,0)-(19,17)
}
func ExampleDecodeConfig() {
	f, err := os.Open("container/testdata/blue-purple-pink.lossless.webp")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	// Config inspection can succeed for VP8L although Decode cannot yet decode
	// its pixels. Header success is never proof of complete-file validity.
	config, err := webp.DecodeConfig(f)
	if err != nil {
		panic(err)
	}
	fmt.Println(config.Width, config.Height)
	// Output: 150 100
}
