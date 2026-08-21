package encoder

import (
	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/token"
)

// Key-frame mode probabilities, RFC 6386 sections 11.2 and 11.3. They are
// fixed for a key frame: no frame header field can change them.
const (
	// use16x16Prob gates the choice between a whole-block luma mode and
	// the 4x4 sub-mode set. The public path still takes the whole-block
	// branch; the B_PRED integration harness also exercises the zero branch.
	use16x16Prob = 145

	lumaDCvsRestProb = 156
	lumaDCvsVProb    = 163
	lumaHvsTMProb    = 128

	chromaDCProb = 142
	chromaVProb  = 114
	chromaHProb  = 183
)

// writeLumaMode writes the whole-block luma mode of one macroblock. The
// key-frame tree of RFC 6386 section 11.2 shapes the decisions.
func writeLumaMode(enc *boolenc.Encoder, m predict.Mode) {
	enc.WriteBool(use16x16Prob, true)
	switch m {
	case predict.DC:
		enc.WriteBool(lumaDCvsRestProb, false)
		enc.WriteBool(lumaDCvsVProb, false)
	case predict.V:
		enc.WriteBool(lumaDCvsRestProb, false)
		enc.WriteBool(lumaDCvsVProb, true)
	case predict.H:
		enc.WriteBool(lumaDCvsRestProb, true)
		enc.WriteBool(lumaHvsTMProb, false)
	case predict.TM:
		enc.WriteBool(lumaDCvsRestProb, true)
		enc.WriteBool(lumaHvsTMProb, true)
	default:
		panic("tqwebp: unknown luma mode")
	}
}

// writeChromaMode writes the chroma mode of one macroblock, RFC 6386
// section 11.3.
func writeChromaMode(enc *boolenc.Encoder, m predict.Mode) {
	switch m {
	case predict.DC:
		enc.WriteBool(chromaDCProb, false)
	case predict.V:
		enc.WriteBool(chromaDCProb, true)
		enc.WriteBool(chromaVProb, false)
	case predict.H:
		enc.WriteBool(chromaDCProb, true)
		enc.WriteBool(chromaVProb, true)
		enc.WriteBool(chromaHProb, false)
	case predict.TM:
		enc.WriteBool(chromaDCProb, true)
		enc.WriteBool(chromaVProb, true)
		enc.WriteBool(chromaHProb, true)
	default:
		panic("tqwebp: unknown chroma mode")
	}
}

// mbContext holds the coefficient contexts one macroblock hands to its
// neighbours. The decoder keeps the same state in its own left and above
// records, so the encoder must update it in the same order.
type mbContext struct {
	// y2 is 1 when the macroblock's Walsh-Hadamard block carried
	// coefficients.
	y2 uint8
	// luma holds one flag per row for the left context, and one flag per
	// column for the above context.
	luma [4]uint8
	// u and v hold the same, two entries each, for the chroma planes.
	u [2]uint8
	v [2]uint8
}

// writeTokens codes every macroblock's coefficients into the single token
// partition, in raster order, and returns the finished partition.
func (e *encoder) writeTokens() []byte {
	enc := boolenc.New(4096 + len(e.mbs)*16)
	w := token.NewWriter(enc, &token.DefaultProbs)

	above := make([]mbContext, e.mbw)
	var left mbContext

	for mby := 0; mby < e.mbh; mby++ {
		left = mbContext{}
		for mbx := 0; mbx < e.mbw; mbx++ {
			mb := &e.mbs[mby*e.mbw+mbx]
			up := &above[mbx]

			if mb.skip {
				// A skipped macroblock codes no coefficient tokens. The
				// decoder clears its ordinary luma and chroma contexts. It
				// clears the separate Y2 context only for a 16x16-predicted
				// macroblock; a B_PRED macroblock has no Y2 syntax and leaves
				// that state untouched.
				if !mb.useBPred {
					left.y2 = 0
					up.y2 = 0
				}
				clearBlockContexts(&left)
				clearBlockContexts(up)
				continue
			}

			if mb.useBPred {
				// B_PRED has no Walsh-Hadamard block. Every luma block
				// carries its own direct-current value and starts at scan
				// position zero. The independent Y2 contexts remain exactly
				// as the preceding 16x16 macroblocks left them.
				e.writeLumaTokens(w, mb, token.YWithDC, 0, &left.luma, &up.luma)
			} else {
				// A 16x16-predicted macroblock writes the Walsh-Hadamard
				// block first.
				nz := btou(w.WriteBlock(token.Y2, int(left.y2+up.y2), 0, &mb.levels[blockY2]))
				left.y2, up.y2 = nz, nz

				// Its sixteen luma blocks begin at coefficient 1, because
				// their direct-current values travelled through Y2.
				e.writeLumaTokens(w, mb, token.YAfterY2, 1, &left.luma, &up.luma)
			}

			// Then the two chroma planes, four blocks each.
			e.writeChromaTokens(w, mb, blockU, &left.u, &up.u)
			e.writeChromaTokens(w, mb, blockV, &left.v, &up.v)
		}
	}
	return enc.Finish()
}

// writeLumaTokens codes the sixteen luma blocks of one macroblock and updates
// the ordinary luma neighbour contexts. plane and first distinguish the
// B_PRED path (YWithDC, 0) from the 16x16 path (YAfterY2, 1).
func (e *encoder) writeLumaTokens(w *token.Writer, mb *macroblock, plane, first int, left, up *[4]uint8) {
	for y := 0; y < 4; y++ {
		nz := left[y]
		for x := 0; x < 4; x++ {
			ctx := int(nz + up[x])
			nz = btou(w.WriteBlock(plane, ctx, first, &mb.levels[blockLuma+4*y+x]))
			up[x] = nz
		}
		left[y] = nz
	}
}

// clearBlockContexts clears the coefficient contexts shared by every
// macroblock type while deliberately retaining c.y2.
func clearBlockContexts(c *mbContext) {
	c.luma = [4]uint8{}
	c.u = [2]uint8{}
	c.v = [2]uint8{}
}

// writeChromaTokens codes the four blocks of one chroma plane and updates
// its share of the neighbour contexts.
func (e *encoder) writeChromaTokens(w *token.Writer, mb *macroblock, base int, left, up *[2]uint8) {
	for y := 0; y < 2; y++ {
		nz := left[y]
		for x := 0; x < 2; x++ {
			ctx := int(nz + up[x])
			nz = btou(w.WriteBlock(token.UV, ctx, 0, &mb.levels[base+2*y+x]))
			up[x] = nz
		}
		left[y] = nz
	}
}

func btou(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
