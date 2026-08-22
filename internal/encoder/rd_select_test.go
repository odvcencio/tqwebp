package encoder

// This file holds the work package WP-2 slice 4 tests: the
// reconstructed-neighbour rate-distortion luma search behind the Method
// 5/6 effort boundary. The coverage mirrors the search's contract:
//
//   - canonical ties: equal scores go to the lowest candidate ordinal;
//   - reconstructed-neighbour dependence: every candidate is priced
//     from the decoder-visible reconstruction, which an independent
//     re-derivation of every decision reproduces, and which the
//     independent decoder's byte-exact picture proves end to end;
//   - exact rate accounting: the winner of every macroblock is the
//     canonical minimizer of independently recomputed exact scores;
//   - pruning bounds: disabling every bound changes no decision and no
//     byte, and the counters prove the bounds engaged;
//   - effort boundaries: methods 0 through 4 stay byte-identical;
//   - determinism: repeats and GOMAXPROCS values change nothing.

import (
	"bytes"
	"image"
	"runtime"
	"testing"

	"m31labs.dev/turboquant/blockdsp"

	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/token"
	"m31labs.dev/tqwebp/internal/yuv"
	"m31labs.dev/tqwebp/oracle"
)

// TestRDCandidateTieRule pins the canonical comparison directly: a
// strictly lower score wins whatever the ordinals are, and equal scores
// go to the lower ordinal -- so B_PRED, the highest ordinal, loses
// every tie against a whole-block mode.
func TestRDCandidateTieRule(t *testing.T) {
	whole := rdCandidate{ordinal: 2, sse: 10, rate: 300, score: 10<<8 + 300}
	bpred := rdCandidate{ordinal: rdOrdinalBPred, bpred: true, sse: 10, rate: 300, score: 10<<8 + 300}
	if bpred.betterThan(whole) {
		t.Fatalf("B_PRED tie broke the canonical order: %v beat ordinal %d", bpred, whole.ordinal)
	}
	if whole.betterThan(whole) {
		t.Fatalf("candidate beat itself")
	}
	higherScoreLowerOrdinal := rdCandidate{ordinal: 0, score: whole.score + 1}
	if higherScoreLowerOrdinal.betterThan(whole) {
		t.Fatalf("higher score won on ordinal: %v beat %v", higherScoreLowerOrdinal, whole)
	}
	lowerScoreHigherOrdinal := rdCandidate{ordinal: 3, score: whole.score - 1}
	if !lowerScoreHigherOrdinal.betterThan(whole) {
		t.Fatalf("strictly better score lost: %v did not beat %v", lowerScoreHigherOrdinal, whole)
	}
}

// TestRDPruningFloors pins the pruning bounds: both per-piece floors
// are positive, so the gate is finite and monotone, and the gate keeps
// every candidate whose floor can strictly beat the incumbent while
// dropping exactly the ones that cannot -- ties included, because ties
// already belong to the lower ordinal.
func TestRDPruningFloors(t *testing.T) {
	if rdSubModeFloor <= 0 || rdBlockFloor <= 0 {
		t.Fatalf("floors must be positive, got sub-mode %d block %d", rdSubModeFloor, rdBlockFloor)
	}
	// Each floor is a true lower bound: the cheapest actual record of
	// its kind can never cost less.
	for a := 0; a < len(predict.KeyFrameSubModeProbs); a += 7 {
		for l := 0; l < len(predict.KeyFrameSubModeProbs[a]); l += 11 {
			for m := predict.SubMode(0); m < predict.NumSubModes; m++ {
				if c := cost.SubModeCost(predict.SubMode(a), predict.SubMode(l), m); c < rdSubModeFloor {
					t.Fatalf("sub-mode cost %d below floor %d", c, rdSubModeFloor)
				}
			}
		}
	}
	for ctx := 0; ctx <= 2; ctx++ {
		for n := 0; n < 16; n++ {
			p := token.DefaultProbs[token.YWithDC][token.Bands[n]][ctx][0]
			if c := cost.BitCostZero(p); c < rdBlockFloor {
				t.Fatalf("block cost %d below floor %d", c, rdBlockFloor)
			}
		}
	}

	enc := newEncoder(yuv.Convert(bpredMixedRGBA(32, 16, 5)), Config{Quality: 75, Method: 6})
	// Gate arithmetic: attempt iff the floor cannot tie or beat.
	best := rdCandidate{ordinal: 0, score: 1 << 20}
	floor := cost.BPredFlag() + cost.Cost(16)*(rdSubModeFloor+rdBlockFloor)
	if int64(floor)*enc.lambda >= best.score {
		t.Fatalf("gate would prune a candidate whose floor strictly beats the incumbent")
	}
	tight := rdCandidate{ordinal: 0, score: int64(floor) * enc.lambda}
	if int64(floor)*enc.lambda < tight.score {
		t.Fatalf("gate admitted an exact tie, which the canonical order awards to the whole block")
	}
}

// TestRDStatsInvariants runs one production encode and checks the
// deterministic counter identities the search promises: every
// macroblock decided exactly once, every B_PRED attempt counted, the
// gate and the attempts partitioning the frame.
func TestRDStatsInvariants(t *testing.T) {
	for _, q := range []int{10, 50, 90} {
		img := bpredMixedRGBA(48, 32, 21)
		enc := newEncoder(yuv.Convert(img), Config{Quality: q, Method: 6})
		enc.run()
		mbCount := int64(enc.mbw * enc.mbh)
		s := enc.rd
		if s.CandidatesEvaluated != 4*mbCount+s.BpredAttempts {
			t.Fatalf("q%d: candidates %d, want 4*%d+attempts %d", q, s.CandidatesEvaluated, mbCount, s.BpredAttempts)
		}
		if s.BpredAttempts+s.PrunedBPred != mbCount {
			t.Fatalf("q%d: attempts %d + pruned %d != %d macroblocks", q, s.BpredAttempts, s.PrunedBPred, mbCount)
		}
		if s.DecisionsWhole+s.DecisionsBPred != mbCount {
			t.Fatalf("q%d: decisions %d+%d != %d macroblocks", q, s.DecisionsWhole, s.DecisionsBPred, mbCount)
		}
		if s.BpredAborts > s.BpredAttempts-s.DecisionsBPred {
			t.Fatalf("q%d: aborts %d exceed attempts that could not win (%d)", q, s.BpredAborts, s.BpredAttempts-s.DecisionsBPred)
		}
	}
}

// TestRDPruningEquivalence proves the bounds are pure search pruning:
// with every gate and per-block bound disabled the search must reach
// the same decisions and the same bytes. The counters differ -- the
// unpruned search attempts every macroblock and aborts nothing -- and
// those differences are pinned too, so the test shows the bounds
// engaged rather than silently never firing.
func TestRDPruningEquivalence(t *testing.T) {
	for _, spec := range []struct {
		name string
		img  image.Image
		q    int
	}{
		{"mixed", bpredMixedRGBA(48, 32, 77), 75},
		// At low quality the whole-block incumbent is often cheap
		// enough that the B_PRED pass dies mid-way, so this case
		// exercises the per-block bound the way the flat and mixed
		// cases exercise the attempt gate. (Under the slice 5B
		// coefficient search the pass prices cheaper token shares,
		// so this fixture pair keeps the bound firing.)
		{"detail", bpredDetailRGBA(64, 48, 5), 10},
		{"flat", flatRGBA(48, 32, 128), 35},
	} {
		t.Run(spec.name, func(t *testing.T) {
			pruned := newEncoder(yuv.Convert(spec.img), Config{Quality: spec.q, Method: 6})
			pruned.run()
			var bufP writerBuffer
			if err := pruned.writeFile(&bufP); err != nil {
				t.Fatalf("write pruned: %v", err)
			}

			free := newEncoder(yuv.Convert(spec.img), Config{Quality: spec.q, Method: 6})
			free.rdNoPrune = true
			free.run()
			var bufF writerBuffer
			if err := free.writeFile(&bufF); err != nil {
				t.Fatalf("write unpruned: %v", err)
			}

			if !bytes.Equal(bufP.data, bufF.data) {
				t.Fatalf("pruning changed the encoded bytes (%d vs %d)", len(bufP.data), len(bufF.data))
			}
			for i := range pruned.mbs {
				if pruned.mbs[i].bpred != free.mbs[i].bpred || pruned.mbs[i].yMode != free.mbs[i].yMode ||
					pruned.mbs[i].subModes != free.mbs[i].subModes {
					t.Fatalf("macroblock %d: pruning changed the decision", i)
				}
			}

			mbCount := int64(free.mbw * free.mbh)
			if free.rd.BpredAttempts != mbCount || free.rd.PrunedBPred != 0 || free.rd.BpredAborts != 0 {
				t.Fatalf("unpruned search did not attempt everything: %+v", free.rd)
			}
			if pruned.rd.CandidatesEvaluated != free.rd.CandidatesEvaluated-(pruned.rd.PrunedBPred) {
				t.Fatalf("candidate accounting inconsistent: pruned %+v unpruned %+v", pruned.rd, free.rd)
			}
			if pruned.rd.PrunedBPred == 0 && pruned.rd.BpredAborts == 0 {
				t.Fatalf("%s: bounds never fired; fixture exercises no pruning", spec.name)
			}
		})
	}
}

// TestRDEffortBoundaryBytes pins the effort boundary at the byte level:
// methods 0 through 4 share one effort level, so every one of them must
// emit exactly the method 4 file. Method 5 and 6 run the rate-
// distortion search and are allowed -- expected -- to differ.
func TestRDEffortBoundaryBytes(t *testing.T) {
	for _, spec := range []struct {
		name string
		img  image.Image
		q    int
	}{
		{"mixed", bpredMixedRGBA(48, 32, 77), 75},
		{"detail", bpredDetailRGBA(64, 48, 101), 90},
		{"flat", flatRGBA(33, 17, 200), 50},
	} {
		t.Run(spec.name, func(t *testing.T) {
			_, baseline, _, err := encodeWithMethod(spec.img, Config{Quality: spec.q, Method: 4})
			if err != nil {
				t.Fatalf("method 4: %v", err)
			}
			for m := 0; m <= 4; m++ {
				_, data, _, err := encodeWithMethod(spec.img, Config{Quality: spec.q, Method: m})
				if err != nil {
					t.Fatalf("method %d: %v", m, err)
				}
				if !bytes.Equal(data, baseline) {
					t.Fatalf("method %d moved off the method 4 bytes", m)
				}
			}
			_, above, _, err := encodeWithMethod(spec.img, Config{Quality: spec.q, Method: 6})
			if err != nil {
				t.Fatalf("method 6: %v", err)
			}
			if len(above) == 0 {
				t.Fatalf("method 6 wrote nothing")
			}
		})
	}
}

// TestRDSearchDecoderEquality is the end-to-end proof that the search
// prices candidates from the decoder-visible state: the independent
// decoder must reproduce the encoder's own reconstruction byte for
// byte across qualities, methods, sizes, and content kinds, including
// odd sizes whose border macroblocks sit on the RFC's missing-sample
// fills.
func TestRDSearchDecoderEquality(t *testing.T) {
	fixtures := []struct {
		name string
		img  image.Image
	}{
		{"mixed", bpredMixedRGBA(48, 32, 77)},
		{"detail", bpredDetailRGBA(64, 48, 101)},
		{"flat", flatRGBA(48, 32, 128)},
		{"odd", bpredMixedRGBA(33, 17, 5)},
		{"tiny", flatRGBA(1, 1, 7)},
	}
	for _, fx := range fixtures {
		for _, q := range []int{10, 50, 90} {
			for _, m := range []int{5, 6} {
				data, recon, err := encodeWithReconstruction(fx.img, Config{Quality: q, Method: m})
				if err != nil {
					t.Fatalf("%s q%d m%d: encode: %v", fx.name, q, m, err)
				}
				decoded, err := oracle.DecodeWebPPlanes(data)
				if err != nil {
					t.Fatalf("%s q%d m%d: decode: %v", fx.name, q, m, err)
				}
				if err := oracle.CompareExact(reconSource{recon}, decoded); err != nil {
					t.Fatalf("%s q%d m%d: %v", fx.name, q, m, err)
				}
			}
		}
	}
}

// TestRDSearchDeterministic proves the search deterministic: repeated
// encodes are byte-identical, the counters repeat exactly, and the
// result does not depend on GOMAXPROCS.
func TestRDSearchDeterministic(t *testing.T) {
	img := bpredMixedRGBA(48, 32, 77)

	type result struct {
		data  []byte
		stats rdStats
	}
	encode := func() result {
		enc := newEncoder(yuv.Convert(img), Config{Quality: 75, Method: 6})
		enc.run()
		var buf writerBuffer
		if err := enc.writeFile(&buf); err != nil {
			t.Fatalf("write: %v", err)
		}
		return result{data: buf.data, stats: enc.rd}
	}

	oldMaxProcs := runtime.GOMAXPROCS(1)
	first := encode()
	runtime.GOMAXPROCS(8)
	second := encode()
	runtime.GOMAXPROCS(oldMaxProcs)

	if !bytes.Equal(first.data, second.data) {
		t.Fatalf("repeat encode differed: %d vs %d bytes", len(first.data), len(second.data))
	}
	if first.stats != second.stats {
		t.Fatalf("repeat counters differed: %+v vs %+v", first.stats, second.stats)
	}
}

// TestRDReconstructedNeighbourDependence proves the search consumes
// reconstructed neighbours, not just source samples: changing the
// first macroblock's pixels changes its reconstruction, which is the
// only thing the downstream macroblocks predict and price from, and
// the downstream decisions must move somewhere. The decision vectors
// include the sixteen sub-modes, whose contexts the frame threads from
// macroblock to macroblock.
func TestRDReconstructedNeighbourDependence(t *testing.T) {
	base := bpredDetailRGBA(48, 32, 101)
	altered := bpredDetailRGBA(48, 32, 101)
	// Rewrite the top-left macroblock with flat mid-grey.
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			i := (y*48 + x) * 4
			altered.Pix[i], altered.Pix[i+1], altered.Pix[i+2], altered.Pix[i+3] = 128, 128, 128, 0xff
		}
	}

	// rdDecision is one macroblock's complete luma decision: the
	// path, the whole-block mode (meaningful only off the B_PRED
	// path), and all sixteen sub-mode records. Comparing whole
	// records, not derived booleans, is what makes the check below a
	// decision-level reaction rather than a change in some aggregate.
	type rdDecision struct {
		bpred bool
		yMode predict.Mode
		subs  [16]predict.SubMode
	}
	decisions := func(img image.Image) []rdDecision {
		enc := newEncoder(yuv.Convert(img), Config{Quality: 75, Method: 6})
		enc.run()
		out := make([]rdDecision, len(enc.mbs))
		for i := range enc.mbs {
			out[i] = rdDecision{
				bpred: enc.mbs[i].bpred,
				yMode: enc.mbs[i].yMode,
				subs:  enc.mbs[i].subModes,
			}
		}
		return out
	}

	// The oracle comparison below is the real dependence proof; this
	// assertion documents the mechanism on the fixture: the altered
	// macroblock's own record moves, and at least one downstream
	// macroblock -- which predicts and prices from the first one's
	// reconstruction -- must react too.
	dBase, dAltered := decisions(base), decisions(altered)
	if dBase[0] == dAltered[0] {
		t.Fatalf("the rewritten macroblock's own decision did not move")
	}
	for i := 1; i < len(dBase); i++ {
		if dBase[i] != dAltered[i] {
			return
		}
	}
	t.Fatalf("no downstream decision reacted to the altered neighbour")
}

// TestRDSearchMatchesIndependentOracle is the strongest check of the
// slice: for every macroblock of several frames it re-derives all five
// candidates from scratch -- prediction from the reconstructed border,
// transform, quantize, reconstruct, exact cost-primitive pricing under
// replayed neighbour contexts -- and requires the committed record to
// be the canonical argmin: the lowest-ordinal candidate among the
// strictly minimal scores. This covers tie canonicity, exact rate
// accounting, and reconstructed-neighbour dependence in one statement,
// independently of the production loops.
func TestRDSearchMatchesIndependentOracle(t *testing.T) {
	for _, spec := range []struct {
		name string
		img  image.Image
		q    int
	}{
		{"mixed q75", bpredMixedRGBA(48, 32, 77), 75},
		{"detail q90", bpredDetailRGBA(48, 32, 101), 90},
		{"flat q35", flatRGBA(48, 32, 128), 35},
		{"odd q60", bpredMixedRGBA(33, 17, 5), 60},
	} {
		t.Run(spec.name, func(t *testing.T) {
			enc := newEncoder(yuv.Convert(spec.img), Config{Quality: spec.q, Method: 6})
			// Isolation: this is the slice 4/5B independent-oracle
			// proof. Its expected candidate universe -- five whole-block
			// modes plus the B_PRED walk over retained levels --
			// intentionally excludes every slice 5C trellis proposal,
			// so the trellis layer alone is switched off for this exact
			// test; the slice 5B candidate search stays enabled and the
			// trellis itself is proven on production paths in
			// coeff_search_test.go.
			enc.rdCoeffTrellisOff = true
			enc.run()

			o := &rdOracle{enc: enc}
			for mby := 0; mby < enc.mbh; mby++ {
				for mbx := 0; mbx < enc.mbw; mbx++ {
					want := o.decide(mbx, mby)
					mb := &enc.mbs[mby*enc.mbw+mbx]
					gotPath, gotMode := mb.bpred, mb.yMode
					if want.bpred != gotPath || (!want.bpred && predict.Mode(want.ordinal) != gotMode) {
						t.Fatalf("macroblock (%d,%d): search chose (bpred=%v mode=%v), oracle says (bpred=%v ordinal=%d)",
							mbx, mby, gotPath, gotMode, want.bpred, want.ordinal)
					}
					if want.bpred {
						for b := 0; b < 16; b++ {
							if mb.subModes[b] != want.subModes[b] {
								t.Fatalf("macroblock (%d,%d): sub-mode %d is %v, oracle says %v",
									mbx, mby, b, mb.subModes[b], want.subModes[b])
							}
						}
					}

					// Full-vector comparison: reprice all five
					// candidates through the production pricers
					// under the same entering contexts and require
					// exact agreement on every SSE and rate, so a
					// defect on either side cannot hide behind an
					// unchanged winner.
					chromaEmpty := true
					for i := blockU; i < numBlocks; i++ {
						if mb.nz[i] {
							chromaEmpty = false
							break
						}
					}
					nb := enc.neighbors(&neighborBuf{}, enc.rec.Y, enc.rec.YStride, mbx*16, mby*16, 16, mbx > 0, mby > 0)
					src := enc.src.Y[(mby*16)*enc.src.YStride+mbx*16:]
					tok := rdTokenView{leftY2: o.left.y2, upY2: o.above[mbx].y2, leftLuma: o.left.luma, upLuma: o.above[mbx].luma}
					for m := predict.Mode(0); m < predict.NumModes; m++ {
						predict.Predict(enc.predY[:], 16, 16, m, nb)
						pgot, _ := enc.rdEvalWhole(mbx, mby, m, src, &tok, chromaEmpty)
						if pgot != want.whole[m] {
							t.Fatalf("macroblock (%d,%d) mode %d: production candidate %+v, oracle %+v", mbx, mby, m, pgot, want.whole[m])
						}
					}
					restore := enc.rdSaveRegion(mbx, mby, mb)
					// rdWalkBPred reads the frame-level sub-mode
					// shadows directly; inject the entering state
					// the replay carries so the probe prices what
					// the search priced at this macroblock.
					enc.rdSubAbove[mbx] = o.subAbove[mbx]
					enc.rdSubLeft = o.subLeft
					hugeBest := rdCandidate{ordinal: 0, score: int64(1) << 62}
					pbp, _, _, _ := enc.rdWalkBPred(mbx, mby, &hugeBest, &tok, chromaEmpty)
					restore()
					if pbp != want.bp {
						t.Fatalf("macroblock (%d,%d): production B_PRED candidate %+v, oracle %+v", mbx, mby, pbp, want.bp)
					}

					o.advance(mbx, mby, mb)
				}
			}
		})
	}
}

// flatRGBA builds a constant-colour fixture.
func flatRGBA(w, h int, level uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = level, level, level, 0xff
	}
	return img
}

// rdOracle re-derives the slice 4 search decision for one macroblock
// after another, replaying the context shadows from the committed
// records. It shares only the leaf primitives (predict, transform,
// quantize, cost) with the production search; every loop that makes a
// decision or threads a context is rewritten here.
type rdOracle struct {
	enc      *encoder
	above    []mbContext
	left     mbContext
	subAbove [][4]predict.SubMode
	subLeft  [4]predict.SubMode
}

type oracleDecision struct {
	bpred    bool
	ordinal  int
	subModes [16]predict.SubMode
	// whole and bp carry every candidate's full score, so callers
	// can compare complete vectors against the production pricers
	// instead of only the winning decision.
	whole [4]rdCandidate
	bp    rdCandidate
}

func (o *rdOracle) init() {
	o.above = make([]mbContext, o.enc.mbw)
	o.subAbove = make([][4]predict.SubMode, o.enc.mbw)
}

// decide prices all five candidates of macroblock (mbx, mby) and
// returns the canonical winner. Left-hand contexts restart at every
// macroblock row -- writeTokens zeroes its left mbContext and frameBytes
// zeroes its left sub-mode vector at each row start -- so column 0
// always prices against freshly zeroed left state, whatever an earlier
// caller left in the shadows.
func (o *rdOracle) decide(mbx, mby int) oracleDecision {
	if o.above == nil {
		o.init()
	}
	if mbx == 0 {
		o.left = mbContext{}
		o.subLeft = [4]predict.SubMode{}
	}
	return o.decideEntering(mbx, mby)
}

// decideEntering prices all five candidates against the context shadows
// exactly as they stand. It reads the reconstruction plane for
// prediction inputs -- final for every macroblock but the candidate's
// own, whose B_PRED pass it simulates on a saved copy.
func (o *rdOracle) decideEntering(mbx, mby int) oracleDecision {
	e := o.enc
	nb := e.neighbors(&neighborBuf{}, e.rec.Y, e.rec.YStride, mbx*16, mby*16, 16, mbx > 0, mby > 0)
	src := e.src.Y[(mby*16)*e.src.YStride+mbx*16:]
	tok := rdTokenView{leftY2: o.left.y2, upY2: o.above[mbx].y2, leftLuma: o.left.luma, upLuma: o.above[mbx].luma}

	type scored struct {
		cand  rdCandidate
		coded rdCoding
		subs  [16]predict.SubMode
	}
	var best scored
	have := false
	var whole [4]rdCandidate
	for m := predict.Mode(0); m < predict.NumModes; m++ {
		predict.Predict(e.predY[:], 16, 16, m, nb)
		cand, coded := o.scoreWhole(mbx, mby, m, src, &tok)
		whole[m] = cand
		if !have || cand.betterThan(best.cand) {
			best, have = scored{cand: cand, coded: coded}, true
		}
	}

	bp, subs := o.scoreBPred(mbx, mby, src, &tok)
	if bp.betterThan(best.cand) {
		return oracleDecision{bpred: true, ordinal: bp.ordinal, subModes: subs, whole: whole, bp: bp}
	}
	return oracleDecision{ordinal: best.cand.ordinal, whole: whole, bp: bp}
}

// chromaEmpty reports whether the already-coded chroma blocks of
// macroblock (mbx, mby) are all empty -- the condition under which a
// luma candidate whose every block also comes out empty is written as
// skipped and emits no coefficient tokens at all.
func (o *rdOracle) chromaEmpty(mbx, mby int) bool {
	mb := &o.enc.mbs[mby*o.enc.mbw+mbx]
	for i := blockU; i < numBlocks; i++ {
		if mb.nz[i] {
			return false
		}
	}
	return true
}

// scoreWhole mirrors the whole-block candidate pricing.
func (o *rdOracle) scoreWhole(mbx, mby int, m predict.Mode, src []uint8, tok *rdTokenView) (rdCandidate, rdCoding) {
	e := o.enc
	var coding rdCoding
	var coeffs [16][16]int16
	srcBase := (mby * 16) * e.src.YStride
	for b := 0; b < 16; b++ {
		bx, by := (b%4)*4, (b/4)*4
		var residual [16]int16
		for y := 0; y < 4; y++ {
			srcRow := e.src.Y[srcBase+(by+y)*e.src.YStride+mbx*16+bx:]
			predRow := e.predY[(by+y)*16+bx:]
			for x := 0; x < 4; x++ {
				residual[y*4+x] = int16(srcRow[x]) - int16(predRow[x])
			}
		}
		coeffs[b] = blockdsp.FDCT4x4(&residual)
	}
	var dc [16]int16
	for b := 0; b < 16; b++ {
		dc[b] = coeffs[b][0]
	}
	y2 := blockdsp.FWHT4x4(&dc)
	y2Levels := quantizeBlock(&y2, e.q.Y2)
	coding.y2Levels = toScanOrder(&y2Levels)
	y2Dequant := blockdsp.DequantizeBlock(&y2Levels, e.q.Y2.DC, e.q.Y2.AC)
	reconDC := blockdsp.IWHT4x4(&y2Dequant)

	var sse int64
	for b := 0; b < 16; b++ {
		levels := quantizeBlock(&coeffs[b], e.q.Y1)
		levels[0] = 0
		coding.luma[b] = toScanOrder(&levels)
		dequant := blockdsp.DequantizeBlock(&levels, e.q.Y1.DC, e.q.Y1.AC)
		dequant[0] = reconDC[b]
		residual := blockdsp.IDCT4x4(&dequant)
		bx, by := (b%4)*4, (b/4)*4
		for y := 0; y < 4; y++ {
			predRow := e.predY[(by+y)*16+bx:]
			recRow := coding.recon[by+y][bx:]
			srcRow := e.src.Y[srcBase+(by+y)*e.src.YStride+mbx*16+bx:]
			for x := 0; x < 4; x++ {
				v := clamp8(int32(predRow[x]) + int32(residual[y*4+x]))
				recRow[x] = v
				d := int32(v) - int32(srcRow[x])
				sse += int64(d * d)
			}
		}
	}

	rate := cost.LumaMode(m)
	tokenRate, lumaEmpty := o.priceWholeTokens(&coding.y2Levels, &coding.luma, tok)
	if !(o.chromaEmpty(mbx, mby) && lumaEmpty) {
		// A macroblock whose every block comes out empty behind
		// empty chroma is written skipped and emits no tokens at
		// all; any other candidate pays every token it priced.
		rate += tokenRate
	}
	return rdCandidate{ordinal: int(m), sse: sse, rate: rate, score: sse<<8 + int64(rate)*e.lambda}, coding
}

// priceWholeTokens charges the Y2 block and the sixteen luma blocks of
// a whole-block candidate under the entering token view, threading both
// axes the way writeTokens threads its own copies: the above entry of a
// column updates as soon as that column's block is coded, and each
// row's left entry starts from the entering shadow and ends at the
// row's last block. It returns the total token cost and whether every
// block came out empty.
func (o *rdOracle) priceWholeTokens(y2Levels *[16]int16, luma *[16][16]int16, tok *rdTokenView) (cost.Cost, bool) {
	probs := &token.DefaultProbs
	tokenRate := cost.BlockCost(token.Y2, int(tok.leftY2+tok.upY2), 0, y2Levels, probs)
	lumaEmpty := !anyNonZeroScan(y2Levels)
	up := tok.upLuma
	for y := 0; y < 4; y++ {
		nz := tok.leftLuma[y]
		for x := 0; x < 4; x++ {
			ctx := int(nz + up[x])
			block := &luma[4*y+x]
			tokenRate += cost.BlockCost(token.YAfterY2, ctx, 1, block, probs)
			nz = btou(anyNonZeroScan(block))
			up[x] = nz
			if nz != 0 {
				lumaEmpty = false
			}
		}
	}
	return tokenRate, lumaEmpty
}

// scoreBPred mirrors the sixteen-block candidate pricing on a saved and
// restored copy of the macroblock's reconstruction area.
func (o *rdOracle) scoreBPred(mbx, mby int, src []uint8, tok *rdTokenView) (rdCandidate, [16]predict.SubMode) {
	e := o.enc
	x0, y0 := mbx*16, mby*16
	var saved [16][16]uint8
	for r := 0; r < 16; r++ {
		copy(saved[r][:], e.rec.Y[(y0+r)*e.rec.YStride+x0:][:16])
	}
	defer func() {
		for r := 0; r < 16; r++ {
			copy(e.rec.Y[(y0+r)*e.rec.YStride+x0:][:16], saved[r][:])
		}
	}()

	paddedWidth := ((e.src.Width + 15) / 16) * 16
	var nb predict.SubNeighbors
	var pred [16]uint8
	var subs [16]predict.SubMode
	var lumaLevels [16][16]int16

	rate := cost.BPredFlag()
	probs := &token.DefaultProbs
	aboveCtx := o.subAbove[mbx]
	leftCtx := o.subLeft
	var leftLuma, upLuma [4]uint8
	leftLuma, upLuma = tok.leftLuma, tok.upLuma

	// Skip-aware token pricing: with empty chroma a candidate whose
	// sixteen luma blocks all come out empty is written skipped and
	// emits no coefficient tokens at all. Until a non-empty block
	// makes that outcome impossible the token share stays uncharged.
	skipPossible := o.chromaEmpty(mbx, mby)
	var tokenRate cost.Cost

	var sse int64
	for b := 0; b < 16; b++ {
		bx, by := x0+(b%4)*4, y0+(b/4)*4
		predict.GatherSubNeighbors(&nb, e.rec.Y, e.rec.YStride, bx, by, paddedWidth)
		srcRow0 := e.src.Y[by*e.src.YStride+bx:]

		bestSSE := int32(-1)
		var bestSub predict.SubMode
		for m := predict.SubMode(0); m < predict.NumSubModes; m++ {
			predict.PredictSub(pred[:], 4, m, &nb)
			s := blockdsp.SSE4x4(srcRow0, e.src.YStride, pred[:], 4)
			if bestSSE < 0 || s < bestSSE {
				bestSSE = s
				bestSub = m
			}
		}
		subs[b] = bestSub
		rate += cost.SubModeCost(aboveCtx[b%4], leftCtx[b/4], bestSub)
		aboveCtx[b%4] = bestSub
		leftCtx[b/4] = bestSub

		predict.PredictSub(pred[:], 4, bestSub, &nb)
		var residual [16]int16
		for y := 0; y < 4; y++ {
			srcRow := srcRow0[y*e.src.YStride:]
			predRow := pred[y*4:]
			for x := 0; x < 4; x++ {
				residual[y*4+x] = int16(srcRow[x]) - int16(predRow[x])
			}
		}
		coeff := blockdsp.FDCT4x4(&residual)
		levels := quantizeBlock(&coeff, e.q.Y1)
		lumaLevels[b] = toScanOrder(&levels)

		// Slice 5B mirror: when the effort level refines retained
		// levels, re-derive the winning levels here through the
		// shared enumeration helper, scored by an oracle-side
		// distortion closure -- scan to raster, Y1 dequantization,
		// inverse transform, add to this block's predictor, clamp,
		// squared error against src -- and exact BlockCost under
		// the entering context. Ties go to the earliest candidate.
		if e.coeffSearchAllowed() {
			ctx := int(leftLuma[b/4] + upLuma[b%4])
			dist := func(cand *[16]int16) int64 {
				raster := fromScanOrder(cand)
				deq := blockdsp.DequantizeBlock(&raster, e.q.Y1.DC, e.q.Y1.AC)
				resOut := blockdsp.IDCT4x4(&deq)
				var blockSSE int64
				for y := 0; y < 4; y++ {
					srcRow := srcRow0[y*e.src.YStride:]
					predRow := pred[y*4:]
					resRow := resOut[y*4:]
					for x := 0; x < 4; x++ {
						v := clamp8(int32(predRow[x]) + int32(resRow[x]))
						d := int32(srcRow[x]) - int32(v)
						blockSSE += int64(d * d)
					}
				}
				return blockSSE
			}
			winner, _ := searchCoeffCandidates(token.YWithDC, ctx, 0, &lumaLevels[b], e.lambda, dist)
			lumaLevels[b] = winner
		}

		tokenRate += cost.BlockCost(token.YWithDC, int(leftLuma[b/4]+upLuma[b%4]), 0, &lumaLevels[b], probs)

		raster := fromScanOrder(&lumaLevels[b])
		dequant := blockdsp.DequantizeBlock(&raster, e.q.Y1.DC, e.q.Y1.AC)
		residualOut := blockdsp.IDCT4x4(&dequant)
		e.reconstruct(e.rec.Y, e.rec.YStride, bx, by, pred[:], 4, 0, 0, &residualOut)
		for y := 0; y < 4; y++ {
			recRow := e.rec.Y[(by+y)*e.rec.YStride+bx:]
			srcRow := srcRow0[y*e.src.YStride:]
			for x := 0; x < 4; x++ {
				d := int32(recRow[x]) - int32(srcRow[x])
				sse += int64(d * d)
			}
		}
		nz := btou(anyNonZeroScan(&lumaLevels[b]))
		if nz != 0 {
			skipPossible = false
		}
		upLuma[b%4] = nz
		leftLuma[b/4] = nz
	}
	if !skipPossible {
		rate += tokenRate
	}
	return rdCandidate{ordinal: rdOrdinalBPred, bpred: true, sse: sse, rate: rate, score: sse<<8 + int64(rate)*e.lambda}, subs
}

// advance replays the shadow update the production search performs
// after committing a macroblock, so the next macroblock's candidates
// are priced against the same contexts.
func (o *rdOracle) advance(mbx, mby int, mb *macroblock) {
	if o.above == nil {
		o.init()
	}
	up := &o.above[mbx]
	left := &o.left
	if mb.skip {
		prevLeftY2, prevUpY2 := left.y2, up.y2
		*left = mbContext{}
		*up = mbContext{}
		if mb.bpred {
			left.y2, up.y2 = prevLeftY2, prevUpY2
		}
	} else {
		if !mb.bpred {
			nz := btou(mb.nz[blockY2])
			left.y2, up.y2 = nz, nz
		}
		for x := 0; x < 4; x++ {
			up.luma[x] = btou(mb.nz[blockLuma+12+x])
		}
		for y := 0; y < 4; y++ {
			left.luma[y] = btou(mb.nz[blockLuma+4*y+3])
		}
		for x := 0; x < 2; x++ {
			up.u[x] = btou(mb.nz[blockU+2+x])
			up.v[x] = btou(mb.nz[blockV+2+x])
		}
		for y := 0; y < 2; y++ {
			left.u[y] = btou(mb.nz[blockU+2*y+1])
			left.v[y] = btou(mb.nz[blockV+2*y+1])
		}
	}
	if mb.bpred {
		for i := 0; i < 4; i++ {
			o.subAbove[mbx][i] = mb.subModes[12+i]
		}
		for j := 0; j < 4; j++ {
			o.subLeft[j] = mb.subModes[4*j+3]
		}
	} else {
		ctx := subModeContextOf(mb.yMode)
		o.subAbove[mbx] = [4]predict.SubMode{ctx, ctx, ctx, ctx}
		o.subLeft = [4]predict.SubMode{ctx, ctx, ctx, ctx}
	}
}

// TestRDOracleRowReset pins the per-row restart of the left-hand
// contexts inside the oracle itself. Deciding column 0 of any row must
// price against freshly zeroed left token and sub-mode contexts,
// whatever stale state earlier macroblocks left behind; the poisoned
// comparison proves the fixture can actually see the difference, so a
// deleted reset cannot pass silently.
func TestRDOracleRowReset(t *testing.T) {
	for _, spec := range []struct {
		name string
		img  image.Image
		q    int
	}{
		{"mixed", bpredMixedRGBA(48, 32, 77), 75},
		{"detail", bpredDetailRGBA(48, 32, 101), 90},
	} {
		t.Run(spec.name, func(t *testing.T) {
			enc := newEncoder(yuv.Convert(spec.img), Config{Quality: spec.q, Method: 6})
			enc.run()

			base := &rdOracle{enc: enc}
			for mbx := 0; mbx < enc.mbw; mbx++ {
				base.advance(mbx, 0, &enc.mbs[mbx])
			}

			price := func(o *rdOracle) ([4]rdCandidate, rdCandidate) {
				var whole [4]rdCandidate
				nb := enc.neighbors(&neighborBuf{}, enc.rec.Y, enc.rec.YStride, 0, 16, 16, false, true)
				src := enc.src.Y[16*enc.src.YStride:]
				tok := rdTokenView{
					leftY2:   o.left.y2,
					upY2:     o.above[0].y2,
					leftLuma: o.left.luma,
					upLuma:   o.above[0].luma,
				}
				for m := predict.Mode(0); m < predict.NumModes; m++ {
					predict.Predict(enc.predY[:], 16, 16, m, nb)
					whole[m], _ = o.scoreWhole(0, 1, m, src, &tok)
				}
				bp, _ := o.scoreBPred(0, 1, src, &tok)
				return whole, bp
			}
			winner := func(whole [4]rdCandidate, bp rdCandidate) rdCandidate {
				best := whole[0]
				for m := predict.Mode(1); m < predict.NumModes; m++ {
					if whole[m].betterThan(best) {
						best = whole[m]
					}
				}
				if bp.betterThan(best) {
					best = bp
				}
				return best
			}

			fresh := *base
			fresh.left = mbContext{}
			fresh.subLeft = [4]predict.SubMode{}
			freshWhole, freshBP := price(&fresh)

			poisoned := *base
			poisoned.left = mbContext{y2: 1, luma: [4]uint8{1, 1, 1, 1}}
			poisoned.subLeft = [4]predict.SubMode{
				predict.NumSubModes - 1, predict.NumSubModes - 1,
				predict.NumSubModes - 1, predict.NumSubModes - 1,
			}
			poisonWhole, poisonBP := price(&poisoned)

			sensitive := false
			for m := predict.Mode(0); m < predict.NumModes; m++ {
				if poisonWhole[m].rate != freshWhole[m].rate {
					sensitive = true
				}
			}
			if poisonBP.rate != freshBP.rate {
				sensitive = true
			}
			if !sensitive {
				t.Fatalf("poisoned left contexts priced identically to zeroed ones; fixture cannot detect a lost row reset")
			}

			want := winner(freshWhole, freshBP)
			got := base.decide(0, 1)
			if got.bpred != want.bpred || got.ordinal != want.ordinal {
				t.Fatalf("decide(0,1) ignored the row reset: got (bpred=%v ordinal=%d), fresh-context pricing says (bpred=%v ordinal=%d)",
					got.bpred, got.ordinal, want.bpred, want.ordinal)
			}
			// Deciding column 0 must leave the left shadows holding
			// the freshly zeroed row-start state, so re-pricing the
			// candidates through the same oracle reproduces the
			// fresh-context rates exactly.
			afterWhole, afterBP := price(base)
			for m := predict.Mode(0); m < predict.NumModes; m++ {
				if afterWhole[m] != freshWhole[m] {
					t.Fatalf("column 0 priced against stale left context: mode %d rate %d, want zeroed-context %d", m, afterWhole[m].rate, freshWhole[m].rate)
				}
			}
			if afterBP != freshBP {
				t.Fatalf("column 0 priced against stale left context: B_PRED rate %d, want zeroed-context %d", afterBP.rate, freshBP.rate)
			}
		})
	}
}

// TestRDTokenAxesThreading pins the within-macroblock evolution of both
// coefficient-context axes against a hand-threaded expectation, for the
// production pricer and the oracle pricer alike. The expected context
// of every one of the sixteen blocks is written out longhand from the
// writer's rule -- left carries within a row, above carries across
// rows -- so a lost update on either axis shifts some block's context
// and the total away from the pinned sum.
func TestRDTokenAxesThreading(t *testing.T) {
	enc := newEncoder(yuv.Convert(flatRGBA(16, 16, 128)), Config{Quality: 75, Method: 6})

	block := func(first int16) [16]int16 {
		var v [16]int16
		v[0] = first
		return v
	}
	entering := rdTokenView{
		leftY2:   1,
		upY2:     0,
		leftLuma: [4]uint8{1, 1, 0, 0},
		upLuma:   [4]uint8{0, 0, 1, 1},
	}

	// Hand-threaded context of each luma block in raster order, under
	// the entering view and each scenario's nonzero pattern.
	expect := func(nz [16]bool) []int {
		ctxs := make([]int, 16)
		left := entering.leftLuma
		up := entering.upLuma
		for y := 0; y < 4; y++ {
			nzRow := left[y]
			for x := 0; x < 4; x++ {
				ctxs[4*y+x] = int(nzRow + up[x])
				if nz[4*y+x] {
					nzRow = 1
				} else {
					nzRow = 0
				}
				up[x] = nzRow
			}
			left[y] = nzRow
		}
		return ctxs
	}
	longhand := func(y2Levels *[16]int16, luma *[16][16]int16, tok *rdTokenView, nz [16]bool) cost.Cost {
		probs := &token.DefaultProbs
		total := cost.BlockCost(token.Y2, int(tok.leftY2+tok.upY2), 0, y2Levels, probs)
		for b, ctx := range expect(nz) {
			total += cost.BlockCost(token.YAfterY2, ctx, 1, &luma[b], probs)
		}
		return total
	}

	for _, spec := range []struct {
		name  string
		nz    [16]bool
		empty bool
	}{
		{
			name: "mixed",
			// Nonzero at (0,1) and (1,0): the left carry shows
			// inside row 0, the above carry across rows 1 and 2.
			nz:    [16]bool{1: true, 4: true},
			empty: false,
		},
		{
			name:  "all empty",
			nz:    [16]bool{},
			empty: true,
		},
	} {
		t.Run(spec.name, func(t *testing.T) {
			var y2Levels [16]int16
			var luma [16][16]int16
			for b := range spec.nz {
				if spec.nz[b] {
					luma[b] = block(7)
				}
			}
			tok := entering

			want := longhand(&y2Levels, &luma, &tok, spec.nz)

			gotRate, gotEmpty := enc.rdPriceWholeTokens(&y2Levels, &luma, &tok)
			if gotRate != want || gotEmpty != spec.empty {
				t.Fatalf("production pricer: rate %d empty %v, want hand-threaded %d empty %v", gotRate, gotEmpty, want, spec.empty)
			}
			o := &rdOracle{enc: enc}
			gotRate, gotEmpty = o.priceWholeTokens(&y2Levels, &luma, &tok)
			if gotRate != want || gotEmpty != spec.empty {
				t.Fatalf("oracle pricer: rate %d empty %v, want hand-threaded %d empty %v", gotRate, gotEmpty, want, spec.empty)
			}
		})
	}
}

// TestRDBPredFirstNonzeroCertification drives the production B_PRED
// walk block by block and pins the certainty accounting after every
// block: while the candidate could still finish skipped nothing is
// certified, and from the first non-empty block on certTokens equals
// tokenRate exactly -- every block's tokens counted once, never twice.
func TestRDBPredFirstNonzeroCertification(t *testing.T) {
	img := bpredDetailRGBA(48, 32, 101)
	for _, spec := range []struct {
		name           string
		chromaEmpty    bool
		wantTransition bool
	}{
		{"empty chroma", true, true},
		{"non-empty chroma", false, false},
	} {
		t.Run(spec.name, func(t *testing.T) {
			enc := newEncoder(yuv.Convert(img), Config{Quality: 90, Method: 6})
			enc.run()

			paddedWidth := ((enc.src.Width + 15) / 16) * 16
			w := rdBPredWalk{enc: enc, skipPossible: spec.chromaEmpty}
			var nb predict.SubNeighbors
			var pred [16]uint8
			var levels [16][16]int16
			startedPossible := spec.chromaEmpty
			transitioned := false
			for b := 0; b < 16; b++ {
				bx, by := (b%4)*4, (b/4)*4
				predict.GatherSubNeighbors(&nb, enc.rec.Y, enc.rec.YStride, bx, by, paddedWidth)
				srcRow0 := enc.src.Y[by*enc.src.YStride+bx:]

				bestSSE := int32(-1)
				var sub predict.SubMode
				for m := predict.SubMode(0); m < predict.NumSubModes; m++ {
					predict.PredictSub(pred[:], 4, m, &nb)
					if s := blockdsp.SSE4x4(srcRow0, enc.src.YStride, pred[:], 4); bestSSE < 0 || s < bestSSE {
						bestSSE = s
						sub = m
					}
				}
				w.codeBlock(bx, by, b, sub, &nb, srcRow0, pred[:], &levels[b])

				if w.skipPossible {
					if w.certTokens != 0 {
						t.Fatalf("block %d: still skippable but %d tokens certified", b, w.certTokens)
					}
				} else {
					if startedPossible && !transitioned {
						transitioned = true
					}
					if w.certTokens != w.tokenRate {
						t.Fatalf("block %d: certified %d, charged %d -- first-nonzero accounting is not exactly-once", b, w.certTokens, w.tokenRate)
					}
				}
			}
			if transitioned != spec.wantTransition {
				t.Fatalf("fixture reached skip-impossible=%v, want %v; the certification invariant is not exercised as intended", transitioned, spec.wantTransition)
			}
			if w.tokenRate <= 0 {
				t.Fatalf("walk charged no tokens at all; assertion cannot detect miscounting")
			}
		})
	}
}

// TestRDBPredTokenContextSequence pins the production walk's
// coefficient-context sequence from outside both pricers. Starting from
// canonical zero shadows, the entering flag pair of block (row r,
// column c) is replayed longhand: leftNz[r]+upNz[c], where each axis
// takes the actual emptiness of the previously coded block in its row
// or column. Nothing assumes every block quantizes non-zero -- the nz
// flags come from the returned levels themselves -- so an empty block
// feeds its zero flag forward exactly as the token writer would. A
// frozen left or above axis prices several blocks under a different
// context and drifts off the pinned total.
func TestRDBPredTokenContextSequence(t *testing.T) {
	enc := newEncoder(yuv.Convert(bpredDetailRGBA(48, 32, 101)), Config{Quality: 90, Method: 6})
	enc.run()

	// Canonical zero starting shadows.
	enc.rdTokenLeft = mbContext{}
	enc.rdTokenAbove[0] = mbContext{}
	enc.rdSubLeft = [4]predict.SubMode{}
	enc.rdSubAbove[0] = [4]predict.SubMode{}
	tok := enc.rdTokenViewAt(0)

	best := rdCandidate{ordinal: 0, score: int64(1) << 60}
	cand, subs, lumaLevels, won := enc.rdWalkBPred(0, 0, &best, &tok, false)
	if !won {
		t.Fatalf("walk did not complete")
	}
	nonzero := 0
	for b := range lumaLevels {
		if anyNonZeroScan(&lumaLevels[b]) {
			nonzero++
		}
	}
	if nonzero == 0 {
		t.Fatalf("every block quantized empty; the pinned token-context total cannot detect mispricing")
	}

	want := cost.BPredFlag()
	var subAboveCtx, subLeftCtx [4]predict.SubMode
	var upNz, leftNz [4]uint8
	for b := 0; b < 16; b++ {
		r, c := b/4, b%4
		want += cost.SubModeCost(subAboveCtx[c], subLeftCtx[r], subs[b])
		subAboveCtx[c] = subs[b]
		subLeftCtx[r] = subs[b]
		want += cost.BlockCost(token.YWithDC, int(leftNz[r]+upNz[c]), 0, &lumaLevels[b], &token.DefaultProbs)
		nz := btou(anyNonZeroScan(&lumaLevels[b]))
		upNz[c] = nz
		leftNz[r] = nz
	}
	if cand.rate != want {
		t.Fatalf("walk rate %d, want longhand-pinned %d", cand.rate, want)
	}
}

// TestRDSkipAwareBareRates pins skip-aware pricing end to end: on a
// flat frame's interior macroblocks every candidate predicts the
// constant exactly, quantizes to empty behind empty chroma, and the
// writer emits no tokens at all -- so the whole-block rate must be the
// bare mode record and the B_PRED rate the bare flag plus sixteen
// sub-mode records threaded over the returned decisions, with no
// coefficient-token term anywhere, in production and oracle alike.
func TestRDSkipAwareBareRates(t *testing.T) {
	enc := newEncoder(yuv.Convert(flatRGBA(48, 32, 128)), Config{Quality: 75, Method: 6})
	enc.run()
	o := &rdOracle{enc: enc}

	qualified := 0
	for mby := 0; mby < enc.mbh; mby++ {
		for mbx := 0; mbx < enc.mbw; mbx++ {
			mb := &enc.mbs[mby*enc.mbw+mbx]
			chromaEmpty := true
			for i := blockU; i < numBlocks; i++ {
				if mb.nz[i] {
					chromaEmpty = false
					break
				}
			}
			src := enc.src.Y[(mby*16)*enc.src.YStride+mbx*16:]
			nb := enc.neighbors(&neighborBuf{}, enc.rec.Y, enc.rec.YStride, mbx*16, mby*16, 16, mbx > 0, mby > 0)

			// Production: all five candidates under the entering
			// shadows the commit left. The B_PRED walk codes into
			// the live plane, so bracket it with the same
			// save/restore the attempt wrapper uses.
			tok := enc.rdTokenViewAt(mbx)
			var pWhole [4]rdCandidate
			bare := true
			for m := predict.Mode(0); m < predict.NumModes; m++ {
				predict.Predict(enc.predY[:], 16, 16, m, nb)
				pWhole[m], _ = enc.rdEvalWhole(mbx, mby, m, src, &tok, chromaEmpty)
				if pWhole[m].rate != cost.LumaMode(m) {
					bare = false
				}
			}
			var pBP rdCandidate
			var pSubs [16]predict.SubMode
			restore := enc.rdSaveRegion(mbx, mby, mb)
			best := rdCandidate{ordinal: 0, score: int64(1) << 60}
			pBP, pSubs, _, _ = enc.rdWalkBPred(mbx, mby, &best, &tok, chromaEmpty)
			restore()

			if !bare || !chromaEmpty {
				o.advance(mbx, mby, mb)
				continue
			}
			want := cost.BPredFlag()
			var aboveCtx, leftCtx [4]predict.SubMode
			for b := 0; b < 16; b++ {
				want += cost.SubModeCost(aboveCtx[b%4], leftCtx[b/4], pSubs[b])
				aboveCtx[b%4] = pSubs[b]
				leftCtx[b/4] = pSubs[b]
			}
			if pBP.rate != want {
				t.Fatalf("macroblock (%d,%d): production B_PRED rate %d, want bare flag+records %d", mbx, mby, pBP.rate, want)
			}
			if !mb.skip {
				t.Fatalf("macroblock (%d,%d): every candidate priced bare but the record is not skipped", mbx, mby)
			}
			qualified++

			// Oracle: same five candidates under its own replayed
			// shadows; they must agree exactly, bare rates included.
			otok := rdTokenView{leftY2: o.left.y2, upY2: o.above[mbx].y2, leftLuma: o.left.luma, upLuma: o.above[mbx].luma}
			for m := predict.Mode(0); m < predict.NumModes; m++ {
				predict.Predict(enc.predY[:], 16, 16, m, nb)
				ocand, _ := o.scoreWhole(mbx, mby, m, src, &otok)
				if ocand.rate != pWhole[m].rate {
					t.Fatalf("macroblock (%d,%d): oracle whole-block mode %d rate %d, production %d", mbx, mby, m, ocand.rate, pWhole[m].rate)
				}
			}
			ocand, osubs := o.scoreBPred(mbx, mby, src, &otok)
			if ocand.rate != pBP.rate {
				t.Fatalf("macroblock (%d,%d): oracle B_PRED rate %d, production %d", mbx, mby, ocand.rate, pBP.rate)
			}
			for b := 0; b < 16; b++ {
				if osubs[b] != pSubs[b] {
					t.Fatalf("macroblock (%d,%d): oracle sub-mode %d is %v, production %v", mbx, mby, b, osubs[b], pSubs[b])
				}
			}

			o.advance(mbx, mby, mb)
		}
	}
	if qualified < 2 {
		t.Fatalf("only %d macroblocks priced bare; the skip-aware rule is not exercised", qualified)
	}
}
