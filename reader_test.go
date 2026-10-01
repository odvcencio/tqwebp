package webp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"io"
	"m31labs.dev/tqwebp/container"
	"os"
	"reflect"
	"testing"
	"time"
	"unsafe"
)

type animationFrameFixture struct {
	Width, Height, X, Y, Duration int
	Replace, Dispose              bool
	CanvasFile                    string `json:"canvas_file"`
	SampleFile                    string `json:"sample_file"`
	Timestamp                     int    `json:"timestamp_ms"`
}
type animationFixture struct {
	File          string
	Width, Height int
	LoopCount     uint16 `json:"loop_count"`
	Background    uint32
	Frames        []animationFrameFixture
}

func animationCases(t testing.TB) []animationFixture {
	t.Helper()
	b, e := os.ReadFile("testdata/animation/provenance.json")
	if e != nil {
		t.Fatal(e)
	}
	var p struct{ Fixtures []animationFixture }
	if e = json.Unmarshal(b, &p); e != nil {
		t.Fatal(e)
	}
	if len(p.Fixtures) != 7 {
		t.Fatal("animation fixture count", len(p.Fixtures))
	}
	return p.Fixtures
}
func animationBytes(t testing.TB, file string) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/animation/" + file)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestReaderIndependentAnimationOracle(t *testing.T) {
	for _, tc := range animationCases(t) {
		t.Run(tc.File, func(t *testing.T) {
			b := animationBytes(t, tc.File)
			original := append([]byte(nil), b...)
			input := bytes.NewReader(append(append([]byte(nil), b...), 9, 8, 7))
			r, e := NewReader(context.Background(), input, ReadLimits{MaxDuration: 24 * time.Hour})
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			info := r.Info()
			if !info.Animated || info.Canvas != image.Pt(tc.Width, tc.Height) || info.LoopCount != tc.LoopCount || info.Completion != nil || info.Declared == nil {
				t.Fatal(info)
			}
			wantBG := color.NRGBA{R: byte(tc.Background >> 16), G: byte(tc.Background >> 8), B: byte(tc.Background), A: byte(tc.Background >> 24)}
			if info.Background != wantBG {
				t.Fatal(info.Background, wantBG)
			}
			if _, e = r.Metadata(); e != ErrNotComplete {
				t.Fatal(e)
			}
			var previous *image.NRGBA
			var duration time.Duration
			var decoded int64
			for index, f := range tc.Frames {
				got, e := r.Next()
				if e != nil {
					t.Fatalf("frame %d: %v", index, e)
				}
				m := got.Pixels.(*image.NRGBA)
				want := animationBytes(t, f.CanvasFile)
				if !bytes.Equal(m.Pix, want) {
					for i, x := range want {
						if i >= len(m.Pix) || m.Pix[i] != x {
							t.Fatalf("frame %d channel %d: got %d want %d", index, i, m.Pix[i], x)
						}
					}
				}
				if previous != nil && previous != m {
					t.Fatal("unexpected second compositor canvas")
				}
				previous = m
				duration += got.Duration
				decoded += int64(f.Width * f.Height)
				if got.Duration != time.Duration(f.Duration)*time.Millisecond || duration != time.Duration(f.Timestamp)*time.Millisecond {
					t.Fatal(got.Duration, f.Duration, duration, f.Timestamp)
				}
				if r.Info().Completion != nil {
					t.Fatal("completion before EOF")
				}
			}
			if _, e = r.Next(); e != io.EOF {
				t.Fatal(e)
			}
			if _, e = r.Next(); e != io.EOF {
				t.Fatal(e)
			}
			complete := r.Info().Completion
			if complete == nil || complete.Frames != int64(len(tc.Frames)) || complete.DecodedPixels != decoded || complete.Duration != duration {
				t.Fatal(complete)
			}
			if !complete.Observed.ICC || !complete.Observed.EXIF || !complete.Observed.XMP {
				t.Fatal(complete)
			}
			complete.Frames = 0
			if r.Info().Completion.Frames == 0 {
				t.Fatal("mutable completion alias")
			}
			used := r.working.Used
			m, e := r.Metadata()
			if e != nil {
				t.Fatal(e)
			}
			if string(m.ICC) != "profile" || string(m.XMP) != "xmp" {
				t.Fatal(m)
			}
			expectedEXIF := "late-exif"
			if tc.File == "blend-dispose.webp" {
				expectedEXIF = "early-exif"
			}
			if string(m.EXIF) != expectedEXIF {
				t.Fatalf("metadata %q", m.EXIF)
			}
			m.ICC[0] ^= 255
			m2, e := r.Metadata()
			if e != nil || string(m2.ICC) != "profile" {
				t.Fatal(e, m2)
			}
			if r.working.Used != used {
				t.Fatal("caller metadata retained in library accounting")
			}
			if input.Len() != 3 {
				t.Fatal("consumed beyond RIFF", input.Len())
			}
			r.Close()
			if r.working.Used != 0 {
				t.Fatal("working reservation leaked", r.working.Used)
			}
			r.Close()
			if _, e = r.Metadata(); e != ErrInvalidReader {
				t.Fatal(e)
			}
			doc, e := DecodeAll(context.Background(), bytes.NewReader(b), ReadLimits{MaxDuration: 24 * time.Hour})
			if e != nil {
				t.Fatal(e)
			}
			if !doc.Animated || len(doc.Frames) != len(tc.Frames) {
				t.Fatal(doc)
			}
			for i, f := range doc.Frames {
				if !bytes.Equal(f.Pixels.(*image.NRGBA).Pix, animationBytes(t, tc.Frames[i].CanvasFile)) {
					t.Fatal("owned document differs", i)
				}
			}
			if len(doc.Frames) > 1 {
				next := doc.Frames[1].Pixels.(*image.NRGBA).Pix[0]
				doc.Frames[0].Pixels.(*image.NRGBA).Pix[0] ^= 255
				if doc.Frames[1].Pixels.(*image.NRGBA).Pix[0] != next {
					t.Fatal("document frames share backing")
				}
			}
			if !bytes.Equal(b, original) {
				t.Fatal("input mutated")
			}
			if _, e = Decode(bytes.NewReader(b)); e != ErrAnimatedImage {
				t.Fatal(e)
			}
		})
	}
}
func TestReaderStaticHiddenRGB(t *testing.T) {
	b, e := os.ReadFile("testdata/lossless/hidden-rgb.webp")
	if e != nil {
		t.Fatal(e)
	}
	r, e := NewReader(context.Background(), bytes.NewReader(b), ReadLimits{})
	if e != nil {
		t.Fatal(e)
	}
	f, e := r.Next()
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(f.Pixels.(*image.NRGBA).Pix, []byte{19, 57, 91, 0}) {
		t.Fatal(f)
	}
	if _, e = r.Next(); e != io.EOF {
		t.Fatal(e)
	}
	if r.Info().Animated || !r.Info().Completion.Observed.Alpha {
		t.Fatal(r.Info())
	}
	r.Close()
	if r.working.Used != 0 {
		t.Fatal(r.working.Used)
	}
	doc, e := DecodeAll(context.Background(), bytes.NewReader(b), ReadLimits{})
	if e != nil || doc.Animated || len(doc.Frames) != 1 || !bytes.Equal(doc.Frames[0].Pixels.(*image.NRGBA).Pix, []byte{19, 57, 91, 0}) {
		t.Fatal(doc, e)
	}
	if unsafe.Sizeof(Frame{}) > 32 {
		t.Fatal("update retained frame slice growth reservation")
	}
}
func TestReaderLimitsAndState(t *testing.T) {
	b := animationBytes(t, "blend-dispose.webp")
	for _, l := range []ReadLimits{{MaxFrames: 1}, {MaxDecodedPixels: 10}, {MaxFramePixels: 1}, {MaxCanvasPixels: 1}, {MaxMetadataBytes: 1}, {MaxDuration: 16 * time.Millisecond}, {MaxWorkingBytes: 1}} {
		if _, e := DecodeAll(context.Background(), bytes.NewReader(b), l); !errors.Is(e, ErrLimitExceeded) || errors.Is(e, ErrInvalidFormat) {
			t.Fatalf("%+v: %v", l, e)
		}
	}
	r, e := NewReader(context.Background(), bytes.NewReader(b), ReadLimits{})
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
	if r.Info().Completion != nil {
		t.Fatal("early close completed")
	}
	if _, e = r.Metadata(); e != ErrNotComplete {
		t.Fatal(e)
	}
	if _, e = r.Next(); e != ErrInvalidReader {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = NewReader(ctx, nil, ReadLimits{MaxFrames: -1}); e != context.Canceled {
		t.Fatal(e)
	}
	if _, e = NewReader(nil, nil, ReadLimits{}); e != ErrInvalidContext {
		t.Fatal(e)
	}
	if _, e = NewReader(context.Background(), nil, ReadLimits{}); e != ErrInvalidReader {
		t.Fatal(e)
	}
}
func TestReaderTruncationsAndSkippedUnknown(t *testing.T) {
	b := animationBytes(t, "one-frame-animation.webp")
	for i := 0; i < len(b); i++ {
		if _, e := DecodeAll(context.Background(), bytes.NewReader(b[:i]), ReadLimits{}); e == nil {
			t.Fatalf("accepted truncation %d", i)
		}
	}
	// A large unknown after the first frame is drained, not spooled. Its byte
	// count still belongs to input admission; pixel/metadata working stays small.
	b = animationBytes(t, "blend-dispose.webp")
	extra := make([]byte, 8+40000)
	copy(extra, "JUNK")
	binary.LittleEndian.PutUint32(extra[4:], 40000)
	b = append(append([]byte(nil), b...), extra...)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	if _, e := DecodeAll(context.Background(), bytes.NewReader(b), ReadLimits{MaxWorkingBytes: 80000}); e != nil {
		t.Fatal(e)
	}
}
func FuzzReadAnimation(f *testing.F) {
	for _, name := range []string{"blend-dispose.webp", "one-frame-animation.webp", "mixed-neutral.webp"} {
		f.Add(animationBytes(f, name))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64<<10 {
			return
		}
		ctx := context.Background()
		_, _ = DecodeAll(ctx, bytes.NewReader(b), ReadLimits{MaxInputBytes: 64 << 10, MaxCanvasPixels: 4096, MaxFramePixels: 4096, MaxDecodedPixels: 16384, MaxFrames: 16, MaxMetadataBytes: 1024, MaxWorkingBytes: 1 << 20})
	})
}

type animationCheckpointContext struct{ calls, stop int }

func (c *animationCheckpointContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *animationCheckpointContext) Done() <-chan struct{}       { return nil }
func (c *animationCheckpointContext) Value(any) any               { return nil }
func (c *animationCheckpointContext) Err() error {
	c.calls++
	if c.calls >= c.stop {
		return context.Canceled
	}
	return nil
}

func TestReaderEveryCheckpointCancellation(t *testing.T) {
	b := animationBytes(t, "blend-dispose.webp")
	run := func(ctx context.Context) error {
		r, e := NewReader(ctx, bytes.NewReader(b), ReadLimits{})
		if e != nil {
			return e
		}
		defer func() {
			r.Close()
			if r.working.Used != 0 {
				t.Fatalf("leaked reservation %d", r.working.Used)
			}
		}()
		for {
			_, e = r.Next()
			if e == io.EOF {
				_, e = r.Metadata()
				return e
			}
			if e != nil {
				if _, again := r.Next(); again != e {
					t.Fatal("nonsticky error")
				}
				return e
			}
		}
	}
	count := &animationCheckpointContext{stop: 1 << 30}
	if e := run(count); e != nil {
		t.Fatal(e)
	}
	for stop := 1; stop <= count.calls; stop++ {
		ctx := &animationCheckpointContext{stop: stop}
		if e := run(ctx); e != context.Canceled {
			t.Fatalf("checkpoint %d/%d: %v", stop, count.calls, e)
		}
	}
}
func TestReaderWorkingRefusalAndRelease(t *testing.T) {
	b := animationBytes(t, "adversarial-alpha-sequence.webp")
	// Sweep admission thresholds around fixed state and codec/canvas transient
	// coexistence. Every refusal must be solely a limit, with all leases released.
	succeeded := false
	for limit := int64(65536); limit < 80000; limit += 31 {
		r, e := NewReader(context.Background(), bytes.NewReader(b), ReadLimits{MaxWorkingBytes: limit})
		if e == nil {
			for {
				_, e = r.Next()
				if e != nil {
					break
				}
			}
			r.Close()
			if r.working.Used != 0 {
				t.Fatal(limit, r.working.Used)
			}
		}
		if e == io.EOF {
			succeeded = true
			break
		}
		if !errors.Is(e, ErrLimitExceeded) || errors.Is(e, ErrInvalidFormat) {
			t.Fatalf("limit %d: %v", limit, e)
		}
	}
	if !succeeded {
		t.Fatal("no passing budget")
	}
	// Scratch is released per frame; the 64-frame reader must not accumulate
	// conservative per-codec reservations. DecodeAll additionally retains copies.
	r, e := NewReader(context.Background(), bytes.NewReader(b), ReadLimits{MaxWorkingBytes: 80000})
	if e != nil {
		t.Fatal(e)
	}
	var first int64
	for i := 0; i < 64; i++ {
		if _, e = r.Next(); e != nil {
			t.Fatal(i, e)
		}
		if i == 0 {
			first = r.working.Used
		}
		if r.working.Used > first+1024 {
			t.Fatal("accumulating scratch", i, r.working.Used, first)
		}
	}
	r.Close()
	if _, e := DecodeAll(context.Background(), bytes.NewReader(b), ReadLimits{MaxWorkingBytes: 80000}); !errors.Is(e, ErrLimitExceeded) || errors.Is(e, ErrInvalidFormat) {
		t.Fatal(e)
	}
}
func TestReaderIOIdentityAndNoProgress(t *testing.T) {
	sentinel := errors.New("last-byte source error")
	b := animationBytes(t, "one-frame-animation.webp")
	if _, e := DecodeAll(context.Background(), finalErrorReader{bytes.NewReader(b), sentinel}, ReadLimits{}); !errors.Is(e, sentinel) || errors.Is(e, ErrInvalidFormat) {
		t.Fatal(e)
	}
	if _, e := DecodeAll(context.Background(), finalErrorReader{bytes.NewReader(b), io.EOF}, ReadLimits{}); e != nil {
		t.Fatal(e)
	}
	z := &zeroProgressReader{}
	if _, e := NewReader(context.Background(), z, ReadLimits{}); !errors.Is(e, io.ErrNoProgress) || z.calls != 100 {
		t.Fatal(e, z.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	z = &zeroProgressReader{cancel: cancel}
	if _, e := NewReader(ctx, z, ReadLimits{}); e != context.Canceled || z.calls != 3 {
		t.Fatal(e, z.calls)
	}
	for _, input := range []io.Reader{invalidCountReader{1}, invalidCountReader{-1}} {
		if _, e := NewReader(context.Background(), input, ReadLimits{}); !errors.Is(e, ErrInvalidReader) {
			t.Fatal(e)
		}
	}
}

func TestAnimationIndependentFixtureCanonicalRemux(t *testing.T) {
	for _, tc := range animationCases(t) {
		b := animationBytes(t, tc.File)
		f, e := container.Demux(context.Background(), bytes.NewReader(b), container.Limits{})
		if e != nil {
			t.Fatal(e)
		}
		var out bytes.Buffer
		if e = container.Mux(context.Background(), &out, f, container.Limits{}); e != nil {
			t.Fatal(e)
		}
		g, e := container.Demux(context.Background(), bytes.NewReader(out.Bytes()), container.Limits{})
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(f.Frames, g.Frames) || !reflect.DeepEqual(f.MetadataChunks, g.MetadataChunks) || !reflect.DeepEqual(f.UnknownChunks, g.UnknownChunks) {
			t.Fatal(tc.File, "payload/order change")
		}
		doc, e := DecodeAll(context.Background(), bytes.NewReader(out.Bytes()), ReadLimits{MaxDuration: 24 * time.Hour})
		if e != nil {
			t.Fatal(e)
		}
		for i, frame := range tc.Frames {
			if !bytes.Equal(doc.Frames[i].Pixels.(*image.NRGBA).Pix, animationBytes(t, frame.CanvasFile)) {
				t.Fatal(tc.File, i)
			}
		}
	}
}
