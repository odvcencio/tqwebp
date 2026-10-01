package webp

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type encodeOracleCase struct {
	Name     string `json:"name"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Animated bool   `json:"animated"`
	Loops    uint16 `json:"loops"`
	Frames   []struct {
		Source   string `json:"source"`
		Duration int    `json:"duration_ms"`
	} `json:"frames"`
}

func TestEncodeIndependentLibwebpOracle(t *testing.T) {
	dir := "testdata/encode-alpha"
	b, e := os.ReadFile(filepath.Join(dir, "inputs.json"))
	if e != nil {
		t.Fatal(e)
	}
	var cases []encodeOracleCase
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	if len(cases) != 5 {
		t.Fatal(len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			doc := &Document{Canvas: image.Pt(tc.Width, tc.Height), Animated: tc.Animated, LoopCount: tc.Loops, Metadata: Metadata{ICC: []byte("profile"), EXIF: []byte("selected-exif"), XMP: []byte("selected-xmp")}}
			if tc.Animated {
				doc.Background = color.NRGBA{R: 1, G: 2, B: 3, A: 127}
			}
			for _, f := range tc.Frames {
				raw, e := os.ReadFile(filepath.Join(dir, f.Source))
				if e != nil {
					t.Fatal(e)
				}
				if len(raw) != 4*tc.Width*tc.Height {
					t.Fatal("source size")
				}
				doc.Frames = append(doc.Frames, Frame{Pixels: &image.NRGBA{Pix: raw, Stride: 4 * tc.Width, Rect: image.Rect(0, 0, tc.Width, tc.Height)}, Duration: time.Duration(f.Duration) * time.Millisecond})
			}
			var out bytes.Buffer
			if e = EncodeAll(context.Background(), &out, doc, &Options{Quality: 80, Method: 4}, DocumentLimits{Limits: Limits{MaxOutputBytes: 1 << 20}}); e != nil {
				t.Fatal(e)
			}
			encoded, e := os.ReadFile(filepath.Join(dir, tc.Name+".webp"))
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(out.Bytes(), encoded) {
				t.Fatal("encoded fixture changed; regenerate and requalify independent oracle")
			}
			got, e := DecodeAll(context.Background(), bytes.NewReader(out.Bytes()), ReadLimits{})
			if e != nil {
				t.Fatal(e)
			}
			if got.Animated != doc.Animated || got.LoopCount != doc.LoopCount || got.Canvas != doc.Canvas || len(got.Frames) != len(doc.Frames) {
				t.Fatal("document controls changed")
			}
			for i, f := range got.Frames {
				name := tc.Name + "-" + string([]byte{'0' + byte(i/10), '0' + byte(i%10)}) + ".oracle.rgba"
				oracle, e := os.ReadFile(filepath.Join(dir, name))
				if e != nil {
					t.Fatal(e)
				}
				if !bytes.Equal(f.Pixels.(*image.NRGBA).Pix, oracle) {
					t.Fatal("libwebp pixel difference", i)
				}
				if f.Duration != doc.Frames[i].Duration {
					t.Fatal("duration", i)
				}
			}
		})
	}
}
