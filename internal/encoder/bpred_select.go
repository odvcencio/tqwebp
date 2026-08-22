package encoder

// This file is work package WP-2 slice 2B: production's conservative
// B_PRED selector, available only at effort Method 5 or 6. Methods below
// the boundary never reach this code, so their bytes cannot move.
//
// The selector is honest about what it is. It prices nothing: no rate
// term, no trellis, no probability updates -- the exact rate-distortion
// search of a later work package would price bits as well as error, and
// this rule does not pretend to. It is a bounded sum-of-squares proxy,
// called here the detailed-block rule:
//
//   - Code the macroblock's luma the old way first, with one whole-block
//     mode behind the Walsh-Hadamard transform, and keep its record and
//     its reconstruction exactly as earlier releases built them.
//   - Then tentatively code the same luma as sixteen independent 4x4
//     blocks, and total each block's smallest sum of squared errors over
//     the ten sub-modes.
//   - Adopt the sixteen-block result only when its total error is
//     strictly below half of the whole-block error. Equal, near-equal,
//     or merely somewhat better candidates lose: sixteen sub-modes cost
//     real bits next to one whole-block mode, so a selector with no rate
//     model needs a wide margin to stay conservative.
//
// Ties and margins are decided by strict integer comparison, so the rule
// is deterministic at every value of GOMAXPROCS, like everything else in
// this package.

// minBPredMethod is the lowest effort level whose luma search may leave
// a macroblock on the B_PRED path. It pins the slice's effort boundary:
// at Method 4 and below the tentative pass does not run at all.
const minBPredMethod = 5

// bPredWinNum and bPredWinDen shape the detailed-block rule's dominance
// test. The candidate wins only when
//
//	candidateSSE * bPredWinDen < wholeSSE * bPredWinNum,
//
// which for 1/2 means strictly below half of the whole-block error. The
// fraction is a named constant rather than an inline literal so tests can
// pin the exact margin the documentation describes.
const (
	bPredWinNum = 1
	bPredWinDen = 2
)

// bPredAllowed reports whether this encode's effort level may select the
// B_PRED path. The forced path of the tests bypasses selection entirely
// and never consults this gate.
func (e *encoder) bPredAllowed() bool {
	return !e.forceBPred && e.cfg.Method >= minBPredMethod
}

// bPredClearlyWins applies the detailed-block rule: it reports whether
// the sixteen-block candidate's total prediction error is strictly below
// half of the whole-block error, per the bPredWinNum/bPredWinDen margin.
func bPredClearlyWins(wholeSSE, candidateSSE int64) bool {
	return candidateSSE*bPredWinDen < wholeSSE*bPredWinNum
}

// tryDetailedLuma gives one macroblock's luma a tentative pass through
// the sixteen-4x4 coder and keeps the result only when the detailed-
// block rule clearly prefers it. wholeSSE is the sum of squared errors
// chooseLumaMode measured for the whole-block mode already coded and
// reconstructed into e.rec.Y.
//
// On rejection the macroblock's record and its 16x16 reconstruction are
// restored byte for byte, leaving exactly what the whole-block path of
// earlier releases wrote. Nothing outside the macroblock is touched:
// both paths write samples only inside their own 16x16 area, and chroma
// coding runs afterwards from the restored state either way.
func (e *encoder) tryDetailedLuma(mbx, mby int, mb *macroblock, wholeSSE int64) {
	saved := *mb
	x0, y0 := mbx*16, mby*16
	var savedRec [16][16]uint8
	for r := 0; r < 16; r++ {
		copy(savedRec[r][:], e.rec.Y[(y0+r)*e.rec.YStride+x0:][:16])
	}

	candidateSSE := e.codeLumaBPred(mbx, mby, mb)
	if bPredClearlyWins(wholeSSE, candidateSSE) {
		mb.bpred = true
		return
	}

	*mb = saved
	for r := 0; r < 16; r++ {
		copy(e.rec.Y[(y0+r)*e.rec.YStride+x0:][:16], savedRec[r][:])
	}
}
