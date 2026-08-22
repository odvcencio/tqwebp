package encoder

import (
	"bytes"
	"fmt"
	"image"
	"reflect"
	"runtime"
	"testing"

	"m31labs.dev/tqwebp/internal/container"
	"m31labs.dev/tqwebp/internal/token"
	"m31labs.dev/tqwebp/internal/yuv"
	"m31labs.dev/tqwebp/oracle"
)

// TestRunFrameReconsidersUnderFrozenProbs pins the Slice 6A production
// walk of runFrame end to end on one known profitable Method 6 fixture:
// exactly one probability derivation, exactly one reconsideration pass,
// the derived table both frozen in the encoder state and installed as
// the active pricing table, and a serialization that is byte-stable
// across repeated calls without disturbing any of that state. The
// shipped frame must still decode, through the independent decoder, to
// pixels equal to the encoder's own reconstruction.
func TestRunFrameReconsidersUnderFrozenProbs(t *testing.T) {
	enc := newEncoder(yuv.Convert(bpredDetailRGBA(64, 48, 101)), Config{Quality: 90, Method: 6})

	enc.runFrame()

	// The refinement ran to completion and reconsidered the analysis:
	// probReconsiderations is 1 only when a derived table actually
	// shipped, so this assertion doubles as the profitability check --
	// if it fails here, bpredDetailRGBA(64, 48, 101) at Quality 90 no
	// longer yields strictly-profitable updates and cannot exercise
	// this path at all.
	if !enc.probOptimizationDone {
		t.Fatal("fixture not profitable: runFrame did not complete its probability derivation (probOptimizationDone false)")
	}
	if enc.probDerivations != 1 {
		t.Fatalf("probDerivations = %d, want exactly 1", enc.probDerivations)
	}
	if enc.probReconsiderations != 1 {
		t.Fatal("fixture not profitable: runFrame never re-ran under derived probabilities (probReconsiderations != 1)")
	}
	if enc.frozenTokenProbs == nil {
		t.Fatal("fixture not profitable: no table was frozen (frozenTokenProbs nil)")
	}
	if enc.rateProbs != enc.frozenTokenProbs {
		t.Fatal("rateProbs does not point at the frozen token probability table")
	}

	// Snapshot every piece of state the serialization must leave alone.
	frozen := enc.frozenTokenProbs
	done := enc.probOptimizationDone
	derivations := enc.probDerivations
	reconsiderations := enc.probReconsiderations
	rdBefore := enc.rd

	first, err := enc.frameBytes()
	if err != nil {
		t.Fatalf("first frameBytes: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("frameBytes produced an empty VP8 payload")
	}

	second, err := enc.frameBytes()
	if err != nil {
		t.Fatalf("second frameBytes: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("repeated frameBytes differ (%d vs %d bytes)", len(first), len(second))
	}

	if enc.frozenTokenProbs != frozen {
		t.Error("frameBytes replaced the frozen probability table")
	}
	if enc.rateProbs != frozen {
		t.Error("frameBytes changed the active rateProbs pointer")
	}
	if enc.probOptimizationDone != done {
		t.Errorf("probOptimizationDone changed from %v to %v", done, enc.probOptimizationDone)
	}
	if enc.probDerivations != derivations {
		t.Errorf("probDerivations changed from %d to %d", derivations, enc.probDerivations)
	}
	if enc.probReconsiderations != reconsiderations {
		t.Errorf("probReconsiderations changed from %d to %d", reconsiderations, enc.probReconsiderations)
	}
	if enc.rd != rdBefore {
		t.Errorf("rd stats changed across frameBytes: %+v -> %+v", rdBefore, enc.rd)
	}

	// Wrap the serialized VP8 key frame in its RIFF container and decode
	// it with the independent decoder; the picture must be equal to the
	// encoder's own reconstruction on every plane, byte for byte.
	var buf writerBuffer
	if err := container.WriteSimpleLossy(&buf, first); err != nil {
		t.Fatalf("container.WriteSimpleLossy: %v", err)
	}
	decoded, err := oracle.DecodeWebPPlanes(buf.data)
	if err != nil {
		t.Fatalf("oracle decode: %v", err)
	}
	if err := oracle.CompareExact(reconSource{enc.reconstruction()}, decoded); err != nil {
		t.Fatalf("decoded picture differs from reconstruction: %v", err)
	}
}

// TestRunFrameProbOptOffRecordsDerivationOnly pins the disabled-refinement
// walk of runFrame: with rdProbOptOff set, Method 6 still derives token
// probabilities exactly once -- so probOptimizationDone and
// probDerivations record the completed pass -- but no update ships, so
// nothing is frozen, no reconsideration runs, and pricing stays on the
// default table. Serialization must be byte-stable across repeated calls
// without disturbing any of that state.
func TestRunFrameProbOptOffRecordsDerivationOnly(t *testing.T) {
	enc := newEncoder(yuv.Convert(bpredDetailRGBA(64, 48, 101)), Config{Quality: 90, Method: 6})
	enc.rdProbOptOff = true

	enc.runFrame()

	if !enc.probOptimizationDone {
		t.Fatal("probOptimizationDone = false, want true")
	}
	if enc.probDerivations != 1 {
		t.Fatalf("probDerivations = %d, want exactly 1", enc.probDerivations)
	}
	if enc.probReconsiderations != 0 {
		t.Fatalf("probReconsiderations = %d, want 0 (nothing may ship with the refinement off)", enc.probReconsiderations)
	}
	if enc.frozenTokenProbs != nil {
		t.Fatal("frozenTokenProbs is non-nil; a disabled refinement must never freeze a table")
	}
	if enc.rateProbs != &token.DefaultProbs {
		t.Fatal("rateProbs does not point at &token.DefaultProbs")
	}

	done := enc.probOptimizationDone
	derivations := enc.probDerivations
	reconsiderations := enc.probReconsiderations

	first, err := enc.frameBytes()
	if err != nil {
		t.Fatalf("first frameBytes: %v", err)
	}

	second, err := enc.frameBytes()
	if err != nil {
		t.Fatalf("second frameBytes: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("repeated frameBytes differ (%d vs %d bytes)", len(first), len(second))
	}

	if enc.probOptimizationDone != done ||
		enc.probDerivations != derivations ||
		enc.probReconsiderations != reconsiderations {
		t.Errorf("frameBytes changed refinement counters: done=%v->%v derivations=%d->%d reconsiderations=%d->%d",
			done, enc.probOptimizationDone, derivations, enc.probDerivations, reconsiderations, enc.probReconsiderations)
	}
	if enc.frozenTokenProbs != nil {
		t.Error("frameBytes froze a probability table")
	}
	if enc.rateProbs != &token.DefaultProbs {
		t.Error("frameBytes changed the active rateProbs pointer")
	}
}

// TestRunFrameBelowBoundaryEqualsPlainRun proves that for every method
// below the Slice 6A boundary, runFrame is exactly run plus serialization:
// two fresh encoders over one deterministic fixture, one driven through
// runFrame and one through a plain run, must agree byte for byte in their
// payloads, macroblock records, and RD counters, while runFrame itself
// records nothing about the refinement at all.
func TestRunFrameBelowBoundaryEqualsPlainRun(t *testing.T) {
	src := yuv.Convert(bpredDetailRGBA(64, 48, 7))

	for m := 0; m <= 5; m++ {
		refined := newEncoder(src, Config{Quality: 75, Method: m})
		refined.runFrame()
		payloadRefined, err := refined.frameBytes()
		if err != nil {
			t.Fatalf("method %d: frameBytes after runFrame: %v", m, err)
		}

		plain := newEncoder(src, Config{Quality: 75, Method: m})
		plain.run()
		payloadPlain, err := plain.frameBytes()
		if err != nil {
			t.Fatalf("method %d: frameBytes after run: %v", m, err)
		}

		if !bytes.Equal(payloadRefined, payloadPlain) {
			t.Errorf("method %d: runFrame wrote different bytes than run (%d vs %d)",
				m, len(payloadRefined), len(payloadPlain))
		}
		if !reflect.DeepEqual(refined.mbs, plain.mbs) {
			t.Errorf("method %d: macroblock records differ between runFrame and run", m)
		}
		if refined.rd != plain.rd {
			t.Errorf("method %d: rd stats differ between runFrame (%+v) and run (%+v)",
				m, refined.rd, plain.rd)
		}

		if refined.probOptimizationDone {
			t.Errorf("method %d: runFrame set probOptimizationDone below the boundary", m)
		}
		if refined.probDerivations != 0 || refined.probReconsiderations != 0 {
			t.Errorf("method %d: runFrame counted refinements below the boundary (derivations=%d reconsiderations=%d)",
				m, refined.probDerivations, refined.probReconsiderations)
		}
		if refined.frozenTokenProbs != nil {
			t.Errorf("method %d: runFrame froze a table below the boundary", m)
		}
		if refined.rateProbs != &token.DefaultProbs {
			t.Errorf("method %d: rateProbs does not point at &token.DefaultProbs below the boundary", m)
		}
	}
}

// TestRunFrameDeterminismAcrossGOMAXPROCS proves that the Slice 6A
// refinement walk of runFrame is fully deterministic regardless of how
// many OS threads the Go runtime may schedule it across: fresh encoders
// on the known profitable Method 6 fixture must produce byte-identical
// payloads and identical frozen tables, macroblocks, RD counters, and
// reconstruction planes under GOMAXPROCS 1, 2, and 4.
func TestRunFrameDeterminismAcrossGOMAXPROCS(t *testing.T) {
	src := yuv.Convert(bpredDetailRGBA(64, 48, 101))

	orig := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(orig) })

	type runSnapshot struct {
		payload []byte
		frozen  token.Probs
		mbs     []macroblock
		rd      rdStats
		recon   *image.YCbCr
	}

	var first *runSnapshot

	for _, procs := range []int{1, 2, 4} {
		runtime.GOMAXPROCS(procs)

		enc := newEncoder(src, Config{Quality: 90, Method: 6})
		enc.runFrame()

		if !enc.probOptimizationDone {
			t.Fatalf("GOMAXPROCS=%d: runFrame did not complete its probability derivation (probOptimizationDone false)", procs)
		}
		if enc.probDerivations != 1 {
			t.Fatalf("GOMAXPROCS=%d: probDerivations = %d, want exactly 1", procs, enc.probDerivations)
		}
		if enc.probReconsiderations != 1 {
			t.Fatalf("GOMAXPROCS=%d: probReconsiderations = %d, want exactly 1", procs, enc.probReconsiderations)
		}
		if enc.frozenTokenProbs == nil {
			t.Fatalf("GOMAXPROCS=%d: no table was frozen (frozenTokenProbs nil)", procs)
		}

		payload, err := enc.frameBytes()
		if err != nil {
			t.Fatalf("GOMAXPROCS=%d: frameBytes: %v", procs, err)
		}

		snap := &runSnapshot{
			payload: payload,
			frozen:  *enc.frozenTokenProbs,
			mbs:     enc.mbs,
			rd:      enc.rd,
			recon:   enc.reconstruction(),
		}

		if first == nil {
			first = snap
			continue
		}

		label := fmt.Sprintf("GOMAXPROCS=%d vs GOMAXPROCS=first", procs)
		if !bytes.Equal(first.payload, snap.payload) {
			t.Errorf("%s: payload differs (%d vs %d bytes)", label, len(first.payload), len(snap.payload))
		}
		if first.frozen != snap.frozen {
			t.Errorf("%s: frozen probability table value differs", label)
		}
		if !reflect.DeepEqual(first.mbs, snap.mbs) {
			t.Errorf("%s: macroblock records differ", label)
		}
		if first.rd != snap.rd {
			t.Errorf("%s: rd stats differ (%+v vs %+v)", label, first.rd, snap.rd)
		}
		if first.recon.YStride != snap.recon.YStride ||
			first.recon.CStride != snap.recon.CStride ||
			len(first.recon.Y) != len(snap.recon.Y) ||
			len(first.recon.Cb) != len(snap.recon.Cb) ||
			len(first.recon.Cr) != len(snap.recon.Cr) {
			t.Fatalf("%s: reconstruction plane geometry differs", label)
		}
		if !bytes.Equal(first.recon.Y, snap.recon.Y) ||
			!bytes.Equal(first.recon.Cb, snap.recon.Cb) ||
			!bytes.Equal(first.recon.Cr, snap.recon.Cr) {
			t.Errorf("%s: reconstruction planes differ", label)
		}
	}
}

// TestRunFrameNoWinUniformFlatRecordsDerivationOnly pins the unprofitable
// walk of runFrame at full production settings: a tiny constant-colour
// fixture carries no coefficient tokens whose branch counts could ever
// price an update above its signalling cost, so the Slice 6A derivation
// completes exactly once, records the completed pass, and keeps the
// default table -- nothing frozen, nothing reconsidered. The refinement
// itself runs normally (rdProbOptOff stays false); only the ledger comes
// up empty. Serialization must be byte-stable across repeated calls, and
// the shipped frame must still decode, through the independent decoder,
// to pixels equal to the encoder's own reconstruction.
func TestRunFrameNoWinUniformFlatRecordsDerivationOnly(t *testing.T) {
	enc := newEncoder(yuv.Convert(flatRGBA(1, 1, 128)), Config{Quality: 90, Method: 6})

	enc.runFrame()

	// The optimizer ran to completion over the real analysis: done and
	// derivations are recorded even though the dry pass found no update
	// worth shipping.
	if !enc.probOptimizationDone {
		t.Fatal("runFrame did not complete its probability derivation (probOptimizationDone false)")
	}
	if enc.probDerivations != 1 {
		t.Fatalf("probDerivations = %d, want exactly 1", enc.probDerivations)
	}
	if enc.probReconsiderations != 0 {
		t.Fatalf("probReconsiderations = %d, want 0 (a no-win fixture must never trigger a reconsideration)", enc.probReconsiderations)
	}
	if enc.frozenTokenProbs != nil {
		t.Fatal("frozenTokenProbs is non-nil; a no-win derivation must never freeze a table")
	}
	if enc.rateProbs != &token.DefaultProbs {
		t.Fatal("rateProbs does not point at &token.DefaultProbs")
	}

	first, err := enc.frameBytes()
	if err != nil {
		t.Fatalf("first frameBytes: %v", err)
	}

	second, err := enc.frameBytes()
	if err != nil {
		t.Fatalf("second frameBytes: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("repeated frameBytes differ (%d vs %d bytes)", len(first), len(second))
	}

	// Wrap the serialized VP8 key frame in its RIFF container and decode
	// it with the independent decoder; the picture must be equal to the
	// encoder's own reconstruction on every plane, byte for byte.
	var buf writerBuffer
	if err := container.WriteSimpleLossy(&buf, first); err != nil {
		t.Fatalf("container.WriteSimpleLossy: %v", err)
	}
	decoded, err := oracle.DecodeWebPPlanes(buf.data)
	if err != nil {
		t.Fatalf("oracle decode: %v", err)
	}
	if err := oracle.CompareExact(reconSource{enc.reconstruction()}, decoded); err != nil {
		t.Fatalf("decoded picture differs from reconstruction: %v", err)
	}
}
