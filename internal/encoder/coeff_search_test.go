package encoder

// This file is work package WP-2 slice 5B's integration tests: they
// pin the coefficient-candidate search into the Method 6 B_PRED walk
// of rd_select.go from the outside. The pure helper tests live in
// coeff_opt_test.go; everything here drives whole encodes or the real
// walk and checks observable outcomes -- bytes, counters, decisions,
// decoder-visible reconstruction.

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

// encodeMethod runs one full encode and returns the file bytes and the
// frame's statistics. Optional tuners adjust encoder fields -- such as
// test-only overrides -- between construction and the run.
func encodeMethod(t *testing.T, img image.Image, cfg Config, tune ...func(*encoder)) ([]byte, rdStats) {
	t.Helper()
	enc := newEncoder(yuv.Convert(img), cfg)
	for _, f := range tune {
		f(enc)
	}
	enc.run()
	var buf writerBuffer
	if err := enc.writeFile(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	return buf.data, enc.rd
}

// TestCoeffSearchDisabledEqualsMethod5 pins the effort boundary from
// both sides: a Method 6 encode whose search is test-disabled writes
// exactly a Method 5 encode's bytes and reports exactly its counters,
// proving the disabled path never diverges from the retained-level
// writer.
func TestCoeffSearchDisabledEqualsMethod5(t *testing.T) {
	for _, spec := range []struct {
		name string
		img  image.Image
		q    int
	}{
		{"mixed", bpredMixedRGBA(48, 32, 77), 75},
		{"detail", bpredDetailRGBA(48, 32, 101), 90},
		{"flat", flatRGBA(33, 17, 200), 50},
	} {
		t.Run(spec.name, func(t *testing.T) {
			m5Data, m5Stats := encodeMethod(t, spec.img, Config{Quality: spec.q, Method: 5})
			m6OffData, m6OffStats := encodeMethod(t, spec.img, Config{Quality: spec.q, Method: 6},
				func(e *encoder) { e.rdCoeffOptOff = true })
			if !bytes.Equal(m5Data, m6OffData) {
				t.Fatalf("disabled Method 6 wrote different bytes: %d vs %d", len(m5Data), len(m6OffData))
			}
			if m5Stats != m6OffStats {
				t.Fatalf("disabled Method 6 reported different stats:\n %+v\n %+v", m5Stats, m6OffStats)
			}
			if m5Stats.CoeffBlocksSearched != 0 || m5Stats.CoeffCandidatesScored != 0 || m5Stats.CoeffBlocksChanged != 0 {
				t.Fatalf("Method 5 ran the coefficient search: %+v", m5Stats)
			}
		})
	}
}

// TestCoeffSearchEnabledChangesBlocks proves the enabled search does
// real work on deterministic fixtures: blocks are searched, candidates
// are scored, and at least one block's winning levels differ from the
// retained ones -- while every counter keeps its structural invariants.
func TestCoeffSearchEnabledChangesBlocks(t *testing.T) {
	for _, spec := range []struct {
		name string
		img  image.Image
		q    int
	}{
		{"mixed", bpredMixedRGBA(48, 32, 77), 75},
		{"detail", bpredDetailRGBA(64, 48, 101), 90},
	} {
		t.Run(spec.name, func(t *testing.T) {
			_, s := encodeMethod(t, spec.img, Config{Quality: spec.q, Method: 6})
			if s.CoeffBlocksSearched == 0 {
				t.Fatalf("searched no blocks: %+v", s)
			}
			if s.CoeffBlocksChanged == 0 {
				t.Fatalf("no block changed coefficients: %+v", s)
			}
			if s.CoeffCandidatesScored < s.CoeffBlocksSearched {
				t.Fatalf("scored %d candidates for %d searches", s.CoeffCandidatesScored, s.CoeffBlocksSearched)
			}
			if s.CoeffBlocksChanged > s.CoeffBlocksSearched {
				t.Fatalf("%d changed blocks exceed %d searched", s.CoeffBlocksChanged, s.CoeffBlocksSearched)
			}
			// Every attempted pass codes between 1 and 16 blocks;
			// committed passes code exactly sixteen.
			if s.CoeffBlocksSearched > 16*s.BpredAttempts || s.CoeffBlocksSearched < 16*s.DecisionsBPred {
				t.Fatalf("searched %d outside [%d,%d] attempts=%d decisions=%d",
					s.CoeffBlocksSearched, 16*s.DecisionsBPred, 16*s.BpredAttempts, s.BpredAttempts, s.DecisionsBPred)
			}
			// The enumeration scores at most maxCoeffCandidates-1
			// unique vectors per search (see coeff_opt.go).
			if s.CoeffCandidatesScored > 48*s.CoeffBlocksSearched {
				t.Fatalf("scored %d candidates for %d searches, above the enumeration bound",
					s.CoeffCandidatesScored, s.CoeffBlocksSearched)
			}
		})
	}
}

// TestCoeffWinnerIsIndependentArgmin replays the committed B_PRED
// macroblock of a single-macroblock Method 6 encode and re-derives,
// with fully independent arithmetic, every block's winning levels. It
// requires each changed winner to belong to enumerateCoeffCandidates'
// list and to be the exact minimizer of spatial SSE times 256 plus
// lambda times exact BlockCost under the entering context, ties going
// to the earliest candidate.
func TestCoeffWinnerIsIndependentArgmin(t *testing.T) {
	img := bpredMixedRGBA(16, 16, 77)
	const quality = 75

	enc := newEncoder(yuv.Convert(img), Config{Quality: quality, Method: 6})
	enc.run()
	mb := &enc.mbs[0]
	if !mb.bpred {
		t.Fatal("fixture's only macroblock did not choose B_PRED")
	}

	chromaEmpty := true
	for i := blockU; i < numBlocks; i++ {
		if mb.nz[i] {
			chromaEmpty = false
			break
		}
	}

	restore := enc.rdSaveRegion(0, 0, mb)
	hugeBest := rdCandidate{ordinal: 0, score: int64(1) << 62}
	tok := rdTokenView{}
	cand, subs, lumaLevels, completed := enc.rdWalkBPred(0, 0, &hugeBest, &tok, chromaEmpty)
	restore()
	if !completed {
		t.Fatal("replay did not complete")
	}
	if cand.ordinal != rdOrdinalBPred || !cand.bpred {
		t.Fatalf("replay lost the B_PRED candidate: %+v", cand)
	}

	paddedWidth := ((enc.src.Width + 15) / 16) * 16
	var leftLuma, upLuma [4]uint8 // MB 0 enters with cleared token flags
	changed := 0
	for b := 0; b < 16; b++ {
		bx, by := (b%4)*4, (b/4)*4

		// Retained levels, rebuilt independently from the replayed
		// sub-mode and the reconstruction border the walk saw.
		var nb predict.SubNeighbors
		predict.GatherSubNeighbors(&nb, enc.rec.Y, enc.rec.YStride, bx, by, paddedWidth)
		srcRow0 := enc.src.Y[by*enc.src.YStride+bx:]
		var pred [16]uint8
		predict.PredictSub(pred[:], 4, subs[b], &nb)
		var residual [16]int16
		for y := 0; y < 4; y++ {
			srcRow := srcRow0[y*enc.src.YStride:]
			predRow := pred[y*4:]
			for x := 0; x < 4; x++ {
				residual[y*4+x] = int16(srcRow[x]) - int16(predRow[x])
			}
		}
		coeff := blockdsp.FDCT4x4(&residual)
		retainedRaster := quantizeBlock(&coeff, enc.q.Y1)
		retained := toScanOrder(&retainedRaster)

		ctx := int(leftLuma[b/4] + upLuma[b%4])

		// Fully independent scoring: raster conversion straight off
		// the zig-zag table, dequantize, inverse transform, clamp,
		// squared error against src, exact BlockCost rate.
		scoreOf := func(levels *[16]int16) int64 {
			var raster [16]int16
			for i, pos := range blockdsp.ZigZag {
				raster[pos] = levels[i]
			}
			deq := blockdsp.DequantizeBlock(&raster, enc.q.Y1.DC, enc.q.Y1.AC)
			resOut := blockdsp.IDCT4x4(&deq)
			var sse int64
			for y := 0; y < 4; y++ {
				srcRow := srcRow0[y*enc.src.YStride:]
				predRow := pred[y*4:]
				resRow := resOut[y*4:]
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
			rate := cost.BlockCost(token.YWithDC, ctx, 0, levels, &token.DefaultProbs)
			return sse<<8 + int64(rate)*enc.lambda
		}

		winner := lumaLevels[b]
		cands, _ := enumerateCoeffCandidates(&retained)

		bestScore := int64(1) << 62
		bestIdx := -1
		for i := range cands {
			if score := scoreOf(&cands[i]); score < bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestIdx < 0 || winner != cands[bestIdx] {
			t.Fatalf("block %d: walk kept %v, independent argmin is %v (ordinal %d)",
				b, winner, cands[bestIdx], bestIdx)
		}
		if winner != retained {
			changed++
		}
		leftLuma[b/4] = btou(anyNonZeroScan(&winner))
		upLuma[b%4] = btou(anyNonZeroScan(&winner))
	}
	if changed == 0 {
		t.Fatal("fixture exercised no changed block; argmin check is vacuous")
	}
}

// TestCoeffSearchDecoderEquality requires the independent decoder to
// reproduce the encoder's reconstruction planes exactly when the
// slice 5B search picked the levels: whatever the winners are, they
// are codable tokens and the bitstream stays self-consistent.
func TestCoeffSearchDecoderEquality(t *testing.T) {
	for _, spec := range []struct {
		name string
		img  image.Image
		q    int
	}{
		{"mixed q75", bpredMixedRGBA(48, 32, 77), 75},
		{"detail q90", bpredDetailRGBA(64, 48, 101), 90},
		{"mixed q35", bpredMixedRGBA(33, 17, 5), 35},
	} {
		t.Run(spec.name, func(t *testing.T) {
			data, recon, err := encodeWithReconstruction(spec.img, Config{Quality: spec.q, Method: 6})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			decoded, err := oracle.DecodeWebPPlanes(data)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if err := oracle.CompareExact(reconSource{recon}, decoded); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestCoeffSearchDeterminism repeats a Method 6 encode across
// GOMAXPROCS settings and requires byte-identical files and identical
// counters, including the coefficient-search bookkeeping.
func TestCoeffSearchDeterminism(t *testing.T) {
	img := bpredMixedRGBA(33, 17, 5)
	type result struct {
		data  []byte
		stats rdStats
	}
	encode := func() result {
		data, stats := encodeMethod(t, img, Config{Quality: 60, Method: 6})
		return result{data: data, stats: stats}
	}

	oldMaxProcs := runtime.GOMAXPROCS(1)
	first := encode()
	runtime.GOMAXPROCS(4)
	second := encode()
	runtime.GOMAXPROCS(oldMaxProcs)

	if !bytes.Equal(first.data, second.data) {
		t.Fatalf("repeat encode differed: %d vs %d bytes", len(first.data), len(second.data))
	}
	if first.stats != second.stats {
		t.Fatalf("repeat counters differed:\n %+v\n %+v", first.stats, second.stats)
	}
	if first.stats.CoeffBlocksSearched == 0 || first.stats.CoeffBlocksChanged == 0 {
		t.Fatalf("determinism check ran on an unexercised fixture: %+v", first.stats)
	}
}

// TestCoeffSearchEffortBoundary walks the effort ladder and requires
// the coefficient search to stay silent below Method 6: methods 0
// through 5 report no searched blocks, no scored candidates, and no
// changed blocks, while Method 6 on the same fixture exercises all
// three counters.
func TestCoeffSearchEffortBoundary(t *testing.T) {
	img := bpredMixedRGBA(48, 32, 77)
	var prev []byte
	for method := 0; method <= 6; method++ {
		data, s := encodeMethod(t, img, Config{Quality: 75, Method: method})
		if method < 6 {
			if s.CoeffBlocksSearched != 0 || s.CoeffCandidatesScored != 0 || s.CoeffBlocksChanged != 0 {
				t.Fatalf("method %d ran the coefficient search: %+v", method, s)
			}
		} else if s.CoeffBlocksSearched == 0 || s.CoeffBlocksChanged == 0 {
			t.Fatalf("method 6 exercised no coefficient search: %+v", s)
		}
		if method >= 1 && method <= 4 && !bytes.Equal(data, prev) {
			t.Fatalf("method %d bytes differ from method %d", method, method-1)
		}
		prev = data
	}
}
