package token

import "m31labs.dev/tqwebp/internal/boolenc"

// Writer codes quantized coefficient blocks into a boolean-coded
// partition. Create one per token partition.
type Writer struct {
	enc   *boolenc.Encoder
	probs *Probs
}

// NewWriter returns a Writer that codes into enc against the probability
// table probs. The table must be the one the frame header signalled.
func NewWriter(enc *boolenc.Encoder, probs *Probs) *Writer {
	return &Writer{enc: enc, probs: probs}
}

// WriteBlock codes one 4x4 block and reports whether the block carried
// any coefficient. The report becomes the neighbour context of the blocks
// below and to the right, exactly as the decoder's own flag does.
//
// Argument levels holds the quantized levels in scan (zigzag) order.
// Argument first is the scan position coding starts at: 1 for a luma
// block whose direct-current value travels in the Y2 block, 0 everywhere
// else. Argument ctx is 0, 1, or 2, and counts how many of the block
// above and the block to the left carried coefficients.
func (w *Writer) WriteBlock(plane int, ctx int, first int, levels *[16]int16) bool {
	last := -1
	for i := 15; i >= first; i-- {
		if levels[i] != 0 {
			last = i
			break
		}
	}

	planeProbs := &w.probs[plane]
	n := first
	p := &planeProbs[Bands[n]][ctx]

	if last < 0 {
		// End of block before any coefficient: the block is empty.
		w.enc.WriteBool(p[0], false)
		return false
	}
	w.enc.WriteBool(p[0], true)

	for n < 16 {
		v := levels[n]
		if v == 0 {
			w.enc.WriteBool(p[1], false)
			n++
			p = &planeProbs[Bands[n]][0]
			continue
		}

		w.enc.WriteBool(p[1], true)
		mag := int(v)
		if mag < 0 {
			mag = -mag
		}
		if mag > MaxLevel {
			panic("tqwebp/token: level exceeds the largest codable magnitude")
		}

		nextCtx := 2
		if mag == 1 {
			w.enc.WriteBool(p[2], false)
			nextCtx = 1
		} else {
			w.enc.WriteBool(p[2], true)
			w.writeMagnitude(p, mag)
		}
		w.enc.WriteBool(128, v < 0)

		n++
		if n == 16 {
			return true
		}
		p = &planeProbs[Bands[n]][nextCtx]
		if n > last {
			// No coefficient is left: end the block.
			w.enc.WriteBool(p[0], false)
			return true
		}
		w.enc.WriteBool(p[0], true)
	}
	return true
}

// writeMagnitude codes a magnitude of two or more, RFC 6386 section 13.2.
// The tree splits at 2, at 3 or 4, at the two small categories, and then
// at the four large categories, each of which carries its magnitude as
// extra bits with their own probabilities.
func (w *Writer) writeMagnitude(p *[NumProbs]uint8, mag int) {
	switch {
	case mag <= 4:
		w.enc.WriteBool(p[3], false)
		if mag == 2 {
			w.enc.WriteBool(p[4], false)
			return
		}
		w.enc.WriteBool(p[4], true)
		w.enc.WriteBool(p[5], mag == 4)

	case mag <= 10:
		w.enc.WriteBool(p[3], true)
		w.enc.WriteBool(p[6], false)
		if mag <= 6 {
			// Category 1 covers 5 and 6.
			w.enc.WriteBool(p[7], false)
			w.enc.WriteBool(cat1Prob, mag == 6)
			return
		}
		// Category 2 covers 7 to 10.
		w.enc.WriteBool(p[7], true)
		w.enc.WriteBool(cat2Prob0, (mag-7)>>1 == 1)
		w.enc.WriteBool(cat2Prob1, (mag-7)&1 == 1)

	default:
		// Categories 3 to 6 cover 11 to 2114.
		cat := categoryOf(mag)
		w.enc.WriteBool(p[3], true)
		w.enc.WriteBool(p[6], true)
		b1 := cat >> 1
		b0 := cat & 1
		w.enc.WriteBool(p[8], b1 == 1)
		w.enc.WriteBool(p[9+b1], b0 == 1)

		bits := extraBits[cat]
		rest := mag - categoryBase(cat)
		for i := 0; i < bits; i++ {
			w.enc.WriteBool(ExtraProbs[cat][i], rest>>(bits-1-i)&1 == 1)
		}
	}
}

// categoryBase returns the smallest magnitude of a large category.
func categoryBase(cat int) int { return 3 + (8 << uint(cat)) }

// categoryOf returns the large category, 0 to 3, that holds mag.
func categoryOf(mag int) int {
	for cat := 0; cat < 4; cat++ {
		if mag < categoryBase(cat)+(1<<uint(extraBits[cat])) {
			return cat
		}
	}
	return 3
}

// WriteProbUpdates writes one "no update" decision per coefficient
// probability, which is what a frame that keeps the default table must
// send (RFC 6386 section 13.4). WP-2 replaces this with real updates.
func WriteProbUpdates(enc *boolenc.Encoder) {
	for i := 0; i < NumPlanes; i++ {
		for j := 0; j < NumBands; j++ {
			for k := 0; k < NumContexts; k++ {
				for l := 0; l < NumProbs; l++ {
					WriteProbUpdate(enc, UpdateProbs[i][j][k][l], 0, false)
				}
			}
		}
	}
}

// WriteProbUpdate writes one coefficient-probability update gate and, when
// update is true, the replacement probability as an eight-bit literal. The
// literal's value changes the model but not its encoded Q8 cost.
func WriteProbUpdate(enc *boolenc.Encoder, updateProb, newProb uint8, update bool) {
	enc.WriteBool(updateProb, update)
	if !update {
		return
	}
	for bit := 7; bit >= 0; bit-- {
		enc.WriteBool(128, newProb>>uint(bit)&1 != 0)
	}
}
