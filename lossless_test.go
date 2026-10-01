package webp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	nativeLossless "m31labs.dev/tqwebp/internal/vp8ldecode"
	"os"
	"path/filepath"
	"testing"
)

func TestLosslessIndependentOracle(t *testing.T) {
	files, e := filepath.Glob("testdata/lossless/*.rgba")
	if e != nil {
		t.Fatal(e)
	}
	if len(files) != 23 {
		t.Fatalf("fixture count %d", len(files))
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, e := os.ReadFile(path[:len(path)-5])
			if e != nil {
				t.Fatal(e)
			}
			want, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			m, e := Decode(bytes.NewReader(b))
			if e != nil {
				t.Fatal(e)
			}
			got := m.(*image.NRGBA)
			if !bytes.Equal(got.Pix, want) {
				for i, v := range want {
					if i >= len(got.Pix) || v != got.Pix[i] {
						t.Fatalf("oracle mismatch channel %d", i)
					}
				}
			}
		})
	}
}
func TestLosslessBudget(t *testing.T) {
	b, e := os.ReadFile("testdata/lossless/blue-purple-pink-large.lossless.webp")
	if e != nil {
		t.Fatal(e)
	}
	_, e = DecodeContext(context.Background(), bytes.NewReader(b), ReadLimits{MaxWorkingBytes: 100000})
	var limit *LimitError
	if !errors.As(e, &limit) || limit.Resource != "working_bytes" || errors.Is(e, ErrInvalidFormat) {
		t.Fatal(e)
	}
}

func TestLosslessTruncationsAndHiddenRGB(t *testing.T) {
	for _, name := range []string{"hidden-rgb.webp", "lossless-pattern-19x17.webp", "compressed-alpha-filter-3.webp"} {
		b, e := os.ReadFile("testdata/lossless/" + name)
		if e != nil {
			t.Fatal(e)
		}
		for i := 0; i < len(b); i++ {
			if _, e = Decode(bytes.NewReader(b[:i])); e == nil {
				t.Fatalf("accepted %s truncation %d", name, i)
			}
		}
	}
	b, e := os.ReadFile("testdata/lossless/hidden-rgb.webp")
	if e != nil {
		t.Fatal(e)
	}
	m, e := Decode(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(m.(*image.NRGBA).Pix, []byte{19, 57, 91, 0}) {
		t.Fatal(m)
	}
}
func FuzzLosslessContainer(f *testing.F) {
	for _, name := range []string{"hidden-rgb.webp", "lossless-pattern-19x17.webp", "compressed-alpha-filter-3.webp"} {
		b, e := os.ReadFile("testdata/lossless/" + name)
		if e != nil {
			f.Fatal(e)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64<<10 {
			return
		}
		_, _ = DecodeContext(context.Background(), bytes.NewReader(b), ReadLimits{MaxInputBytes: 64 << 10, MaxCanvasPixels: 4096, MaxFramePixels: 4096, MaxDecodedPixels: 4096, MaxWorkingBytes: 1 << 20, MaxMetadataBytes: 1024})
	})
}

func TestLosslessAllocationErrorCategories(t *testing.T) {
	for _, name := range []string{"blue-purple-pink.lossless.webp", "gopher-doc.1bpp.lossless.webp", "lossless-pattern-19x17.webp"} {
		t.Run(name, func(t *testing.T) {
			b, e := os.ReadFile("testdata/lossless/" + name)
			if e != nil {
				t.Fatal(e)
			}
			if string(b[12:16]) != "VP8L" {
				t.Fatal("fixture framing changed")
			}
			n := int(binary.LittleEndian.Uint32(b[16:20]))
			p := b[20 : 20+n]
			cfg, e := DecodeConfig(bytes.NewReader(b))
			if e != nil {
				t.Fatal(e)
			}
			calls := 0
			if _, e = nativeLossless.Decode(context.Background(), p, cfg.Width, cfg.Height, false, func(int64) error { calls++; return nil }); e != nil {
				t.Fatal(e)
			}
			for stop := 1; stop <= calls; stop++ {
				seen := 0
				refusal := &LimitError{"working_bytes", 1, 2}
				_, e = nativeLossless.Decode(context.Background(), p, cfg.Width, cfg.Height, false, func(int64) error {
					seen++
					if seen == stop {
						return refusal
					}
					return nil
				})
				got := losslessError(context.Background(), "VP8L", e)
				var typed *LimitError
				if got != refusal || !errors.As(got, &typed) || !errors.Is(got, ErrLimitExceeded) || errors.Is(got, ErrInvalidFormat) || errors.Is(got, ErrUnsupportedFeature) {
					t.Fatalf("allocation %d/%d: %T %v", stop, calls, got, got)
				}
			}
		})
	}
}
func TestCompressedAlphaBudgetCategory(t *testing.T) {
	b, e := os.ReadFile("testdata/lossless/compressed-alpha-filter-3.webp")
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := DecodeConfig(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	var payload, chunks, vp8Bytes int64
	var alpha []byte
	for off := 12; off < len(b); {
		n := int(binary.LittleEndian.Uint32(b[off+4:]))
		chunks++
		payload += int64(n)
		switch string(b[off : off+4]) {
		case "VP8 ":
			vp8Bytes = int64(n)
		case "ALPH":
			alpha = b[off+9 : off+8+n]
		}
		off += 8 + n + n&1
	}
	w, h := int64(cfg.Width), int64(cfg.Height)
	working := int64(len(b)) + payload + 256*(chunks+1) + 65536 + 388*((w+15)/16)*((h+15)/16) + 6*((w+15)/16) + vp8Bytes + 4*w*h
	var alphaCost int64
	if _, e = nativeLossless.Decode(context.Background(), alpha, cfg.Width, cfg.Height, true, func(n int64) error { alphaCost += n; return nil }); e != nil {
		t.Fatal(e)
	}
	for _, cap := range []int64{working, working + alphaCost - 1} {
		_, e = DecodeContext(context.Background(), bytes.NewReader(b), ReadLimits{MaxWorkingBytes: cap})
		var typed *LimitError
		if !errors.As(e, &typed) || typed.Resource != "working_bytes" || typed.Actual <= working || errors.Is(e, ErrInvalidFormat) {
			t.Fatal(e)
		}
	}
	if _, e = DecodeContext(context.Background(), bytes.NewReader(b), ReadLimits{MaxWorkingBytes: working + alphaCost}); e != nil {
		t.Fatal(e)
	}
}
