//go:build ignore

// Development-only deterministic public-API fixture producer.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	webp "m31labs.dev/tqwebp"
	"os"
	"path/filepath"
	"time"
)

type frameRecord struct {
	Source   string `json:"source"`
	Duration int    `json:"duration_ms"`
}
type record struct {
	Name     string        `json:"name"`
	Width    int           `json:"width"`
	Height   int           `json:"height"`
	Animated bool          `json:"animated"`
	Loops    uint16        `json:"loops"`
	Frames   []frameRecord `json:"frames"`
}

func run() error {
	out := flag.String("out", "", "explicit output directory")
	flag.Parse()
	if *out == "" {
		return fmt.Errorf("-out required")
	}
	if e := os.MkdirAll(*out, 0755); e != nil {
		return e
	}
	var records []record
	for _, tc := range []struct {
		name     string
		w, h, n  int
		animated bool
	}{{"alpha-odd", 17, 19, 1, false}, {"alpha-tiny", 1, 1, 1, false}, {"opaque-still", 3, 5, 1, false}, {"replace-holes", 7, 5, 4, true}, {"one-frame-alpha", 3, 3, 1, true}} {
		doc := &webp.Document{Canvas: image.Pt(tc.w, tc.h), Animated: tc.animated, Metadata: webp.Metadata{ICC: []byte("profile"), EXIF: []byte("selected-exif"), XMP: []byte("selected-xmp")}}
		if tc.animated {
			doc.LoopCount = 3
			doc.Background = color.NRGBA{R: 1, G: 2, B: 3, A: 127}
		}
		rec := record{Name: tc.name, Width: tc.w, Height: tc.h, Animated: tc.animated, Loops: doc.LoopCount}
		for i := 0; i < tc.n; i++ {
			m := image.NewNRGBA(image.Rect(0, 0, tc.w, tc.h))
			for y := 0; y < tc.h; y++ {
				for x := 0; x < tc.w; x++ {
					gray := uint8(40 + 50*i)
					a := []uint8{0, 1, 127, 254, 255}[(x+3*y+i)%5]
					if tc.name == "opaque-still" || tc.name == "replace-holes" && (i == 0 || i == 3) {
						a = 255
					}
					m.SetNRGBA(x, y, color.NRGBA{R: gray, G: gray, B: gray, A: a})
				}
			}
			duration := 0
			if tc.animated {
				duration = []int{0, 17, 1, 33}[i]
			}
			name := fmt.Sprintf("%s-%02d.source.rgba", tc.name, i)
			if e := os.WriteFile(filepath.Join(*out, name), m.Pix, 0644); e != nil {
				return e
			}
			rec.Frames = append(rec.Frames, frameRecord{Source: name, Duration: duration})
			doc.Frames = append(doc.Frames, webp.Frame{Pixels: m, Duration: time.Duration(duration) * time.Millisecond})
		}
		var encoded bytes.Buffer
		if e := webp.EncodeAll(context.Background(), &encoded, doc, &webp.Options{Quality: 80, Method: 4}, webp.DocumentLimits{Limits: webp.Limits{MaxOutputBytes: 1 << 20}}); e != nil {
			return e
		}
		if e := os.WriteFile(filepath.Join(*out, tc.name+".webp"), encoded.Bytes(), 0644); e != nil {
			return e
		}
		records = append(records, rec)
	}
	b, e := json.MarshalIndent(records, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(*out, "inputs.json"), append(b, '\n'), 0644)
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
