package encoder

// This file holds the Slice 6B tests for (*encoder).resetAnalysis: the
// bounded primitive that returns every mutable per-analysis state to a
// fresh newEncoder's state while preserving the active rateProbs pointer
// and the explicit behavior/test overrides. The coverage mirrors the
// primitive's contract:
//
//   - freshness: after an encode dirtied the state, every reconstruction
//     plane, macroblock record, RD counter, token/sub-mode neighbour
//     context, serialization sub-context, and scratch buffer equals the
//     state a fresh encoder carries;
//   - preservation: source, config, quantizer, dimensions, rateProbs
//     pointer, and all five overrides survive the reset;
//   - no aliasing: no slice or buffer of the reset encoder points into
//     the old mutable storage, and the old storage keeps its dirt;
//   - functional freshness: a reset followed by a second analysis
//     reproduces the first analysis's macroblocks and counters exactly.

import (
	"bytes"
	"testing"

	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/token"
	"m31labs.dev/tqwebp/internal/yuv"
)

func equalMbs(a, b []macroblock) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalTokenCtx(a, b []mbContext) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalSubVecs(a, b [][4]predict.SubMode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestResetAnalysisFreshAndPreserved dirties every mutable state group,
// calls resetAnalysis, and proves the reset state equals a fresh
// encoder's state while the preserved state survives untouched.
func TestResetAnalysisFreshAndPreserved(t *testing.T) {
	src := yuv.Convert(bpredMixedRGBA(32, 16, 5))
	cfg := Config{Quality: 75, Method: 6}

	custom := token.DefaultProbs
	custom[0][0][0][0] = 200
	customProbs := &custom

	enc := newEncoder(src, cfg)
	enc.rateProbs = customProbs
	enc.forceBPred = true
	enc.rdNoPrune = true
	enc.rdCoeffOptOff = true
	enc.rdCoeffTrellisOff = true
	enc.rdProbOptOff = true

	// Dirty the state with a real analysis pass first.
	enc.run()

	// Force-dirty every state group the pass may legitimately leave at
	// its zero value, so freshness cannot pass vacuously.
	dirtyCtx := mbContext{y2: 1, luma: [4]uint8{1, 1, 1, 1}, u: [2]uint8{1, 1}, v: [2]uint8{1, 1}}
	dirtySub := [4]predict.SubMode{7, 7, 7, 7}
	for i := range enc.rdTokenAbove {
		enc.rdTokenAbove[i] = dirtyCtx
	}
	for i := range enc.rdSubAbove {
		enc.rdSubAbove[i] = dirtySub
	}
	for i := range enc.subCtxAbove {
		enc.subCtxAbove[i] = dirtySub
	}
	for i := range enc.mbs {
		enc.mbs[i].yMode = predict.NumModes
		enc.mbs[i].uvMode = predict.NumModes
		enc.mbs[i].skip = true
		enc.mbs[i].bpred = true
		for s := range enc.mbs[i].subModes {
			enc.mbs[i].subModes[s] = predict.SubMode(15)
		}
		enc.mbs[i].levels[0][0] = -32768
		enc.mbs[i].nz[0] = true
	}
	fill(enc.predY[:], 255)
	fill(enc.bestY[:], 255)
	fill(enc.predU[:], 255)
	fill(enc.predV[:], 255)
	fill(enc.bestU[:], 255)
	fill(enc.bestV[:], 255)
	nine := [16]uint8{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}
	enc.nbY.top, enc.nbY.left = nine, nine
	enc.nbU.top, enc.nbU.left = nine, nine
	enc.nbV.top, enc.nbV.left = nine, nine
	fill(enc.rec.Y, 7)
	fill(enc.rec.U, 7)
	fill(enc.rec.V, 7)

	// Capture the old mutable storage before the reset.
	oldRec := enc.rec
	oldRecY := &enc.rec.Y[0]
	oldRecU := &enc.rec.U[0]
	oldRecV := &enc.rec.V[0]
	oldMbs0 := &enc.mbs[0]
	oldSubCtx0 := &enc.subCtxAbove[0]
	oldTokenAbove0 := &enc.rdTokenAbove[0]
	oldSubAbove0 := &enc.rdSubAbove[0]
	dirtyMbs := make([]macroblock, len(enc.mbs))
	copy(dirtyMbs, enc.mbs)
	dirtyRecY := append([]byte(nil), oldRec.Y...)

	enc.resetAnalysis()

	// Freshness: every mutable state group equals a fresh encoder's.
	ref := newEncoder(src, cfg)
	if !equalPlanes(enc.rec, ref.rec) {
		t.Fatalf("reconstruction planes survived reset")
	}
	if !equalMbs(enc.mbs, ref.mbs) {
		t.Fatalf("macroblock records survived reset")
	}
	if enc.rd != ref.rd {
		t.Fatalf("RD counters survived reset: %+v", enc.rd)
	}
	if !equalTokenCtx(enc.rdTokenAbove, ref.rdTokenAbove) || enc.rdTokenLeft != ref.rdTokenLeft {
		t.Fatalf("token neighbour contexts survived reset")
	}
	if !equalSubVecs(enc.rdSubAbove, ref.rdSubAbove) || enc.rdSubLeft != ref.rdSubLeft {
		t.Fatalf("sub-mode analysis contexts survived reset")
	}
	if !equalSubVecs(enc.subCtxAbove, ref.subCtxAbove) {
		t.Fatalf("serialization sub-contexts survived reset")
	}
	if !bytesEqual(enc.predY[:], ref.predY[:]) || !bytesEqual(enc.bestY[:], ref.bestY[:]) ||
		!bytesEqual(enc.predU[:], ref.predU[:]) || !bytesEqual(enc.predV[:], ref.predV[:]) ||
		!bytesEqual(enc.bestU[:], ref.bestU[:]) || !bytesEqual(enc.bestV[:], ref.bestV[:]) {
		t.Fatalf("prediction scratch buffers survived reset")
	}
	for _, nb := range [3]*neighborBuf{&enc.nbY, &enc.nbU, &enc.nbV} {
		if nb.top != [16]uint8{} || nb.left != [16]uint8{} {
			t.Fatalf("neighbour scratch buffer survived reset")
		}
	}

	// Preservation: source, config, quantizer, dimensions.
	if enc.src != src {
		t.Fatalf("source pointer changed")
	}
	if enc.cfg != cfg {
		t.Fatalf("config changed: %+v != %+v", enc.cfg, cfg)
	}
	if enc.q != ref.q {
		t.Fatalf("quantizer changed: %+v != %+v", enc.q, ref.q)
	}
	if enc.lambda != ref.lambda {
		t.Fatalf("lambda changed: %d != %d", enc.lambda, ref.lambda)
	}
	if enc.mbw != src.MBW || enc.mbh != src.MBH {
		t.Fatalf("dimensions changed: %dx%d != %dx%d", enc.mbw, enc.mbh, src.MBW, src.MBH)
	}

	// Preservation: the exact active rateProbs pointer and overrides.
	if enc.rateProbs != customProbs {
		t.Fatalf("rateProbs pointer changed")
	}
	if *enc.rateProbs != custom {
		t.Fatalf("rateProbs content changed")
	}
	if !enc.forceBPred || !enc.rdNoPrune || !enc.rdCoeffOptOff || !enc.rdCoeffTrellisOff || !enc.rdProbOptOff {
		t.Fatalf("override lost: forceBPred=%v rdNoPrune=%v rdCoeffOptOff=%v rdCoeffTrellisOff=%v rdProbOptOff=%v",
			enc.forceBPred, enc.rdNoPrune, enc.rdCoeffOptOff, enc.rdCoeffTrellisOff, enc.rdProbOptOff)
	}

	// No aliasing: nothing points into the old mutable storage, and the
	// old storage still holds the dirt the reset displaced.
	if enc.rec == oldRec {
		t.Fatalf("reconstruction planes alias old storage")
	}
	if &enc.rec.Y[0] == oldRecY || &enc.rec.U[0] == oldRecU || &enc.rec.V[0] == oldRecV {
		t.Fatalf("reconstruction plane backing arrays alias old storage")
	}
	if &enc.mbs[0] == oldMbs0 {
		t.Fatalf("macroblock slice aliases old storage")
	}
	if &enc.subCtxAbove[0] == oldSubCtx0 {
		t.Fatalf("sub-context slice aliases old storage")
	}
	if &enc.rdTokenAbove[0] == oldTokenAbove0 {
		t.Fatalf("token context slice aliases old storage")
	}
	if &enc.rdSubAbove[0] == oldSubAbove0 {
		t.Fatalf("sub-mode context slice aliases old storage")
	}
	if !bytesEqual(dirtyRecY, oldRec.Y) {
		t.Fatalf("old reconstruction storage was mutated in place")
	}
	if *oldMbs0 != dirtyMbs[0] {
		t.Fatalf("old macroblock storage was mutated in place")
	}
	if oldRec.Y[0] != 7 || oldRec.U[0] != 7 {
		t.Fatalf("old reconstruction storage lost its dirt")
	}
}

// TestResetAnalysisReproducibleAnalysis proves functional freshness: a
// reset between two analysis passes reproduces the first pass's
// macroblock decisions and counters exactly.
func TestResetAnalysisReproducibleAnalysis(t *testing.T) {
	src := yuv.Convert(bpredMixedRGBA(32, 16, 5))
	cfg := Config{Quality: 75, Method: 6}

	enc := newEncoder(src, cfg)
	enc.run()
	firstMbs := make([]macroblock, len(enc.mbs))
	copy(firstMbs, enc.mbs)
	firstRd := enc.rd

	enc.resetAnalysis()
	if enc.rd != (rdStats{}) {
		t.Fatalf("reset left RD counters dirty: %+v", enc.rd)
	}
	enc.run()

	if enc.rd != firstRd {
		t.Fatalf("second analysis diverged in counters: %+v != %+v", enc.rd, firstRd)
	}
	if !equalMbs(enc.mbs, firstMbs) {
		for i := range enc.mbs {
			if enc.mbs[i] != firstMbs[i] {
				t.Fatalf("second analysis diverged at macroblock %d: %+v != %+v", i, enc.mbs[i], firstMbs[i])
			}
		}
	}
}

func fill(p []byte, v byte) {
	for i := range p {
		p[i] = v
	}
}

func bytesEqual(a, b []byte) bool {
	return bytes.Equal(a, b)
}

func equalPlanes(a, b *yuv.Planes) bool {
	return bytesEqual(a.Y, b.Y) && bytesEqual(a.U, b.U) && bytesEqual(a.V, b.V)
}
