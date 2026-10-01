package webp

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"m31labs.dev/tqwebp/container"
	"m31labs.dev/tqwebp/internal/encoder"
	"m31labs.dev/tqwebp/internal/webpwire"
	"math"
	"testing"
	"time"
)

func TestEncodeOddPayloadPaddingExactCaps(t *testing.T) {
	var selected *image.NRGBA
	for seed := 0; seed < 16; seed++ {
		m := image.NewNRGBA(image.Rect(0, 0, 4, 4))
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				m.SetNRGBA(x, y, color.NRGBA{R: byte(31*x + 7*y + seed*13), G: byte(93*x + 17*y), B: byte(11*x + 57*y + seed*5), A: byte(17*x + 31*y)})
			}
		}
		var out bytes.Buffer
		if e := Encode(&out, m, &Options{Quality: 75, Method: 1}); e != nil {
			t.Fatal(e)
		}
		f, e := container.Demux(context.Background(), bytes.NewReader(out.Bytes()), container.Limits{})
		if e != nil {
			t.Fatal(e)
		}
		if len(f.Frames[0].VP8)&1 == 1 {
			selected = m
			break
		}
	}
	if selected == nil {
		t.Fatal("fixture search did not exercise odd VP8 payload")
	}
	doc := &Document{Canvas: image.Pt(4, 4), Animated: true, Frames: []Frame{{Pixels: selected, Duration: time.Millisecond}, {Pixels: selected}}, Metadata: Metadata{ICC: []byte{1, 2, 3}, EXIF: []byte{4, 5, 6}, XMP: []byte{7}}}
	var base bytes.Buffer
	if e := EncodeAll(context.Background(), &base, doc, &Options{Method: 1}, DocumentLimits{}); e != nil {
		t.Fatal(e)
	}
	parsed, e := container.Demux(context.Background(), bytes.NewReader(base.Bytes()), container.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range parsed.Frames {
		if len(f.VP8)&1 != 1 || len(f.ALPH)&1 != 1 {
			t.Fatal("padding fixture does not cover both odd payloads")
		}
	}
	for _, limit := range []int64{int64(base.Len() - 1), int64(base.Len())} {
		var out bytes.Buffer
		e := EncodeAll(context.Background(), &out, doc, &Options{Method: 1}, DocumentLimits{Limits: Limits{MaxOutputBytes: limit}})
		if limit < int64(base.Len()) {
			if !errors.Is(e, ErrOutputTooLarge) || out.Len() != 0 {
				t.Fatal(e, out.Len())
			}
		} else if e != nil || !bytes.Equal(out.Bytes(), base.Bytes()) {
			t.Fatal(e, "exact cap changed output")
		}
	}
}
func TestEncodingEffectiveCeilingFields(t *testing.T) {
	want := webpwire.MaxRIFFSize + 8
	if int64(int(^uint(0)>>1)) < want {
		want = int64(int(^uint(0) >> 1))
	}
	if encodingOutputCap(0) != want || encodingOutputCap(math.MaxInt64) != want || encodingOutputCap(57) != 57 {
		t.Fatal("effective ceiling")
	}
	for _, tc := range []struct{ requested, effective, want int64 }{{0, want, want}, {math.MaxInt64, want, want}, {57, 57, 57}} {
		e := outputLimit(tc.requested, tc.effective, -1)
		var limit *LimitError
		if !errors.As(e, &limit) || limit.Limit != tc.want || limit.Actual != -1 || !errors.Is(e, ErrOutputTooLarge) || errors.Is(e, ErrInvalidFormat) {
			t.Fatal(e)
		}
	}
	if limitDurationActual(time.Duration(math.MaxInt64-1), 2) != -1 || limitDurationActual(3, 4) != 7 {
		t.Fatal("duration overflow")
	}
}

type encodeFinalWriter struct {
	cancel       context.CancelFunc
	err          error
	short        bool
	calls, bytes int
}

func (w *encodeFinalWriter) Write(p []byte) (int, error) {
	w.calls++
	w.bytes += len(p)
	w.cancel()
	if w.short {
		return len(p) - 1, w.err
	}
	return len(p), w.err
}
func TestTransparentAndDocumentLastWriteCancellation(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	m.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 127})
	for _, document := range []bool{false, true} {
		for _, cause := range []error{nil, &encoder.OutputLimitError{Limit: 5, Actual: -1}, &container.LimitError{Resource: "output_bytes", Limit: 5, Actual: 6}} {
			ctx, cancel := context.WithCancel(context.Background())
			w := &encodeFinalWriter{cancel: cancel, err: cause}
			var e error
			if document {
				e = EncodeAll(ctx, w, &Document{Canvas: image.Pt(2, 2), Animated: true, Frames: []Frame{{Pixels: m}}}, nil, DocumentLimits{})
			} else {
				e = EncodeContext(ctx, w, m, nil, Limits{})
			}
			want := cause
			if want == nil {
				want = context.Canceled
			}
			if e != want || w.calls != 1 || w.bytes == 0 {
				t.Fatal(e, want, w.calls, w.bytes)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &encodeFinalWriter{cancel: cancel, short: true}
	if e := EncodeContext(ctx, w, m, nil, Limits{}); e != io.ErrShortWrite {
		t.Fatal(e)
	}
}
