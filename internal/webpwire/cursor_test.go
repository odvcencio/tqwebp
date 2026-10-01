package webpwire

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"reflect"
	"testing"
	"time"
)

func chunk(id string, p []byte) []byte {
	b := make([]byte, 8+len(p)+(len(p)&1))
	copy(b, id)
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(p)))
	copy(b[8:], p)
	return b
}
func riff(chunks ...[]byte) []byte {
	b := make([]byte, 12)
	copy(b, "RIFF")
	copy(b[8:], "WEBP")
	for _, c := range chunks {
		b = append(b, c...)
	}
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	return b
}
func extended(flags byte) []byte { p := make([]byte, 10); p[0] = flags; return chunk("VP8X", p) }
func animation() []byte          { return chunk("ANIM", []byte{9, 8, 7, 6, 3, 0}) }
func lossless() []byte           { return chunk("VP8L", []byte{0x2f, 0, 0, 0, 0}) }
func anmf(ms uint32, children ...[]byte) []byte {
	p := make([]byte, 16)
	Put24(p[12:15], int(ms))
	for _, c := range children {
		p = append(p, c...)
	}
	return chunk("ANMF", p)
}
func anim(children ...[]byte) []byte { return riff(extended(2), animation(), anmf(0, children...)) }
func drain(t *testing.T, c *Cursor, w *Working) error {
	t.Helper()
	for {
		f, e := c.NextFrame()
		if e != nil {
			return e
		}
		if w != nil {
			w.Release(f.OwnedBytes)
		}
	}
}

type gated struct {
	data      []byte
	pos, gate int
	calls     int
}

var errGate = errors.New("read beyond admitted frame")

func (r *gated) Read(p []byte) (int, error) {
	r.calls++
	if r.pos >= r.gate {
		return 0, errGate
	}
	n := len(p)
	if n > r.gate-r.pos {
		n = r.gate - r.pos
	}
	if n > len(r.data)-r.pos {
		n = len(r.data) - r.pos
	}
	if n == 0 {
		return 0, io.EOF
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func TestOpenAndFramesAreIncremental(t *testing.T) {
	first, second := anmf(7, lossless()), anmf(11, lossless())
	data := riff(extended(2|8), animation(), first, second, chunk("EXIF", []byte{1, 2, 3}))
	start := 12 + len(extended(2|8)) + len(animation())
	r := &gated{data: data, gate: start + 8 + 16}
	w := &Working{Limit: 65536 + 1024, Used: 65536}
	c, e := Open(context.Background(), r, Policy{Working: w})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if r.pos != r.gate || c.Summary() != nil {
		t.Fatal("Open read payload or finalized")
	}
	h := c.Header()
	if h.Canvas != image.Pt(1, 1) || !h.Animated || h.LoopCount != 3 || h.Background.R != 7 || h.Background.A != 6 {
		t.Fatalf("header %+v", h)
	}
	r.gate = start + len(first)
	f, e := c.NextFrame()
	if e != nil || f.Duration != 7*time.Millisecond || f.Index != 0 || r.pos != r.gate || f.OwnedBytes != 5 {
		t.Fatalf("first %+v %v pos %d", f, e, r.pos)
	}
	w.Release(f.OwnedBytes)
	f = Frame{}
	r.gate += len(second)
	f, e = c.NextFrame()
	if e != nil || f.Duration != 11*time.Millisecond || f.Index != 1 || r.pos != r.gate {
		t.Fatal("second", f, e, r.pos)
	}
	w.Release(f.OwnedBytes)
	if c.Summary() != nil {
		t.Fatal("summary before trailers")
	}
	r.gate = len(data)
	if _, e = c.NextFrame(); e != io.EOF {
		t.Fatal(e)
	}
	if s := c.Summary(); s == nil || s.Frames != 2 || s.DurationMilliseconds != 18 || s.DecodedPixels != 2 || s.Observed != 10 {
		t.Fatalf("summary %+v", s)
	}
	if m := c.MetadataChunks(); len(m) != 1 || !bytes.Equal(m[0].Data, []byte{1, 2, 3}) {
		t.Fatal(m)
	}
	c.Close()
	if w.Used != 65536 {
		t.Fatal("leaked cursor reservation", w.Used)
	}
	calls := r.calls
	if _, e = c.NextFrame(); e != io.ErrClosedPipe || r.calls != calls {
		t.Fatal("closed reader made I/O")
	}
}

func TestUnknownSkipVersusPreserve(t *testing.T) {
	data := riff(extended(2), animation(), anmf(0, chunk("BEFR", []byte{4}), lossless(), chunk("ODD!", []byte{1, 2, 3}), chunk("LAST", nil)), chunk("TAIL", []byte{7}))
	for _, preserve := range []bool{false, true} {
		w := &Working{Limit: 4096}
		c, e := Open(context.Background(), bytes.NewReader(data), Policy{Preserve: preserve, Working: w})
		if e != nil {
			t.Fatal(e)
		}
		f, e := c.NextFrame()
		if e != nil {
			t.Fatal(e)
		}
		if preserve {
			if len(f.UnknownChunks) != 3 || f.UnknownChunks[0].FourCC != "BEFR" || f.UnknownChunks[1].FourCC != "ODD!" || f.UnknownChunks[2].FourCC != "LAST" {
				t.Fatal(f.UnknownChunks)
			}
		} else if len(f.UnknownChunks) != 0 || w.Used != 5 {
			t.Fatal("unknown retained", f, w.Used)
		}
		w.Release(f.OwnedBytes)
		if _, e = c.NextFrame(); e != io.EOF {
			t.Fatal(e)
		}
		if preserve != (len(c.UnknownChunks()) == 1) {
			t.Fatal(c.UnknownChunks())
		}
		c.Close()
		if w.Used != 0 {
			t.Fatal("reservation", w.Used)
		}
	}
	huge := riff(extended(2), animation(), anmf(0, lossless(), chunk("HUGE", make([]byte, 65535))), chunk("TAIL", make([]byte, 65535)))
	w := &Working{Limit: 65536 + 16, Used: 65536}
	c, e := Open(context.Background(), bytes.NewReader(huge), Policy{Working: w})
	if e != nil {
		t.Fatal(e)
	}
	if e = drain(t, c, w); e != io.EOF {
		t.Fatal(e)
	}
	c.Close()
	if w.Used != 65536 {
		t.Fatal(w.Used)
	}
	w = &Working{Limit: 65536 + 16, Used: 65536}
	c, e = Open(context.Background(), bytes.NewReader(huge), Policy{Preserve: true, Working: w})
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.NextFrame()
	var limit *ResourceError
	if !errors.As(e, &limit) || limit.Resource != "working_bytes" || w.Used != 65536 {
		t.Fatal(e, w.Used)
	}
	c.Close()
}

func TestMetadataFirstEmptyAndAggregateBytes(t *testing.T) {
	data := riff(extended(2|8), chunk("EXIF", []byte{}), animation(), anmf(0, lossless()), chunk("EXIF", []byte{9, 8}))
	for _, preserve := range []bool{false, true} {
		c, e := Open(context.Background(), bytes.NewReader(data), Policy{Preserve: preserve})
		if e != nil {
			t.Fatal(e)
		}
		if e = drain(t, c, nil); e != io.EOF {
			t.Fatal(e)
		}
		m := c.MetadataChunks()
		if len(m) == 0 || m[0].Data == nil || len(m[0].Data) != 0 {
			t.Fatal("lost empty first", m)
		}
		if preserve && len(m) != 2 || !preserve && len(m) != 1 {
			t.Fatal(m)
		}
		c.Close()
	}
	c, e := Open(context.Background(), bytes.NewReader(data), Policy{Limits: Limits{MaxMetadataBytes: 1}})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	e = drain(t, c, nil)
	var limit *ResourceError
	if !errors.As(e, &limit) || limit.Resource != "metadata_bytes" || limit.Actual != 2 || c.Summary() != nil {
		t.Fatal(e)
	}
}

func TestNestedLengthPaddingAndKnownChunkRejection(t *testing.T) {
	known := []string{"VP8X", "ANIM", "ANMF", "ICCP", "EXIF", "XMP "}
	cases := map[string][]byte{}
	for _, id := range known {
		cases[id] = anim(lossless(), chunk(id, nil))
	}
	cases["duplicate image"] = anim(lossless(), lossless())
	cases["missing image"] = anim(chunk("WHAT", nil))
	bad := anim(lossless(), chunk("ODD!", []byte{1}))
	bad[len(bad)-1] = 1
	cases["pad"] = bad
	overflow := chunk("HUGE", nil)
	binary.LittleEndian.PutUint32(overflow[4:], 0xffffffff)
	cases["child overflow"] = anim(lossless(), overflow)
	tail := append([]byte{}, lossless()...)
	tail = append(tail, 1, 2, 3, 4, 5, 6)
	cases["short nested header"] = anim(tail)
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			c, e := Open(context.Background(), bytes.NewReader(b), Policy{})
			if e == nil {
				defer c.Close()
				_, e = c.NextFrame()
			}
			var f *Fault
			if !errors.As(e, &f) || f.Kind != Invalid || f.Frame != 0 {
				t.Fatalf("%v", e)
			}
		})
	}
	valid := anim(lossless(), chunk("ODD!", []byte{1}))
	for i := 0; i < len(valid); i++ {
		c, e := Open(context.Background(), bytes.NewReader(valid[:i]), Policy{})
		if e == nil {
			e = drain(t, c, nil)
			c.Close()
		}
		if e == nil || e == io.EOF {
			t.Fatalf("accepted truncation %d", i)
		}
	}
}

func TestLateTrailerFailureIsNotEOF(t *testing.T) {
	data := riff(extended(2|8), animation(), anmf(0, lossless()), chunk("EXIF", []byte{1}))
	data[len(data)-1] = 1
	c, e := Open(context.Background(), bytes.NewReader(data), Policy{})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if _, e = c.NextFrame(); e != nil {
		t.Fatal(e)
	}
	if _, e = c.NextFrame(); e == nil || e == io.EOF || c.Summary() != nil {
		t.Fatal("invalid trailer finalized", e)
	}
	previous := e
	if _, e = c.NextFrame(); e != previous {
		t.Fatal("terminal error not sticky")
	}
	missing := riff(extended(2|8), animation(), anmf(0, lossless()))
	c, e = Open(context.Background(), bytes.NewReader(missing), Policy{})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e = drain(t, c, nil); e == io.EOF || e == nil {
		t.Fatal("missing declared metadata", e)
	}
}

func TestNestedChunksShareAggregateBudgets(t *testing.T) {
	data := riff(extended(2), animation(), anmf(3, lossless(), chunk("ONE!", nil)), anmf(4, lossless(), chunk("TWO!", nil)))
	for _, test := range []struct {
		l        Limits
		resource string
		actual   int64
	}{
		{Limits{MaxChunks: 7}, "chunk_count", 8},
		{Limits{MaxFrames: 1}, "frames", 2},
		{Limits{MaxDecodedPixels: 1}, "decoded_pixels", 2},
		{Limits{MaxDuration: 6 * time.Millisecond}, "duration", int64(7 * time.Millisecond)},
	} {
		c, e := Open(context.Background(), bytes.NewReader(data), Policy{Limits: test.l})
		if e == nil {
			e = drain(t, c, nil)
			c.Close()
		}
		var limit *ResourceError
		if !errors.As(e, &limit) || limit.Resource != test.resource || limit.Actual != test.actual {
			t.Fatalf("%s: %v", test.resource, e)
		}
	}
	// Retained payload allowance charges enclosing ANMF once, not children again.
	retained := int64(10 + 6 + 16 + len(lossless()) + 8 + 16 + len(lossless()) + 8)
	c, e := Open(context.Background(), bytes.NewReader(data), Policy{Preserve: true, Limits: Limits{MaxRetainedBytes: retained}})
	if e != nil {
		t.Fatal(e)
	}
	if e = drain(t, c, nil); e != io.EOF {
		t.Fatal(e)
	}
	c.Close()
	c, e = Open(context.Background(), bytes.NewReader(data), Policy{Preserve: true, Limits: Limits{MaxRetainedBytes: retained - 1}})
	if e == nil {
		e = drain(t, c, nil)
		c.Close()
	}
	var limit *ResourceError
	if !errors.As(e, &limit) || limit.Resource != "retained_bytes" {
		t.Fatal(e)
	}
}

func TestProbePrecedesPayloadAllocation(t *testing.T) {
	bad := lossless()
	bad[9] = 1 // codec width=2 while ANMF width=1
	data := anim(bad)
	w := &Working{Limit: 1}
	c, e := Open(context.Background(), bytes.NewReader(data), Policy{Working: w})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	_, e = c.NextFrame()
	var f *Fault
	if !errors.As(e, &f) || f.Kind != Invalid || f.Chunk != "VP8L" || w.Used != 0 {
		t.Fatal(e, w.Used)
	}
}

func TestStillPrefixBoundaryAndExtent(t *testing.T) {
	data := riff(lossless())
	trailing := []byte{4, 5, 6}
	r := bytes.NewReader(append(data, trailing...))
	c, e := Open(context.Background(), r, Policy{})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if r.Len() != 1+len(trailing) || c.Header().Canvas != image.Pt(1, 1) {
		t.Fatal("read beyond codec prefix", r.Len())
	}
	f, e := c.NextFrame()
	if e != nil || !reflect.DeepEqual(f.VP8L, []byte{0x2f, 0, 0, 0, 0}) {
		t.Fatal(f, e)
	}
	if _, e = c.NextFrame(); e != io.EOF || r.Len() != len(trailing) {
		t.Fatal(e, r.Len())
	}
}

type coerror struct {
	data []byte
	err  error
}

func (r *coerror) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, r.err
}

type cancelRead struct {
	r      io.Reader
	cancel context.CancelFunc
	calls  int
}

func (r *cancelRead) Read(p []byte) (int, error) {
	r.calls++
	n, e := r.r.Read(p)
	r.cancel()
	return n, e
}

type emptyRead struct{ calls int }

func (r *emptyRead) Read([]byte) (int, error) { r.calls++; return 0, nil }
func TestCursorReadErrorAndCancellation(t *testing.T) {
	sentinel := errors.New("co-returned failure")
	_, e := Open(context.Background(), &coerror{riff(lossless()), sentinel}, Policy{})
	if !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelRead{r: bytes.NewReader(riff(lossless())), cancel: cancel}
	_, e = Open(ctx, r, Policy{})
	if e != context.Canceled || r.calls != 1 {
		t.Fatal(e, r.calls)
	}
	zero := &emptyRead{}
	_, e = Open(context.Background(), zero, Policy{})
	if !errors.Is(e, io.ErrNoProgress) || zero.calls != 100 {
		t.Fatal(e, zero.calls)
	}
}

func TestMetadataDescriptorGrowthIsBudgeted(t *testing.T) {
	data := riff(extended(8|4), chunk("EXIF", []byte{1}), chunk("XMP ", []byte{2, 3}), lossless())
	for _, cap := range []int64{64, 194} {
		w := &Working{Limit: cap}
		_, e := Open(context.Background(), bytes.NewReader(data), Policy{Working: w})
		var limit *ResourceError
		if !errors.As(e, &limit) || limit.Resource != "working_bytes" || w.Used != 0 {
			t.Fatal(e, w.Used)
		}
	}
	w := &Working{Limit: 195}
	c, e := Open(context.Background(), bytes.NewReader(data), Policy{Working: w})
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	if w.Used != 0 {
		t.Fatal(w.Used)
	}
}

func TestJoinedReadErrorIdentity(t *testing.T) {
	sentinel := errors.New("source failed too")
	for _, underlying := range []error{io.EOF, io.ErrUnexpectedEOF} {
		_, e := Open(context.Background(), &coerror{riff(lossless()), errors.Join(sentinel, underlying)}, Policy{})
		var fault *Fault
		if !errors.Is(e, sentinel) || !errors.Is(e, io.ErrUnexpectedEOF) || !errors.As(e, &fault) || fault.Kind != Invalid {
			t.Fatal(e)
		}
	}
}

func TestDurationAggregationCheckedBeforeNarrowing(t *testing.T) {
	// Direct state setup exercises an otherwise multi-gigabyte boundary without
	// allocating a huge fixture. Root caps are nanoseconds; parser sums are ms.
	c := &Cursor{policy: Policy{Limits: Limits{MaxDuration: time.Duration(1<<63 - 1)}}}
	c.stats.DurationMilliseconds = int64((time.Duration(1<<63 - 1)) / time.Millisecond)
	e := c.admit(Frame{Rect: image.Rect(0, 0, 1, 1), Duration: time.Millisecond})
	var limit *ResourceError
	if !errors.As(e, &limit) || limit.Resource != "duration" || limit.Actual != -1 {
		t.Fatal(e)
	}
	c.policy.Limits.MaxDuration = 0
	c.stats.DurationMilliseconds = 1<<63 - 1
	if e = c.admit(Frame{Rect: image.Rect(0, 0, 1, 1), Duration: time.Millisecond}); !errors.As(e, &limit) || limit.Actual != -1 {
		t.Fatal(e)
	}
}

func FuzzCursor(f *testing.F) {
	f.Add(riff(lossless()))
	f.Add(anim(lossless(), chunk("odd!", []byte{1, 2, 3})))
	f.Add(riff(extended(2|8), chunk("EXIF", nil), animation(), anmf(7, lossless()), chunk("EXIF", []byte{1})))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, preserve := range []bool{false, true} {
			w := &Working{Limit: 1 << 20, Used: 65536}
			c, e := Open(context.Background(), bytes.NewReader(b), Policy{Preserve: preserve, Working: w, Limits: Limits{MaxInputBytes: 64 << 10, MaxChunks: 128, MaxFrames: 32, MaxMetadataBytes: 4096, MaxRetainedBytes: 64 << 10, MaxCanvasPixels: 4096, MaxFramePixels: 4096, MaxDecodedPixels: 16384, MaxDuration: time.Minute}})
			if e != nil {
				if w.Used != 65536 {
					t.Fatal("Open leaked reservation", w.Used)
				}
				continue
			}
			e = drain(t, c, w)
			if e == io.EOF && c.Summary() == nil {
				t.Fatal("EOF lacks summary")
			}
			c.Close()
			if w.Used != 65536 {
				t.Fatal("cursor leaked reservation", w.Used)
			}
		}
	})
}
