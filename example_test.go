package webp_test

import (
	"bytes"
	"errors"
	"fmt"
	decoder "golang.org/x/image/webp"
	"image"
	"image/color"
	"image/draw"
	webp "m31labs.dev/tqwebp"
)

func ExampleEncode() {
	img := image.NewGray(image.Rect(0, 0, 32, 24))
	var output bytes.Buffer
	if err := webp.Encode(&output, img, &webp.Options{Quality: 80}); err != nil {
		panic(err)
	}
	config, err := decoder.DecodeConfig(bytes.NewReader(output.Bytes()))
	if err != nil {
		panic(err)
	}
	fmt.Printf("WebP: %dx%d\n", config.Width, config.Height)
	// Output: WebP: 32x24
}

func ExampleEncodeWithLimits() {
	img := image.NewGray(image.Rect(0, 0, 32, 24))
	var output bytes.Buffer
	err := webp.EncodeWithLimits(&output, img, nil, webp.Limits{MaxPixels: 512})
	fmt.Println(errors.Is(err, webp.ErrLimitExceeded), output.Len())
	// Output: true 0
}

// Choose an explicit background when alpha is unnecessary for the output.
func ExampleEncode_flattenAlpha() {
	source := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	opaque := image.NewRGBA(source.Bounds())
	draw.Draw(opaque, opaque.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(opaque, opaque.Bounds(), source, source.Bounds().Min, draw.Over)
	var output bytes.Buffer
	fmt.Println(webp.Encode(&output, opaque, nil))
	// Output: <nil>
}
