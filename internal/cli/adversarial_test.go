package cli

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	webp "m31labs.dev/tqwebp"
	"os"
	"path/filepath"
	"testing"
)

func TestCompressedMetadataBombAndCancellation(t *testing.T) {
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	z.Write(make([]byte, 1<<20))
	z.Close()
	o := testOptions(t)
	p := pngBytes(t, tinyImage())
	var b bytes.Buffer
	b.Write(p[:33])
	if e := pngChunk(&b, "iCCP", append([]byte("profile\x00\x00"), compressed.Bytes()...)); e != nil {
		t.Fatal(e)
	}
	b.Write(p[33:])
	if _, e := extractMetadata(context.Background(), b.Bytes(), "png", 128); !errors.Is(e, webp.ErrLimitExceeded) || errors.Is(e, webp.ErrInvalidMetadata) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := extractMetadata(ctx, b.Bytes(), "png", o.metadataBytes); e != context.Canceled {
		t.Fatal(e)
	}
	var apng bytes.Buffer
	apng.Write(p[:33])
	pngChunk(&apng, "acTL", make([]byte, 8))
	apng.Write(p[33:])
	if _, _, e := decodeDocument(context.Background(), apng.Bytes(), o); e == nil {
		t.Fatal("APNG silently flattened")
	}
}
func TestManifestStrictArraysObjectsAndLatePreflight(t *testing.T) {
	for _, raw := range []string{
		`{"canvas":[1,1,9],"frames":[{"path":"x"}]}`,
		`{"canvas":[1],"frames":[{"path":"x"}]}`,
		`{"canvas":[1,1],"background":[1,2,3,4,5],"frames":[{"path":"x"}]}`,
		`{"canvas":[1,1],"frames":[{"path":"x","path":"y"}]}`,
		`{"canvas":[1,1],"frames":[{"path":"x","duration_ms":0.5}]}`,
		`{"canvas":[1,1],"frames":[{"path":"x"}],"metadata":{"icc":"x","icc":"y"}}`,
	} {
		if _, e := parseManifest([]byte(raw), 10); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	o := testOptions(t)
	raw := []byte(`{"canvas":[1,1],"frames":[{"path":"definitely-not-present"},{"path":"x","duration_ms":-1}]}`)
	if _, e := encodeManifest(context.Background(), raw, o); e == nil {
		t.Fatal("late duration accepted")
	} else if code, _ := classify(e); code != 3 {
		t.Fatal("opened earlier file before late preflight", e)
	}
}
func TestJPEGMetadataFragmentsAndBounds(t *testing.T) {
	segment := func(tag byte, p []byte) []byte {
		b := []byte{255, tag, 0, 0}
		binary.BigEndian.PutUint16(b[2:], uint16(len(p)+2))
		return append(b, p...)
	}
	profile := rgbProfile()
	prefix := []byte("ICC_PROFILE\x00")
	var b []byte
	b = append(b, 255, 216)
	b = append(b, segment(0xe2, append(append(append([]byte{}, prefix...), 2, 2), profile[70:]...))...)
	b = append(b, segment(0xe1, append([]byte("Exif\x00\x00"), exifOrientation(8)...))...)
	b = append(b, segment(0xe2, append(append(append([]byte{}, prefix...), 1, 2), profile[:70]...))...)
	b = append(b, 255, 217)
	m, e := extractMetadata(context.Background(), b, "jpeg", 1024)
	if e != nil || !bytes.Equal(m.ICC, profile) {
		t.Fatal(e)
	}
	if n, e := orientation(m.EXIF); e != nil || n != 8 {
		t.Fatal(n, e)
	}
	if _, e = extractMetadata(context.Background(), b, "jpeg", 10); !errors.Is(e, webp.ErrLimitExceeded) {
		t.Fatal(e)
	}
	bad := append([]byte{255, 216}, segment(0xe2, append(append([]byte{}, prefix...), 1, 2))...)
	bad = append(bad, 255, 217)
	if _, e = extractMetadata(context.Background(), bad, "jpeg", 1024); !errors.Is(e, webp.ErrInvalidMetadata) {
		t.Fatal(e)
	}
	for i := 0; i < len(b); i++ {
		_, _ = extractMetadata(context.Background(), b[:i], "jpeg", 1024)
	}
}

type emptyReader struct {
	calls  int
	cancel context.CancelFunc
}

func (r *emptyReader) Read([]byte) (int, error) {
	r.calls++
	if r.cancel != nil && r.calls == 3 {
		r.cancel()
	}
	return 0, nil
}

type badCountReader struct{}

func (badCountReader) Read(p []byte) (int, error) { return len(p) + 1, nil }
func TestInputNoProgressErrorAndCancellation(t *testing.T) {
	r := &emptyReader{}
	if _, e := readBounded(context.Background(), r, 20); !errors.Is(e, io.ErrNoProgress) || r.calls != 100 {
		t.Fatal(e, r.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r = &emptyReader{cancel: cancel}
	if _, e := readBounded(ctx, r, 20); e != context.Canceled || r.calls != 3 {
		t.Fatal(e, r.calls)
	}
	if _, e := readBounded(context.Background(), badCountReader{}, 20); !errors.Is(e, webp.ErrInvalidReader) {
		t.Fatal(e)
	}
}
func TestSameInodeAliasesAndManifestMetadataRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.png")
	alias := filepath.Join(dir, "alias.png")
	data := pngBytes(t, tinyImage())
	if e := os.WriteFile(src, data, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(src, alias); e != nil {
		t.Skip(e)
	}
	code, _, _ := runJSON(t, []string{src, "-o", alias, "--force"}, nil)
	if code != 2 {
		t.Fatal(code)
	}
	after, _ := os.ReadFile(src)
	if !bytes.Equal(after, data) {
		t.Fatal("source changed")
	}
	input := makeAnimation(t, webp.Metadata{ICC: rgbProfile(), EXIF: exifOrientation(1), XMP: []byte("selected")})
	outDir := filepath.Join(dir, "frames")
	code, result, _ := runJSON(t, []string{"decode", "-", "--out-dir", outDir, "--metadata", "all"}, input)
	if code != 0 {
		t.Fatal(code, result)
	}
	code, result, out := runJSON(t, []string{"encode", filepath.Join(outDir, "manifest.json"), "-o", "-", "--metadata", "all", "--method", "1"}, nil)
	if code != 0 {
		t.Fatal(code, result)
	}
	d, e := webp.DecodeAll(context.Background(), bytes.NewReader(out), webp.ReadLimits{})
	if e != nil || !bytes.Equal(d.Metadata.ICC, rgbProfile()) || !bytes.Equal(d.Metadata.EXIF, exifOrientation(1)) || string(d.Metadata.XMP) != "selected" {
		t.Fatal(e, d)
	}
}
func FuzzCLIMetadata(f *testing.F) {
	f.Add(exifOrientation(6), uint8(0))
	f.Add(rgbProfile(), uint8(1))
	f.Add([]byte{255, 216, 255, 217}, uint8(2))
	f.Fuzz(func(t *testing.T, b []byte, kind uint8) {
		if len(b) > 8192 {
			return
		}
		switch kind % 4 {
		case 0:
			_, _ = orientation(b)
		case 1:
			_ = validRGBProfile(b)
		case 2:
			_, _ = extractMetadata(context.Background(), b, "jpeg", 1024)
		case 3:
			_, _ = extractMetadata(context.Background(), b, "png", 1024)
		}
	})
}
func FuzzCLIManifest(f *testing.F) {
	f.Add([]byte(`{"canvas":[1,1],"frames":[{"path":"x","duration_ms":0}]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 8192 {
			return
		}
		_, _ = parseManifest(b, 4)
	})
}
func FuzzCLIStdin(f *testing.F) {
	f.Add(pngBytes(f, image.NewNRGBA(image.Rect(0, 0, 1, 1))))
	f.Add(makeAnimation(f, webp.Metadata{}))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 4096 {
			return
		}
		var out, errout bytes.Buffer
		code := Run(context.Background(), []string{"-", "-o", "-", "--json", "--ignore-orientation", "--metadata", "none", "--method", "1", "--max-input-bytes", "4096", "--max-output-bytes", "4096", "--max-pixels", "64", "--max-total-pixels", "128", "--max-frames", "4", "--max-metadata-bytes", "128"}, bytes.NewReader(b), &out, &errout, false)
		if code != 0 && out.Len() != 0 {
			t.Fatal("failed conversion wrote binary output")
		}
	})
}
