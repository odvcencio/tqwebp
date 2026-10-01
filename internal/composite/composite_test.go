package composite

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"
)

type checkpointContext struct{ calls, stop int }

func (c *checkpointContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *checkpointContext) Done() <-chan struct{}       { return nil }
func (c *checkpointContext) Value(any) any               { return nil }
func (c *checkpointContext) Err() error {
	c.calls++
	if c.calls >= c.stop {
		return context.Canceled
	}
	return nil
}

func TestEveryCompositorCheckpointCancellation(t *testing.T) {
	// Reconstruct state per trial; cancel at every observed checkpoint through
	// source-alpha scan, disposal rows/chunks, composition rows/pixel intervals.
	run := func(ctx context.Context) error {
		c, e := New(context.Background(), 2051, 4, func(int64) error { return nil })
		if e != nil {
			return e
		}
		first := image.NewNRGBA(image.Rect(0, 0, 2051, 4))
		if _, _, e = c.Apply(context.Background(), Control{Rect: first.Rect, Dispose: true}, first); e != nil {
			return e
		}
		_, _, e = c.Apply(ctx, Control{Rect: first.Rect}, first)
		return e
	}
	count := &checkpointContext{stop: 1 << 30}
	if e := run(count); e != nil {
		t.Fatal(e)
	}
	if count.calls < 20 {
		t.Fatal("insufficient checkpoints", count.calls)
	}
	for stop := 1; stop <= count.calls; stop++ {
		ctx := &checkpointContext{stop: stop}
		if e := run(ctx); !errors.Is(e, context.Canceled) {
			t.Fatalf("checkpoint %d/%d: %v", stop, count.calls, e)
		}
	}
}
func TestCompositorAdmissionAndMalformedStorage(t *testing.T) {
	sentinel := errors.New("budget")
	if c, e := New(context.Background(), 10, 10, func(int64) error { return sentinel }); c != nil || e != sentinel {
		t.Fatal(c, e)
	}
	maxInt := int(^uint(0) >> 1)
	if c, e := New(context.Background(), maxInt, maxInt, func(int64) error { t.Fatal("overflow reached allocator"); return nil }); c != nil || e != ErrFrame {
		t.Fatal(c, e)
	}
	c, e := New(context.Background(), 3, 3, func(int64) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	for _, src := range []*image.NRGBA{
		{Rect: image.Rect(0, 0, 3, 3), Stride: 12, Pix: make([]byte, 35)},
		{Rect: image.Rect(0, 0, 3, 3), Stride: maxInt, Pix: make([]byte, 36)},
		{Rect: image.Rect(0, 0, 3, 3), Stride: -12, Pix: make([]byte, 36)},
		{Rect: image.Rect(0, 0, 3, 3), Stride: 11, Pix: make([]byte, 36)},
	} {
		if _, _, e := c.Apply(context.Background(), Control{Rect: image.Rect(0, 0, 3, 3)}, src); e != ErrFrame {
			t.Fatal(e)
		}
	}
	c.Close()
	if c.Bytes() != 0 {
		t.Fatal(c.Bytes())
	}
}
func TestBlendAllAlphaPairsBounded(t *testing.T) {
	// Every alpha pair: integer output alpha follows the normative unpremultiplied
	// formula; source transparency is a no-op, including hidden destination RGB.
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			dst := []byte{255, 1, 127, byte(b)}
			src := []byte{1, 254, 0, byte(a)}
			before := append([]byte(nil), dst...)
			blend(dst, src)
			if a == 0 {
				for i := range dst {
					if dst[i] != before[i] {
						t.Fatal(a, b, dst)
					}
				}
				continue
			}
			want := a + (b * (256 - a) >> 8)
			if int(dst[3]) != want {
				t.Fatal(a, b, dst, want)
			}
		}
	}
}
