package encoder

// This file pins the public B_PRED effort boundary. The production selector
// itself is the reconstructed-neighbor rate-distortion search in rd_select.go.

// minBPredMethod is the lowest effort level whose luma search may leave
// a macroblock on the B_PRED path. At Method 4 and below the rate-distortion
// pass does not run at all.
const minBPredMethod = 5

// bPredAllowed reports whether this encode's effort level may select the
// B_PRED path. The forced path of the tests bypasses selection entirely
// and never consults this gate.
func (e *encoder) bPredAllowed() bool {
	return !e.forceBPred && e.cfg.Method >= minBPredMethod
}
