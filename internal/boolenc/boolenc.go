// Package boolenc implements the VP8 boolean entropy coder of RFC 6386
// section 7.3. The encoder writes a stream of binary decisions. Each
// decision carries an 8-bit probability. The probability states how likely
// the value zero is, on a scale of 256.
//
// The package holds two types. Encoder produces bitstreams. Decoder reads
// them back. The library's encode path uses Encoder only. Decoder exists
// so tests can prove that every stream decodes to the bits that went in
// (test T1 of the tqwebp specification).
//
// # Determinism
//
// Every operation uses fixed-width unsigned integers. The package contains
// no floating point, no maps, and no goroutines. The same input bits and
// probabilities always produce the same output bytes, on every platform.
//
// # Carry propagation
//
// The encoder keeps a 32-bit accumulator named bottom. A renormalization
// shift can push a carry out of bit 31. The carry then adds one to the
// last byte already written, and the addition ripples backwards across any
// 0xff bytes. Method addOneToOutput does that ripple.
//
// The ripple never runs off the front of the output. Proof: a carry needs
// bit 31 of bottom set. The coder keeps the invariant
// bottom + range<<bitCount <= 1<<32 with range >= 128, so bottom stays at
// or below 1<<31. Directly after a byte leaves the accumulator, bottom is
// below 1<<24 and bitCount is 8, so seven more shifts at most can happen
// before the next byte leaves. Bottom therefore stays below 1<<31 until at
// least one byte exists to absorb the carry. The first byte needs 24
// shifts, and bottom stays below 1<<31 for all of them, because it starts
// below 1<<8 and each write adds less than 1<<8.
package boolenc

// Encoder writes binary decisions as a VP8 boolean-coded byte stream.
// Create one with New, write decisions with WriteBool and its helpers,
// then call Finish exactly once to get the bytes.
type Encoder struct {
	out      []byte
	rng      uint32 // arithmetic range, always in [128, 255] between writes
	bottom   uint32 // low end of the coding interval
	bitCount int    // shifts left before the next byte leaves bottom
	finished bool
}

// New returns an Encoder that writes into a fresh buffer. Argument hint
// sizes that buffer; pass 0 when the size is unknown.
func New(hint int) *Encoder {
	var out []byte
	if hint > 0 {
		out = make([]byte, 0, hint)
	}
	return &Encoder{out: out, rng: 255, bottom: 0, bitCount: 24}
}

// Len reports how many bytes the encoder has written so far. The count
// excludes the bytes that Finish flushes out of the accumulator.
func (e *Encoder) Len() int { return len(e.out) }

// WriteBool writes one binary decision. Argument prob is the probability
// that bit is false, on a scale of 256. WriteBool panics when prob is 0,
// because a zero probability makes the stream undecodable.
func (e *Encoder) WriteBool(prob uint8, bit bool) {
	if prob == 0 {
		panic("tqwebp/boolenc: probability 0 is not codable")
	}
	split := 1 + (((e.rng - 1) * uint32(prob)) >> 8)
	if bit {
		e.bottom += split
		e.rng -= split
	} else {
		e.rng = split
	}
	for e.rng < 128 {
		e.rng <<= 1
		if e.bottom&(1<<31) != 0 {
			e.addOneToOutput()
		}
		e.bottom <<= 1
		e.bitCount--
		if e.bitCount == 0 {
			e.out = append(e.out, byte(e.bottom>>24))
			e.bottom &= 1<<24 - 1
			e.bitCount = 8
		}
	}
}

// WriteFlag writes one decision at probability 128, the even-odds case
// the VP8 frame header uses for its plain header fields.
func (e *Encoder) WriteFlag(bit bool) { e.WriteBool(128, bit) }

// WriteLiteral writes the low n bits of v, most significant bit first, at
// probability 128. RFC 6386 calls this an L(n) field.
func (e *Encoder) WriteLiteral(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		e.WriteFlag(v>>uint(i)&1 == 1)
	}
}

// WriteSigned writes an n-bit magnitude followed by a sign bit, all at
// probability 128. RFC 6386 uses this shape for quantizer deltas.
func (e *Encoder) WriteSigned(v int32, n int) {
	mag := v
	if mag < 0 {
		mag = -mag
	}
	e.WriteLiteral(uint32(mag), n)
	e.WriteFlag(v < 0)
}

// WriteOptionalSigned writes a present flag, and writes v as an n-bit
// signed value when v is not zero. RFC 6386 uses this shape wherever zero
// is the likely value.
func (e *Encoder) WriteOptionalSigned(v int32, n int) {
	if v == 0 {
		e.WriteFlag(false)
		return
	}
	e.WriteFlag(true)
	e.WriteSigned(v, n)
}

// Finish flushes the accumulator and returns the finished stream. Call it
// once. A second call panics, because the flush changes coder state.
//
// Finish emits four bytes from the accumulator, as the reference encoder
// of RFC 6386 section 7.3 does. The last one to three bytes can be
// redundant. They cost the stream up to three bytes and they guarantee
// that a decoder which prefetches bytes never reads past the buffer.
func (e *Encoder) Finish() []byte {
	if e.finished {
		panic("tqwebp/boolenc: Finish called twice")
	}
	e.finished = true

	c := uint(e.bitCount)
	v := e.bottom
	if v&(1<<(32-c)) != 0 {
		e.addOneToOutput()
	}
	// The shift drops every bit at or above position 32-c, the carry bit
	// included, and moves the next byte to be written into bits 31 to 24.
	v <<= c
	for i := 0; i < 4; i++ {
		e.out = append(e.out, byte(v>>24))
		v <<= 8
	}
	return e.out
}

// addOneToOutput adds one to the last byte written and ripples the carry
// backwards over 0xff bytes. See the package comment for the proof that
// the ripple always stops inside the buffer.
func (e *Encoder) addOneToOutput() {
	i := len(e.out) - 1
	for i >= 0 && e.out[i] == 0xff {
		e.out[i] = 0
		i--
	}
	if i < 0 {
		panic("tqwebp/boolenc: carry ran off the front of the stream")
	}
	e.out[i]++
}
