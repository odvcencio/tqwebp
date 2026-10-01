package container

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"reflect"
	"testing"
)

func vp8() []byte  { return []byte{0x10, 0, 0, 0x9d, 1, 0x2a, 1, 0, 1, 0} }
func vp8l() []byte { return []byte{0x2f, 0, 0, 0, 0} }
func still() *File {
	return &File{Canvas: image.Pt(1, 1), Frames: []EncodedFrame{{Rect: image.Rect(0, 0, 1, 1), VP8: vp8()}}}
}
func riff(cs ...Chunk) []byte {
	b := make([]byte, 12)
	copy(b, "RIFF")
	copy(b[8:], "WEBP")
	for _, c := range cs {
		h := make([]byte, 8)
		copy(h, c.FourCC)
		binary.LittleEndian.PutUint32(h[4:], uint32(len(c.Data)))
		b = append(b, h...)
		b = append(b, c.Data...)
		if len(c.Data)&1 != 0 {
			b = append(b, 0)
		}
	}
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	return b
}
func ext(flags byte) Chunk          { p := make([]byte, 10); p[0] = flags; return Chunk{"VP8X", p} }
func parse(b []byte) (*File, error) { return Demux(context.Background(), bytes.NewReader(b), Limits{}) }
func TestRoundTripPreservesPayloads(t *testing.T) {
	for _, lossless := range []bool{false, true} {
		f := still()
		f.MetadataChunks = []Chunk{{"ICCP", []byte{1, 2, 3}}, {"EXIF", []byte{4}}, {"EXIF", []byte{}}, {"XMP ", []byte("<x>&external;</x>")}}
		f.UnknownChunks = []Chunk{{"abcd", []byte{9}}, {"zzzz", nil}}
		if lossless {
			f.Frames[0].VP8 = nil
			f.Frames[0].VP8L = vp8l()
		}
		var b bytes.Buffer
		if err := Mux(context.Background(), &b, f, Limits{}); err != nil {
			t.Fatal(err)
		}
		got, err := parse(b.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Frames, f.Frames) || !bytes.Equal(got.Metadata().EXIF, []byte{4}) || len(got.MetadataChunks) != 4 || len(got.UnknownChunks) != 2 {
			t.Fatalf("roundtrip: %#v", got)
		}

		for i, c := range f.MetadataChunks {
			if got.MetadataChunks[i].FourCC != c.FourCC || !bytes.Equal(got.MetadataChunks[i].Data, c.Data) {
				t.Fatal("metadata payload/order changed")
			}
		}
		for i, c := range f.UnknownChunks {
			if got.UnknownChunks[i].FourCC != c.FourCC || !bytes.Equal(got.UnknownChunks[i].Data, c.Data) {
				t.Fatal("unknown payload/order changed")
			}
		}
		var b2 bytes.Buffer
		if err = Mux(context.Background(), &b2, got, Limits{}); err != nil || !bytes.Equal(b.Bytes(), b2.Bytes()) {
			t.Fatalf("canonical remux: %v", err)
		}
		// Demux owns its payloads, independent of caller input storage.
		owned := got.Frames[0].VP8
		if lossless {
			owned = got.Frames[0].VP8L
		}
		original := append([]byte(nil), owned...)
		for i := range b.Bytes() {
			b.Bytes()[i] = 0
		}
		if !bytes.Equal(original, owned) {
			t.Fatal("borrowed input")
		}
	}
}
func TestReaderCompatibility(t *testing.T) {
	x := ext(0x8)
	x.Data[0] |= 0xc1
	x.Data[1] = 99
	x.Data = append(x.Data, 7, 8)
	b := riff(x, Chunk{"EXIF", []byte{}}, Chunk{"EXIF", []byte{4}}, Chunk{"VP8 ", vp8()}, Chunk{"what", []byte{6}})
	f, err := parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Metadata().EXIF) != 0 || len(f.MetadataChunks) != 2 {
		t.Fatal("first empty metadata lost")
	}
	f.MetadataChunks = nil
	var out bytes.Buffer
	if err = Mux(context.Background(), &out, f, Limits{}); err != nil {
		t.Fatal(err)
	}
	got, err := parse(out.Bytes())
	if err != nil || len(got.MetadataChunks) != 0 {
		t.Fatal("strip", err)
	}
	// The declared RIFF extent is the only input consumed.
	r := bytes.NewReader(append(riff(Chunk{"VP8 ", vp8()}), 1, 2, 3))
	if _, err = Demux(context.Background(), r, Limits{}); err != nil || r.Len() != 3 {
		t.Fatal("trailing bytes", err, r.Len())
	}
}
func TestMalformedContainers(t *testing.T) {
	huge := riff(Chunk{"VP8 ", vp8()})
	binary.LittleEndian.PutUint32(huge[16:20], 0xffffffff)
	badDim := ext(0)
	badDim.Data[4] = 1
	tooBig := ext(0)
	for i := 4; i < 10; i++ {
		tooBig.Data[i] = 255
	}
	cases := map[string][]byte{
		"signature": []byte("012345678901"), "size_overflow": []byte("RIFF\xff\xff\xff\xffWEBP"), "chunk_overflow": huge,
		"missing_image": riff(ext(0)), "duplicate_image": riff(Chunk{"VP8 ", vp8()}, Chunk{"VP8 ", vp8()}), "duplicate_vp8x": riff(ext(0), ext(0), Chunk{"VP8 ", vp8()}),
		"late_vp8x": riff(Chunk{"VP8 ", vp8()}, ext(0)), "canvas_mismatch": riff(badDim, Chunk{"VP8 ", vp8()}), "canvas_overflow": riff(tooBig, Chunk{"VP8 ", vp8()}),
		"icc_late": riff(ext(0x20), Chunk{"VP8 ", vp8()}, Chunk{"ICCP", []byte{1}}), "missing_meta_flag": riff(ext(0), Chunk{"VP8 ", vp8()}, Chunk{"EXIF", nil}), "missing_meta": riff(ext(8), Chunk{"VP8 ", vp8()}),
		"alpha_lossless": riff(ext(0x10), Chunk{"ALPH", []byte{0, 255}}, Chunk{"VP8L", vp8l()}), "alpha_bad_size": riff(ext(0x10), Chunk{"ALPH", []byte{0}}, Chunk{"VP8 ", vp8()}),
		"alpha_duplicate": riff(ext(0x10), Chunk{"ALPH", []byte{0, 1}}, Chunk{"ALPH", []byte{0, 1}}, Chunk{"VP8 ", vp8()}), "alpha_late": riff(ext(0x10), Chunk{"VP8 ", vp8()}, Chunk{"ALPH", []byte{0, 1}}),
		"metadata_simple": riff(Chunk{"VP8 ", vp8()}, Chunk{"EXIF", nil}), "empty_vp8": riff(Chunk{"VP8 ", nil}), "empty_vp8l": riff(Chunk{"VP8L", nil}),
	}
	pad := riff(ext(8), Chunk{"VP8 ", vp8()}, Chunk{"EXIF", []byte{1}})
	pad[len(pad)-1] = 1
	cases["padding"] = pad
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(b); err == nil {
				t.Fatal("accepted malformed file")
			}
		})
	}
	valid := riff(ext(8), Chunk{"VP8 ", vp8()}, Chunk{"EXIF", []byte{1}})
	for i := 0; i < len(valid); i++ {
		if _, err := parse(valid[:i]); err == nil {
			t.Fatalf("accepted truncation %d", i)
		}
	}
	for _, b := range [][]byte{riff(ext(2)), riff(ext(0), Chunk{"ANMF", nil})} {
		if _, err := parse(b); !errors.Is(err, ErrUnsupportedFeature) {
			t.Fatal(err)
		}
	}
}
func TestAlphaTransport(t *testing.T) {
	for _, p := range [][]byte{{0, 0}, {4, 77}, {8, 255}, {12, 23}, {1, 42}, {0xc0, 99}} {
		b := riff(ext(0x10), Chunk{"ALPH", p}, Chunk{"VP8 ", vp8()})
		f, err := parse(b)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(f.Frames[0].ALPH, p) {
			t.Fatal("alpha bytes")
		}
		var out bytes.Buffer
		err = Mux(context.Background(), &out, f, Limits{})
		if p[0]&0xc0 != 0 {
			if err == nil {
				t.Fatal("writer reserved bits")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}
func TestLimits(t *testing.T) {
	f := still()
	f.MetadataChunks = []Chunk{{"EXIF", []byte{1, 2, 3}}, {"EXIF", []byte{4, 5}}}
	var out bytes.Buffer
	if err := Mux(context.Background(), &out, f, Limits{}); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	for _, tc := range []struct {
		l        Limits
		resource string
	}{{Limits{MaxInputBytes: int64(len(b) - 1)}, "input_bytes"}, {Limits{MaxChunks: 3}, "chunk_count"}, {Limits{MaxMetadataBytes: 4}, "metadata_bytes"}, {Limits{MaxRetainedBytes: 24}, "retained_bytes"}} {
		_, err := Demux(context.Background(), bytes.NewReader(b), tc.l)
		var le *LimitError
		if !errors.As(err, &le) || le.Resource != tc.resource {
			t.Fatalf("%s: %v", tc.resource, err)
		}
	}
	for _, l := range []Limits{{MaxOutputBytes: int64(len(b) - 1)}, {MaxChunks: 3}, {MaxMetadataBytes: 4}, {MaxRetainedBytes: 24}} {
		var dst bytes.Buffer
		err := Mux(context.Background(), &dst, f, l)
		if !errors.Is(err, ErrLimitExceeded) || dst.Len() != 0 {
			t.Fatalf("cap wrote: %v %d", err, dst.Len())
		}
	}
	var dst bytes.Buffer
	if err := Mux(context.Background(), &dst, f, Limits{MaxOutputBytes: int64(len(b))}); err != nil || !bytes.Equal(dst.Bytes(), b) {
		t.Fatal("exact cap", err)
	}
}

type callbacks struct {
	calls  int
	err    error
	cancel context.CancelFunc
}

func (c *callbacks) Read(p []byte) (int, error) { c.calls++; return 0, c.err }
func (c *callbacks) Write(p []byte) (int, error) {
	c.calls++
	if c.cancel != nil {
		c.cancel()
	}
	if c.err != nil {
		return 0, c.err
	}
	return len(p) - 1, nil
}
func TestContextAndIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &callbacks{}
	if _, err := Demux(ctx, c, Limits{}); err != context.Canceled || c.calls != 0 {
		t.Fatal(err)
	}
	if err := Mux(ctx, c, still(), Limits{}); err != context.Canceled || c.calls != 0 {
		t.Fatal(err)
	}
	var nilIO *callbacks
	for _, r := range []io.Reader{nil, nilIO} {
		if _, err := Demux(context.Background(), r, Limits{}); err != ErrInvalidReader {
			t.Fatal(err)
		}
	}
	if _, err := Demux(nil, c, Limits{}); err != ErrInvalidContext {
		t.Fatal(err)
	}
	if err := Mux(nil, c, still(), Limits{}); err != ErrInvalidContext {
		t.Fatal(err)
	}
	sentinel := errors.New("io failure")
	c.err = sentinel
	if _, err := Demux(context.Background(), c, Limits{}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := Mux(context.Background(), c, still(), Limits{}); err != sentinel {
		t.Fatal(err)
	}
	c.err = nil
	if err := Mux(context.Background(), c, still(), Limits{}); err != io.ErrShortWrite {
		t.Fatal(err)
	}
	if _, err := parse([]byte("RIFF")); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}
func FuzzDemux(f *testing.F) {
	f.Add(riff(Chunk{"VP8 ", vp8()}))
	f.Add(riff(Chunk{"VP8L", vp8l()}))
	f.Add(riff(ext(8), Chunk{"VP8 ", vp8()}, Chunk{"EXIF", []byte{1}}))
	f.Fuzz(func(t *testing.T, b []byte) {
		limits := Limits{MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxRetainedBytes: 1 << 20, MaxMetadataBytes: 1 << 16, MaxChunks: 128}
		file, err := Demux(context.Background(), bytes.NewReader(b), limits)
		if err != nil {
			return
		}
		var out bytes.Buffer
		if err = Mux(context.Background(), &out, file, limits); err != nil {
			return
		}
		again, err := Demux(context.Background(), bytes.NewReader(out.Bytes()), limits)
		if err != nil {
			t.Fatalf("mux output rejected: %v", err)
		}
		if file.Canvas != again.Canvas || !reflect.DeepEqual(file.Frames, again.Frames) {
			t.Fatal("payload changed")
		}
	})
}

type cancelReader struct {
	cancel context.CancelFunc
	calls  int
	zeros  bool
}

func (r *cancelReader) Read(p []byte) (int, error) {
	r.calls++
	r.cancel()
	if r.zeros {
		return 0, nil
	}
	p[0] = 'R'
	return 1, nil
}
func TestReadCancellationBetweenCallbacks(t *testing.T) {
	for _, zeros := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		r := &cancelReader{cancel: cancel, zeros: zeros}
		_, err := Demux(ctx, r, Limits{})
		if !errors.Is(err, context.Canceled) || r.calls != 1 {
			t.Fatalf("%v calls %d", err, r.calls)
		}
	}
}
func TestTinyInputBudgetNoCallbacks(t *testing.T) {
	for n := int64(1); n < 12; n++ {
		r := &callbacks{}
		_, err := Demux(context.Background(), r, Limits{MaxInputBytes: n})
		if !errors.Is(err, ErrLimitExceeded) || r.calls != 0 {
			t.Fatal(n, err, r.calls)
		}
	}
}
func TestPreserveAlphaDeclaration(t *testing.T) {
	f, err := parse(riff(ext(0x10), Chunk{"VP8L", vp8l()}))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err = Mux(context.Background(), &b, f, Limits{}); err != nil {
		t.Fatal(err)
	}
	if b.Bytes()[20]&0x10 == 0 {
		t.Fatal("lost container alpha declaration")
	}
}
func TestUnknownRequiresCanonicalExtended(t *testing.T) {
	f := still()
	f.UnknownChunks = []Chunk{{"TEST", nil}}
	var b bytes.Buffer
	if err := Mux(context.Background(), &b, f, Limits{}); err != nil {
		t.Fatal(err)
	}
	if string(b.Bytes()[12:16]) != "VP8X" {
		t.Fatal("not extended")
	}
}

func TestAlphaErrorLocation(t *testing.T) {
	_, err := parse(riff(ext(0x10), Chunk{"ALPH", []byte{0}}, Chunk{"VP8 ", vp8()}))
	var fe *FormatError
	if !errors.As(err, &fe) || fe.Offset != 30 || fe.Chunk != "ALPH" {
		t.Fatal(err)
	}
}
func TestInvalidWriterAndLimits(t *testing.T) {
	var w *callbacks
	for _, dst := range []io.Writer{nil, w} {
		if err := Mux(context.Background(), dst, still(), Limits{}); err != ErrInvalidWriter {
			t.Fatal(err)
		}
	}
	for _, l := range []Limits{{MaxInputBytes: -1}, {MaxOutputBytes: -1}, {MaxChunks: -1}, {MaxMetadataBytes: -1}, {MaxRetainedBytes: -1}} {
		r := &callbacks{}
		if _, err := Demux(context.Background(), r, l); err != ErrInvalidLimits || r.calls != 0 {
			t.Fatal(err)
		}
		if err := Mux(context.Background(), r, still(), l); err != ErrInvalidLimits || r.calls != 0 {
			t.Fatal(err)
		}
	}
}
func TestMuxPreflightRefusesWithoutWrites(t *testing.T) {
	cases := []*File{nil, {}, still(), still(), still(), still(), still(), still()}
	cases[2].Frames[0].VP8L = vp8l()
	cases[3].Frames[0].Rect.Min.X = 1
	cases[4].MetadataChunks = []Chunk{{"bad!", nil}}
	cases[5].UnknownChunks = []Chunk{{"VP8 ", nil}}
	cases[6].UnknownChunks = []Chunk{{"bad", nil}}
	cases[7].Canvas.X = 2
	for i, f := range cases {
		w := &callbacks{}
		if err := Mux(context.Background(), w, f, Limits{}); err == nil || w.calls != 0 {
			t.Fatal(i, err, w.calls)
		}
	}
}

type noProgress struct{ calls int }

func (r *noProgress) Read([]byte) (int, error) { r.calls++; return 0, nil }
func TestNoProgressReaderBounded(t *testing.T) {
	r := &noProgress{}
	_, err := Demux(context.Background(), r, Limits{})
	if !errors.Is(err, io.ErrNoProgress) || r.calls != 100 {
		t.Fatal(err, r.calls)
	}
}

type cancelOnWrite struct {
	cancel context.CancelFunc
	calls  int
}

func (w *cancelOnWrite) Write(p []byte) (int, error) { w.calls++; w.cancel(); return len(p), nil }
func TestCancelAfterFirstWrite(t *testing.T) {
	f := still()
	f.UnknownChunks = []Chunk{{"DATA", make([]byte, 70000)}}
	ctx, cancel := context.WithCancel(context.Background())
	w := &cancelOnWrite{cancel: cancel}
	if err := Mux(ctx, w, f, Limits{}); err != context.Canceled || w.calls != 1 {
		t.Fatal(err, w.calls)
	}
}

func TestAnimationControlsExplicitlyUnsupported(t *testing.T) {
	for _, change := range []func(*File){func(f *File) { f.Animated = true }, func(f *File) { f.LoopCount = 1 }, func(f *File) { f.Background.A = 1 }, func(f *File) { f.Frames[0].Duration = 1 }, func(f *File) { f.Frames[0].Blend = BlendReplace }, func(f *File) { f.Frames[0].Dispose = DisposeBackground }} {
		f := still()
		change(f)
		var b bytes.Buffer
		if err := Mux(context.Background(), &b, f, Limits{}); !errors.Is(err, ErrUnsupportedFeature) || b.Len() != 0 {
			t.Fatal(err)
		}
	}
	f := still()
	f.Frames[0].Blend = Blend(99)
	if err := Mux(context.Background(), io.Discard, f, Limits{}); !errors.Is(err, ErrInvalidFormat) {
		t.Fatal(err)
	}
	f = still()
	f.Frames = append(f.Frames, f.Frames[0])
	if err := Mux(context.Background(), io.Discard, f, Limits{MaxFrames: 1}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatal(err)
	}
	if _, err := Demux(context.Background(), bytes.NewReader(nil), Limits{MaxFrames: -1}); err != ErrInvalidLimits {
		t.Fatal(err)
	}
}

type finalErrorReader struct {
	data []byte
	err  error
}

func (r *finalErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, r.err
}
func TestReaderCoReturnedErrorPreserved(t *testing.T) {
	sentinel := errors.New("read completed with failure")
	r := &finalErrorReader{data: riff(Chunk{"VP8 ", vp8()}), err: sentinel}
	if _, err := Demux(context.Background(), r, Limits{}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

type checkpointContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *checkpointContext) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestMuxCancellationDuringPrivatePreparation(t *testing.T) {
	f := still()
	f.UnknownChunks = []Chunk{{"DATA", make([]byte, 100000)}}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &checkpointContext{Context: base, cancel: cancel, remaining: 11}
	w := &callbacks{}
	if err := Mux(ctx, w, f, Limits{}); err != context.Canceled || w.calls != 0 {
		t.Fatal(err, w.calls)
	}
}
