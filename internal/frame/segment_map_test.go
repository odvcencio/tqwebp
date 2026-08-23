package frame

import (
	"fmt"
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
	"m31labs.dev/tqwebp/internal/cost"
)

// TestEffectiveSegmentTreeProbsDefaults covers every case in which no
// map update reaches the tree: segmentation disabled outright, enabled
// without UpdateMap, and UpdateMap signalled while Enabled is off. All
// of them must leave the key-frame defaults [255,255,255] untouched,
// even when TreeProbs entries claim updates.
func TestEffectiveSegmentTreeProbsDefaults(t *testing.T) {
	tests := []struct {
		name string
		seg  Segmentation
	}{
		{
			name: "disabled zero value",
			seg:  Segmentation{},
		},
		{
			name: "disabled with pending updates",
			seg: Segmentation{
				Enabled:   false,
				UpdateMap: true,
				TreeProbs: [3]SegmentProbability{
					{Update: true, Value: 1},
					{Update: true, Value: 2},
					{Update: true, Value: 3},
				},
			},
		},
		{
			name: "enabled without UpdateMap",
			seg: Segmentation{
				Enabled:   true,
				UpdateMap: false,
				TreeProbs: [3]SegmentProbability{
					{Update: true, Value: 10},
					{Update: true, Value: 20},
					{Update: true, Value: 30},
				},
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveSegmentTreeProbs(tc.seg)
			want := DefaultSegmentTreeProbs
			if got != want {
				t.Errorf("EffectiveSegmentTreeProbs(%+v) = %v, want %v", tc.seg, got, want)
			}
		})
	}
}

// TestEffectiveSegmentTreeProbsPartialUpdate proves that a partial map
// update replaces only the gated entries -- including boundary values 0
// and 255 -- and that omitted nodes retain the default [255,255,255].
func TestEffectiveSegmentTreeProbsPartialUpdate(t *testing.T) {
	tests := []struct {
		name string
		seg  Segmentation
		want [3]uint8
	}{
		{
			name: "only root updated",
			seg: Segmentation{
				Enabled:   true,
				UpdateMap: true,
				TreeProbs: [3]SegmentProbability{
					{Update: true, Value: 42},
					{Update: false},
					{Update: false},
				},
			},
			want: [3]uint8{42, 255, 255},
		},
		{
			name: "middle node updated",
			seg: Segmentation{
				Enabled:   true,
				UpdateMap: true,
				TreeProbs: [3]SegmentProbability{
					{Update: false},
					{Update: true, Value: 7},
					{Update: false},
				},
			},
			want: [3]uint8{255, 7, 255},
		},
		{
			name: "leaf updated",
			seg: Segmentation{
				Enabled:   true,
				UpdateMap: true,
				TreeProbs: [3]SegmentProbability{
					{Update: false},
					{Update: false},
					{Update: true, Value: 99},
				},
			},
			want: [3]uint8{255, 255, 99},
		},
		{
			name: "zero value update is honored",
			seg: Segmentation{
				Enabled:   true,
				UpdateMap: true,
				TreeProbs: [3]SegmentProbability{
					{Update: true, Value: 0},
					{Update: false},
					{Update: false},
				},
			},
			want: [3]uint8{0, 255, 255},
		},
		{
			name: "255 value update equals inherited default",
			seg: Segmentation{
				Enabled:   true,
				UpdateMap: true,
				TreeProbs: [3]SegmentProbability{
					{Update: false},
					{Update: true, Value: 255},
					{Update: false},
				},
			},
			want: [3]uint8{255, 255, 255},
		},
		{
			name: "ungated entries never leak their value",
			seg: Segmentation{
				Enabled:   true,
				UpdateMap: true,
				TreeProbs: [3]SegmentProbability{
					{Update: false, Value: 13},
					{Update: true, Value: 51},
					{Update: false, Value: 77},
				},
			},
			want: [3]uint8{255, 51, 255},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveSegmentTreeProbs(tc.seg)
			if got != tc.want {
				t.Errorf("EffectiveSegmentTreeProbs(%+v) = %v, want %v", tc.seg, got, tc.want)
			}
		})
	}
}

// TestEffectiveSegmentTreeProbsFullUpdate checks that a frame updating
// all three nodes yields exactly its own values, including extremes.
func TestEffectiveSegmentTreeProbsFullUpdate(t *testing.T) {
	tests := []struct {
		name string
		vals [3]uint8
	}{
		{"typical values", [3]uint8{128, 64, 192}},
		{"all zero", [3]uint8{0, 0, 0}},
		{"all max", [3]uint8{255, 255, 255}},
		{"mixed extremes", [3]uint8{0, 255, 128}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			seg := Segmentation{
				Enabled:   true,
				UpdateMap: true,
				TreeProbs: [3]SegmentProbability{
					{Update: true, Value: tc.vals[0]},
					{Update: true, Value: tc.vals[1]},
					{Update: true, Value: tc.vals[2]},
				},
			}
			if got := EffectiveSegmentTreeProbs(seg); got != tc.vals {
				t.Errorf("EffectiveSegmentTreeProbs = %v, want %v", got, tc.vals)
			}
		})
	}
}

// segmentDecision is one boolean decision of the segment-map tree walk,
// recorded so tests can price it independently with cost.BitCost.
type segmentDecision struct {
	prob uint8
	bit  bool
}

// walkSegmentID performs the real independent tree walk. It reads each
// decision at the given probability and derives the leaf id purely from
// the observed bits, without consulting WriteSegmentID's logic.
func walkSegmentID(dec *boolenc.Decoder, probs *[3]uint8) (id uint8, ds []segmentDecision, errPath bool) {
	bit0 := dec.ReadBool(probs[0])
	ds = append(ds, segmentDecision{probs[0], bit0})
	if !bit0 {
		return 0, ds, true
	}
	bit1 := dec.ReadBool(probs[1])
	ds = append(ds, segmentDecision{probs[1], bit1})
	if !bit1 {
		return 1, ds, true
	}
	bit2 := dec.ReadBool(probs[2])
	ds = append(ds, segmentDecision{probs[2], bit2})
	if !bit2 {
		return 2, ds, true
	}
	return 3, ds, true
}

// expectedSegmentPath returns the root-to-leaf decision path an id must
// take through the tree, computed independently of the writer under
// test: the tree is {-0, 2, -1, 4, -2, -3}.
func expectedSegmentPath(id uint8) []segmentDecision {
	switch id {
	case 0:
		return []segmentDecision{{0, false}}
	case 1:
		return []segmentDecision{{0, true}, {1, false}}
	case 2:
		return []segmentDecision{{0, true}, {1, true}, {2, false}}
	default:
		return []segmentDecision{{0, true}, {1, true}, {2, true}}
	}
}

// TestWriteSegmentIDRoundTrip encodes every id 0..3 against four
// probability triples, then decodes each stream with a test-local tree
// walk driven by boolenc.Decoder alone. It requires the exact expected
// path, no unexpected EOF, deterministic repeated bytes, and that the
// independently summed cost.BitCost of the decoded decisions equals
// cost.SegmentIDCost for the same triple and id.
func TestWriteSegmentIDRoundTrip(t *testing.T) {
	triples := [][3]uint8{
		{1, 1, 1},
		{255, 255, 255},
		{128, 128, 128},
		{42, 99, 200},
	}
	for _, probs := range triples {
		probs := probs
		for id := uint8(0); id <= 3; id++ {
			id := id
			name := fmt.Sprintf("probs_%d_%d_%d_id%d", probs[0], probs[1], probs[2], id)
			t.Run(name, func(t *testing.T) {
				p := probs

				// Encode twice to prove byte determinism.
				first := encodeSegmentID(t, &p, id)
				second := encodeSegmentID(t, &p, id)
				if string(first) != string(second) {
					t.Fatalf("repeated encode differs:\n first %v\nsecond %v", first, second)
				}

				// Decode with the independent tree walk.
				dec := boolenc.NewDecoder(first)
				gotID, decisions, ok := walkSegmentID(dec, &p)
				if !ok {
					t.Fatal("tree walk did not terminate")
				}
				if gotID != id {
					t.Fatalf("decoded id = %d, want %d (decisions %v)", gotID, id, decisions)
				}

				// The path must match the expected root-to-leaf bits exactly.
				want := expectedSegmentPath(id)
				if len(decisions) != len(want) {
					t.Fatalf("decision count = %d, want %d (%v)", len(decisions), len(want), decisions)
				}
				for i, d := range decisions {
					if d.bit != want[i].bit {
						t.Fatalf("decision[%d].bit = %v, want %v (full %v)", i, d.bit, want[i].bit, decisions)
					}
				}

				// A well-formed stream must not report EOF while its own
				// decisions are being read back.
				if dec.UnexpectedEOF() {
					t.Error("decoder reported unexpected EOF during tree walk")
				}

				// Price the decoded decisions independently and compare
				// with cost.SegmentIDCost.
				var sum cost.Cost
				for _, d := range decisions {
					sum += cost.BitCost(d.prob, d.bit)
				}
				wantCost := cost.SegmentIDCost(&p, id)
				if sum != wantCost {
					t.Errorf("summed BitCost of decoded decisions = %d, want cost.SegmentIDCost = %d", sum, wantCost)
				}
			})
		}
	}
}

// encodeSegmentID writes one segment id with frame.WriteSegmentID and
// finishes the stream.
func encodeSegmentID(t *testing.T, probs *[3]uint8, id uint8) []byte {
	t.Helper()
	enc := boolenc.New(0)
	WriteSegmentID(enc, probs, id)
	return enc.Finish()
}

// TestWriteSegmentIDInvalidPanicsBeforeWrite proves that an out-of-range
// id panics before anything reaches the stream, and that the same
// encoder remains usable afterwards: finishing it after a subsequent
// valid write must produce bytes identical to a fresh encoder that wrote
// only that valid id.
func TestWriteSegmentIDInvalidPanicsBeforeWrite(t *testing.T) {
	probs := [3]uint8{128, 128, 128}
	const validID = uint8(2)

	enc := boolenc.New(0)

	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Error("WriteSegmentID with id 4 did not panic")
				return
			}
			msg, ok := r.(string)
			if !ok {
				t.Errorf("panic value is not a string: %v", r)
				return
			}
			wantSub := "tqwebp/frame:"
			if len(msg) < len(wantSub) || msg[:len(wantSub)] != wantSub {
				t.Errorf("panic message %q does not start with %q", msg, wantSub)
			}
			if !containsInt(msg, 4) {
				t.Errorf("panic message %q does not mention the offending id 4", msg)
			}
		}()
		WriteSegmentID(enc, &probs, 4)
		t.Error("WriteSegmentID returned after invalid id; expected panic")
	}()

	if n := enc.Len(); n != 0 {
		t.Errorf("encoder wrote %d bytes before panicking on invalid id, want 0", n)
	}

	// The encoder state must be untouched by the panic: reuse it for a
	// valid write and require identical output to a fresh encoder.
	WriteSegmentID(enc, &probs, validID)
	reused := enc.Finish()

	fresh := boolenc.New(0)
	WriteSegmentID(fresh, &probs, validID)
	wantBytes := fresh.Finish()

	if string(reused) != string(wantBytes) {
		t.Errorf("reused-encoder output = %v, want fresh-encoder output %v", reused, wantBytes)
	}

	// Sanity: decoding the shared output must yield the valid id.
	dec := boolenc.NewDecoder(reused)
	gotID, _, ok := walkSegmentID(dec, &probs)
	if !ok || gotID != validID {
		t.Errorf("decoded id = %d (ok=%v), want %d", gotID, ok, validID)
	}
	if dec.UnexpectedEOF() {
		t.Error("unexpected EOF while decoding reused-encoder output")
	}
}

// containsInt reports whether s contains the decimal representation of v
// as a standalone number.
func containsInt(s string, v int) bool {
	digits := []byte{'0' + byte(v)}
	for i := 0; i+len(digits) <= len(s); i++ {
		match := true
		for j, c := range digits {
			if s[i+j] != c {
				match = false
				break
			}
		}
		if match && (i == 0 || s[i-1] < '0' || s[i-1] > '9') &&
			(i+len(digits) >= len(s) || s[i+len(digits)] < '0' || s[i+len(digits)] > '9') {
			return true
		}
	}
	return false
}
