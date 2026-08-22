package cost

import "m31labs.dev/tqwebp/internal/token"

// This file holds the Slice 6A probability optimizer: it turns measured
// token-branch counts into the per-node Q8 table that codes the same
// tokens most cheaply, and it prices every header update exactly so a
// change ships only when it strictly pays for its own signalling.
//
// The unit of account is the package's own: 1/256-bit costs from
// zeroCostQ8, an update gate priced at BitCostOne of its RFC 6386
// section 13.4 gate probability, and a fixed 8-bit literal. Everything is
// integer arithmetic over fixed tables, so results are identical on every
// platform at every GOMAXPROCS.

// ProbUpdateLiteralBits is the exact width in bits of the L(8) literal
// that carries one updated coefficient probability, RFC 6386 section
// 13.4.
const ProbUpdateLiteralBits = 8

// Histogram records how often every codable token branch was taken.
// It implements token.Observer, so a token.Writer can feed it during a
// dry coding pass without any output being produced.
type Histogram struct {
	// counts[plane][band][ctx][node] holds {falseCount, trueCount}.
	counts [token.NumPlanes][token.NumBands][token.NumContexts][token.NumProbs][2]int32
}

// ObserveBranch implements token.Observer. It panics on coordinates
// outside the RFC-shaped table, mirroring the writer's own refusal to
// code outside them; a writer can only report branches it actually
// codes, so a panic here means a caller misused the API.
func (h *Histogram) ObserveBranch(plane, band, ctx, node int, bit bool) {
	if plane < 0 || plane >= token.NumPlanes ||
		band < 0 || band >= token.NumBands ||
		ctx < 0 || ctx >= token.NumContexts ||
		node < 0 || node >= token.NumProbs {
		panic("tqwebp/cost: branch outside the coefficient probability table")
	}
	b := 0
	if bit {
		b = 1
	}
	h.counts[plane][band][ctx][node][b]++
}

// Counts returns the false and true tallies of one branch.
func (h *Histogram) Counts(plane, band, ctx, node int) (falseCount, trueCount int) {
	return int(h.counts[plane][band][ctx][node][0]), int(h.counts[plane][band][ctx][node][1])
}

// OptimalProb returns the Q8 probability that prices nFalse "no" and
// nTrue "yes" observations cheapest under this package's exact cost
// tables. It scans all 255 codable values and keeps the first minimum,
// which makes the choice deterministic and independent of the old
// probability. A node with no observations has no evidence; the scan
// then returns 128, the even-odds default, but Optimize never updates an
// unobserved node anyway because no update can pay for itself there.
func OptimalProb(nFalse, nTrue int) uint8 {
	if nFalse <= 0 && nTrue <= 0 {
		return 128
	}
	best := uint8(1)
	var bestCost Cost
	for p := 1; p <= 255; p++ {
		c := BranchCost(uint8(p), nFalse, nTrue)
		if p == 1 || c < bestCost {
			bestCost = c
			best = uint8(p)
		}
	}
	return best
}

// BranchCost prices nFalse false branches and nTrue true branches coded
// against one Q8 probability, exactly, in 1/256-bit units.
func BranchCost(p uint8, nFalse, nTrue int) Cost {
	return Cost(nFalse)*BitCostZero(p) + Cost(nTrue)*BitCostOne(p)
}

// Optimize derives the cheapest per-node table from h and returns the
// entries whose updates strictly pay for themselves against old, the
// table the frame would otherwise signal (for a key frame, the defaults).
//
// One entry's ledger compares two exact totals, both priced by this
// package's own tables:
//
//	old total = BranchCost(old) + ProbUpdateCost(gate, false)
//	new total = BranchCost(new) + ProbUpdateCost(gate, true)
//
// The new total carries the true-side cost of the section 13.4 gate
// decision plus the 8-bit literal that follows it; the old total carries
// the false-side gate decision the frame writes when it keeps the
// previous probability. An entry ships only when its new total is
// strictly below the old total; a tie keeps the old probability, as does
// a loss. Unobserved nodes never
// ship: their ledger cannot beat the fixed update cost. Optimize returns
// nil when no entry won, meaning the caller should keep old untouched.
//
// The walk visits nodes once, in plane, band, context, node order; the
// whole derivation is one pass over fixed-size state with no maps and no
// floating point.
func (h *Histogram) Optimize(old *token.Probs) *token.Probs {
	out := *old
	won := false
	for i := 0; i < token.NumPlanes; i++ {
		for j := 0; j < token.NumBands; j++ {
			for k := 0; k < token.NumContexts; k++ {
				for l := 0; l < token.NumProbs; l++ {
					nFalse, nTrue := h.Counts(i, j, k, l)
					total := nFalse + nTrue
					if total == 0 {
						continue
					}
					oldP := old[i][j][k][l]
					newP := OptimalProb(nFalse, nTrue)
					gate := token.UpdateProbs[i][j][k][l]
					oldTotal := BranchCost(oldP, nFalse, nTrue) +
						ProbUpdateCost(gate, false)
					newTotal := BranchCost(newP, nFalse, nTrue) +
						ProbUpdateCost(gate, true)
					// Strictly cheaper totals only: a tie keeps the
					// old probability. When newP equals oldP the new
					// total is strictly larger by construction (the
					// true-side gate plus literal outweighs the
					// false-side gate), so no separate check is
					// needed to avoid writing a no-op update.
					if newTotal < oldTotal {
						out[i][j][k][l] = newP
						won = true
					}
				}
			}
		}
	}
	if !won {
		return nil
	}
	return &out
}
