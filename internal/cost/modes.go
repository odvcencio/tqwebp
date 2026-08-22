package cost

import "m31labs.dev/tqwebp/internal/predict"

// Key-frame mode probabilities, RFC 6386 sections 11.2 and 11.3. They are
// constants of a key frame: no header field can change them. They price
// the same trees internal/encoder writes (tokens.go), which the decoder
// side of golang.org/x/image/vp8 reads back; the parity test in the
// encoder package proves the pricing matches those writers decision for
// decision on real frames.
const (
	// use16x16Prob gates between a whole-block luma mode (true branch)
	// and the sixteen 4x4 sub-modes of B_PRED (false branch).
	use16x16Prob = 145

	lumaDCvsRestProb = 156
	lumaDCvsVProb    = 163
	lumaHvsTMProb    = 128

	chromaDCProb   = 142
	chromaVProb    = 114
	chromaHvsHProb = 183
)

// BPredFlag prices only the whole-block-versus-submode branch decision of
// a luma mode record: the false branch of use16x16Prob. A B_PRED
// macroblock pays this plus its sixteen sub-mode records, each priced by
// SubModeCost.
func BPredFlag() Cost { return BitCost(use16x16Prob, false) }

// LumaMode prices one whole-block luma mode record, RFC 6386 section
// 11.2: the B_PRED-vs-rest branch taken toward the whole-block subtree,
// then the two further splits the tree makes among DC, V, H, and TM. It
// panics on a mode outside the four whole-block modes.
func LumaMode(m predict.Mode) Cost {
	c := BitCost(use16x16Prob, true)
	switch m {
	case predict.DC:
		c += BitCost(lumaDCvsRestProb, false) + BitCost(lumaDCvsVProb, false)
	case predict.V:
		c += BitCost(lumaDCvsRestProb, false) + BitCost(lumaDCvsVProb, true)
	case predict.H:
		c += BitCost(lumaDCvsRestProb, true) + BitCost(lumaHvsTMProb, false)
	case predict.TM:
		c += BitCost(lumaDCvsRestProb, true) + BitCost(lumaHvsTMProb, true)
	default:
		panic("tqwebp/cost: unknown luma mode")
	}
	return c
}

// ChromaMode prices one chroma mode record, RFC 6386 section 11.3. Both
// chroma planes share the record, exactly as the bitstream carries one
// mode for both. It panics on a mode outside the four block modes.
func ChromaMode(m predict.Mode) Cost {
	switch m {
	case predict.DC:
		return BitCost(chromaDCProb, false)
	case predict.V:
		return BitCost(chromaDCProb, true) + BitCost(chromaVProb, false)
	case predict.H:
		return BitCost(chromaDCProb, true) + BitCost(chromaVProb, true) + BitCost(chromaHvsHProb, false)
	case predict.TM:
		return BitCost(chromaDCProb, true) + BitCost(chromaVProb, true) + BitCost(chromaHvsHProb, true)
	default:
		panic("tqwebp/cost: unknown chroma mode")
	}
}

// SubModeCost prices one 4x4 sub-mode choice against the key-frame
// contextual probabilities selected by the sub-mode directly above in
// the same macroblock column and the one directly to the left in the
// same macroblock row. The decision sequence comes from
// predict.SubModePath -- the same path predict.WriteSubMode codes -- so
// writer and pricer cannot drift apart. It panics on any out-of-range
// argument.
func SubModeCost(aboveCtx, leftCtx, m predict.SubMode) Cost {
	if m < 0 || m >= predict.NumSubModes || aboveCtx < 0 || aboveCtx >= predict.NumSubModes ||
		leftCtx < 0 || leftCtx >= predict.NumSubModes {
		panic("tqwebp/cost: unknown sub-mode")
	}
	row := &predict.KeyFrameSubModeProbs[aboveCtx][leftCtx]
	probIdx, bits, n := predict.SubModePath(m)
	var c Cost
	for i := 0; i < n; i++ {
		c += BitCost(row[probIdx[i]], bits[i])
	}
	return c
}

// SkipCost prices one macroblock skip decision. The skip flag is coded
// against the probability the frame header signalled; skip=true means
// the macroblock carries no coefficients. RFC 6386 section 9.10.
func SkipCost(skipProb uint8, skip bool) Cost { return BitCost(skipProb, skip) }

// SegmentIDCost prices one segment id under the segment map tree of RFC
// 6386 section 9.3, {-0, 2, -1, 4, -2, -3}, whose three nodes read
// treeProbs[0], [1], [2] in root-to-leaf order. No production path
// enables segmentation yet; the primitive exists so a later slice can
// price segmentation-aware choices without rederiving the tree. It
// panics on an id above 3.
func SegmentIDCost(treeProbs *[3]uint8, id uint8) Cost {
	if id > 3 {
		panic("tqwebp/cost: segment id out of range")
	}
	t0 := BitCost(treeProbs[0], id != 0)
	if id == 0 {
		return t0
	}
	t1 := BitCost(treeProbs[1], id != 1)
	if id == 1 {
		return t0 + t1
	}
	return t0 + t1 + BitCost(treeProbs[2], id != 2)
}
