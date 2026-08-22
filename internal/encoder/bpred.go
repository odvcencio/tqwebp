package encoder

import (
	"m31labs.dev/turboquant/blockdsp"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/predict"
)

// This file is work package WP-2 slice 2A: the forced B_PRED macroblock
// path. Every macroblock an encoder with forceBPred set codes its luma as
// sixteen independent 4x4 blocks -- RFC 6386 chapter 12's intra 4x4
// predictions -- instead of one 16x16 block behind a Walsh-Hadamard
// transform. Nothing here chooses between B_PRED and the whole-block
// modes yet: production selection keeps walking the four whole-block
// modes, and this file runs only when the caller forces it on.
//
// The rules mirror what a decoder does, because the repository's
// exact-match gate compares the encoder's picture with
// golang.org/x/image/vp8's decode of the same bytes:
//
//   - The sixteen blocks are decided and reconstructed in raster order,
//     and each block predicts from samples its predecessors have already
//     written into the reconstruction plane.
//   - A B_PRED macroblock owns no Y2 block. Each 4x4 block carries its
//     own direct-current value, quantized and coded in the YWithDC
//     coefficient plane starting at scan position 0.
//   - The sub-mode record codes against contextual probabilities keyed
//     by the sub-mode directly above in the same macroblock column and
//     the sub-mode directly to the left in the same macroblock row, both
//     seeded with B_DC_PRED wherever no real sub-block sits.

// codeLumaBPred codes one macroblock's luma as sixteen 4x4 blocks. For
// each block, in raster order, it picks the ten-way sub-mode with the
// smallest sum of squared errors against the source, transforms and
// quantizes the residual without a Y2 detour, and writes the
// reconstruction back before the next block reads its neighbours.
func (e *encoder) codeLumaBPred(mbx, mby int, mb *macroblock) {
	paddedWidth := ((e.src.Width + 15) / 16) * 16

	var nb predict.SubNeighbors
	var pred [16]uint8

	for b := 0; b < 16; b++ {
		x0, y0 := mbx*16+(b%4)*4, mby*16+(b/4)*4
		predict.GatherSubNeighbors(&nb, e.rec.Y, e.rec.YStride, x0, y0, paddedWidth)
		src := e.src.Y[y0*e.src.YStride+x0:]

		// Ten-way decision against the source. Ties stay with the
		// lowest sub-mode number, which keeps the choice deterministic.
		bestSSE := int32(-1)
		var best predict.SubMode
		for m := predict.SubMode(0); m < predict.NumSubModes; m++ {
			predict.PredictSub(pred[:], 4, m, &nb)
			sse := blockdsp.SSE4x4(src, e.src.YStride, pred[:], 4)
			if bestSSE < 0 || sse < bestSSE {
				bestSSE = sse
				best = m
			}
		}
		mb.subModes[b] = best
		predict.PredictSub(pred[:], 4, best, &nb)

		var residual [16]int16
		for y := 0; y < 4; y++ {
			srcRow := src[y*e.src.YStride:]
			predRow := pred[y*4:]
			for x := 0; x < 4; x++ {
				residual[y*4+x] = int16(srcRow[x]) - int16(predRow[x])
			}
		}
		coeff := blockdsp.FDCT4x4(&residual)
		levels := quantizeBlock(&coeff, e.q.Y1)
		mb.levels[blockLuma+b] = toScanOrder(&levels)
		mb.nz[blockLuma+b] = anyNonZero(&levels, 0)

		dequant := blockdsp.DequantizeBlock(&levels, e.q.Y1.DC, e.q.Y1.AC)
		residualOut := blockdsp.IDCT4x4(&dequant)
		e.reconstruct(e.rec.Y, e.rec.YStride, x0, y0, pred[:], 4, 0, 0, &residualOut)
	}

	// A B_PRED macroblock owns no Walsh-Hadamard block: its record stays
	// empty and not-nonzero, so the token writer skips the Y2 block and
	// the skip analysis sees nothing extra.
	mb.levels[blockY2] = [16]int16{}
	mb.nz[blockY2] = false
}

// writeBPredModes writes one B_PRED macroblock's luma record: the
// whole-block flag that takes the sub-mode branch, then the sixteen
// sub-modes in raster order, each coded against the key-frame contextual
// probabilities of the block above and the block to the left.
//
// above holds one entry per macroblock column and left one per
// macroblock row, exactly as the decoder's own per-column and per-row
// records do. Coding updates both in place: a sub-mode becomes the above
// context of the block one row down in its column and the left context
// of the next block in its row.
func writeBPredModes(enc *boolenc.Encoder, mb *macroblock, above, left *[4]predict.SubMode) {
	enc.WriteBool(use16x16Prob, false)
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			m := mb.subModes[4*j+i]
			predict.WriteSubMode(enc, above[i], left[j], m)
			above[i] = m
			left[j] = m
		}
	}
}

// subModeContextOf returns the sub-mode value a decoder records for a
// whole-block luma mode. A 16x16 macroblock fills all four above and
// left entries with one mode, and the decoder stores it in the ten-value
// sub-mode numbering: DC_PRED lands on B_DC_PRED, V_PRED on B_VE_PRED,
// H_PRED on B_HE_PRED, and TM_PRED on B_TM_PRED.
func subModeContextOf(m predict.Mode) predict.SubMode {
	switch m {
	case predict.DC:
		return predict.BDC
	case predict.V:
		return predict.BVE
	case predict.H:
		return predict.BHE
	case predict.TM:
		return predict.BTM
	}
	panic("tqwebp: unknown luma mode")
}
