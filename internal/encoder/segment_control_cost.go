package encoder

import (
	"fmt"

	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/frame"
)

// This file holds the exact cost primitive for the control part of RFC
// 6386 section 9.3 -- every decision writeSegmentation emits after the
// segmentation_enabled flag, plus the flag itself, in Q8 cost units.
// It is byte-inert -- nothing here writes or mutates encoder state --
// and has no production caller; it exists so a later segmentation
// planner can price whole segmentation configurations without
// rederiving any of this arithmetic.
//
// # Boundary
//
// This is the full incremental section 9.3 control-partition pressure,
// including the per-macroblock segment-map id records but excluding the
// rest of partition zero: sections 9.2, 9.4 through 9.10, and every
// per-macroblock prediction record outside the segment ids stay out of
// scope. Because the id records are folded into TotalCost, a planner
// comparing an enabled configuration against the disabled one-bit
// baseline must NOT add deriveSegmentMapPlan.SignalCost on top: the
// update-gate and probability-literal bits priced here by mirroring
// writeSegmentation already cover what SignalCost would charge, and
// double-counting them would overstate every enabled plan.
//
// # Units and determinism
//
// All costs come from internal/cost in 1/256-bit units. Every section
// 9.3 decision is an L(1) flag coded at even odds, so each carries
// cost.Literal(1); the magnitude fields are L(7) and L(6) literals and
// each shipped tree probability is an L(8) literal. Only id records pay
// model-dependent rates, through cost.SegmentIDCost under the table
// frame.EffectiveSegmentTreeProbs reports. Everything is integer
// arithmetic over fixed-size state with no maps and no allocations, so
// results are identical on every platform at every GOMAXPROCS.

const (
	maxQuantDelta   = 127 // 7 magnitude bits
	maxFilterDelta  = 63  // 6 magnitude bits
	quantMagBits    = 7
	filterMagBits   = 6
	signBits        = 1
	probLiteralBits = 8
)

type segmentControlCost struct {
	// HeaderCost prices the segmentation_enabled flag and, when
	// enabled, every further section 9.3 control decision: the
	// update_map and update_data flags, the absolute-vs-delta flag
	// and the four-plus-four feature gates with their magnitude and
	// sign literals, and the three probability-update gates with
	// their 8-bit literals.
	HeaderCost cost.Cost

	// IDCost prices every given segment id under the effective
	// segment-map tree. It is zero whenever ids is empty.
	IDCost cost.Cost

	// TotalCost is HeaderCost + IDCost.
	TotalCost cost.Cost
}

// priceSegmentControl prices the complete incremental section 9.3
// control-partition pressure of s plus the segment ids it codes: the
// header decisions byte for byte as frame.WriteHeader's writeSegmentation
// emits them, then the id records through cost.SegmentIDCost.
//
// Ids are legal only when segmentation is both enabled and updating the
// map; any other combination rejects a nonempty slice. Every id must be
// 0..3 and is validated before anything is priced, as is every enabled
// feature value (quantizer -127..127, loop filter -63..63) while dormant
// out-of-range values are ignored exactly as frame.WriteHeader ignores
// them. When ids are present, an effective tree probability of 0 is
// rejected before cost.SegmentIDCost is called, because probability 0
// cannot code a decision.
func priceSegmentControl(s frame.Segmentation, ids []uint8) segmentControlCost {
	validateSegmentControl(s, ids)
	header := priceSegmentHeader(s)
	id := priceSegmentIDs(s, ids)
	return segmentControlCost{
		HeaderCost: header,
		IDCost:     id,
		TotalCost:  header + id,
	}
}

// validateSegmentControl rejects any input priceSegmentControl cannot
// code, before anything is priced so no partially computed result can
// escape: ids behind a segmentation that is not coding them, out-of-range
// ids, and out-of-range enabled feature values.
func validateSegmentControl(s frame.Segmentation, ids []uint8) {
	// Ids exist in the stream only behind an updated map of an
	// enabled segmentation, so reject them anywhere else before
	// touching them further.
	if !s.Enabled || !s.UpdateMap {
		if len(ids) != 0 {
			panic(fmt.Sprintf("tqwebp/encoder: %d segment ids given but segmentation is not coding ids", len(ids)))
		}
	}

	// Validate every id before pricing anything, so a bad slice
	// cannot leave a partially computed result behind.
	for _, id := range ids {
		if id > 3 {
			panic(fmt.Sprintf("tqwebp/encoder: segment id %d is out of range 0..3", id))
		}
	}

	// Mirror frame.WriteHeader's validateSegmentation: only enabled
	// feature gates are checked, whatever the enabled flag says, so
	// dormant values cannot fail the call.
	for i, f := range s.Quantizer {
		if !f.Enabled {
			continue
		}
		if f.Value < -maxQuantDelta || f.Value > maxQuantDelta {
			panic(fmt.Sprintf("tqwebp/encoder: segmentation quantizer delta for segment %d is %d, out of range -%d..%d", i, f.Value, maxQuantDelta, maxQuantDelta))
		}
	}
	for i, f := range s.LoopFilter {
		if !f.Enabled {
			continue
		}
		if f.Value < -maxFilterDelta || f.Value > maxFilterDelta {
			panic(fmt.Sprintf("tqwebp/encoder: segmentation loop-filter delta for segment %d is %d, out of range -%d..%d", i, f.Value, maxFilterDelta, maxFilterDelta))
		}
	}
}

// priceSegmentHeader prices the section 9.3 header decisions byte for
// byte as frame.WriteHeader's writeSegmentation emits them: the
// segmentation-enabled flag always, then, when enabled, the update_map
// and update_data flags, the feature updates with their magnitude and
// sign literals, and the probability-update gates with their 8-bit
// literals.
func priceSegmentHeader(s frame.Segmentation) cost.Cost {
	var c cost.Cost

	// Section 9.3 opens with the segmentation-enabled flag, which is
	// charged in every configuration.
	c += cost.Literal(1)
	if !s.Enabled {
		return c
	}

	// The two top-level flags follow the enabled bit unconditionally.
	c += cost.Literal(1) // update_map
	c += cost.Literal(1) // update_data

	if s.UpdateData {
		c += cost.Literal(1) // absolute vs delta
		// One gate per segment; an open gate adds its magnitude
		// literal and its sign flag, exactly as
		// writeSegmentFeature writes them.
		for _, f := range s.Quantizer {
			c += cost.Literal(1) // gate
			if f.Enabled {
				c += cost.Literal(quantMagBits) + cost.Literal(signBits)
			}
		}
		for _, f := range s.LoopFilter {
			c += cost.Literal(1) // gate
			if f.Enabled {
				c += cost.Literal(filterMagBits) + cost.Literal(signBits)
			}
		}
	}

	if s.UpdateMap {
		for _, p := range s.TreeProbs {
			c += cost.Literal(1) // update gate
			if p.Update {
				c += cost.Literal(probLiteralBits)
			}
		}
	}

	return c
}

// priceSegmentIDs prices every given segment id under the effective
// segment-map tree through cost.SegmentIDCost. It is zero whenever ids
// is empty.
func priceSegmentIDs(s frame.Segmentation, ids []uint8) cost.Cost {
	if len(ids) == 0 {
		return 0
	}

	// The ids code against the table the decoder ends up
	// with. Probability 0 cannot code a decision, and
	// cost.SegmentIDCost would panic from deep inside its
	// tables, so refuse here first.
	probs := frame.EffectiveSegmentTreeProbs(s)
	for node, p := range probs {
		if p == 0 {
			panic(fmt.Sprintf("tqwebp/encoder: effective segment-tree probability for node %d is 0, which cannot code a decision", node))
		}
	}

	var c cost.Cost
	for _, id := range ids {
		c += cost.SegmentIDCost(&probs, id)
	}
	return c
}
