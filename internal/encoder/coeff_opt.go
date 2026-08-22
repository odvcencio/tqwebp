package encoder

// This file is work package WP-2 slice 5A: the reusable coefficient-
// search foundation that a later slice wires into the rate-distortion
// machinery of rd_select.go. Nothing in the production encode path
// calls it yet; the existing writers keep deciding and emitting exactly
// what they always did, so every encoded file stays byte for byte what
// earlier releases wrote.
//
// The helper answers one question per coded 4x4 block: given the
// quantized levels a quantizer produced, which nearby level vector is
// worth coding instead? It enumerates a bounded, deterministic set of
// candidates around the initial vector, prices each as
//
//	distortion(candidate)*256 + lambda * exactSyntaxRate
//
// -- the same scale rd_select.go already compares macroblock candidates
// on -- and returns the winner. Distortion is caller-supplied, because
// only the caller can reconstruct what a decoder would build from a
// candidate; rate is exact, from cost.BlockCost under the default
// probability table.
//
// # Determinism
//
// Enumeration walks fixed index orders over fixed-size arrays,
// deduplication keeps the first ordinal, and scoring compares strictly
// smaller values only. There is no floating point, no map, and no
// shared mutable state, so results are identical on every platform at
// every GOMAXPROCS.

import (
	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/token"
)

// maxCoeffCandidates hard-bounds how many distinct level vectors the
// candidate enumeration can produce for one 4x4 block. The bound is a
// property of the enumeration shape, not of its input:
//
//	1 retain
//	+ 2 mutations (one step toward zero, zeroed outright) per
//	  initially nonzero scan position -- at most 2*16 = 32, reached
//	  only when all sixteen positions start nonzero
//	+ 1 suffix-zero vector per scan cut 0..15 -- at most 16
//
// so the worst case is 1 + 32 + 16 = 49 offers. Deduplication only
// shrinks the set. Whenever any position starts nonzero, the cut at
// that last nonzero position repeats its zeroing mutation, so the
// enumeration yields at most 1 + 32 + 15 = 48 unique candidates; a
// unit magnitude merges one position's two mutation offers into one,
// and the all-zero input collapses all sixteen cuts onto the retain.
// Offers therefore never exceed 49 and unique candidates never
// exceed 48, strictly below the bound. The panic below documents that
// any future widening of the enumeration must revisit this constant
// deliberately rather than silently grow the search.
const maxCoeffCandidates = 49

// spatialDistortionFn prices one candidate block against the source in
// squared-error units, the units cost.Lambda trades bits against. The
// function must be deterministic and pure: it observes only the
// candidate levels handed to it and returns the same value for the
// same input on every call. Each surviving candidate is scored exactly
// once, but the winner guarantee rests on reproducibility.
type spatialDistortionFn func(levels *[16]int16) int64

// coeffSearchStats records the bounded, deterministic counters of one
// searchCoeffCandidates pass. They are plain integers updated in
// enumeration order, identical at every GOMAXPROCS, and exist so the
// later integration slice can wire the search into production paths
// with observable bookkeeping -- and the winning score -- from day
// one.
type coeffSearchStats struct {
	// CandidatesEnumerated counts every candidate offered to the
	// list before deduplication: the retain, two mutations per
	// initially nonzero scan position, and one suffix-zero vector
	// per scan cut 0..15, cut 0 clearing every coefficient. At most
	// 1 + 32 + 16 = 49.
	CandidatesEnumerated int
	// CandidatesUnique counts the candidates that survived stable
	// deduplication. It never exceeds maxCoeffCandidates.
	CandidatesUnique int
	// CandidatesScored equals CandidatesUnique: the search prices
	// every unique candidate exactly once and prunes nothing.
	CandidatesScored int
	// WinningOrdinal is the stable ordinal of the winner within the
	// deduplicated list; 0 means the retained input levels won or
	// tied.
	WinningOrdinal int
	// Improved reports whether the winning levels differ from the
	// retained input levels.
	Improved bool
	// BestScore is the exact winning score,
	// distortion(winner)*256 + lambda*rate(winner), on the same
	// scale the caller compares other blocks on.
	BestScore int64
}

// enumerateCoeffCandidates builds the deduplicated candidate list for
// initial levels in stable construction order and reports how many
// offers deduplication absorbed.
//
// Order is fixed and independent of the input magnitudes: ordinal 0
// retains the initial levels untouched; then, walking scan positions
// front to back, every initially nonzero position contributes up to
// two mutations, first moving its magnitude exactly one toward zero
// and then zeroing it outright; finally every scan cut c in 0..15
// contributes the vector whose positions >= c are cleared and whose
// positions below c keep their initial values. Cut 0 clears every
// coefficient: the all-zero vector, which codes end-of-block before
// the first coded coefficient, so any input with a nonzero position
// always offers an EOB-at-start candidate. Identical [16]int16
// vectors collapse to their first ordinal, so the returned slice is
// sorted by construction order. The result never exceeds
// maxCoeffCandidates entries.
func enumerateCoeffCandidates(initial *[16]int16) (cands [][16]int16, enumerated int) {
	cands = make([][16]int16, 0, maxCoeffCandidates)
	add := func(v [16]int16) {
		enumerated++
		for i := range cands {
			if cands[i] == v {
				return // duplicate: keep the earlier ordinal
			}
		}
		cands = append(cands, v)
	}

	retained := *initial
	add(retained)

	for i := 0; i < 16; i++ {
		if initial[i] == 0 {
			continue
		}
		oneStep := retained
		if oneStep[i] > 0 {
			oneStep[i]--
		} else {
			oneStep[i]++
		}
		add(oneStep)

		zeroed := retained
		zeroed[i] = 0
		add(zeroed)
	}

	// Scan cuts c = 0..15 in ascending order: position c and above
	// are cleared, positions below keep their initial values. Cut 0
	// clears every coefficient, the all-zero vector that ends the
	// block before its first coded coefficient. Cuts that leave the
	// vector unchanged, and cuts matching an earlier candidate --
	// cut 15 clearing a lone last nonzero, for instance -- are
	// absorbed by add() while preserving order.
	for c := 0; c < 16; c++ {
		suffix := retained
		for j := c; j < 16; j++ {
			suffix[j] = 0
		}
		add(suffix)
	}

	if len(cands) > maxCoeffCandidates {
		panic("encoder: coefficient candidate bound breached")
	}
	return cands, enumerated
}

// searchCoeffCandidates enumerates the bounded candidate set around
// initial levels, scores every unique candidate under the default
// probability table, and returns the winning levels plus the counters
// and score of the pass. It delegates to searchCoeffCandidatesWithProbs
// with &token.DefaultProbs.
//
// Scoring compares strictly smaller values of
//
//	distortion(candidate)*256 + lambda*int64(cost.BlockCost(plane, ctx, first, candidate, &token.DefaultProbs))
//
// only, so a tie keeps the earliest candidate -- in particular the
// retained input wins every tie it reaches. Lambda may be zero or
// negative without breaking the ordering guarantees; distortion comes
// entirely from the caller-supplied function, which the search never
// second-guesses. The levels must be codable (magnitude at most
// token.MaxLevel), mirroring cost.BlockCost's own refusal otherwise.
func searchCoeffCandidates(plane, ctx, first int, initial *[16]int16, lambda int64, distortion spatialDistortionFn) ([16]int16, coeffSearchStats) {
	return searchCoeffCandidatesWithProbs(plane, ctx, first, &token.DefaultProbs, initial, lambda, distortion)
}

// searchCoeffCandidatesWithProbs is searchCoeffCandidates with a
// caller-supplied probability table: every unique candidate is priced
// through cost.BlockCost under exactly the supplied probs -- no
// candidate ever falls back to another table -- and ties are resolved
// by strict-smaller comparison only, keeping the earliest candidate.
func searchCoeffCandidatesWithProbs(plane, ctx, first int, probs *token.Probs, initial *[16]int16, lambda int64, distortion spatialDistortionFn) ([16]int16, coeffSearchStats) {
	var stats coeffSearchStats

	cands, enumerated := enumerateCoeffCandidates(initial)
	stats.CandidatesEnumerated = enumerated
	stats.CandidatesUnique = len(cands)
	stats.CandidatesScored = len(cands)

	best := cands[0]
	stats.WinningOrdinal = 0
	stats.BestScore = scoreCoeffCandidateWithProbs(plane, ctx, first, probs, &best, lambda, distortion)
	for ord := 1; ord < len(cands); ord++ {
		score := scoreCoeffCandidateWithProbs(plane, ctx, first, probs, &cands[ord], lambda, distortion)
		if score < stats.BestScore { // strictly smaller: ties keep earliest
			stats.BestScore = score
			best = cands[ord]
			stats.WinningOrdinal = ord
		}
	}
	stats.Improved = best != *initial

	return best, stats
}

// scoreCoeffCandidate prices one candidate exactly as the caller sees
// the trade-off: the caller-supplied spatial distortion times 256 plus
// lambda times the exact token rate that cost.BlockCost charges for
// the supplied plane, neighbour context, and start position under the
// default probability table. It delegates to scoreCoeffCandidateWithProbs.
func scoreCoeffCandidate(plane, ctx, first int, levels *[16]int16, lambda int64, distortion spatialDistortionFn) int64 {
	return scoreCoeffCandidateWithProbs(plane, ctx, first, &token.DefaultProbs, levels, lambda, distortion)
}

// scoreCoeffCandidateWithProbs is scoreCoeffCandidate with a
// caller-supplied probability table; the token rate comes from
// cost.BlockCost under exactly the supplied probs.
func scoreCoeffCandidateWithProbs(plane, ctx, first int, probs *token.Probs, levels *[16]int16, lambda int64, distortion spatialDistortionFn) int64 {
	return distortion(levels)*256 + lambda*int64(cost.BlockCost(plane, ctx, first, levels, probs))
}
