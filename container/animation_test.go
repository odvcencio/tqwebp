package container

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"reflect"
	"testing"
	"time"
)

func animationFile() *File {
	return &File{Animated: true, LoopCount: 17, Background: color.NRGBA{R: 1, G: 2, B: 3, A: 4}, Canvas: image.Pt(6, 4),
		Frames: []EncodedFrame{
			{Rect: image.Rect(2, 0, 3, 1), Duration: 7 * time.Millisecond, Blend: BlendReplace, Dispose: DisposeBackground, VP8: vp8(), UnknownChunks: []Chunk{{"ABCD", []byte{7}}, {"MORE", []byte{8, 9}}, {"LAST", []byte{}}}},
			{Rect: image.Rect(4, 2, 5, 3), Duration: 0, Blend: BlendOver, Dispose: DisposeNone, VP8L: vp8l()},
		}, MetadataChunks: []Chunk{{"ICCP", []byte{}}, {"ICCP", []byte{3}}, {"EXIF", []byte{9}}, {"XMP ", []byte{1, 2}}, {"EXIF", []byte{}}}, UnknownChunks: []Chunk{{"FILE", []byte{5}}}}
}
func cloneAnimation(f *File) *File {
	g := *f
	g.Frames = append([]EncodedFrame(nil), f.Frames...)
	g.MetadataChunks = append([]Chunk(nil), f.MetadataChunks...)
	g.UnknownChunks = append([]Chunk(nil), f.UnknownChunks...)
	return &g
}

func TestAnimationCanonicalRoundTrip(t *testing.T) {
	f := animationFile()
	var out bytes.Buffer
	if e := Mux(context.Background(), &out, f, Limits{}); e != nil {
		t.Fatal(e)
	}
	b := append([]byte(nil), out.Bytes()...)
	g, e := Demux(context.Background(), bytes.NewReader(b), Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if !g.Animated || g.Canvas != f.Canvas || g.LoopCount != f.LoopCount || g.Background != f.Background || !reflect.DeepEqual(g.Frames, f.Frames) || !reflect.DeepEqual(g.MetadataChunks, f.MetadataChunks) || !reflect.DeepEqual(g.UnknownChunks, f.UnknownChunks) {
		t.Fatalf("roundtrip got %#v want %#v", g, f)
	}
	var again bytes.Buffer
	if e = Mux(context.Background(), &again, g, Limits{}); e != nil || !bytes.Equal(out.Bytes(), again.Bytes()) {
		t.Fatal("canonical stability", e)
	}
	for i := range b {
		b[i] = 0
	}
	if !bytes.Equal(g.Frames[0].VP8, vp8()) || g.Frames[0].UnknownChunks[0].Data[0] != 7 {
		t.Fatal("borrowed caller input")
	}
	if len(g.Metadata().ICC) != 0 || g.Metadata().ICC == nil {
		t.Fatal("empty first ICC")
	}
	g.Frames[0].UnknownChunks = nil
	again.Reset()
	if e = Mux(context.Background(), &again, g, Limits{}); e != nil {
		t.Fatal(e)
	}
	stripped, e := parse(again.Bytes())
	if e != nil || len(stripped.Frames[0].UnknownChunks) != 0 || len(stripped.UnknownChunks) != 1 {
		t.Fatal("nested strip", e)
	}
}

func TestOneFrameAnimationAndMaximumTiming(t *testing.T) {
	for _, duration := range []time.Duration{0, 0xffffff * time.Millisecond} {
		f := still()
		f.Animated = true
		f.LoopCount = 0
		f.Background = color.NRGBA{1, 2, 3, 0}
		f.Frames[0].Duration = duration
		var b bytes.Buffer
		if e := Mux(context.Background(), &b, f, Limits{}); e != nil {
			t.Fatal(e)
		}
		g, e := parse(b.Bytes())
		if e != nil || !g.Animated || g.LoopCount != 0 || g.Background != f.Background || g.Frames[0].Duration != duration {
			t.Fatal(g, e)
		}
	}
}

func TestAnimatedMuxRejectsBeforeWrites(t *testing.T) {
	cases := map[string]func(*File){
		"no frames":           func(f *File) { f.Frames = nil },
		"odd x":               func(f *File) { f.Frames[0].Rect = image.Rect(1, 0, 2, 1) },
		"odd y":               func(f *File) { f.Frames[0].Rect = image.Rect(0, 1, 1, 2) },
		"negative":            func(f *File) { f.Frames[0].Rect = image.Rect(-2, 0, -1, 1) },
		"outside":             func(f *File) { f.Frames[0].Rect = image.Rect(6, 0, 7, 1) },
		"empty rect":          func(f *File) { f.Frames[0].Rect = image.Rectangle{} },
		"codec mismatch":      func(f *File) { f.Frames[0].Rect = image.Rect(2, 0, 4, 1) },
		"bad blend":           func(f *File) { f.Frames[0].Blend = Blend(2) },
		"bad disposal":        func(f *File) { f.Frames[0].Dispose = Dispose(2) },
		"negative duration":   func(f *File) { f.Frames[0].Duration = -time.Millisecond },
		"fractional duration": func(f *File) { f.Frames[0].Duration = time.Millisecond + 1 },
		"excess duration":     func(f *File) { f.Frames[0].Duration = 0x1000000 * time.Millisecond },
		"conflicting codec":   func(f *File) { f.Frames[0].VP8L = vp8l() },
		"missing codec":       func(f *File) { f.Frames[0].VP8 = nil },
		"lossless alpha":      func(f *File) { f.Frames[1].ALPH = []byte{0, 255} },
		"raw alpha size":      func(f *File) { f.Frames[0].ALPH = []byte{0} },
		"known unknown":       func(f *File) { f.Frames[0].UnknownChunks = []Chunk{{"EXIF", nil}} },
		"short FourCC":        func(f *File) { f.Frames[0].UnknownChunks = []Chunk{{"bad", nil}} },
		"canvas overflow":     func(f *File) { f.Canvas = image.Pt(1<<24, 1<<24) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := animationFile()
			change(f)
			w := &callbacks{}
			e := Mux(context.Background(), w, f, Limits{})
			if !errors.Is(e, ErrInvalidFormat) || w.calls != 0 {
				t.Fatal(e, w.calls)
			}
		})
	}
}

func TestAnimatedAggregateBudgetsAndWriterErrors(t *testing.T) {
	f := animationFile()
	var b bytes.Buffer
	if e := Mux(context.Background(), &b, f, Limits{}); e != nil {
		t.Fatal(e)
	}
	// Outer and nested counts: VP8X+ANIM + 2 ANMF + 2 codecs + 3 nested
	// unknowns + 5 metadata + 1 top-level unknown = 15.
	for _, l := range []Limits{{MaxChunks: 14}, {MaxFrames: 1}, {MaxOutputBytes: int64(b.Len() - 1)}, {MaxMetadataBytes: 3}, {MaxRetainedBytes: 20}} {
		w := &callbacks{}
		e := Mux(context.Background(), w, f, l)
		if !errors.Is(e, ErrLimitExceeded) || w.calls != 0 {
			t.Fatal(e, w.calls)
		}
	}
	for _, l := range []Limits{{MaxChunks: 14}, {MaxFrames: 1}, {MaxInputBytes: int64(b.Len() - 1)}, {MaxMetadataBytes: 3}, {MaxRetainedBytes: 20}} {
		_, e := Demux(context.Background(), bytes.NewReader(b.Bytes()), l)
		if !errors.Is(e, ErrLimitExceeded) || errors.Is(e, ErrInvalidFormat) {
			t.Fatal(e)
		}
	}
	var exact bytes.Buffer
	if e := Mux(context.Background(), &exact, f, Limits{MaxChunks: 15, MaxOutputBytes: int64(b.Len())}); e != nil || !bytes.Equal(b.Bytes(), exact.Bytes()) {
		t.Fatal(e)
	}
	sentinel := errors.New("animation writer failure")
	w := &callbacks{err: sentinel}
	if e := Mux(context.Background(), w, f, Limits{}); e != sentinel {
		t.Fatal(e)
	}
	w.err = nil
	if e := Mux(context.Background(), w, f, Limits{}); e != io.ErrShortWrite {
		t.Fatal(e)
	}
}

func wireANMF(flags byte, children ...Chunk) Chunk {
	p := make([]byte, 16)
	p[15] = flags
	for _, c := range children {
		h := make([]byte, 8)
		copy(h, c.FourCC)
		binary.LittleEndian.PutUint32(h[4:], uint32(len(c.Data)))
		p = append(p, h...)
		p = append(p, c.Data...)
		if len(c.Data)&1 != 0 {
			p = append(p, 0)
		}
	}
	return Chunk{"ANMF", p}
}
func TestIndependentAnimationWireSemantics(t *testing.T) {
	x := ext(2 | 8)
	x.Data[0] |= 0xc1
	x.Data[1] = 22
	control := Chunk{"ANIM", []byte{30, 20, 10, 0, 0xff, 0xff, 99, 98}} // Future extension is ignored.
	first := wireANMF(0xff, Chunk{"BEFR", []byte{7}}, Chunk{"VP8L", vp8l()}, Chunk{"AFTR", []byte{8}})
	b := riff(x, Chunk{"EXIF", []byte{}}, control, first, Chunk{"EXIF", []byte{4}}, wireANMF(0, Chunk{"VP8 ", vp8()}), Chunk{"TAIL", nil})
	f, e := parse(b)
	if e != nil {
		t.Fatal(e)
	}
	if len(f.Frames) != 2 || f.LoopCount != 65535 || f.Background != (color.NRGBA{R: 10, G: 20, B: 30, A: 0}) || f.Frames[0].Blend != BlendReplace || f.Frames[0].Dispose != DisposeBackground || len(f.Frames[0].UnknownChunks) != 2 || len(f.MetadataChunks) != 2 {
		t.Fatalf("%+v", f)
	}
	var out bytes.Buffer
	if e = Mux(context.Background(), &out, f, Limits{}); e != nil {
		t.Fatal(e)
	}
	g, e := parse(out.Bytes())
	if e != nil || !reflect.DeepEqual(g.Frames, f.Frames) || !reflect.DeepEqual(g.MetadataChunks, f.MetadataChunks) {
		t.Fatal(e)
	}
}

func TestAnimationStructuralFailures(t *testing.T) {
	a := Chunk{"ANIM", make([]byte, 6)}
	frame := wireANMF(0, Chunk{"VP8L", vp8l()})
	cases := map[string][]byte{
		"missing ANIM":       riff(ext(2), frame),
		"duplicate ANIM":     riff(ext(2), a, a, frame),
		"late ANIM":          riff(ext(2), a, frame, a),
		"missing frame":      riff(ext(2), a),
		"top codec":          riff(ext(2), a, Chunk{"VP8L", vp8l()}),
		"frame without flag": riff(ext(0), a, frame),
		"late ICC":           riff(ext(2|0x20), a, Chunk{"ICCP", nil}, frame),
		"nested ICC":         riff(ext(2), a, wireANMF(0, Chunk{"ICCP", nil}, Chunk{"VP8L", vp8l()})),
		"nested ANMF":        riff(ext(2), a, wireANMF(0, frame, Chunk{"VP8L", vp8l()})),
		"duplicate codec":    riff(ext(2), a, wireANMF(0, Chunk{"VP8L", vp8l()}, Chunk{"VP8 ", vp8()})),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			_, e := parse(b)
			if !errors.Is(e, ErrInvalidFormat) {
				t.Fatal(e)
			}
		})
	}
	_, e := parse(cases["nested ICC"])
	var fe *FormatError
	if !errors.As(e, &fe) || fe.Frame != 0 || fe.Chunk != "ICCP" || fe.Offset != 68 {
		t.Fatal(e)
	}
}

func TestMixedAnimationAlphaDeclaration(t *testing.T) {
	f := animationFile()
	f.Frames[0].ALPH = []byte{0, 127}
	var b bytes.Buffer
	if e := Mux(context.Background(), &b, f, Limits{}); e != nil {
		t.Fatal(e)
	}
	g, e := parse(b.Bytes())
	if e != nil || !g.Alpha || !bytes.Equal(g.Frames[0].ALPH, []byte{0, 127}) {
		t.Fatal(e)
	}
	// Global alpha applies to the animation, not every individual lossy frame.
	f.Frames[1].VP8L = nil
	f.Frames[1].VP8 = vp8()
	b.Reset()
	if e = Mux(context.Background(), &b, f, Limits{}); e != nil {
		t.Fatal(e)
	}
	if _, e = parse(b.Bytes()); e != nil {
		t.Fatal(e)
	}
	// A lossless frame leaves actual transparency unknown at container level.
	f = animationFile()
	f.Alpha = true
	b.Reset()
	if e = Mux(context.Background(), &b, f, Limits{}); e != nil {
		t.Fatal(e)
	}
	g, e = parse(b.Bytes())
	if e != nil || !g.Alpha {
		t.Fatal(e)
	}
}

func TestAnimationCanvasDoesNotInheritEncoderCeiling(t *testing.T) {
	for _, canvas := range []image.Point{image.Pt(1<<24, 1), image.Pt(65535, 65537)} {
		f := still()
		f.Animated = true
		f.Canvas = canvas
		f.Frames[0].Rect = image.Rect(0, 0, 1, 1)
		if canvas.Y == 1 {
			f.Frames[0].Rect = image.Rect(canvas.X-2, 0, canvas.X-1, 1)
		}
		var out bytes.Buffer
		if e := Mux(context.Background(), &out, f, Limits{}); e != nil {
			t.Fatal(e)
		}
		g, e := parse(out.Bytes())
		if e != nil || g.Canvas != canvas || g.Frames[0].Rect != f.Frames[0].Rect {
			t.Fatal(g, e)
		}
	}
}

func TestAnimatedMuxCancellationAndInputImmutability(t *testing.T) {
	f := animationFile()
	f.Frames[0].UnknownChunks = []Chunk{{"DATA", make([]byte, 100000)}}
	before := cloneAnimation(f)
	ctx, cancel := context.WithCancel(context.Background())
	w := &cancelOnWrite{cancel: cancel}
	if e := Mux(ctx, w, f, Limits{}); e != context.Canceled || w.calls != 1 {
		t.Fatal(e, w.calls)
	}
	if !reflect.DeepEqual(f, before) {
		t.Fatal("Mux mutated input")
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	checkpoint := &checkpointContext{Context: base, cancel: cancel, remaining: 15}
	sink := &callbacks{}
	if e := Mux(checkpoint, sink, f, Limits{}); e != context.Canceled || sink.calls != 0 {
		t.Fatal("preparation cancellation wrote bytes", e, sink.calls)
	}
}
