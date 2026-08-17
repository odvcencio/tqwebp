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
	// the 4x4 sub-mode set. This release always takes the whole-block
	// branch, which the decoder reads as a one.
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
				// A skipped macroblock codes nothing, and the decoder
				// clears both contexts. Its blocks are all empty, so
				// clearing matches what the blocks would have said.
				left = mbContext{}
				*up = mbContext{}
				continue
			}

			// The Walsh-Hadamard block comes first.
			nz := btou(w.WriteBlock(token.Y2, int(left.y2+up.y2), 0, &mb.levels[blockY2]))
			left.y2, up.y2 = nz, nz

			// Then the sixteen luma blocks, in raster order. Each one
			// starts at coefficient 1, because its direct-current value
			// travelled in the block above.
			for y := 0; y < 4; y++ {
				nz := left.luma[y]
				for x := 0; x < 4; x++ {
					ctx := int(nz + up.luma[x])
					nz = btou(w.WriteBlock(token.YAfterY2, ctx, 1, &mb.levels[blockLuma+4*y+x]))
					up.luma[x] = nz
				}
				left.luma[y] = nz
			}

			// Then the two chroma planes, four blocks each.
			e.writeChromaTokens(w, mb, blockU, &left.u, &up.u)
			e.writeChromaTokens(w, mb, blockV, &left.v, &up.v)
		}
	}
	return enc.Finish()
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
