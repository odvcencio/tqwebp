package encoder

// This file is work package WP-2 slice 5B: the coefficient-candidate
// refinement wired into the B_PRED rate-distortion walk of rd_select.go
// behind the Method>=6 effort boundary. Slice 5C layers a bounded
// backward coefficient trellis (coeff_trellis.go) after this search
// from the same Method 6 boundary upward; both run only through
// refineBlockLevels below. Method 5 keeps the retained-
// level path byte for byte; production encodes below Method 6 never
// reach this file, so their bytes cannot move.
//
// After the walk's initial transform, quantization, and scan conversion,
// each coded YWithDC block hands its retained scan levels to the slice
// 5A helper searchCoeffCandidates. The helper enumerates a bounded,
// deterministic set of nearby level vectors and prices each as
//
//	distortion(candidate)*256 + lambda * exactSyntaxRate
//
// where rate is cost.BlockCost under the block's entering neighbour
// context and distortion comes from a caller-supplied callback that
// rebuilds exactly what a decoder would: scan levels to raster order,
// dequantization with the frame's Y1 factors, inverse transform, add to
// the already-selected 4x4 predictor, clamp to samples, squared error
// against the source. The callback never touches the reconstruction
// plane; only the winning levels are converted back to raster,
// reconstructed once into the live plane, and charged their exact token
// cost, so every later reader -- the nz flags, the threaded neighbour
// contexts, the per-block pruning bounds, and finally the bitstream --
// sees one consistent coding decision.
//
// # Determinism
//
// The helper's enumeration and tie-breaking are fixed-index integer
// walks (coeff_opt.go); the distortion callback reads only arrays that
// do not change during the search. Counters accumulate in raster block
// order inside plain struct fields, so results are identical on every
// platform at every GOMAXPROCS.

import (
	"m31labs.dev/turboquant/blockdsp"

	"m31labs.dev/tqwebp/internal/token"
)

// coeffSearchAllowed reports whether this encode's effort level may
// refine retained coefficient levels with the candidate search. The
// boundary pins the slice: Method 5 and below keep the retained-level
// path byte for byte, rdNoPrune-style test overrides aside.
func (e *encoder) coeffSearchAllowed() bool {
	return e.cfg.Method >= minCoeffSearchMethod && !e.rdCoeffOptOff
}

// minCoeffSearchMethod is the lowest effort level whose B_PRED walk may
// refine a block's retained levels through searchCoeffCandidates.
const minCoeffSearchMethod = 6

// minCoeffTrellisMethod is the lowest effort level whose refinement
// runs the slice 5C trellis. It coincides with the candidate-search
// boundary because Method 6 is the highest publicly supported effort:
// webp.go validates Methods 0 to 6, so any higher gate would leave the
// trellis unreachable from every valid public encode. At Method 6 the
// trellis may move winners; at Method 5 and below neither layer runs.
const minCoeffTrellisMethod = 6

// coeffTrellisAllowed reports whether this encode's refinement may run
// the slice 5C bounded trellis after the candidate search. It honors
// both test overrides, so disabling either layer leaves the other
// intact.
func (e *encoder) coeffTrellisAllowed() bool {
	return e.cfg.Method >= minCoeffTrellisMethod && !e.rdCoeffOptOff && !e.rdCoeffTrellisOff
}

// spatialDistortionFor prices candidate scan levels against the source
// without touching any plane state: levels go back to raster order, are
// dequantized with the frame's Y1 factors, inverted, added to the fixed
// predictor, clamped to the sample range, and differenced against src.
// It is pure over its captured inputs, which the search requires.
func (e *encoder) spatialDistortionFor(src []uint8, srcStride int, pred []uint8) spatialDistortionFn {
	return func(levels *[16]int16) int64 {
		raster := fromScanOrder(levels)
		dequant := blockdsp.DequantizeBlock(&raster, e.q.Y1.DC, e.q.Y1.AC)
		residual := blockdsp.IDCT4x4(&dequant)
		var sse int64
		for y := 0; y < 4; y++ {
			srcRow := src[y*srcStride:]
			predRow := pred[y*4:]
			resRow := residual[y*4:]
			for x := 0; x < 4; x++ {
				v := int32(predRow[x]) + int32(resRow[x])
				if v < 0 {
					v = 0
				} else if v > 255 {
					v = 255
				}
				d := int32(srcRow[x]) - v
				sse += int64(d * d)
			}
		}
		return sse
	}
}

// refineBlockLevels runs the candidate search over the retained scan
// levels of one coded block and returns the winning levels together
// with the counters the frame accumulates. The entering neighbour
// context and start position are exactly the ones the block's tokens
// are priced and later written under. The caller converts the winner
// back to raster order and reconstructs it through rd_select.go's
// shared path, so losing candidates never touch any plane state.
//
// When the slice 5C trellis is allowed, its bounded backward pass runs
// after the candidate search over the same retained levels, and the
// final winner comes from exact full-objective scoring over the
// canonically ordered list -- retained levels, candidate-search
// winner, trellis path -- so the result can never score worse than
// what the 5B search alone would have kept, and strict ties keep that
// earlier baseline.
func (e *encoder) refineBlockLevels(ctx int, levels *[16]int16, dist spatialDistortionFn) ([16]int16, coeffSearchStats) {
	return e.refineBlockLevelsWithProbs(ctx, levels, dist, &token.DefaultProbs)
}

func (e *encoder) refineBlockLevelsWithProbs(ctx int, levels *[16]int16, dist spatialDistortionFn, probs *token.Probs) ([16]int16, coeffSearchStats) {
	retained := *levels
	winner, stats := searchCoeffCandidatesWithProbs(token.YWithDC, ctx, 0, probs, levels, e.lambda, dist)
	e.rd.CoeffBlocksSearched++
	e.rd.CoeffCandidatesScored += int64(stats.CandidatesScored)
	if stats.Improved {
		e.rd.CoeffBlocksChanged++
	}
	if e.coeffTrellisAllowed() {
		s5b := winner
		final, tstats := runCoeffTrellisWithProbs(trellisWholeScan, token.YWithDC, ctx, &retained,
			e.lambda, dist, probs, retained, s5b)
		if final != s5b {
			e.rd.TrellisBlocksChanged++
		}
		e.rd.TrellisBlocksSearched++
		e.rd.TrellisProbesScored += int64(tstats.Probes)
		e.rd.TrellisEdgesRelaxed += int64(tstats.EdgesRelaxed)
		winner = final
		stats.BestScore = tstats.BestScore
	}
	return winner, stats
}
