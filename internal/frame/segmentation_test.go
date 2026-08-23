package frame

import (
	"encoding/hex"
	"strings"
	"testing"

	"m31labs.dev/tqwebp/internal/boolenc"
)

// parsedFeature mirrors one optional signed segment feature as it comes
// off the wire: a gate flag and, when the gate is on, an n-bit magnitude
// followed by a sign flag (RFC 6386 section 9.3).
type parsedFeature struct {
	Enabled bool
	Value   int // signed; meaningful only when Enabled
}

// parsedProbability mirrors one segment-map tree probability update.
type parsedProbability struct {
	Update bool
	Value  uint8
}

// parsedSegmentation holds every field this test parses from the front of
// a first partition: the two colour-space bits, the whole segmentation
// body of section 9.3, and the loop-filter fields that immediately follow
// it. Parsing through the trailing filter fields proves that the
// segmentation reader consumes exactly its own bits and no more.
type parsedSegmentation struct {
	ColorSpace bool
	Clamping   bool

	Enabled    bool
	UpdateMap  bool
	UpdateData bool
	Absolute   bool

	Quantizer  [4]parsedFeature
	LoopFilter [4]parsedFeature

	TreeProbs [3]parsedProbability

	FilterSimple       bool
	FilterLevel        int
	FilterSharpness    int
	FilterDeltasEnable bool
}

// parseSegmentationPrefix decodes the leading fields of a first partition
// written by WriteHeader. It fails the test when the stream ends early,
// which would mean the encoder emitted fewer bits than the format calls
// for before the loop-filter block.
func parseSegmentationPrefix(t *testing.T, data []byte) parsedSegmentation {
	t.Helper()

	d := boolenc.NewDecoder(data)
	var p parsedSegmentation

	p.ColorSpace = d.ReadFlag()
	p.Clamping = d.ReadFlag()

	p.Enabled = d.ReadFlag()
	if p.Enabled {
		p.UpdateMap = d.ReadFlag()
		p.UpdateData = d.ReadFlag()
		if p.UpdateData {
			p.Absolute = d.ReadFlag()
			for i := range p.Quantizer {
				p.Quantizer[i] = parseOptionalSigned(t, d, 7)
			}
			for i := range p.LoopFilter {
				p.LoopFilter[i] = parseOptionalSigned(t, d, 6)
			}
		}
		if p.UpdateMap {
			for i := range p.TreeProbs {
				upd := d.ReadFlag()
				v := uint8(0)
				if upd {
					v = uint8(d.ReadLiteral(8))
				}
				p.TreeProbs[i] = parsedProbability{Update: upd, Value: v}
			}
		}
	}

	// The fields immediately following segmentation. If any earlier
	// read misaligned by even one bit, these come back wrong.
	p.FilterSimple = d.ReadFlag()
	p.FilterLevel = int(d.ReadLiteral(6))
	p.FilterSharpness = int(d.ReadLiteral(3))
	p.FilterDeltasEnable = d.ReadFlag()

	if d.UnexpectedEOF() {
		t.Fatal("boolean decoder hit end of partition before the loop-filter fields completed")
	}
	return p
}

// parseOptionalSigned reads one gated signed value: a gate flag, then an
// n-bit magnitude plus sign flag when the gate is on.
func parseOptionalSigned(t *testing.T, d *boolenc.Decoder, n int) parsedFeature {
	t.Helper()
	f := parsedFeature{Enabled: d.ReadFlag()}
	if f.Enabled {
		v := int(d.ReadLiteral(n))
		if d.ReadFlag() {
			v = -v
		}
		f.Value = v
	}
	return f
}

// encodeFirstPartition runs WriteHeader over a fresh encoder and returns
// the boolean-coded first-partition bytes.
func encodeFirstPartition(t *testing.T, h Header) []byte {
	t.Helper()
	enc := boolenc.New(0)
	WriteHeader(enc, h)
	return enc.Finish()
}

// requireFeatures checks one array of parsed segment features against the
// header-side description that produced them.
func requireFeatures(t *testing.T, name string, got [4]parsedFeature, want [4]SegmentFeature) {
	t.Helper()
	for i, g := range got {
		if g.Enabled != want[i].Enabled {
			t.Errorf("%s[%d].Enabled = %v, want %v", name, i, g.Enabled, want[i].Enabled)
			continue
		}
		if g.Enabled && g.Value != want[i].Value {
			t.Errorf("%s[%d].Value = %d, want %d", name, i, g.Value, want[i].Value)
		}
	}
}

func TestSegmentationRoundTrip(t *testing.T) {
	tests := []struct {
		name         string
		seg          Segmentation
		filterSimple bool
		filterLevel  int
		sharpness    int
	}{
		{
			name:        "disabled",
			seg:         Segmentation{},
			filterLevel: 0,
			sharpness:   0,
		},
		{
			name:         "enabled_no_gates",
			seg:          Segmentation{Enabled: true},
			filterSimple: true,
			filterLevel:  42,
			sharpness:    5,
		},
		{
			name: "map_only_values_0_and_255",
			seg: Segmentation{
				Enabled:   true,
				UpdateMap: true,
				TreeProbs: [3]SegmentProbability{
					{Update: true, Value: 0},
					{Update: true, Value: 255},
					{Update: false},
				},
			},
			filterLevel: 17,
			sharpness:   3,
		},
		{
			name: "data_only_delta_mode",
			seg: Segmentation{
				Enabled:    true,
				UpdateData: true,
				Absolute:   false,
				Quantizer: [4]SegmentFeature{
					{Enabled: true, Value: -100},
					{},
					{Enabled: true, Value: 55},
					{Enabled: true, Value: 7},
				},
				LoopFilter: [4]SegmentFeature{
					{Enabled: true, Value: -30},
					{Enabled: true, Value: 12},
					{},
					{},
				},
			},
			filterLevel: 9,
			sharpness:   1,
		},
		{
			name: "data_only_absolute_mode",
			seg: Segmentation{
				Enabled:    true,
				UpdateData: true,
				Absolute:   true,
				Quantizer: [4]SegmentFeature{
					{Enabled: true, Value: 120},
					{Enabled: true, Value: -1},
					{},
					{Enabled: true, Value: 64},
				},
				LoopFilter: [4]SegmentFeature{
					{},
					{Enabled: true, Value: -62},
					{Enabled: true, Value: 61},
					{},
				},
			},
			filterLevel: 63,
			sharpness:   7,
		},
		{
			name: "fully_populated_extremes",
			seg: Segmentation{
				Enabled:    true,
				UpdateMap:  true,
				UpdateData: true,
				Absolute:   true,
				Quantizer: [4]SegmentFeature{
					{Enabled: true, Value: -127},
					{Enabled: false, Value: 0},
					{Enabled: true, Value: 1},
					{Enabled: true, Value: 127},
				},
				LoopFilter: [4]SegmentFeature{
					{Enabled: true, Value: -63},
					{Enabled: true, Value: 0},
					{Enabled: false, Value: 1},
					{Enabled: true, Value: 63},
				},
				TreeProbs: [3]SegmentProbability{
					{Update: true, Value: 0},
					{Update: true, Value: 1},
					{Update: true, Value: 255},
				},
			},
			filterSimple: true,
			filterLevel:  31,
			sharpness:    6,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := Header{
				Width:           16,
				Height:          16,
				FilterSimple:    tc.filterSimple,
				FilterLevel:     tc.filterLevel,
				FilterSharpness: tc.sharpness,
				QuantIndex:      40,
				SkipProb:        128,
				Segmentation:    tc.seg,
			}
			data := encodeFirstPartition(t, h)
			got := parseSegmentationPrefix(t, data)

			// Colour-space prefix stays at the defaults WP-1 writes.
			if got.ColorSpace || got.Clamping {
				t.Errorf("colour space = %v, clamping = %v, want both false", got.ColorSpace, got.Clamping)
			}

			if got.Enabled != tc.seg.Enabled {
				t.Errorf("segmentation enabled = %v, want %v", got.Enabled, tc.seg.Enabled)
			}
			if got.UpdateMap != tc.seg.UpdateMap {
				t.Errorf("update map = %v, want %v", got.UpdateMap, tc.seg.UpdateMap)
			}
			if got.UpdateData != tc.seg.UpdateData {
				t.Errorf("update data = %v, want %v", got.UpdateData, tc.seg.UpdateData)
			}
			if got.Absolute != tc.seg.Absolute {
				t.Errorf("absolute = %v, want %v", got.Absolute, tc.seg.Absolute)
			}
			requireFeatures(t, "quantizer", got.Quantizer, tc.seg.Quantizer)
			requireFeatures(t, "loopFilter", got.LoopFilter, tc.seg.LoopFilter)

			for i, gp := range got.TreeProbs {
				want := tc.seg.TreeProbs[i]
				if gp.Update != want.Update {
					t.Errorf("treeProbs[%d].Update = %v, want %v", i, gp.Update, want.Update)
					continue
				}
				if gp.Update && gp.Value != want.Value {
					t.Errorf("treeProbs[%d].Value = %d, want %d", i, gp.Value, want.Value)
				}
			}

			// Adjacent post-segmentation fields: any drift in the
			// segmentation bit layout lands here first.
			if got.FilterSimple != tc.filterSimple {
				t.Errorf("filterSimple = %v, want %v", got.FilterSimple, tc.filterSimple)
			}
			if got.FilterLevel != tc.filterLevel {
				t.Errorf("filterLevel = %d, want %d", got.FilterLevel, tc.filterLevel)
			}
			if got.FilterSharpness != tc.sharpness {
				t.Errorf("filterSharpness = %d, want %d", got.FilterSharpness, tc.sharpness)
			}
			if got.FilterDeltasEnable {
				t.Error("filter delta-enabled = true, want false")
			}
		})
	}
}

// TestSegmentationInvalidRange proves that WriteHeader panics when an
// enabled segment feature value would be truncated by its fixed-width
// field (quantizer deltas carry 7 magnitude bits, loop-filter deltas 6),
// and that identical values behind a closed gate are silently skipped
// because writeSegmentFeature never emits them.
func TestSegmentationInvalidRange(t *testing.T) {
	tests := []struct {
		name       string
		seg        Segmentation
		wantSubstr string // exact panic text when one is expected; empty means none
	}{
		{
			name: "enabled_quantizer_low_end",
			seg: Segmentation{
				Enabled:    true,
				UpdateData: true,
				Quantizer: [4]SegmentFeature{
					{Enabled: true, Value: -128},
					{}, {}, {},
				},
			},
			wantSubstr: "tqwebp/frame: segmentation quantizer delta for segment 0 is -128, out of range -127..127",
		},
		{
			name: "enabled_quantizer_high_end",
			seg: Segmentation{
				Enabled:    true,
				UpdateData: true,
				Quantizer: [4]SegmentFeature{
					{}, {},
					{Enabled: true, Value: 128},
					{},
				},
			},
			wantSubstr: "tqwebp/frame: segmentation quantizer delta for segment 2 is 128, out of range -127..127",
		},
		{
			name: "enabled_loop_filter_low_end",
			seg: Segmentation{
				Enabled:    true,
				UpdateData: true,
				LoopFilter: [4]SegmentFeature{
					{},
					{Enabled: true, Value: -64},
					{}, {},
				},
			},
			wantSubstr: "tqwebp/frame: segmentation loop-filter delta for segment 1 is -64, out of range -63..63",
		},
		{
			name: "enabled_loop_filter_high_end",
			seg: Segmentation{
				Enabled:    true,
				UpdateData: true,
				LoopFilter: [4]SegmentFeature{
					{}, {}, {},
					{Enabled: true, Value: 64},
				},
			},
			wantSubstr: "tqwebp/frame: segmentation loop-filter delta for segment 3 is 64, out of range -63..63",
		},
		{
			// Same offending values as above, but every gate is off.
			// validateSegmentation skips disabled features and
			// writeSegmentFeature emits only the gate flag, so encoding
			// must succeed and the wire must carry no values.
			name: "disabled_gates_carry_out_of_range_values",
			seg: Segmentation{
				Enabled:    true,
				UpdateData: true,
				Quantizer: [4]SegmentFeature{
					{Enabled: false, Value: -128},
					{},
					{Enabled: false, Value: 128},
					{},
				},
				LoopFilter: [4]SegmentFeature{
					{},
					{Enabled: false, Value: -64},
					{},
					{Enabled: false, Value: 64},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := Header{
				Width:           16,
				Height:          16,
				FilterLevel:     20,
				FilterSharpness: 2,
				QuantIndex:      40,
				SkipProb:        128,
				Segmentation:    tc.seg,
			}

			if tc.wantSubstr == "" {
				// Disabled gates: encoding must not panic, and the
				// decoded stream must show every gate still closed.
				data := encodeFirstPartition(t, h)
				got := parseSegmentationPrefix(t, data)
				requireFeatures(t, "quantizer", got.Quantizer, tc.seg.Quantizer)
				requireFeatures(t, "loopFilter", got.LoopFilter, tc.seg.LoopFilter)
				return
			}

			func() {
				defer func() {
					r := recover()
					if r == nil {
						t.Fatalf("WriteHeader did not panic, want panic containing %q", tc.wantSubstr)
					}
					msg, ok := r.(string)
					if !ok {
						t.Fatalf("panic value = %v (%T), want string", r, r)
					}
					if !strings.Contains(msg, "tqwebp/frame") {
						t.Errorf("panic message %q does not contain package prefix \"tqwebp/frame\"", msg)
					}
					if !strings.Contains(msg, tc.wantSubstr) {
						t.Errorf("panic message = %q, want it to contain %q", msg, tc.wantSubstr)
					}
				}()
				encodeFirstPartition(t, h)
			}()
		})
	}
}

// TestDisabledGoldenAndDeterminism locks the byte-exact encoding of a
// header that keeps segmentation disabled, alongside the loop-filter and
// quantizer settings the test fixes. The golden is derived from the
// current implementation; any later encoder change must update it
// deliberately. It then re-encodes the same header repeatedly and
// requires byte identity each time (determinism), and finally parses the
// stream back with parseSegmentationPrefix to prove the colour-space and
// clamping flags are false, segmentation stays fully disabled, and the
// adjacent loop-filter fields decode as written.
func TestDisabledGoldenAndDeterminism(t *testing.T) {
	const wantHex = "15e85408002ce000"

	h := Header{
		FilterSimple:    true,
		FilterLevel:     23,
		FilterSharpness: 5,
		QuantIndex:      42,
		SkipProb:        220,
	}

	first := encodeFirstPartition(t, h)
	if got := hex.EncodeToString(first); got != wantHex {
		t.Fatalf("golden mismatch:\n got %s\nwant %s", got, wantHex)
	}

	for i := 1; i <= 20; i++ {
		got := hex.EncodeToString(encodeFirstPartition(t, h))
		if got != wantHex {
			t.Fatalf("determinism: encode %d = %s, want %s", i, got, wantHex)
		}
	}

	p := parseSegmentationPrefix(t, first)

	if p.ColorSpace {
		t.Errorf("ColorSpace = true, want false")
	}
	if p.Clamping {
		t.Errorf("Clamping = true, want false")
	}
	if p.Enabled {
		t.Errorf("Segmentation.Enabled = true, want false")
	}
	if p.UpdateMap {
		t.Errorf("UpdateMap = true, want false")
	}
	if p.UpdateData {
		t.Errorf("UpdateData = true, want false")
	}
	if p.Absolute {
		t.Errorf("Absolute = true, want false")
	}

	if !p.FilterSimple {
		t.Errorf("FilterSimple = false, want true")
	}
	if p.FilterLevel != 23 {
		t.Errorf("FilterLevel = %d, want 23", p.FilterLevel)
	}
	if p.FilterSharpness != 5 {
		t.Errorf("FilterSharpness = %d, want 5", p.FilterSharpness)
	}
	if p.FilterDeltasEnable {
		t.Errorf("FilterDeltasEnable = true, want false")
	}
}
