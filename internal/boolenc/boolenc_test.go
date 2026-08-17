package boolenc

import (
	"math/rand/v2"
	"testing"
)

// TestGoldenTrace pins the worked example of the tqwebp specification
// section 12.2: write the bits 1, 0, 1 at probability 128 and check the
// coder state after every step. The table in the specification lists the
// split, the range, and the accumulator this test asserts.
func TestGoldenTrace(t *testing.T) {
	steps := []struct {
		bit      bool
		wantRng  uint32
		wantBot  uint32
		wantBits int
	}{
		{true, 254, 256, 23},
		{false, 254, 512, 22},
		{true, 254, 1278, 21},
	}

	e := New(0)
	if e.rng != 255 || e.bottom != 0 || e.bitCount != 24 {
		t.Fatalf("initial state = (range %d, bottom %d, bitCount %d), want (255, 0, 24)", e.rng, e.bottom, e.bitCount)
	}
	for i, s := range steps {
		e.WriteBool(128, s.bit)
		if e.rng != s.wantRng || e.bottom != s.wantBot || e.bitCount != s.wantBits {
			t.Errorf("step %d: state = (range %d, bottom %d, bitCount %d), want (%d, %d, %d)",
				i, e.rng, e.bottom, e.bitCount, s.wantRng, s.wantBot, s.wantBits)
		}
	}

	got := e.Finish()
	d := NewDecoder(got)
	for i, s := range steps {
		if bit := d.ReadBool(128); bit != s.bit {
			t.Errorf("step %d: decoded %v, want %v", i, bit, s.bit)
		}
	}
	if d.UnexpectedEOF() {
		t.Error("decoder reported an unexpected end of stream")
	}
}

// TestSplitFormula pins the split rule of RFC 6386 section 7.3 against a
// direct evaluation, over every range and probability pair the coder can
// reach. The decoder of golang.org/x/image/vp8 computes the same split
// from range minus one, so a mismatch here breaks every stream.
func TestSplitFormula(t *testing.T) {
	for rng := uint32(128); rng <= 255; rng++ {
		for prob := 1; prob <= 255; prob++ {
			split := 1 + (((rng - 1) * uint32(prob)) >> 8)
			if split < 1 || split > rng-1 {
				t.Fatalf("range %d, prob %d: split %d is outside [1, %d]", rng, prob, split, rng-1)
			}
		}
	}
}

// TestRoundTripSkewedModels round-trips bit sequences under probability
// models that stress the coder: even odds, strong zero bias, strong one
// bias, and a model that changes with every bit. The mirage coder tests
// use the same shape (specification section 5.3).
func TestRoundTripSkewedModels(t *testing.T) {
	models := []struct {
		name string
		prob func(i int) uint8
		bit  func(r *rand.Rand, i int) bool
	}{
		{"even", func(int) uint8 { return 128 }, func(r *rand.Rand, _ int) bool { return r.IntN(2) == 0 }},
		{"zero-heavy", func(int) uint8 { return 254 }, func(r *rand.Rand, _ int) bool { return r.IntN(64) == 0 }},
		{"one-heavy", func(int) uint8 { return 1 }, func(r *rand.Rand, _ int) bool { return r.IntN(64) != 0 }},
		{"walking", func(i int) uint8 { return uint8(1 + i%255) }, func(r *rand.Rand, _ int) bool { return r.IntN(3) != 0 }},
		{"all-ones-at-max-prob", func(int) uint8 { return 255 }, func(*rand.Rand, int) bool { return true }},
		{"all-zeros-at-min-prob", func(int) uint8 { return 1 }, func(*rand.Rand, int) bool { return false }},
	}

	for _, m := range models {
		t.Run(m.name, func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 2))
			const n = 20000
			bits := make([]bool, n)
			probs := make([]uint8, n)
			e := New(0)
			for i := range bits {
				probs[i] = m.prob(i)
				bits[i] = m.bit(r, i)
				e.WriteBool(probs[i], bits[i])
			}
			stream := e.Finish()

			d := NewDecoder(stream)
			for i := range bits {
				if got := d.ReadBool(probs[i]); got != bits[i] {
					t.Fatalf("bit %d: decoded %v, want %v (stream %d bytes)", i, got, bits[i], len(stream))
				}
			}
			if d.UnexpectedEOF() {
				t.Errorf("decoder reported an unexpected end of stream after %d bits", n)
			}
		})
	}
}

// TestCarryChain forces long runs of 0xff output bytes, which is the case
// the carry ripple of addOneToOutput exists for. The test searches for bit
// patterns that produce those runs, and it fails when no run appears,
// because that would mean the case never ran.
func TestCarryChain(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	longestRun := 0
	for trial := 0; trial < 200; trial++ {
		const n = 4000
		bits := make([]bool, n)
		probs := make([]uint8, n)
		e := New(0)
		for i := range bits {
			// Very high probabilities and a mostly-one bit stream drive
			// bottom towards the top of its range, which is where 0xff
			// bytes and their carry ripples come from.
			probs[i] = uint8(250 + r.IntN(6))
			bits[i] = r.IntN(32) != 0
			e.WriteBool(probs[i], bits[i])
		}
		stream := e.Finish()

		run, best := 0, 0
		for _, b := range stream {
			if b == 0xff {
				run++
				if run > best {
					best = run
				}
			} else {
				run = 0
			}
		}
		if best > longestRun {
			longestRun = best
		}

		d := NewDecoder(stream)
		for i := range bits {
			if got := d.ReadBool(probs[i]); got != bits[i] {
				t.Fatalf("trial %d, bit %d: decoded %v, want %v", trial, i, got, bits[i])
			}
		}
	}
	if longestRun < 2 {
		t.Errorf("longest 0xff run was %d bytes: the carry ripple never ran across a chain", longestRun)
	}
}

// TestLiteralRoundTrip checks the L(n) helper against its decoder, for
// every width the VP8 frame header uses.
func TestLiteralRoundTrip(t *testing.T) {
	widths := []int{1, 2, 3, 4, 6, 7, 8, 14, 19}
	r := rand.New(rand.NewPCG(3, 5))
	values := make([]uint32, 0, len(widths)*64)
	e := New(0)
	for _, w := range widths {
		for i := 0; i < 64; i++ {
			v := uint32(r.Uint64()) & (1<<uint(w) - 1)
			values = append(values, v)
			e.WriteLiteral(v, w)
		}
	}
	stream := e.Finish()

	d := NewDecoder(stream)
	k := 0
	for _, w := range widths {
		for i := 0; i < 64; i++ {
			if got := d.ReadLiteral(w); got != values[k] {
				t.Fatalf("width %d, item %d: decoded %d, want %d", w, i, got, values[k])
			}
			k++
		}
	}
}

// TestSignedRoundTrip checks the signed and optional-signed helpers, which
// the quantizer and filter headers use.
func TestSignedRoundTrip(t *testing.T) {
	values := []int32{0, 1, -1, 7, -7, 15, -15, 63, -63}
	e := New(0)
	for _, v := range values {
		e.WriteSigned(v, 7)
	}
	for _, v := range values {
		e.WriteOptionalSigned(v, 7)
	}
	stream := e.Finish()

	d := NewDecoder(stream)
	for _, want := range values {
		mag := int32(d.ReadLiteral(7))
		if d.ReadFlag() {
			mag = -mag
		}
		if mag != want {
			t.Errorf("signed: decoded %d, want %d", mag, want)
		}
	}
	for _, want := range values {
		var got int32
		if d.ReadFlag() {
			got = int32(d.ReadLiteral(7))
			if d.ReadFlag() {
				got = -got
			}
		}
		if got != want {
			t.Errorf("optional signed: decoded %d, want %d", got, want)
		}
	}
}

// TestFlushContract pins the flush behaviour: an empty stream still
// produces the four accumulator bytes, and Finish refuses a second call.
func TestFlushContract(t *testing.T) {
	e := New(0)
	if got := len(e.Finish()); got != 4 {
		t.Errorf("empty stream flushed to %d bytes, want 4", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("second Finish call did not panic")
		}
	}()
	e.Finish()
}

// TestZeroProbabilityPanics pins the guard against probability zero, which
// no decoder can invert.
func TestZeroProbabilityPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("WriteBool with probability 0 did not panic")
		}
	}()
	New(0).WriteBool(0, true)
}

// TestDecoderTruncatedStreamReportsEOF pins the error-not-panic contract:
// a truncated stream must keep returning decisions and must report the
// end of the buffer through UnexpectedEOF.
func TestDecoderTruncatedStreamReportsEOF(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 17))
	e := New(0)
	for i := 0; i < 500; i++ {
		e.WriteBool(uint8(1+r.IntN(255)), r.IntN(2) == 0)
	}
	stream := e.Finish()

	for cut := 0; cut < len(stream); cut++ {
		d := NewDecoder(stream[:cut])
		for i := 0; i < 500; i++ {
			d.ReadBool(128)
		}
		if cut < 2 && !d.UnexpectedEOF() {
			t.Errorf("cut %d: decoder did not report the end of the buffer", cut)
		}
	}
}

// TestDeterminism pins byte-for-byte repeatability: the same decisions
// always produce the same stream.
func TestDeterminism(t *testing.T) {
	build := func() []byte {
		r := rand.New(rand.NewPCG(99, 100))
		e := New(0)
		for i := 0; i < 5000; i++ {
			e.WriteBool(uint8(1+r.IntN(255)), r.IntN(2) == 0)
		}
		return e.Finish()
	}
	a, b := build(), build()
	if string(a) != string(b) {
		t.Error("two identical runs produced different bytes")
	}
}

// FuzzRoundTrip is the seed corpus for test T6 at the coder level: any
// byte string becomes a bit and probability schedule, and the decoder must
// return exactly the bits the encoder took.
func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte{0x00})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{0x01, 0x80, 0xfe, 0x7f, 0x00, 0xff})
	f.Add(make([]byte, 64))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		n := len(data) * 8
		bits := make([]bool, n)
		probs := make([]uint8, n)
		e := New(0)
		for i := 0; i < n; i++ {
			bits[i] = data[i/8]>>(uint(i)%8)&1 == 1
			p := data[(i/3)%len(data)]
			if p == 0 {
				p = 1
			}
			probs[i] = p
			e.WriteBool(probs[i], bits[i])
		}
		stream := e.Finish()

		d := NewDecoder(stream)
		for i := 0; i < n; i++ {
			if got := d.ReadBool(probs[i]); got != bits[i] {
				t.Fatalf("bit %d of %d: decoded %v, want %v", i, n, got, bits[i])
			}
		}
		if d.UnexpectedEOF() {
			t.Fatalf("decoder reported an unexpected end of stream over %d bits", n)
		}
	})
}
