package vp8

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"unsafe"
)

type countContext struct {
	context.Context
	n, stop int
}

func (c *countContext) Err() error {
	c.n++
	if c.n >= c.stop {
		return context.Canceled
	}
	return nil
}
func TestAuditedSizes(t *testing.T) {
	if unsafe.Sizeof(mb{}) != 6 || unsafe.Sizeof(filterParam{}) != 4 || unsafe.Sizeof(Decoder{}) > 16000 {
		t.Fatal("update audited bound", unsafe.Sizeof(mb{}), unsafe.Sizeof(filterParam{}), unsafe.Sizeof(Decoder{}))
	}
}
func TestCPUCheckpoints(t *testing.T) {
	b, e := os.ReadFile("../../testdata/decode/blue-purple-pink-large.normal-filter.lossy.webp")
	if e != nil {
		t.Fatal(e)
	}
	for _, stop := range []int{1, 2, 10, 100, 300, 1000, 1500, 1900} {
		d := NewDecoder()
		d.Init(bytes.NewReader(b[20:]), len(b)-20)
		h, e := d.DecodeFrameHeader()
		if e != nil {
			t.Fatal(e)
		}
		c := &countContext{Context: context.Background(), stop: stop}
		_, e = d.DecodeFrame(c)
		total := 2*((h.Width+15)/16)*((h.Height+15)/16) + 2
		if stop <= total && e != context.Canceled {
			t.Fatalf("checkpoint %d/%d: %v", stop, total, e)
		}
	}
}

type noProgress struct {
	calls  int
	cancel context.CancelFunc
}

func (r *noProgress) Read([]byte) (int, error) {
	r.calls++
	if r.cancel != nil && r.calls == 3 {
		r.cancel()
	}
	return 0, nil
}
func TestReaderProgress(t *testing.T) {
	r := &noProgress{}
	lr := limitReader{r: r, n: 1}
	if e := lr.ReadFull(make([]byte, 1)); e != io.ErrNoProgress || r.calls != 100 {
		t.Fatal(e, r.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r = &noProgress{cancel: cancel}
	lr = limitReader{r: r, n: 1, ctx: ctx}
	if e := lr.ReadFull(make([]byte, 1)); e != context.Canceled || r.calls != 3 {
		t.Fatal(e, r.calls)
	}
}

type fullErrorReader struct{ err error }

func (r fullErrorReader) Read(p []byte) (int, error) { clear(p); return len(p), r.err }
func TestReaderFinalError(t *testing.T) {
	sentinel := errors.New("final read failed")
	r := limitReader{r: fullErrorReader{sentinel}, n: 1}
	if e := r.ReadFull(make([]byte, 1)); e != sentinel {
		t.Fatal(e)
	}
	r = limitReader{r: fullErrorReader{io.EOF}, n: 1}
	if e := r.ReadFull(make([]byte, 1)); e != nil {
		t.Fatal(e)
	}
}
