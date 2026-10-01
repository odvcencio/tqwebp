package webp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func decodeFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, e := os.ReadFile(filepath.Join("testdata/decode", name))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestDecodeIndependentOracle(t *testing.T) {
	files, e := filepath.Glob("testdata/decode/*.rgba")
	if e != nil {
		t.Fatal(e)
	}
	if len(files) != 12 {
		t.Fatalf("want 12 independent oracle fixtures, got %d", len(files))
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
			got, ok := m.(*image.NRGBA)
			if !ok {
				t.Fatalf("type %T", m)
			}
			if !bytes.Equal(got.Pix, want) {
				for i, v := range want {
					if got.Pix[i] != v {
						t.Fatalf("oracle mismatch channel %d got %d want %d", i, got.Pix[i], v)
					}
				}
			}
			cfg, e := DecodeConfig(bytes.NewReader(b))
			if e != nil {
				t.Fatal(e)
			}
			if cfg.Width != got.Rect.Dx() || cfg.Height != got.Rect.Dy() || cfg.ColorModel != color.NRGBAModel {
				t.Fatal(cfg)
			}
		})
	}
}
func TestDecodeRefusalAndHeaderOnly(t *testing.T) {
	b := decodeFixture(t, "pattern-19x17.webp")
	var nilReader *bytes.Reader
	if _, e := Decode(nilReader); !errors.Is(e, ErrInvalidReader) {
		t.Fatal(e)
	}
	if _, e := DecodeContext(nil, nil, ReadLimits{}); e != ErrInvalidContext {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := DecodeContext(ctx, nil, ReadLimits{MaxWorkingBytes: -1}); e != context.Canceled {
		t.Fatal(e)
	}
	if _, e := DecodeContext(context.Background(), nil, ReadLimits{MaxWorkingBytes: -1}); e != ErrInvalidLimits {
		t.Fatal(e)
	}
	for _, l := range []ReadLimits{{MaxInputBytes: 1}, {MaxCanvasPixels: 1}, {MaxFramePixels: 1}, {MaxDecodedPixels: 1}, {MaxWorkingBytes: 1}, {MaxWorkingBytes: 66000}} {
		if _, e := DecodeContext(context.Background(), bytes.NewReader(b), l); !errors.Is(e, ErrLimitExceeded) {
			t.Fatalf("%+v: %v", l, e)
		}
	}
	lossless, e := os.ReadFile("container/testdata/blue-purple-pink.lossless.webp")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Decode(bytes.NewReader(lossless)); e != nil {
		t.Fatal(e)
	}
	if _, e = DecodeConfig(bytes.NewReader(lossless)); e != nil {
		t.Fatal(e)
	}
	if _, e = Decode(bytes.NewReader(decodeFixture(t, "compressed-alpha.webp"))); e != nil {
		t.Fatal(e)
	}
	anim := decodeFixture(t, "alpha-filter-0.webp")
	anim[20] |= 2
	if _, e = Decode(bytes.NewReader(anim)); e != ErrAnimatedImage {
		t.Fatal(e)
	}
	if _, e = DecodeConfig(bytes.NewReader(anim)); e != nil {
		t.Fatal(e)
	}
	// Valid header does not imply complete or supported pixel decoding.
	if _, e = DecodeConfig(bytes.NewReader(b[:30])); e != nil {
		t.Fatal(e)
	}
	if _, e = Decode(bytes.NewReader(b[:30])); !errors.Is(e, io.ErrUnexpectedEOF) {
		t.Fatal(e)
	}
	r := bytes.NewReader(append(append([]byte{}, b...), 1, 2, 3))
	if _, e = Decode(r); e != nil {
		t.Fatal(e)
	}
	if r.Len() != 3 {
		t.Fatal("consumed trailing bytes")
	}
}
func TestDecodeMalformed(t *testing.T) {
	b := decodeFixture(t, "pattern-19x17.webp")
	for i := 0; i < len(b); i++ {
		if _, e := Decode(bytes.NewReader(b[:i])); e == nil {
			t.Fatalf("accepted truncation %d", i)
		}
	}
	cases := [][]byte{append([]byte{}, b...), append([]byte{}, b...), append([]byte{}, b...), append([]byte{}, b...)}
	binary.LittleEndian.PutUint32(cases[0][4:], 0xffffffff)
	binary.LittleEndian.PutUint32(cases[1][16:], 0xffffffff)
	cases[2][26], cases[2][27], cases[2][28], cases[2][29] = 255, 63, 255, 63
	cases[3][20], cases[3][21], cases[3][22] = 0xf0, 255, 255
	for i, x := range cases {
		if _, e := Decode(bytes.NewReader(x)); e == nil {
			t.Fatalf("accepted malformed %d", i)
		}
	}
	alpha := decodeFixture(t, "alpha-filter-0.webp")
	alpha[38] = 3
	if _, e := Decode(bytes.NewReader(alpha)); e == nil {
		t.Fatal("accepted unsupported alpha")
	}
}

type decodeFaultReader struct{ err error }

func (r decodeFaultReader) Read([]byte) (int, error) { return 0, r.err }
func TestDecodeIOIdentity(t *testing.T) {
	e := errors.New("external read failed")
	_, got := Decode(decodeFaultReader{e})
	if !errors.Is(got, e) || errors.Is(got, ErrInvalidFormat) {
		t.Fatal(got)
	}
}

func FuzzDecodeStill(f *testing.F) {
	for _, n := range []string{"pattern-19x17.webp", "alpha-filter-3.webp", "compressed-alpha.webp"} {
		f.Add(decodeFixture(f, n))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64<<10 {
			return
		}
		_, _ = DecodeContext(context.Background(), bytes.NewReader(b), ReadLimits{MaxInputBytes: 64 << 10, MaxCanvasPixels: 4096, MaxFramePixels: 4096, MaxDecodedPixels: 4096, MaxWorkingBytes: 1 << 20, MaxMetadataBytes: 1024})
		_, _ = DecodeConfig(bytes.NewReader(b))
	})
}

func TestDecodeWorkingBoundary(t *testing.T) {
	b := decodeFixture(t, "alpha-filter-3.webp")
	// Ask for just enough to pass ingestion, then use the exact final conservative
	// reservation reported by LimitError. Equality must pass, one less must fail.
	l := ReadLimits{MaxWorkingBytes: int64(len(b))*3 + 65536 + 1024}
	_, e := DecodeContext(context.Background(), bytes.NewReader(b), l)
	var lim *LimitError
	if !errors.As(e, &lim) || lim.Resource != "working_bytes" {
		t.Fatalf("expected final budget refusal: %v", e)
	}
	l.MaxWorkingBytes = lim.Actual
	if _, e = DecodeContext(context.Background(), bytes.NewReader(b), l); e != nil {
		t.Fatal(e)
	}
	l.MaxWorkingBytes--
	if _, e = DecodeContext(context.Background(), bytes.NewReader(b), l); !errors.Is(e, ErrLimitExceeded) {
		t.Fatal(e)
	}
}

type cancelRead struct {
	r      *bytes.Reader
	cancel context.CancelFunc
}

func (r *cancelRead) Read(p []byte) (int, error) { n, e := r.r.Read(p); r.cancel(); return n, e }
func TestDecodeCancellationIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, e := DecodeContext(ctx, &cancelRead{bytes.NewReader(decodeFixture(t, "pattern-19x17.webp")), cancel}, ReadLimits{})
	if e != context.Canceled {
		t.Fatalf("cancellation must be unchanged, got %T %v", e, e)
	}
}
func TestDecodeConcurrentOwnedPixels(t *testing.T) {
	b := decodeFixture(t, "pattern-19x17.webp")
	const count = 8
	outputs := make(chan *image.NRGBA, count)
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() {
			m, e := Decode(bytes.NewReader(b))
			if e != nil {
				errs <- e
				return
			}
			outputs <- m.(*image.NRGBA)
		}()
	}
	results := make([]*image.NRGBA, 0, count)
	for len(results) < count {
		select {
		case m := <-outputs:
			results = append(results, m)
		case e := <-errs:
			t.Fatal(e)
		}
	}
	original := results[1].Pix[0]
	results[0].Pix[0] ^= 255
	if results[1].Pix[0] != original {
		t.Fatal("shared output storage")
	}
}

type zeroProgressReader struct {
	calls  int
	cancel context.CancelFunc
}

func (r *zeroProgressReader) Read([]byte) (int, error) {
	r.calls++
	if r.cancel != nil && r.calls == 3 {
		r.cancel()
	}
	return 0, nil
}

type invalidCountReader struct{ count int }

func (r invalidCountReader) Read(p []byte) (int, error) {
	if r.count > 0 {
		return len(p) + 1, nil
	}
	return -1, nil
}
func TestDecodeZeroProgress(t *testing.T) {
	r := &zeroProgressReader{}
	if _, e := Decode(r); !errors.Is(e, io.ErrNoProgress) || r.calls != 100 {
		t.Fatalf("calls=%d error=%v", r.calls, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r = &zeroProgressReader{cancel: cancel}
	if _, e := DecodeContext(ctx, r, ReadLimits{}); e != context.Canceled || r.calls != 3 {
		t.Fatalf("calls=%d error=%v", r.calls, e)
	}
	for _, r := range []io.Reader{invalidCountReader{1}, invalidCountReader{-1}} {
		if _, e := Decode(r); !errors.Is(e, ErrInvalidReader) {
			t.Fatal(e)
		}
	}
	if _, e := Decode(bytes.NewReader([]byte("RI"))); !errors.Is(e, io.ErrUnexpectedEOF) {
		t.Fatal(e)
	}
}

type finalErrorReader struct {
	r   *bytes.Reader
	err error
}

func (r finalErrorReader) Read(p []byte) (int, error) {
	n, e := r.r.Read(p)
	if r.r.Len() == 0 {
		return n, r.err
	}
	return n, e
}
func TestDecodeFinalReadError(t *testing.T) {
	b := decodeFixture(t, "pattern-19x17.webp")
	sentinel := errors.New("final read failed")
	if _, e := Decode(finalErrorReader{bytes.NewReader(b), sentinel}); !errors.Is(e, sentinel) || errors.Is(e, ErrInvalidFormat) {
		t.Fatalf("lost final read error: %v", e)
	}
	if _, e := DecodeConfig(finalErrorReader{bytes.NewReader(b[:30]), sentinel}); !errors.Is(e, sentinel) {
		t.Fatalf("lost config final read error: %v", e)
	}
	if _, e := Decode(finalErrorReader{bytes.NewReader(b), io.EOF}); e != nil {
		t.Fatal(e)
	}
}
