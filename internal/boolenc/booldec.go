package boolenc

// Decoder reads a VP8 boolean-coded byte stream. It is the paired decoder
// of Encoder: tqwebp's tests feed every encoded stream back through it and
// require the original bits (test T1). The library's encode path never
// calls it.
//
// Decoder follows the reference decoder of RFC 6386 section 7.3: a value
// register, a range register, and a bit counter that pulls one byte at a
// time. golang.org/x/image/vp8 decodes the same streams with a lookup
// table instead of the shift loop, so the two implementations agree on the
// format while sharing no code.
//
// Decoder never panics on stream bytes. It reports a read past the end of
// the buffer through UnexpectedEOF, and it keeps returning decisions made
// from zero bytes after that point.
type Decoder struct {
	buf      []byte
	r        int
	rng      uint32
	value    uint32
	bitCount int
	eof      bool
}

// NewDecoder returns a Decoder that reads buf. It consumes the first two
// bytes at once, as RFC 6386 section 7.3 requires.
func NewDecoder(buf []byte) *Decoder {
	d := &Decoder{buf: buf, rng: 255}
	d.value = uint32(d.nextByte())<<8 | uint32(d.nextByte())
	return d
}

// UnexpectedEOF reports whether the decoder tried to read past the end of
// its buffer. A well-formed stream that the matching encoder produced
// never sets this flag while its own bits are being read back.
func (d *Decoder) UnexpectedEOF() bool { return d.eof }

// ReadBool returns the next decision. Argument prob must equal the
// probability the encoder used for the same decision.
func (d *Decoder) ReadBool(prob uint8) bool {
	split := 1 + (((d.rng - 1) * uint32(prob)) >> 8)
	bigSplit := split << 8

	var bit bool
	if d.value >= bigSplit {
		bit = true
		d.rng -= split
		d.value -= bigSplit
	} else {
		d.rng = split
	}

	for d.rng < 128 {
		d.value <<= 1
		d.rng <<= 1
		d.bitCount++
		if d.bitCount == 8 {
			d.bitCount = 0
			d.value |= uint32(d.nextByte())
		}
	}
	return bit
}

// ReadFlag returns the next decision at probability 128.
func (d *Decoder) ReadFlag() bool { return d.ReadBool(128) }

// ReadLiteral returns the next n bits, most significant bit first, each at
// probability 128.
func (d *Decoder) ReadLiteral(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		v <<= 1
		if d.ReadFlag() {
			v |= 1
		}
	}
	return v
}

// nextByte returns the next input byte, or zero once the buffer runs out.
func (d *Decoder) nextByte() uint8 {
	if d.r >= len(d.buf) {
		d.eof = true
		return 0
	}
	b := d.buf[d.r]
	d.r++
	return b
}
