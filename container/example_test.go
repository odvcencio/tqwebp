package container_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"testing"

	oracle "golang.org/x/image/webp"
	webp "m31labs.dev/tqwebp"
	"m31labs.dev/tqwebp/container"
)

func ExampleMux() {
	ctx := context.Background()
	var original bytes.Buffer
	if err := webp.Encode(&original, image.NewGray(image.Rect(0, 0, 1, 1)), nil); err != nil {
		panic(err)
	}
	f, err := container.Demux(ctx, bytes.NewReader(original.Bytes()), container.Limits{})
	if err != nil {
		panic(err)
	}
	// Explicitly choose metadata to write. Opaque transport does not validate EXIF.
	f.MetadataChunks = []container.Chunk{{FourCC: "EXIF", Data: []byte("example opaque payload")}}
	var edited bytes.Buffer
	if err = container.Mux(ctx, &edited, f, container.Limits{}); err != nil {
		panic(err)
	}
	result, err := container.Demux(ctx, &edited, container.Limits{})
	if err != nil {
		panic(err)
	}
	fmt.Println(string(result.Metadata().EXIF))
	// Output: example opaque payload
}
func TestIndependentFixturesRemux(t *testing.T) {
	for _, path := range []string{"../testdata/golden/bt601/libwebp_q100.webp", "testdata/blue-purple-pink.lossless.webp"} {
		t.Run(path, func(t *testing.T) {
			input, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f, err := container.Demux(context.Background(), bytes.NewReader(input), container.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), f.Frames[0].VP8...)
			lossless := append([]byte(nil), f.Frames[0].VP8L...)
			f.MetadataChunks = []container.Chunk{{FourCC: "ICCP", Data: []byte{1, 2, 3}}, {FourCC: "EXIF", Data: []byte{4}}, {FourCC: "XMP ", Data: []byte("opaque")}}
			var b bytes.Buffer
			if err = container.Mux(context.Background(), &b, f, container.Limits{}); err != nil {
				t.Fatal(err)
			}
			got, err := container.Demux(context.Background(), bytes.NewReader(b.Bytes()), container.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, got.Frames[0].VP8) || !bytes.Equal(lossless, got.Frames[0].VP8L) {
				t.Fatal("compressed image changed")
			}
			a, err := oracle.Decode(bytes.NewReader(input))
			if err != nil {
				t.Fatal(err)
			}
			z, err := oracle.Decode(bytes.NewReader(b.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if a.Bounds() != z.Bounds() {
				t.Fatal("bounds")
			}
			for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
				for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
					ar, ag, ab, aa := a.At(x, y).RGBA()
					zr, zg, zb, za := z.At(x, y).RGBA()
					if ar != zr || ag != zg || ab != zb || aa != za {
						t.Fatal("pixels changed")
					}
				}
			}

		})
	}
}
