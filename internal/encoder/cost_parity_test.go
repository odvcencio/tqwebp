package encoder

import (
	"math"
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/predict"
	"m31labs.dev/tqwebp/internal/token"
	"m31labs.dev/tqwebp/internal/yuv"
)

// This file holds the WP-2 slice 3 integration evidence: a full-frame
// replay parser that walks the bytes the production pipeline emits --
// frame header, per-macroblock prediction records, coefficient tokens --
// through boolenc.Decoder while accumulating an independent float-based
// price for every decision it reads. It requires three things to hold at
// once:
//
//   - every symbol the parser reads equals the encoder's own record
//     (modes, sub-modes, skips, levels), so the parser tracks the real
//     syntax rather than a parallel fiction;
//   - the replayed stream's total price equals what internal/cost
//     charges for the same records through its public API, so the rate
//     model prices exactly what the writers emit;
//   - the partition-zero byte estimate stays within a few percent of
//     the actual first-partition length.
//
// Nothing here changes any production decision: the encoder runs
// untouched, and the assertions only observe its output.

// refUnit prices one decision independently of internal/cost: the ideal
// code length -log2(p/256) -- complemented for the true branch -- in
// 1/256-bit units, rounded to nearest. Floats are fine here; this is
// test-only code.
func refUnit(p uint8, bit bool) int64 {
	q := float64(p)
	if bit {
		q = float64(256 - int(p))
	}
	return int64(math.Round(-math.Log2(q/256.0) * 256.0))
}

// acc accumulates the reference price of a replayed stream.
type acc struct {
	total int64
}

func (a *acc) d(dec *boolenc.Decoder, p uint8) bool {
	b := dec.ReadBool(p)
	a.total += refUnit(p, b)
	return b
}

func (a *acc) lit(dec *boolenc.Decoder, n int) uint32 {
	v := dec.ReadLiteral(n)
	a.total += int64(n) * 256
	return v
}

// rfcBModeTree transcribes the key-frame sub-mode tree of RFC 6386
// section 11.4 for the parser's own walk. Negative entries are leaves in
// the RFC's numbering; rfcLeafToNative maps them onto native SubModes.
var rfcBModeTree = [...]int16{
	-0, 2,
	-1, 4,
	-2, 6,
	8, 12,
	-3, 10,
	-4, -5,
	-6, 14,
	-7, 16,
	-8, -9,
}

var rfcLeafToNative = [...]predict.SubMode{
	predict.BDC, predict.BTM, predict.BVE, predict.BHE,
	predict.BRD, predict.BVR, predict.BLD, predict.BVL,
	predict.BHD, predict.BHU,
}

// readSubModeLocal walks one sub-mode out of the stream along the RFC
// tree, charging the reference price of every decision.
func readSubModeLocal(a *acc, dec *boolenc.Decoder, row *[9]uint8) predict.SubMode {
	node := 0
	for {
		entry := rfcBModeTree[node]
		b := a.d(dec, uint8(row[node/2]))
		next := entry
		if b {
			next = rfcBModeTree[node+1]
		}
		if next <= 0 {
			return rfcLeafToNative[-next]
		}
		node = int(next)
	}
}

// readBlockLocal mirrors token.Writer.WriteBlock from the reading side:
// same probabilities, same tree, same order. It fills levels with what
// the stream carries, reports whether any coefficient appeared, and
// charges every decision to a.
func readBlockLocal(a *acc, dec *boolenc.Decoder, plane, ctx, first int, probs *token.Probs, levels *[16]int16) bool {
	for i := range levels {
		(*levels)[i] = 0
	}
	pp := &probs[plane]
	n := first
	p := &pp[token.Bands[n]][ctx]

	if !a.d(dec, p[0]) {
		return false
	}
	for n < 16 {
		if !a.d(dec, p[1]) {
			n++
			p = &pp[token.Bands[n]][0]
			continue
		}
		magIsOne := !a.d(dec, p[2])
		mag := 0
		switch {
		case magIsOne:
			mag = 1
		case !a.d(dec, p[3]):
			if !a.d(dec, p[4]) {
				mag = 2
			} else if a.d(dec, p[5]) {
				mag = 4
			} else {
				mag = 3
			}
		case !a.d(dec, p[6]):
			if !a.d(dec, p[7]) {
				if a.d(dec, 159) {
					mag = 6
				} else {
					mag = 5
				}
			} else {
				hi := a.d(dec, 165)
				lo := a.d(dec, 145)
				mag = 7
				if hi {
					mag += 2
				}
				if lo {
					mag++
				}
			}
		default:
			b1 := a.d(dec, p[8])
			b0 := a.d(dec, p[9+btoi(b1)])
			cat := int(btoi(b1))<<1 | int(btoi(b0))
			bits := [4]int{3, 4, 5, 11}[cat]
			base := 3 + (8 << uint(cat))
			rest := 0
			for i := 0; i < bits; i++ {
				if a.d(dec, token.ExtraProbs[cat][i]) {
					rest |= 1 << uint(bits-1-i)
				}
			}
			mag = base + rest
		}
		sign := a.d(dec, 128)
		v := int16(mag)
		if sign {
			v = -v
		}
		levels[n] = v

		n++
		if n == 16 {
			return true
		}
		nextCtx := 2
		if mag == 1 {
			nextCtx = 1
		}
		p = &pp[token.Bands[n]][nextCtx]
		if !a.d(dec, p[0]) {
			return true
		}
	}
	return true
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// tokCtx mirrors the serializer's coefficient-context state.
type tokCtx struct {
	y2   uint8
	luma [4]uint8
	u, v [2]uint8
}

// replayFrame parses one encoded frame end to end. It fails t unless the
// stream, the encoder's records, and the internal/cost model agree
// everywhere, and it returns nothing: all evidence is in the failures it
// refuses to produce.
func replayFrame(t *testing.T, enc *encoder, data []byte) {
	t.Helper()

	// The file is a RIFF container: 12-byte RIFF/WEBP header, then a
	// "VP8 " chunk header carrying the payload length.
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" || string(data[12:16]) != "VP8 " {
		t.Fatal("file does not carry the simple lossy RIFF layout the tests assume")
	}
	payloadLen := uint32(data[16]) | uint32(data[17])<<8 | uint32(data[18])<<16 | uint32(data[19])<<24
	frame := data[20 : 20+payloadLen]

	size := uint32(frame[0]>>5)&7 | uint32(frame[1])<<3 | uint32(frame[2])<<11
	fp := frame[10 : 10+size]
	tp := frame[10+size:]

	var stream acc // reference price of everything the streams carry
	dec := boolenc.NewDecoder(fp)

	// --- Frame header, mirroring frame.WriteHeader ---
	a := &stream
	a.d(dec, 128) // colour space
	a.d(dec, 128) // clamping type
	if seg := a.d(dec, 128); seg {
		t.Fatal("parser assumes segmentation off; production never enables it")
	}
	filterSimple := a.d(dec, 128)
	filterLevel := a.lit(dec, 6)
	sharpness := a.lit(dec, 3)
	a.d(dec, 128) // no filter deltas
	if parts := a.lit(dec, 2); parts != 0 {
		t.Fatalf("parser assumes one token partition, saw %d", parts)
	}
	quantIndex := a.lit(dec, 7)
	for i := 0; i < 5; i++ {
		a.d(dec, 128) // no quantizer deltas
	}
	if refresh := a.d(dec, 128); !refresh {
		t.Fatal("key frame must signal probability refresh")
	}
	updateDecisions := 0
	for i := 0; i < token.NumPlanes; i++ {
		for j := 0; j < token.NumBands; j++ {
			for k := 0; k < token.NumContexts; k++ {
				for l := 0; l < token.NumProbs; l++ {
					if upd := a.d(dec, token.UpdateProbs[i][j][k][l]); upd {
						a.lit(dec, 8)
						updateDecisions++
					}
				}
			}
		}
	}
	if updateDecisions != 0 {
		t.Fatalf("%d probability updates in a defaults-only frame", updateDecisions)
	}
	if inUse := a.d(dec, 128); !inUse {
		t.Fatal("skip flag must be signalled in use")
	}
	skipProb := uint8(a.lit(dec, 8))

	// Header model: the same decisions priced by the public API.
	model := cost.PlainBit*12 /* fixed flags */ +
		cost.Literal(6+3+2+7+8) /* literals incl. skip prob */
	for i := 0; i < token.NumPlanes; i++ {
		for j := 0; j < token.NumBands; j++ {
			for k := 0; k < token.NumContexts; k++ {
				for l := 0; l < token.NumProbs; l++ {
					model += cost.ProbUpdateCost(token.UpdateProbs[i][j][k][l], false)
				}
			}
		}
	}

	// Assert header fields against the encoder's own state.
	if !filterSimple {
		t.Error("header did not signal the simple filter the encoder writes")
	}
	if got := int(filterLevel); got != enc.filterLevel {
		t.Errorf("parsed filter level %d, encoder holds %d", got, enc.filterLevel)
	}
	if sharpness != 0 {
		t.Errorf("parsed sharpness %d, want 0", sharpness)
	}
	if got := int(quantIndex); got != int(enc.q.Index) {
		t.Errorf("parsed quantizer index %d, encoder holds %d", got, enc.q.Index)
	}
	if got, want := int(skipProb), int(enc.skipProbability()); got != want {
		t.Errorf("parsed skip probability %d, encoder signals %d", got, want)
	}

	// --- Per-macroblock prediction records, mirroring frameBytes ---
	var leftSub [4]predict.SubMode
	for i := range enc.subCtxAbove {
		enc.subCtxAbove[i] = [4]predict.SubMode{}
	}
	for mby := 0; mby < enc.mbh; mby++ {
		leftSub = [4]predict.SubMode{}
		for mbx := 0; mbx < enc.mbw; mbx++ {
			mb := &enc.mbs[mby*enc.mbw+mbx]
			aboveSub := &enc.subCtxAbove[mbx]

			skip := a.d(dec, skipProb)
			if skip != mb.skip {
				t.Fatalf("macroblock (%d,%d): parsed skip %v, record says %v", mbx, mby, skip, mb.skip)
			}
			model += cost.SkipCost(skipProb, mb.skip)

			if mb.bpred {
				if flag := a.d(dec, use16x16Prob); flag {
					t.Fatalf("macroblock (%d,%d): B_PRED record parsed as whole-block", mbx, mby)
				}
				model += cost.BPredFlag()
				for j := 0; j < 4; j++ {
					for i := 0; i < 4; i++ {
						got := readSubModeLocal(a, dec, &predict.KeyFrameSubModeProbs[aboveSub[i]][leftSub[j]])
						if got != mb.subModes[4*j+i] {
							t.Fatalf("macroblock (%d,%d) block %d: parsed sub-mode %v, record %v",
								mbx, mby, 4*j+i, got, mb.subModes[4*j+i])
						}
						model += cost.SubModeCost(aboveSub[i], leftSub[j], got)
						aboveSub[i] = got
						leftSub[j] = got
					}
				}
			} else {
				if flag := a.d(dec, use16x16Prob); !flag {
					t.Fatalf("macroblock (%d,%d): whole-block record parsed as B_PRED", mbx, mby)
				}
				var m predict.Mode
				if !a.d(dec, lumaDCvsRestProb) {
					if !a.d(dec, lumaDCvsVProb) {
						m = predict.DC
					} else {
						m = predict.V
					}
				} else {
					if !a.d(dec, lumaHvsTMProb) {
						m = predict.H
					} else {
						m = predict.TM
					}
				}
				if m != mb.yMode {
					t.Fatalf("macroblock (%d,%d): parsed luma mode %v, record %v", mbx, mby, m, mb.yMode)
				}
				model += cost.LumaMode(m)
				ctx := subModeContextOf(mb.yMode)
				for i := range leftSub {
					aboveSub[i] = ctx
					leftSub[i] = ctx
				}
			}

			var uv predict.Mode
			if !a.d(dec, chromaDCProb) {
				uv = predict.DC
			} else {
				if !a.d(dec, chromaVProb) {
					uv = predict.V
				} else {
					if !a.d(dec, chromaHProb) {
						uv = predict.H
					} else {
						uv = predict.TM
					}
				}
			}
			if uv != mb.uvMode {
				t.Fatalf("macroblock (%d,%d): parsed chroma mode %v, record %v", mbx, mby, uv, mb.uvMode)
			}
			model += cost.ChromaMode(uv)
		}
	}

	if got := stream.total; got != int64(model) {
		t.Fatalf("first partition: stream prices %d, cost model charges %d", got, int64(model))
	}
	est := cost.PartitionZeroBytes(model)
	slack := 8 + len(fp)/20
	if diff := est - len(fp); diff < -slack || diff > slack {
		t.Errorf("first partition: byte estimate %d vs actual %d (slack %d)", est, len(fp), slack)
	}

	// --- Token partition, mirroring writeTokens ---
	dec2 := boolenc.NewDecoder(tp)
	var tokens acc
	above := make([]tokCtx, enc.mbw)
	var left tokCtx
	var modelTokens cost.Cost
	readPlane := func(plane int, base int, l, u *[2]uint8, mb *macroblock) {
		for y := 0; y < 2; y++ {
			nz := l[y]
			for x := 0; x < 2; x++ {
				ctx := int(nz) + int(u[x])
				var levels [16]int16
				gotNZ := readBlockLocal(&tokens, dec2, plane, ctx, 0, &token.DefaultProbs, &levels)
				idx := base + 2*y + x
				if gotNZ != mb.nz[idx] || levels != mb.levels[idx] {
					t.Fatalf("block %d: parsed levels/nz disagree with the record", idx)
				}
				modelTokens += cost.BlockCost(plane, ctx, 0, &mb.levels[idx], &token.DefaultProbs)
				nz = uint8(btoi(gotNZ))
				u[x] = nz
			}
			l[y] = nz
		}
	}
	for mby := 0; mby < enc.mbh; mby++ {
		left = tokCtx{}
		for mbx := 0; mbx < enc.mbw; mbx++ {
			mb := &enc.mbs[mby*enc.mbw+mbx]
			up := &above[mbx]

			if mb.skip {
				prevLeftY2, prevUpY2 := left.y2, up.y2
				left = tokCtx{}
				*up = tokCtx{}
				if mb.bpred {
					left.y2, up.y2 = prevLeftY2, prevUpY2
				}
				continue
			}

			lumaPlane, lumaFirst := token.YAfterY2, 1
			if !mb.bpred {
				ctx := int(left.y2 + up.y2)
				var levels [16]int16
				gotNZ := readBlockLocal(&tokens, dec2, token.Y2, ctx, 0, &token.DefaultProbs, &levels)
				if gotNZ != mb.nz[blockY2] || levels != mb.levels[blockY2] {
					t.Fatal("Y2 block: parsed levels/nz disagree with the record")
				}
				modelTokens += cost.BlockCost(token.Y2, ctx, 0, &mb.levels[blockY2], &token.DefaultProbs)
				nz := uint8(btoi(gotNZ))
				left.y2, up.y2 = nz, nz
			} else {
				lumaPlane, lumaFirst = token.YWithDC, 0
			}

			for y := 0; y < 4; y++ {
				nz := left.luma[y]
				for x := 0; x < 4; x++ {
					ctx := int(nz) + int(up.luma[x])
					var levels [16]int16
					gotNZ := readBlockLocal(&tokens, dec2, lumaPlane, ctx, lumaFirst, &token.DefaultProbs, &levels)
					idx := blockLuma + 4*y + x
					if gotNZ != mb.nz[idx] || levels != mb.levels[idx] {
						t.Fatalf("luma block %d: parsed levels/nz disagree with the record", idx)
					}
					modelTokens += cost.BlockCost(lumaPlane, ctx, lumaFirst, &mb.levels[idx], &token.DefaultProbs)
					nz = uint8(btoi(gotNZ))
					up.luma[x] = nz
				}
				left.luma[y] = nz
			}

			readPlane(token.UV, blockU, &left.u, &up.u, mb)
			readPlane(token.UV, blockV, &left.v, &up.v, mb)
		}
	}
	if got := tokens.total; got != int64(modelTokens) {
		t.Fatalf("token partition: stream prices %d, cost model charges %d", got, int64(modelTokens))
	}
	if dec2.UnexpectedEOF() {
		t.Error("token decoder ran past its buffer")
	}
}

// TestCostModelMatchesEmittedSyntaxMixed runs the replay over a mixed
// method 6 frame: whole-block macroblocks, selected sixteen-block
// macroblocks, and skipped ones share the picture.
func TestCostModelMatchesEmittedSyntaxMixed(t *testing.T) {
	img := bpredMixedRGBA(48, 32, 77)
	enc, data, selected, err := encodeWithMethod(img, Config{Quality: 60, Method: 6})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if selected == 0 || selected == len(enc.mbs) {
		t.Fatalf("fixture lost its mix: %d of %d macroblocks selected", selected, len(enc.mbs))
	}
	replayFrame(t, enc, data)
}

// TestCostModelMatchesEmittedSyntaxForcedBPred runs the replay over a
// forced B_PRED frame: every macroblock codes sixteen sub-modes and
// YWithDC-plane tokens, exercising the other half of the grammar.
func TestCostModelMatchesEmittedSyntaxForcedBPred(t *testing.T) {
	img := bpredDetailRGBA(48, 32, 101)
	e := newEncoder(yuv.Convert(img), Config{Quality: 90})
	e.forceBPred = true
	e.run()
	var buf writerBuffer
	if err := e.writeFile(&buf); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	replayFrame(t, e, buf.data)
}
