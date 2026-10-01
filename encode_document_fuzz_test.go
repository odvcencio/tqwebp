package webp

import (
	"bytes"
	"context"
	"errors"
	decoder "golang.org/x/image/webp"
	"image"
	"image/color"
	"m31labs.dev/tqwebp/container"
	"testing"
	"time"
)

func FuzzEncodeDocument(f *testing.F) {
	f.Add([]byte{0, 1, 127, 254, 255}, uint8(3), uint8(5), uint8(3), uint8(0), uint16(4096))
	f.Add([]byte{255, 0}, uint8(1), uint8(1), uint8(1), uint8(1), uint16(1))
	f.Fuzz(func(t *testing.T, data []byte, w, h, count, flags uint8, cap uint16) {
		if len(data) == 0 || len(data) > 128 {
			return
		}
		width, height, n := int(w%8)+1, int(h%8)+1, int(count%4)+1
		doc := &Document{Canvas: image.Pt(width, height), Animated: n > 1 || flags&1 != 0, LoopCount: uint16(flags)}
		if !doc.Animated {
			doc.LoopCount = 0
		}
		for i := 0; i < n; i++ {
			m := image.NewNRGBA(image.Rect(-2, 3, -2+width, 3+height))
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					v := data[(i+x+3*y)%len(data)]
					m.SetNRGBA(x-2, y+3, color.NRGBA{R: v, G: 127, B: 255 - v, A: v})
				}
			}
			duration := time.Duration(i) * time.Millisecond
			if !doc.Animated {
				duration = 0
			}
			doc.Frames = append(doc.Frames, Frame{Pixels: m, Duration: duration})
		}
		if flags&2 != 0 {
			doc.Metadata.XMP = append([]byte(nil), data...)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if flags&4 != 0 {
			cancel()
		}
		if flags&8 != 0 {
			doc.Frames[n-1].Duration = -time.Millisecond
		}
		var out bytes.Buffer
		e := EncodeAll(ctx, &out, doc, &Options{Quality: 75, Method: 1}, DocumentLimits{Limits: Limits{MaxOutputBytes: int64(cap)}, MaxFrames: 4, MaxTotalPixels: 256, MaxMetadataBytes: 128})
		if e != nil {
			if out.Len() != 0 {
				t.Fatal("prepared refusal wrote bytes", e)
			}
			if !errors.Is(e, ErrLimitExceeded) && !errors.Is(e, ErrInvalidDocument) && e != context.Canceled {
				t.Fatal(e)
			}
			return
		}
		encoded, e := container.Demux(context.Background(), bytes.NewReader(out.Bytes()), container.Limits{})
		if e != nil {
			t.Fatal(e)
		}
		if encoded.Animated != doc.Animated || encoded.Canvas != doc.Canvas || encoded.LoopCount != doc.LoopCount || len(encoded.Frames) != n {
			t.Fatal("control mismatch")
		}
		for i, frame := range encoded.Frames {
			if frame.Duration != doc.Frames[i].Duration {
				t.Fatal("duration mismatch")
			}
			frame.Duration = 0
			frame.Blend = container.BlendOver
			var still bytes.Buffer
			if e = container.Mux(context.Background(), &still, &container.File{Canvas: doc.Canvas, Frames: []container.EncodedFrame{frame}}, container.Limits{}); e != nil {
				t.Fatal(e)
			}
			m, e := decoder.Decode(bytes.NewReader(still.Bytes()))
			if e != nil {
				t.Fatal(e)
			}
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					_, _, _, a := m.At(x, y).RGBA()
					_, _, _, want := doc.Frames[i].Pixels.At(x-2, y+3).RGBA()
					if a != want {
						t.Fatal("independent alpha mismatch")
					}
				}
			}
		}
	})
}
