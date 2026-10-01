package webp

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"testing"
	"time"

	"m31labs.dev/tqwebp/internal/encoder"
)

func TestEncodeContextValidationPrecedence(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	alpha := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name    string
		ctx     context.Context
		w       io.Writer
		m       image.Image
		options *Options
		limits  Limits
		want    error
	}{
		{"nil context", nil, nil, nil, &Options{Quality: -1}, Limits{MaxWidth: -1}, ErrInvalidContext},
		{"cancel before callbacks", cancelled, nil, panicImage{image.Rect(0, 0, 1, 1)}, &Options{Quality: -1}, Limits{MaxWidth: -1}, context.Canceled},
		{"options before limits", context.Background(), nil, nil, &Options{Quality: -1}, Limits{MaxWidth: -1}, ErrInvalidOptions},
		{"limits before writer", context.Background(), nil, nil, nil, Limits{MaxWidth: -1}, ErrInvalidLimits},
		{"writer before image", context.Background(), nil, nil, nil, Limits{}, ErrInvalidWriter},
		{"image before bounds", context.Background(), io.Discard, nil, nil, Limits{}, ErrInvalidImage},
		{"bounds before width", context.Background(), io.Discard, panicImage{image.Rect(0, 0, 0, 1)}, nil, Limits{MaxWidth: 1}, ErrInvalidImage},
		{"width before format", context.Background(), io.Discard, panicImage{image.Rectangle{Min: image.Pt(-maxInt-1, 0), Max: image.Pt(maxInt, 1)}}, nil, Limits{MaxWidth: 1}, ErrLimitExceeded},
		{"height before pixels", context.Background(), io.Discard, panicImage{image.Rect(0, 0, 1, 2)}, nil, Limits{MaxHeight: 1, MaxPixels: 1}, ErrLimitExceeded},
		{"pixels before format", context.Background(), io.Discard, panicImage{image.Rect(0, 0, 16384, 1)}, nil, Limits{MaxPixels: 1}, ErrLimitExceeded},
		{"format before alpha", context.Background(), io.Discard, panicImage{image.Rect(0, 0, 16384, 1)}, nil, Limits{}, ErrTooLarge},
		{"alpha output refusal", context.Background(), io.Discard, alpha, nil, Limits{MaxOutputBytes: 1}, ErrOutputTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := EncodeContext(tt.ctx, tt.w, tt.m, tt.options, tt.limits)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if tt.ctx == context.Background() {
				err = EncodeWithLimits(tt.w, tt.m, tt.options, tt.limits)
				if !errors.Is(err, tt.want) {
					t.Fatalf("legacy got %v, want %v", err, tt.want)
				}
			}
		})
	}
}

type nilPointerContext struct{ context.Context }
type untouchableImage struct{}

func (untouchableImage) ColorModel() color.Model { panic("pre-cancelled ColorModel callback") }
func (untouchableImage) Bounds() image.Rectangle { panic("pre-cancelled Bounds callback") }
func (untouchableImage) At(int, int) color.Color { panic("pre-cancelled At callback") }

type untouchableWriter struct{}

func (untouchableWriter) Write([]byte) (int, error) { panic("pre-cancelled Write callback") }

func TestContextNilAndExpiredTouchNoCallbacks(t *testing.T) {
	var typedNil *nilPointerContext
	if err := EncodeContext(typedNil, untouchableWriter{}, untouchableImage{}, nil, Limits{}); err != ErrInvalidContext {
		t.Fatalf("typed nil got %v", err)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := EncodeContext(expired, untouchableWriter{}, untouchableImage{}, &Options{Quality: -1}, Limits{MaxWidth: -1}); err != context.DeadlineExceeded {
		t.Fatalf("expired got %v", err)
	}
}

func TestLimitErrorDetails(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		limits        Limits
		rect          image.Rectangle
		resource      string
		limit, actual int64
	}{
		{Limits{MaxWidth: 3}, image.Rect(0, 0, 4, 2), "width", 3, 4},
		{Limits{MaxHeight: 3}, image.Rect(0, 0, 2, 4), "height", 3, 4},
		{Limits{MaxPixels: 7}, image.Rect(0, 0, 2, 4), "pixels", 7, 8},
		{Limits{MaxPixels: 1}, image.Rectangle{Min: image.Pt(-maxInt-1, 0), Max: image.Pt(maxInt, 2)}, "pixels", 1, -1},
	}
	for _, tt := range tests {
		err := EncodeWithLimits(io.Discard, panicImage{tt.rect}, nil, tt.limits)
		var detail *LimitError
		if !errors.As(err, &detail) || detail.Resource != tt.resource || detail.Limit != tt.limit || detail.Actual != tt.actual {
			t.Fatalf("got %#v, want %+v", err, tt)
		}
		if !errors.Is(err, ErrLimitExceeded) || errors.Is(err, ErrOutputTooLarge) {
			t.Fatalf("bad limit classification: %v", err)
		}
	}
}

func TestContextOutputCapBoundaries(t *testing.T) {
	for _, method := range []int{4, 5, 6} {
		m := opaqueImage(17, 19)
		options := &Options{Quality: 75, Method: method}
		var baseline bytes.Buffer
		if err := Encode(&baseline, m, options); err != nil {
			t.Fatal(err)
		}
		size := int64(baseline.Len())
		for _, cap := range []int64{1, 20, 29, 30, 31, size - 1, size, size + 1, 1 << 62} {
			var out bytes.Buffer
			err := EncodeContext(context.Background(), &out, m, options, Limits{MaxOutputBytes: cap})
			if cap < size {
				var detail *LimitError
				if !errors.As(err, &detail) || detail.Resource != "output_bytes" || detail.Limit != cap || !errors.Is(err, ErrOutputTooLarge) || !errors.Is(err, ErrLimitExceeded) {
					t.Fatalf("method%d cap%d: %v", method, cap, err)
				}
				if out.Len() != 0 {
					t.Fatal("refusal wrote output")
				}
			} else if err != nil || !bytes.Equal(out.Bytes(), baseline.Bytes()) {
				t.Fatalf("method%d cap%d: %v or bytes changed", method, cap, err)
			}
		}
	}
}

type cancelImage struct {
	image.Image
	cancel context.CancelFunc
	at     int
}

func (m *cancelImage) At(x, y int) color.Color {
	m.at++
	if m.at == 5 {
		m.cancel()
	}
	return m.Image.At(x, y)
}

func TestContextCancellationDuringOpacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &cancelImage{Image: opaqueImage(129, 3), cancel: cancel}
	var out bytes.Buffer
	if err := EncodeContext(ctx, &out, m, nil, Limits{}); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
	if out.Len() != 0 || m.at > 64 {
		t.Fatalf("continued opacity scan: calls=%d bytes=%d", m.at, out.Len())
	}
}

type cancelWriter struct {
	cancel context.CancelFunc
	calls  int
	err    error
}

func (w *cancelWriter) Write(p []byte) (int, error) { w.calls++; w.cancel(); return len(p), w.err }

func TestCancellationAfterWriterCallback(t *testing.T) {
	for _, limits := range []Limits{{}, {MaxOutputBytes: 1 << 20}} {
		for _, external := range []error{nil, errors.New("external write")} {
			ctx, cancel := context.WithCancel(context.Background())
			w := &cancelWriter{cancel: cancel, err: external}
			err := EncodeContext(ctx, w, opaqueImage(16, 16), nil, limits)
			want := external
			if want == nil {
				want = context.Canceled
			}
			if err != want || w.calls != 1 {
				t.Fatalf("got %v/%d writes, want %v/1", err, w.calls, want)
			}
		}
	}
}

func TestWriterLimitLikeErrorRetainsIdentity(t *testing.T) {
	for _, limits := range []Limits{{}, {MaxOutputBytes: 1 << 20}} {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cause := &encoder.OutputLimitError{Limit: 42, Actual: -1}
		w := &cancelWriter{cancel: cancel, err: cause}
		if err := EncodeContext(ctx, w, opaqueImage(1, 1), nil, limits); err != cause {
			t.Fatalf("writer error was reclassified: %#v", err)
		}
	}
}

func TestContextPaletteKeepsLegacyOpaqueSemantics(t *testing.T) {
	m := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.Transparent})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := EncodeContext(ctx, io.Discard, m, nil, Limits{}); err != nil {
		t.Fatalf("palette failed transparent-capable encode: %v", err)
	}
}

type conversionCancelImage struct {
	image.Image
	cancel context.CancelFunc
	calls  int
}

func (m *conversionCancelImage) Opaque() bool { return true }
func (m *conversionCancelImage) At(x, y int) color.Color {
	m.calls++
	if m.calls == 5 {
		m.cancel()
	}
	return m.Image.At(x, y)
}

func TestContextCancellationDuringConversion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &conversionCancelImage{Image: opaqueImage(129, 3), cancel: cancel}
	var out bytes.Buffer
	if err := EncodeContext(ctx, &out, m, nil, Limits{}); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
	if out.Len() != 0 || m.calls > 256 {
		t.Fatalf("continued conversion: calls%d bytes%d", m.calls, out.Len())
	}
}

type blockedOpaqueImage struct {
	image.Image
	entered, release chan struct{}
}

func (m *blockedOpaqueImage) Opaque() bool { close(m.entered); <-m.release; return true }

func TestContextDoesNotPretendToInterruptBlockedImageCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &blockedOpaqueImage{Image: opaqueImage(1, 1), entered: make(chan struct{}), release: make(chan struct{})}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- EncodeContext(ctx, &out, m, nil, Limits{}) }()
	<-m.entered
	cancel()
	select {
	case err := <-done:
		close(m.release)
		t.Fatalf("returned before callback release: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(m.release)
	select {
	case err := <-done:
		if err != context.Canceled || out.Len() != 0 {
			t.Fatalf("got %v/%d bytes", err, out.Len())
		}
	case <-time.After(time.Second):
		t.Fatal("did not return after callback release")
	}
}
