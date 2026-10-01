package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	webp "m31labs.dev/tqwebp"
	"m31labs.dev/tqwebp/container"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testOptions(t testing.TB) options {
	t.Helper()
	o, e := parse([]string{"-", "-o", "-"})
	if e != nil {
		t.Fatal(e)
	}
	o.method = 1
	return o
}
func tinyImage() *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	for i := 0; i < 6; i++ {
		m.SetNRGBA(i%2, i/2, color.NRGBA{R: byte(i + 1), G: 40, B: 80, A: 255})
	}
	return m
}
func pngBytes(t testing.TB, m image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if e := png.Encode(&b, m); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func exifOrientation(n int) []byte {
	b := make([]byte, 26)
	copy(b, "II")
	binary.LittleEndian.PutUint16(b[2:], 42)
	binary.LittleEndian.PutUint32(b[4:], 8)
	binary.LittleEndian.PutUint16(b[8:], 1)
	binary.LittleEndian.PutUint16(b[10:], 0x112)
	binary.LittleEndian.PutUint16(b[12:], 3)
	binary.LittleEndian.PutUint32(b[14:], 1)
	binary.LittleEndian.PutUint16(b[18:], uint16(n))
	return b
}
func rgbProfile() []byte {
	b := make([]byte, 132)
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[16:], "RGB ")
	copy(b[36:], "acsp")
	return b
}
func runJSON(t testing.TB, args []string, input []byte) (int, Result, []byte) {
	t.Helper()
	var out, errout bytes.Buffer
	args = append(append([]string{}, args...), "--json")
	code := Run(context.Background(), args, bytes.NewReader(input), &out, &errout, false)
	var result Result
	d := json.NewDecoder(&errout)
	if e := d.Decode(&result); e != nil {
		t.Fatalf("JSON %v: %s", e, errout.String())
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		t.Fatal("multiple/mixed stderr results", e)
	}
	return code, result, out.Bytes()
}
func TestConversionJSONBinaryAndCaps(t *testing.T) {
	m := tinyImage()
	m.SetNRGBA(1, 1, color.NRGBA{R: 200, G: 30, B: 60, A: 1})
	input := pngBytes(t, m)
	code, result, out := runJSON(t, []string{"-", "-o", "-", "--method", "1"}, input)
	if code != 0 || !result.OK || !webpData(out) || result.Method != 1 || result.Width != 2 || result.Height != 3 {
		t.Fatal(code, result)
	}
	decoded, e := webp.Decode(bytes.NewReader(out))
	if e != nil {
		t.Fatal(e)
	}
	_, _, _, a := decoded.At(1, 1).RGBA()
	if a != 257 {
		t.Fatal(a)
	}
	for _, args := range [][]string{{"-", "-o", "-", "--max-input-bytes", "1"}, {"-", "-o", "-", "--max-output-bytes", "1"}, {"-", "-o", "-", "--max-width", "1"}} {
		code, result, out := runJSON(t, args, input)
		if code != 4 || result.Category != "limit" || len(out) != 0 {
			t.Fatal(code, result, len(out))
		}
	}
	code, result, out = runJSON(t, []string{"-", "-o", "-", "--preset", "smallest", "--method", "1", "--quality", "83"}, input)
	if code != 0 || result.Method != 1 || result.Quality != 83 {
		t.Fatal(code, result)
	}
	var binaryOut, diagnostics bytes.Buffer
	if code = Run(context.Background(), []string{"-", "-o", "-"}, bytes.NewReader(input), &binaryOut, &diagnostics, true); code != 2 || binaryOut.Len() != 0 {
		t.Fatal(code, binaryOut.Len())
	}
}
func TestAllEXIFOrientations(t *testing.T) {
	for i, tc := range []struct {
		w, h   int
		pixels []byte
	}{{2, 3, []byte{1, 2, 3, 4, 5, 6}}, {2, 3, []byte{2, 1, 4, 3, 6, 5}}, {2, 3, []byte{6, 5, 4, 3, 2, 1}}, {2, 3, []byte{5, 6, 3, 4, 1, 2}}, {3, 2, []byte{1, 3, 5, 2, 4, 6}}, {3, 2, []byte{5, 3, 1, 6, 4, 2}}, {3, 2, []byte{6, 4, 2, 5, 3, 1}}, {3, 2, []byte{2, 4, 6, 1, 3, 5}}} {
		n, e := orientation(exifOrientation(i + 1))
		if e != nil || n != i+1 {
			t.Fatal(n, e)
		}
		got, e := orient(context.Background(), tinyImage(), n)
		if e != nil || got.Bounds().Dx() != tc.w || got.Bounds().Dy() != tc.h {
			t.Fatal(n, e, got.Bounds())
		}
		for p, want := range tc.pixels {
			if straight(got.At(p%tc.w, p/tc.w)).R != want {
				t.Fatal(n, p, want)
			}
		}
	}
	for _, b := range [][]byte{nil, exifOrientation(0), exifOrientation(9), []byte("bad")} {
		n, e := orientation(b)
		if len(b) == 0 {
			if n != 1 || e != nil {
				t.Fatal(n, e)
			}
		} else if !errors.Is(e, webp.ErrInvalidMetadata) {
			t.Fatal(e)
		}
	}
	b := exifOrientation(2)
	binary.LittleEndian.PutUint32(b[4:], 0xffffffff)
	if _, e := orientation(b); !errors.Is(e, webp.ErrInvalidMetadata) {
		t.Fatal(e)
	}
}
func TestMetadataTransformPolicyAndPNGTransport(t *testing.T) {
	o := testOptions(t)
	doc := func() *webp.Document {
		return &webp.Document{Canvas: image.Pt(2, 3), Frames: []webp.Frame{{Pixels: tinyImage()}}, Metadata: webp.Metadata{ICC: rgbProfile(), EXIF: exifOrientation(6), XMP: []byte("private-xmp")}}
	}
	d := doc()
	retained, removed, e := transform(context.Background(), d, o)
	if e != nil || d.Canvas != image.Pt(3, 2) || !reflect.DeepEqual(retained, []string{"icc"}) || !reflect.DeepEqual(removed, []string{"exif", "xmp"}) {
		t.Fatal(retained, removed, e, d.Canvas)
	}
	o.metadata = "all"
	if _, _, e = transform(context.Background(), doc(), o); !errors.Is(e, webp.ErrInvalidMetadata) {
		t.Fatal(e)
	}
	o.ignoreOrientation = true
	d = doc()
	if _, _, e = transform(context.Background(), d, o); e != nil {
		t.Fatal(e)
	}
	output, e := encodePNG(context.Background(), d.Frames[0].Pixels, d.Metadata, o)
	if e != nil {
		t.Fatal(e)
	}
	parsed, _, e := decodeDocument(context.Background(), output, o)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(parsed.Metadata.ICC, d.Metadata.ICC) || !bytes.Equal(parsed.Metadata.EXIF, d.Metadata.EXIF) || !bytes.Equal(parsed.Metadata.XMP, d.Metadata.XMP) {
		t.Fatal("PNG metadata transport changed payload")
	}
	p := rgbProfile()
	copy(p[16:], "CMYK")
	if e = validRGBProfile(p); !errors.Is(e, webp.ErrInvalidMetadata) {
		t.Fatal(e)
	}
	o.metadata = "none"
	d = doc()
	d.Metadata.ICC = p
	if _, _, e = transform(context.Background(), d, o); e != nil || len(d.Metadata.ICC) != 0 {
		t.Fatal(e)
	}
}
func makeAnimation(t testing.TB, metadata webp.Metadata) []byte {
	t.Helper()
	first := tinyImage()
	second := image.NewNRGBA(first.Bounds())
	var out bytes.Buffer
	d := &webp.Document{Canvas: image.Pt(2, 3), Animated: true, LoopCount: 3, Frames: []webp.Frame{{Pixels: first, Duration: 17 * time.Millisecond}, {Pixels: second}}, Metadata: metadata}
	if e := webp.EncodeAll(context.Background(), &out, d, &webp.Options{Method: 1}, webp.DocumentLimits{}); e != nil {
		t.Fatal(e)
	}
	return out.Bytes()
}
func TestAnimationConversionInspectionAndExplicitExtraction(t *testing.T) {
	input := makeAnimation(t, webp.Metadata{})
	code, result, out := runJSON(t, []string{"-", "-o", "-", "--method", "1"}, input)
	if code != 0 || result.Frames == nil || *result.Frames != 2 || result.Animated == nil || !*result.Animated {
		t.Fatal(code, result)
	}
	d, e := webp.DecodeAll(context.Background(), bytes.NewReader(out), webp.ReadLimits{})
	if e != nil || !d.Animated || len(d.Frames) != 2 || d.LoopCount != 3 || d.Frames[0].Duration != 17*time.Millisecond {
		t.Fatal(d, e)
	}
	code, result, out = runJSON(t, []string{"decode", "-", "-o", "-"}, input)
	if code != 3 || result.Category != "unsupported" || len(out) != 0 {
		t.Fatal(code, result)
	}
	code, result, out = runJSON(t, []string{"decode", "-", "-o", "-", "--frame", "1"}, input)
	if code != 0 {
		t.Fatal(code, result)
	}
	m, e := png.Decode(bytes.NewReader(out))
	if e != nil {
		t.Fatal(e)
	}
	_, _, _, a := m.At(0, 0).RGBA()
	if a != 0 {
		t.Fatal(a)
	}
	code, result, _ = runJSON(t, []string{"inspect", "-"}, input)
	if code != 0 || result.Validated || result.Frames != nil {
		t.Fatal(code, result)
	}
	code, result, _ = runJSON(t, []string{"inspect", "-", "--validate"}, input)
	if code != 0 || !result.Validated || result.Frames == nil || *result.Frames != 2 {
		t.Fatal(code, result)
	}
	bad := append(append([]byte(nil), input...), []byte("EXIF\x08\x00\x00\x00oops")...)
	binary.LittleEndian.PutUint32(bad[4:], uint32(len(bad)-8))
	code, result, _ = runJSON(t, []string{"inspect", "-", "--validate"}, bad)
	if code == 0 || result.Validated {
		t.Fatal(code, result)
	}
}
func TestRawMetadataRemuxPreservesPayloadsAndOrder(t *testing.T) {
	input := makeAnimation(t, webp.Metadata{ICC: []byte("opaque-unvalidated-ICC"), EXIF: []byte("opaque-exif"), XMP: []byte("opaque-xmp")})
	f, e := container.Demux(context.Background(), bytes.NewReader(input), container.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	f.MetadataChunks = append(f.MetadataChunks, container.Chunk{FourCC: "EXIF", Data: []byte{}})
	f.UnknownChunks = []container.Chunk{{FourCC: "U001", Data: []byte{1}}, {FourCC: "U002", Data: []byte{2}}}
	f.Frames[0].UnknownChunks = []container.Chunk{{FourCC: "NEST", Data: []byte{3}}}
	var encoded bytes.Buffer
	if e = container.Mux(context.Background(), &encoded, f, container.Limits{}); e != nil {
		t.Fatal(e)
	}
	code, result, out := runJSON(t, []string{"metadata", "-", "-o", "-"}, encoded.Bytes())
	if code != 0 || result.MetadataPolicy != "raw preservation" {
		t.Fatal(code, result)
	}
	g, e := container.Demux(context.Background(), bytes.NewReader(out), container.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(g.Frames, f.Frames) || !reflect.DeepEqual(g.MetadataChunks, f.MetadataChunks) || !reflect.DeepEqual(g.UnknownChunks, f.UnknownChunks) {
		t.Fatal("raw remux changed payloads/order")
	}
	code, _, out = runJSON(t, []string{"metadata", "-", "-o", "-", "--strip", "exif,xmp"}, encoded.Bytes())
	if code != 0 {
		t.Fatal(code)
	}
	g, e = container.Demux(context.Background(), bytes.NewReader(out), container.Limits{})
	if e != nil || len(g.MetadataChunks) != 1 || !reflect.DeepEqual(g.Frames, f.Frames) {
		t.Fatal(e, g)
	}
}
func TestManifestExtractionRoundTripAndCaps(t *testing.T) {
	dir := t.TempDir()
	input := makeAnimation(t, webp.Metadata{})
	frames := filepath.Join(dir, "frames with spaces")
	code, result, _ := runJSON(t, []string{"decode", "-", "--out-dir", frames}, input)
	if code != 0 || (result.OutputBytes == nil || *result.OutputBytes == 0) {
		t.Fatal(code, result)
	}
	entries, e := os.ReadDir(frames)
	if e != nil || len(entries) != 3 {
		t.Fatal(e, len(entries))
	}
	code, result, out := runJSON(t, []string{"encode", filepath.Join(frames, "manifest.json"), "-o", "-", "--method", "1"}, nil)
	if code != 0 {
		t.Fatal(code, result)
	}
	d, e := webp.DecodeAll(context.Background(), bytes.NewReader(out), webp.ReadLimits{})
	if e != nil || len(d.Frames) != 2 || d.LoopCount != 3 || d.Frames[0].Duration != 17*time.Millisecond {
		t.Fatal(d, e)
	}
	code, _, _ = runJSON(t, []string{"decode", "-", "--out-dir", frames, "--force"}, input)
	if code != 5 {
		t.Fatal("existing extraction directory accepted", code)
	}
	failDir := filepath.Join(dir, "failed")
	code, _, _ = runJSON(t, []string{"decode", "-", "--out-dir", failDir, "--max-output-bytes", "1"}, input)
	if code != 4 {
		t.Fatal(code)
	}
	if _, e = os.Stat(failDir); !os.IsNotExist(e) {
		t.Fatal("failed extraction left directory", e)
	}
	m := []byte(`{"canvas":[1,1],"frames":[{"path":"missing","duration_ms":0},{"path":"missing","duration_ms":0}]}`)
	if _, e = parseManifest(m, 1); !errors.Is(e, webp.ErrLimitExceeded) {
		t.Fatal(e)
	}
	if _, e = parseManifest([]byte(`{"canvas":[1,1],"canvas":[2,2],"frames":[]}`), 2); e == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestAtomicNoClobberForceAndSameInput(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "target")
	if e := os.WriteFile(dst, []byte("original"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := atomicFile(context.Background(), dst, []byte("new"), false, systemOutputOps()); e == nil {
		t.Fatal("clobbered")
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "original" {
		t.Fatal(string(b))
	}
	if e := atomicFile(context.Background(), dst, []byte("new"), true, systemOutputOps()); e != nil {
		t.Fatal(e)
	}
	b, _ = os.ReadFile(dst)
	if string(b) != "new" {
		t.Fatal(string(b))
	}
	race := filepath.Join(dir, "raced")
	ops := systemOutputOps()
	ops.link = func(src, dst string) error {
		if e := os.WriteFile(dst, []byte("racer"), 0600); e != nil {
			return e
		}
		return os.Link(src, dst)
	}
	if e := atomicFile(context.Background(), race, []byte("ours"), false, ops); e == nil {
		t.Fatal("race clobbered")
	}
	b, _ = os.ReadFile(race)
	if string(b) != "racer" {
		t.Fatal(string(b))
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".tqwebp-*"))
	if len(matches) != 0 {
		t.Fatal("temporary leak", matches)
	}
	code, _, _ := runJSON(t, []string{dst, "-o", dst, "--force"}, nil)
	if code != 2 {
		t.Fatal(code)
	}
	b, _ = os.ReadFile(dst)
	if string(b) != "new" {
		t.Fatal("input changed")
	}
}

type fakeTemp struct {
	name               string
	closeErr, writeErr error
	closed             bool
}

func (f *fakeTemp) Name() string                { return f.name }
func (f *fakeTemp) Write(p []byte) (int, error) { return len(p), f.writeErr }
func (f *fakeTemp) Close() error                { f.closed = true; return f.closeErr }
func TestAtomicCloseWriteAndCleanupFailures(t *testing.T) {
	sentinel := errors.New("close failed")
	temp := &fakeTemp{name: "owned-temp", closeErr: sentinel}
	linked, removed := false, false
	ops := outputOps{create: func(string, string) (tempOutput, error) { return temp, nil }, link: func(string, string) error { linked = true; return nil }, remove: func(name string) error { removed = name == temp.name; return nil }}
	if e := atomicFile(context.Background(), "target", []byte("data"), false, ops); !errors.Is(e, sentinel) || linked || !removed || !temp.closed {
		t.Fatal(e, linked, removed, temp.closed)
	}
	temp.closeErr = nil
	temp.writeErr = sentinel
	removed = false
	if e := atomicFile(context.Background(), "target", []byte("data"), false, ops); !errors.Is(e, sentinel) || linked || !removed {
		t.Fatal(e)
	}
	temp.writeErr = nil
	ops.remove = func(string) error { return sentinel }
	if e := atomicFile(context.Background(), "target", []byte("data"), false, ops); !errors.Is(e, sentinel) || !linked {
		t.Fatal(e)
	}
}
func TestCLIUsageAndCancellation(t *testing.T) {
	for _, args := range [][]string{{}, {"-", "-o", "-", "--quality", "0"}, {"-", "-o", "-", "--unknown"}, {"-", "-o", "-", "--timeout", "-1s"}, {"decode", "-", "-o", "-", "--out-dir", "x"}, {"metadata", "-", "--strip", "all"}, {"metadata", "-", "--metadata", "none"}} {
		code, result, out := runJSON(t, args, nil)
		if code != 2 || result.Category != "usage" || len(out) != 0 {
			t.Fatal(args, code, result)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errout bytes.Buffer
	if code := Run(ctx, []string{"-", "-o", "-", "--json"}, strings.NewReader("data"), &out, &errout, false); code != 130 || out.Len() != 0 {
		t.Fatal(code)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if code := Run(ctx, []string{"-", "-o", "-"}, strings.NewReader("data"), &out, &errout, false); code != 6 {
		t.Fatal(code)
	}
}

func TestJSONSourceAndOutputDimensionsAndResolvedPolicy(t *testing.T) {
	o := testOptions(t)
	input, e := encodePNG(context.Background(), tinyImage(), webp.Metadata{EXIF: exifOrientation(6)}, o)
	if e != nil {
		t.Fatal(e)
	}
	code, r, _ := runJSON(t, []string{"-", "-o", "-", "--preset", "compact", "--method", "1", "--background", "#ffffff"}, input)
	if code != 0 || r.SourceWidth != 2 || r.SourceHeight != 3 || r.Width != 3 || r.Height != 2 || r.Preset != "compact" || r.Method != 1 || r.Background != "#ffffff" || r.IgnoreOrientation {
		t.Fatal(code, r)
	}
	code, r, _ = runJSON(t, []string{"-", "-o", "-", "--ignore-orientation", "--method", "1"}, input)
	if code != 0 || !r.IgnoreOrientation || r.Width != 2 || r.Height != 3 || r.SourceWidth != 2 || r.SourceHeight != 3 {
		t.Fatal(code, r)
	}
}

type failingStdout struct {
	data  []byte
	short bool
}

func (w *failingStdout) Write(p []byte) (int, error) {
	n := len(p) / 2
	w.data = append(w.data, p[:n]...)
	if w.short {
		return n, nil
	}
	return n, io.ErrClosedPipe
}
func TestStdoutFailureIsNonzeroAndJSONNeverClaimsNoPartialBytes(t *testing.T) {
	for _, short := range []bool{false, true} {
		w := &failingStdout{short: short}
		var diagnostics bytes.Buffer
		code := Run(context.Background(), []string{"-", "-o", "-", "--method", "1", "--json"}, bytes.NewReader(pngBytes(t, tinyImage())), w, &diagnostics, false)
		var result Result
		if e := json.Unmarshal(diagnostics.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		if code != 5 || result.OK || result.Category != "io" || result.OutputBytes != nil || len(w.data) == 0 {
			t.Fatal(code, result, len(w.data))
		}
	}
}
