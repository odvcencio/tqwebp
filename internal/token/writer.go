package token

import "m31labs.dev/tqwebp/internal/boolenc"

// Observer receives one call per boolean branch a WriteBlock codes.
// Slice 6A attaches it during the dry histogram pass that measures the
// frame's real token statistics without touching the coded output; the
// production path runs with no observer attached.
//
// Implementations receive the branch coordinates (plane 0 to NumPlanes-1,
// band 0 to NumBands-1, context 0 to NumContexts-1, node 0 to
// NumProbs-1) and the coded bit, where true is the taken side exactly as
// boolenc.WriteBool defines it. The writer calls the observer in coding
// order, immediately before emitting each branch.
type Observer interface {
	ObserveBranch(plane, band, ctx, node int, bit bool)
}

// Writer codes quantized coefficient blocks into a boolean-coded
// partition. Create one per token partition.
type Writer struct {
	enc      *boolenc.Encoder
	probs    *Probs
	observer Observer
}

// NewWriter returns a Writer that codes into enc against the probability
// table probs. The table must be the one the frame header signalled.
func NewWriter(enc *boolenc.Encoder, probs *Probs) *Writer {
	return &Writer{enc: enc, probs: probs}
}

// SetObserver attaches obs to w, replacing any earlier observer. A nil
// obs detaches the current one. Observation is passive: it never changes
// what WriteBlock codes.
func (w *Writer) SetObserver(obs Observer) { w.observer = obs }

// branch codes one boolean decision and reports it to the observer.
// Keeping the report beside the emission makes the histogram exact by
// construction: whatever the writer codes, the observer sees.
func (w *Writer) branch(plane, band, ctx, node int, prob uint8, bit bool) {
	if w.observer != nil {
		w.observer.ObserveBranch(plane, band, ctx, node, bit)
	}
	w.enc.WriteBool(prob, bit)
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
	band := int(Bands[n])
	ctx0 := ctx
	p := &planeProbs[band][ctx0]

	if last < 0 {
		// End of block before any coefficient: the block is empty.
		w.branch(plane, band, ctx0, 0, p[0], false)
		return false
	}
	w.branch(plane, band, ctx0, 0, p[0], true)

	for n < 16 {
		v := levels[n]
		if v == 0 {
			w.branch(plane, band, ctx0, 1, p[1], false)
			n++
			band = int(Bands[n])
			ctx0 = 0
			p = &planeProbs[band][ctx0]
			continue
		}

		w.branch(plane, band, ctx0, 1, p[1], true)
		mag := v
		if mag < 0 {
			mag = -mag
		}
		if mag > MaxLevel {
			panic("tqwebp/token: level exceeds the largest codable magnitude")
		}

		nextCtx := 2
		if mag == 1 {
			w.branch(plane, band, ctx0, 2, p[2], false)
			nextCtx = 1
		} else {
			w.branch(plane, band, ctx0, 2, p[2], true)
			w.writeMagnitude(plane, band, ctx0, p, int(mag))
		}
		w.enc.WriteBool(128, v < 0)

		n++
		if n == 16 {
			return true
		}
		band = int(Bands[n])
		ctx0 = nextCtx
		p = &planeProbs[band][ctx0]
		if n > last {
			// No coefficient is left: end the block.
			w.branch(plane, band, ctx0, 0, p[0], false)
			return true
		}
		w.branch(plane, band, ctx0, 0, p[0], true)
	}
	return true
}

// writeMagnitude codes a magnitude of two or more, RFC 6386 section 13.2.
// The tree splits at 2, at 3 or 4, at the two small categories, and then
// at the four large categories, each of which carries its magnitude as
// extra bits with their own probabilities. The branch probabilities it
// reads from p carry the node indices 3 to 10 of the token tree; the
// fixed extra-bit probabilities that follow are normative constants with
// no header slot, so only the p-branches are reported to the observer.
func (w *Writer) writeMagnitude(plane, band, ctx int, p *[NumProbs]uint8, mag int) {
	switch {
	case mag <= 4:
		w.branch(plane, band, ctx, 3, p[3], false)
		if mag == 2 {
			w.branch(plane, band, ctx, 4, p[4], false)
			return
		}
		w.branch(plane, band, ctx, 4, p[4], true)
		w.branch(plane, band, ctx, 5, p[5], mag == 4)

	case mag <= 10:
		w.branch(plane, band, ctx, 3, p[3], true)
		w.branch(plane, band, ctx, 6, p[6], false)
		if mag <= 6 {
			// Category 1 covers 5 and 6.
			w.branch(plane, band, ctx, 7, p[7], false)
			w.enc.WriteBool(cat1Prob, mag == 6)
			return
		}
		// Category 2 covers 7 to 10.
		w.branch(plane, band, ctx, 7, p[7], true)
		w.enc.WriteBool(cat2Prob0, (mag-7)>>1 == 1)
		w.enc.WriteBool(cat2Prob1, (mag-7)&1 == 1)

	default:
		// Categories 3 to 6 cover 11 to 2114.
		cat := categoryOf(mag)
		w.branch(plane, band, ctx, 3, p[3], true)
		w.branch(plane, band, ctx, 6, p[6], true)
		b1 := cat >> 1
		b0 := cat & 1
		w.branch(plane, band, ctx, 8, p[8], b1 == 1)
		w.branch(plane, band, ctx, 9+b1, p[9+b1], b0 == 1)

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

// WriteProbs writes the section 13.4 coefficient-probability update
// stream that makes a decoder's running table equal table. A key frame
// decodes from DefaultProbs, so an entry equal to the default needs one
// "no update" gate decision and any other entry needs its gate decision
// plus an 8-bit literal carrying the new probability, in exactly the
// plane, band, context, node order the section fixes. A nil table keeps
// the defaults and codes byte for byte what WriteProbUpdates writes.
func WriteProbs(enc *boolenc.Encoder, table *Probs) {
	for i := 0; i < NumPlanes; i++ {
		for j := 0; j < NumBands; j++ {
			for k := 0; k < NumContexts; k++ {
				for l := 0; l < NumProbs; l++ {
					p := DefaultProbs[i][j][k][l]
					if table != nil {
						p = table[i][j][k][l]
					}
					if p == DefaultProbs[i][j][k][l] {
						enc.WriteBool(UpdateProbs[i][j][k][l], false)
						continue
					}
					enc.WriteBool(UpdateProbs[i][j][k][l], true)
					enc.WriteLiteral(uint32(p), 8)
				}
			}
		}
	}
}

// WriteProbUpdates writes one "no update" decision per coefficient
// probability, which is what a frame that keeps the default table must
// send (RFC 6386 section 13.4). It is WriteProbs with the default table.
func WriteProbUpdates(enc *boolenc.Encoder) {
	WriteProbs(enc, nil)
}
