package webp_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	webp "m31labs.dev/tqwebp"
	"time"
)

func ExampleEncodeAll() {
	first := image.NewNRGBA(image.Rect(0, 0, 3, 3))
	first.SetNRGBA(1, 1, color.NRGBA{R: 40, G: 80, B: 120, A: 127})
	second := image.NewNRGBA(image.Rect(0, 0, 3, 3))
	doc := &webp.Document{Canvas: image.Pt(3, 3), Animated: true, LoopCount: 2,
		Frames:   []webp.Frame{{Pixels: first, Duration: 17 * time.Millisecond}, {Pixels: second, Duration: 33 * time.Millisecond}},
		Metadata: webp.Metadata{XMP: []byte("explicitly selected metadata")}}
	var dst bytes.Buffer
	if err := webp.EncodeAll(context.Background(), &dst, doc, nil, webp.DocumentLimits{Limits: webp.Limits{MaxPixels: 100, MaxOutputBytes: 1 << 20}}); err != nil {
		panic(err)
	}
	decoded, err := webp.DecodeAll(context.Background(), bytes.NewReader(dst.Bytes()), webp.ReadLimits{})
	if err != nil {
		panic(err)
	}
	// Re-encoding preserves controls and alpha; lossy RGB is not a pixel-exact remux.
	fmt.Println(decoded.Animated, len(decoded.Frames), decoded.LoopCount, decoded.Frames[1].Duration)
	// Output: true 2 2 33ms
}
func ExampleEncode_opaqueOnly() {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	// Migration for applications that deliberately relied on prior alpha refusal.
	if !img.Opaque() {
		fmt.Println("application policy: opaque images only")
		return
	}
	var dst bytes.Buffer
	if err := webp.Encode(&dst, img, nil); err != nil {
		panic(err)
	}
	// Output: application policy: opaque images only
}
