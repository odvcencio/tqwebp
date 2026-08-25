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
// refuses to produce. The walk splits across cohesive helpers: the
// container layout (framePartitions), the frame header (replayHeader),
// the per-macroblock prediction records (replayMacroblocks), and the
// token partition (replayTokens).
func replayFrame(t *testing.T, enc *encoder, data []byte) {
	t.Helper()

	fp, tp := framePartitions(t, data)

	var stream acc // reference price of everything the streams carry
	dec := boolenc.NewDecoder(fp)
	skipProb, model, probs := replayHeader(t, &stream, dec, enc)
	model += replayMacroblocks(t, &stream, dec, enc, skipProb)

	if got := stream.total; got != int64(model) {
		t.Fatalf("first partition: stream prices %d, cost model charges %d", got, int64(model))
	}
	checkPartitionZeroBytes(t, model, len(fp))

	replayTokens(t, enc, tp, &probs)
}

// framePartitions splits the encoded file into the first partition and
// the token partition. The file is a RIFF container: 12-byte RIFF/WEBP
// header, then a "VP8 " chunk header carrying the payload length.
func framePartitions(t *testing.T, data []byte) (fp, tp []byte) {
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" || string(data[12:16]) != "VP8 " {
		t.Fatal("file does not carry the simple lossy RIFF layout the tests assume")
	}
	payloadLen := uint32(data[16]) | uint32(data[17])<<8 | uint32(data[18])<<16 | uint32(data[19])<<24
	frame := data[20 : 20+payloadLen]

	size := uint32(frame[0]>>5)&7 | uint32(frame[1])<<3 | uint32(frame[2])<<11
	return frame[10 : 10+size], frame[10+size:]
}

// replayHeader walks the frame header, mirroring frame.WriteHeader, and
// asserts every parsed field against the encoder's own state. It returns
// the signalled skip probability, the header's model price, and the
// coefficient-probability table the token partition uses.
func replayHeader(t *testing.T, a *acc, dec *boolenc.Decoder, enc *encoder) (skipProb uint8, model cost.Cost, probs token.Probs) {
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
	probs, updateModel := replayProbUpdates(a, dec)
	expectedProbs := token.DefaultProbs
	if enc.cfg.Method >= minProbOptMethod {
		var optimized *token.Probs
		if enc.probOptimizationDone {
			optimized = enc.frozenTokenProbs
		} else {
			optimized = enc.optimizeTokenProbs()
		}
		if optimized != nil {
			expectedProbs = *optimized
		}
	}
	if probs != expectedProbs {
		t.Fatal("parsed coefficient probabilities disagree with the encoder's frozen table")
	}
	if inUse := a.d(dec, 128); !inUse {
		t.Fatal("skip flag must be signalled in use")
	}
	skipProb = uint8(a.lit(dec, 8))

	// Header model: the same decisions priced by the public API.
	model = cost.PlainBit*12 /* fixed flags */ +
		cost.Literal(6+3+2+7+8) /* literals incl. skip prob */ +
		updateModel

	assertHeaderFields(t, enc, filterSimple, filterLevel, sharpness, quantIndex, skipProb)
	return skipProb, model, probs
}

// replayProbUpdates reads the coefficient-probability update grid and
// independently prices it while rebuilding the table carried by the header.
func replayProbUpdates(a *acc, dec *boolenc.Decoder) (probs token.Probs, model cost.Cost) {
	probs = token.DefaultProbs
	for i := 0; i < token.NumPlanes; i++ {
		for j := 0; j < token.NumBands; j++ {
			for k := 0; k < token.NumContexts; k++ {
				for l := 0; l < token.NumProbs; l++ {
					updateProb := token.UpdateProbs[i][j][k][l]
					upd := a.d(dec, updateProb)
					model += cost.ProbUpdateCost(updateProb, upd)
					if upd {
						probs[i][j][k][l] = uint8(a.lit(dec, 8))
					}
				}
			}
		}
	}
	return probs, model
}

// assertHeaderFields checks the parsed header fields against the
// encoder's own state.
func assertHeaderFields(t *testing.T, enc *encoder, filterSimple bool, filterLevel, sharpness, quantIndex uint32, skipProb uint8) {
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
}

// checkPartitionZeroBytes holds the partition-zero byte estimate within a
// few percent of the actual first-partition length.
func checkPartitionZeroBytes(t *testing.T, model cost.Cost, fpLen int) {
	est := cost.PartitionZeroBytes(model)
	slack := 8 + fpLen/20
	if diff := est - fpLen; diff < -slack || diff > slack {
		t.Errorf("first partition: byte estimate %d vs actual %d (slack %d)", est, fpLen, slack)
	}
}

// replayMacroblocks walks every macroblock's prediction record, mirroring
// frameBytes, and returns the records' model price.
func replayMacroblocks(t *testing.T, a *acc, dec *boolenc.Decoder, enc *encoder, skipProb uint8) cost.Cost {
	var leftSub [4]predict.SubMode
	for i := range enc.subCtxAbove {
		enc.subCtxAbove[i] = [4]predict.SubMode{}
	}
	var model cost.Cost
	for mby := 0; mby < enc.mbh; mby++ {
		leftSub = [4]predict.SubMode{}
		for mbx := 0; mbx < enc.mbw; mbx++ {
			mb := &enc.mbs[mby*enc.mbw+mbx]
			model += replayMacroblock(t, a, dec, mb, &enc.subCtxAbove[mbx], &leftSub, mbx, mby, skipProb)
		}
	}
	return model
}

// replayMacroblock reads one macroblock's prediction record -- skip flag,
// luma mode or sixteen sub-modes, chroma mode -- asserting every decision
// against the encoder's record.
func replayMacroblock(t *testing.T, a *acc, dec *boolenc.Decoder, mb *macroblock, aboveSub, leftSub *[4]predict.SubMode, mbx, mby int, skipProb uint8) cost.Cost {
	var model cost.Cost

	skip := a.d(dec, skipProb)
	if skip != mb.skip {
		t.Fatalf("macroblock (%d,%d): parsed skip %v, record says %v", mbx, mby, skip, mb.skip)
	}
	model += cost.SkipCost(skipProb, mb.skip)

	if mb.bpred {
		model += replayBPredSubModes(t, a, dec, mb, aboveSub, leftSub, mbx, mby)
	} else {
		if flag := a.d(dec, use16x16Prob); !flag {
			t.Fatalf("macroblock (%d,%d): whole-block record parsed as B_PRED", mbx, mby)
		}
		m := readWholeBlockLumaMode(a, dec)
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

	uv := readChromaMode(a, dec)
	if uv != mb.uvMode {
		t.Fatalf("macroblock (%d,%d): parsed chroma mode %v, record %v", mbx, mby, uv, mb.uvMode)
	}
	model += cost.ChromaMode(uv)
	return model
}

// replayBPredSubModes reads the sixteen sub-modes of a B_PRED macroblock
// along the RFC tree, updating the neighbouring sub-mode contexts as the
// serializer does.
func replayBPredSubModes(t *testing.T, a *acc, dec *boolenc.Decoder, mb *macroblock, aboveSub, leftSub *[4]predict.SubMode, mbx, mby int) cost.Cost {
	if flag := a.d(dec, use16x16Prob); flag {
		t.Fatalf("macroblock (%d,%d): B_PRED record parsed as whole-block", mbx, mby)
	}
	var model cost.Cost
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
	return model
}

// readWholeBlockLumaMode walks the whole-block luma-mode tree.
func readWholeBlockLumaMode(a *acc, dec *boolenc.Decoder) predict.Mode {
	if !a.d(dec, lumaDCvsRestProb) {
		if !a.d(dec, lumaDCvsVProb) {
			return predict.DC
		}
		return predict.V
	}
	if !a.d(dec, lumaHvsTMProb) {
		return predict.H
	}
	return predict.TM
}

// readChromaMode walks the chroma-mode tree.
func readChromaMode(a *acc, dec *boolenc.Decoder) predict.Mode {
	if !a.d(dec, chromaDCProb) {
		return predict.DC
	}
	if !a.d(dec, chromaVProb) {
		return predict.V
	}
	if !a.d(dec, chromaHProb) {
		return predict.H
	}
	return predict.TM
}

// replayTokens walks the token partition, mirroring writeTokens, and
// holds the stream's independent price against the cost model's charge
// for the same records.
func replayTokens(t *testing.T, enc *encoder, tp []byte, probs *token.Probs) {
	dec2 := boolenc.NewDecoder(tp)
	var tokens acc
	above := make([]tokCtx, enc.mbw)
	var left tokCtx
	var modelTokens cost.Cost
	for mby := 0; mby < enc.mbh; mby++ {
		left = tokCtx{}
		for mbx := 0; mbx < enc.mbw; mbx++ {
			mb := &enc.mbs[mby*enc.mbw+mbx]
			modelTokens += replayMacroblockTokens(t, &tokens, dec2, mb, &above[mbx], &left, probs)
		}
	}
	if got := tokens.total; got != int64(modelTokens) {
		t.Fatalf("token partition: stream prices %d, cost model charges %d", got, int64(modelTokens))
	}
	if dec2.UnexpectedEOF() {
		t.Error("token decoder ran past its buffer")
	}
}

// replayMacroblockTokens reads one macroblock's coefficient tokens and
// returns their model price. A skipped macroblock carries no tokens but
// still resets the coefficient contexts, preserving the Y2 carry only
// for B_PRED macroblocks.
func replayMacroblockTokens(t *testing.T, tokens *acc, dec *boolenc.Decoder, mb *macroblock, up, left *tokCtx, probs *token.Probs) cost.Cost {
	if mb.skip {
		prevLeftY2, prevUpY2 := left.y2, up.y2
		*left = tokCtx{}
		*up = tokCtx{}
		if mb.bpred {
			left.y2, up.y2 = prevLeftY2, prevUpY2
		}
		return 0
	}

	lumaPlane, lumaFirst := token.YAfterY2, 1
	var model cost.Cost
	if !mb.bpred {
		model += replayY2Block(t, tokens, dec, mb, up, left, probs)
	} else {
		lumaPlane, lumaFirst = token.YWithDC, 0
	}
	model += replayLumaBlocks(t, tokens, dec, mb, up, left, lumaPlane, lumaFirst, probs)
	model += replayChromaBlocks(t, tokens, dec, mb, up, left, probs)
	return model
}

// replayY2Block reads the Y2 block of a whole-block macroblock.
func replayY2Block(t *testing.T, tokens *acc, dec *boolenc.Decoder, mb *macroblock, up, left *tokCtx, probs *token.Probs) cost.Cost {
	ctx := int(left.y2 + up.y2)
	var levels [16]int16
	gotNZ := readBlockLocal(tokens, dec, token.Y2, ctx, 0, probs, &levels)
	if gotNZ != mb.nz[blockY2] || levels != mb.levels[blockY2] {
		t.Fatal("Y2 block: parsed levels/nz disagree with the record")
	}
	model := cost.BlockCost(token.Y2, ctx, 0, &mb.levels[blockY2], probs)
	nz := uint8(btoi(gotNZ))
	left.y2, up.y2 = nz, nz
	return model
}

// replayLumaBlocks reads the sixteen luma blocks.
func replayLumaBlocks(t *testing.T, tokens *acc, dec *boolenc.Decoder, mb *macroblock, up, left *tokCtx, lumaPlane, lumaFirst int, probs *token.Probs) cost.Cost {
	var model cost.Cost
	for y := 0; y < 4; y++ {
		nz := left.luma[y]
		for x := 0; x < 4; x++ {
			ctx := int(nz) + int(up.luma[x])
			var levels [16]int16
			gotNZ := readBlockLocal(tokens, dec, lumaPlane, ctx, lumaFirst, probs, &levels)
			idx := blockLuma + 4*y + x
			if gotNZ != mb.nz[idx] || levels != mb.levels[idx] {
				t.Fatalf("luma block %d: parsed levels/nz disagree with the record", idx)
			}
			model += cost.BlockCost(lumaPlane, ctx, lumaFirst, &mb.levels[idx], probs)
			nz = uint8(btoi(gotNZ))
			up.luma[x] = nz
		}
		left.luma[y] = nz
	}
	return model
}

// replayChromaBlocks reads the U plane's blocks, then the V plane's.
func replayChromaBlocks(t *testing.T, tokens *acc, dec *boolenc.Decoder, mb *macroblock, up, left *tokCtx, probs *token.Probs) cost.Cost {
	var model cost.Cost
	model += replayChromaPlaneBlocks(t, tokens, dec, mb, token.UV, blockU, &left.u, &up.u, probs)
	model += replayChromaPlaneBlocks(t, tokens, dec, mb, token.UV, blockV, &left.v, &up.v, probs)
	return model
}

// replayChromaPlaneBlocks reads one chroma plane's four blocks.
func replayChromaPlaneBlocks(t *testing.T, tokens *acc, dec *boolenc.Decoder, mb *macroblock, plane, base int, l, u *[2]uint8, probs *token.Probs) cost.Cost {
	var model cost.Cost
	for y := 0; y < 2; y++ {
		nz := l[y]
		for x := 0; x < 2; x++ {
			ctx := int(nz) + int(u[x])
			var levels [16]int16
			gotNZ := readBlockLocal(tokens, dec, plane, ctx, 0, probs, &levels)
			idx := base + 2*y + x
			if gotNZ != mb.nz[idx] || levels != mb.levels[idx] {
				t.Fatalf("block %d: parsed levels/nz disagree with the record", idx)
			}
			model += cost.BlockCost(plane, ctx, 0, &mb.levels[idx], probs)
			nz = uint8(btoi(gotNZ))
			u[x] = nz
		}
		l[y] = nz
	}
	return model
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
