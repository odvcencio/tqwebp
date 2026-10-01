package vp8l

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"os"
	"testing"
	"unsafe"
)

var budgetRefusal = errors.New("test budget refusal")

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, e := os.ReadFile("../../testdata/lossless/" + name)
	if e != nil {
		t.Fatal(e)
	}
	for off := 12; off+8 <= len(b); {
		n := int(binary.LittleEndian.Uint32(b[off+4:]))
		if string(b[off:off+4]) == "VP8L" {
			return b[off+8 : off+8+n]
		}
		off += 8 + n + n&1
	}
	t.Fatal("no VP8L")
	return nil
}
func dimensions(b []byte) (int, int) {
	v := binary.LittleEndian.Uint32(b[1:])
	return int(v&0x3fff) + 1, int(v>>14&0x3fff) + 1
}
func unlimited(int64) error { return nil }
func TestAuditedSizes(t *testing.T) {
	if unsafe.Sizeof(hNode{}) != 8 || unsafe.Sizeof(hGroup{}) > 2680 || unsafe.Sizeof(decoder{}) > 1024 {
		t.Fatal("update allocation audit")
	}
}
func TestReserveEveryAllocation(t *testing.T) {
	for _, name := range []string{"blue-purple-pink.lossless.webp", "gopher-doc.1bpp.lossless.webp", "lossless-pattern-19x17.webp"} {
		t.Run(name, func(t *testing.T) {
			b := fixture(t, name)
			w, h := dimensions(b)
			calls := 0
			var total int64
			_, e := Decode(context.Background(), b, w, h, false, func(n int64) error {
				if n < 0 {
					t.Fatal(n)
				}
				calls++
				total += n
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("allocations=%d cumulative=%d", calls, total)
			for stop := 1; stop <= calls; stop++ {
				seen := 0
				_, e = Decode(context.Background(), b, w, h, false, func(int64) error {
					seen++
					if seen == stop {
						return budgetRefusal
					}
					return nil
				})
				if e != budgetRefusal || seen != stop {
					t.Fatalf("site %d: %v, calls %d", stop, e, seen)
				}
			}
		})
	}
}

type countingContext struct {
	context.Context
	calls, stop int
}

func (c *countingContext) Err() error {
	c.calls++
	if c.calls >= c.stop {
		return context.Canceled
	}
	return nil
}
func TestCooperativeCancellation(t *testing.T) {
	b := fixture(t, "blue-purple-pink-large.lossless.webp")
	w, h := dimensions(b)
	full := &countingContext{Context: context.Background(), stop: int(^uint(0) >> 1)}
	if _, e := Decode(full, b, w, h, false, unlimited); e != nil {
		t.Fatal(e)
	}
	for _, stop := range []int{1, 2, 10, 100, full.calls / 3, 2 * full.calls / 3, full.calls - 1} {
		c := &countingContext{Context: context.Background(), stop: stop}
		if _, e := Decode(c, b, w, h, false, unlimited); e != context.Canceled {
			t.Fatalf("checkpoint %d/%d: %v", stop, full.calls, e)
		}
	}
}
func TestHuffmanMalformed(t *testing.T) {
	d := &decoder{ctx: context.Background(), reserve: unlimited, r: bytes.NewReader(nil)}
	for _, lengths := range [][]uint32{nil, {0, 0}, {1, 1, 1}, {2, 2}, {16, 16}, {2}, {16}} {
		var h hTree
		if e := h.build(d, lengths); e == nil {
			t.Fatalf("accepted %v", lengths)
		}
	}
	var h hTree
	for _, n := range []uint32{0, 3} {
		if e := h.buildSimple(d, n, [2]uint32{}, 256); e == nil {
			t.Fatal(n)
		}
	}

	if _, e := h.next(d); e == nil {
		t.Fatal("empty tree")
	}
}

type bitWriter struct {
	data []byte
	bits uint
}

func (b *bitWriter) put(v uint32, n uint) {
	for i := uint(0); i < n; i++ {
		if b.bits%8 == 0 {
			b.data = append(b.data, 0)
		}
		b.data[len(b.data)-1] |= byte(v>>i&1) << (b.bits % 8)
		b.bits++
	}
}
func (b *bitWriter) simple(v uint32) { b.put(1, 1); b.put(0, 1); b.put(1, 1); b.put(v, 8) }
func (b *bitWriter) greenNormal(symbols ...int) {
	b.put(0, 1)
	b.put(0, 4) // normal tree, four code-length code lengths
	for _, n := range []uint32{0, 0, 1, 1} {
		b.put(n, 3)
	} // symbols 0 and 1 each have length 1
	b.put(0, 1) // full alphabet
	for i := 0; i < 280; i++ {
		v := uint32(0)
		for _, s := range symbols {
			if i == s {
				v = 1
			}
		}
		b.put(v, 1)
	}
}
func backrefData(valid bool) []byte {
	b := &bitWriter{}
	b.put(0, 1)
	b.put(0, 1)
	b.put(0, 1) // no transform, cache, meta
	if valid {
		b.greenNormal(1, 260)
	} else {
		b.greenNormal(256)
	}
	b.simple(0)
	b.simple(0)
	b.simple(255)
	b.simple(0)
	if valid {
		b.put(0, 1)
		b.put(1, 1)
		b.put(0, 1)
	} // literal, length5 backref, extra bit0
	return b.data
}
func TestLZ77BackReferences(t *testing.T) {
	m, e := Decode(context.Background(), backrefData(true), 1, 6, true, unlimited)
	if e != nil {
		t.Fatal(e)
	}
	want := image.NewNRGBA(image.Rect(0, 0, 1, 6))
	for i := 0; i < 6; i++ {
		want.Pix[4*i+1] = 1
		want.Pix[4*i+3] = 255
	}
	if !bytes.Equal(m.Pix, want.Pix) {
		t.Fatal(m.Pix)
	}
	if _, e = Decode(context.Background(), backrefData(false), 1, 1, true, unlimited); e == nil {
		t.Fatal("accepted reference before start")
	}
	if _, e = Decode(context.Background(), backrefData(true), 1, 2, true, unlimited); e == nil {
		t.Fatal("accepted reference beyond output")
	}
}
func TestSparseMetaGroupReservation(t *testing.T) {
	b := &bitWriter{}
	b.put(0, 1)
	b.put(0, 1)
	b.put(1, 1)
	b.put(0, 3) // no transform/cache; meta, tile bits2
	b.put(0, 1) // auxiliary color cache disabled (no nested meta flag)
	b.simple(255)
	b.simple(255)
	b.simple(0)
	b.simple(255)
	b.simple(0)
	// Supply the minimum bit capacity for all groups, so refusal exercises
	// allocation admission rather than the earlier impossible-length check.
	b.data = append(b.data, make([]byte, 65536*20/8)...)
	var refused int64
	_, e := Decode(context.Background(), b.data, 1, 1, true, func(n int64) error {
		if n > 100000 {
			refused = n
			return budgetRefusal
		}
		return nil
	})
	if e != budgetRefusal || refused != 2680*65536 {
		t.Fatal(e, refused)
	}
}
func TestAuxiliaryDepth(t *testing.T) {
	d := &decoder{ctx: context.Background(), reserve: unlimited, depth: 2}
	if _, e := d.decodePix(1, 1, 0, false); e == nil {
		t.Fatal("depth allowed")
	}
}
func TestRepeatedTransform(t *testing.T) {
	b := &bitWriter{}
	b.put(1, 1)
	b.put(2, 2)
	b.put(1, 1)
	b.put(2, 2)
	if _, e := Decode(context.Background(), b.data, 1, 1, true, unlimited); e == nil {
		t.Fatal("duplicate transform")
	}
}
func FuzzPayload(f *testing.F) {
	f.Add(fixture(f, "lossless-pattern-19x17.webp"), false)
	f.Add(backrefData(true), true)
	f.Fuzz(func(t *testing.T, p []byte, raw bool) {
		if len(p) > 64<<10 {
			return
		}
		w, h := 1, 6
		if !raw {
			if len(p) < 5 {
				return
			}
			w, h = dimensions(p)
			if w*h > 4096 {
				return
			}
		}
		var used int64
		_, _ = Decode(context.Background(), p, w, h, raw, func(n int64) error {
			if n > 1<<20-used {
				return budgetRefusal
			}
			used += n
			return nil
		})
	})
}

func TestTransformCoverage(t *testing.T) {
	var types, palette uint32
	for _, name := range []string{"blue-purple-pink.lossless.webp", "blue-purple-pink-large.lossless.webp", "gopher-doc.1bpp.lossless.webp", "gopher-doc.2bpp.lossless.webp", "gopher-doc.4bpp.lossless.webp", "gopher-doc.8bpp.lossless.webp", "tux.lossless.webp", "yellow_rose.lossless.webp"} {
		d := &decoder{ctx: context.Background(), r: bytes.NewReader(fixture(t, name)), reserve: unlimited}
		w, h, e := d.decodeHeader()
		if e != nil {
			t.Fatal(e)
		}
		for {
			more, e := d.read(1)
			if e != nil {
				t.Fatal(e)
			}
			if more == 0 {
				break
			}
			tr, nw, e := d.decodeTransform(w, h)
			if e != nil {
				t.Fatal(e)
			}
			types |= 1 << tr.transformType
			if tr.transformType == 3 {
				palette |= 1 << tr.bits
			}
			w = nw
		}
	}
	if types != 15 || palette != 15 {
		t.Fatalf("transform mask %04b, palette packing mask %04b", types, palette)
	}
}
func TestImpossibleHuffmanGroupLength(t *testing.T) {
	b := &bitWriter{}
	b.put(0, 1)
	b.put(0, 1)
	b.put(1, 1)
	b.put(0, 3)
	b.put(0, 1)
	b.simple(255)
	b.simple(255)
	b.simple(0)
	b.simple(255)
	b.simple(0)
	var largest int64
	_, e := Decode(context.Background(), b.data, 1, 1, true, func(n int64) error {
		if n > largest {
			largest = n
		}
		return nil
	})
	if !errors.Is(e, io.ErrUnexpectedEOF) || largest > 100000 {
		t.Fatal(e, largest)
	}
}

func TestColorCacheBounds(t *testing.T) {
	for _, bits := range []uint32{0, 12, 15} {
		b := &bitWriter{}
		b.put(0, 1)
		b.put(1, 1)
		b.put(bits, 4)
		if _, e := Decode(context.Background(), b.data, 1, 1, true, unlimited); e == nil {
			t.Fatal(bits)
		}
	}
	b := &bitWriter{}
	b.put(0, 1)
	b.put(1, 1)
	b.put(11, 4)
	var first int64
	_, e := Decode(context.Background(), b.data, 1, 1, true, func(n int64) error { first = n; return budgetRefusal })
	if e != budgetRefusal || first != 8192 {
		t.Fatal(e, first)
	}
}

func TestSimpleDuplicateBitConsumption(t *testing.T) {
	d := &decoder{ctx: context.Background(), reserve: unlimited, r: bytes.NewReader(nil), bits: 5, nBits: 3}
	var tree hTree
	if e := tree.buildSimple(d, 2, [2]uint32{57, 57}, 256); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 4; i++ {
		v, e := tree.next(d)
		if e != nil || v != 57 {
			t.Fatal(v, e)
		}
	}
	if d.nBits != 3 || d.bits != 5 {
		t.Fatal("duplicate singleton consumed alignment bits", d.bits, d.nBits)
	}
	if v, e := d.read(3); e != nil || v != 5 {
		t.Fatal(v, e)
	}
	var normal hTree
	if e := normal.build(d, []uint32{0, 1}); e != nil {
		t.Fatal("valid normal singleton rejected", e)
	}
}
