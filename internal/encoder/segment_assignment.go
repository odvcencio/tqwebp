package encoder

import (
	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/frame"
)

const (
	segmentAssignmentMaxSegments     = 4
	segmentAssignmentExhaustiveLimit = 6
	segmentVarianceMaximum           = uint32(1065369600)
	segmentEdgeMaximum               = uint32(122400)
	// The selected map has at most 30 Q8 literal bits and each ID crosses at
	// most three branches priced at cost's 2048-unit worst case.
	segmentAssignmentMaxHeaderCost   = uint64(30 * 256)
	segmentAssignmentMaxIDCost       = uint64(3 * 2048)
	segmentAssignmentMaxSafeFeatures = ((^uint64(0) >> 1) - segmentAssignmentMaxHeaderCost) / segmentAssignmentMaxIDCost
)

// segmentAssignmentResult is byte-inert planning evidence. Nothing in the
// production encoder consumes it yet.
type segmentAssignmentResult struct {
	IDs                 []uint8
	Segmentation        frame.Segmentation
	Enabled             bool
	Gain                cost.Cost
	EnabledCost         segmentControlCost
	DisabledCost        segmentControlCost
	CandidatesEvaluated int
	ExhaustiveSearch    bool
}

type normalizedSegmentFeature struct {
	mean     uint16
	variance uint16
	edge     uint16
}

type segmentClusterAggregate struct {
	count                int
	mean, variance, edge uint64
}

// assignSegments selects a deterministic one-to-four segment labeling, then
// prices only that selected labeling. gain and both ledgers use Q8 rate units.
func assignSegments(features []segmentFeature, gain cost.Cost) segmentAssignmentResult {
	result := segmentAssignmentResult{
		IDs:          make([]uint8, len(features)),
		Gain:         gain,
		DisabledCost: priceSegmentControl(frame.Segmentation{}, nil),
	}
	if len(features) == 0 {
		return result
	}
	if uint64(len(features)) > segmentAssignmentMaxSafeFeatures {
		return result
	}

	normalized := normalizeSegmentFeatures(features)
	candidate := make([]uint8, len(features))
	canonical := make([]uint8, len(features))
	best := make([]uint8, len(features))
	bestScore := ^uint64(0)
	bestSegments := segmentAssignmentMaxSegments + 1
	found := false

	evaluate := func() {
		result.CandidatesEvaluated++
		segments, ok := canonicalizeSegmentIDs(canonical, candidate)
		if !ok {
			return
		}
		segments = collapseEquivalentSegmentClusters(canonical, normalized, segments)
		score := segmentCandidateDispersion(canonical, normalized, segments)
		if !found || score < bestScore ||
			(score == bestScore && segments < bestSegments) ||
			(score == bestScore && segments == bestSegments && segmentIDLexLess(canonical, best)) {
			copy(best, canonical)
			bestScore = score
			bestSegments = segments
			found = true
		}
	}

	result.ExhaustiveSearch = len(features) <= segmentAssignmentExhaustiveLimit
	if result.ExhaustiveSearch {
		for {
			evaluate()
			position := len(candidate) - 1
			for position >= 0 && candidate[position] == segmentAssignmentMaxSegments-1 {
				candidate[position] = 0
				position--
			}
			if position < 0 {
				break
			}
			candidate[position]++
		}
	} else {
		evaluate()
		thresholds := [3][3]uint32{
			{64, 128, 192},
			{266342400, 532684800, 799027200},
			{30600, 61200, 91800},
		}
		for channel := 0; channel < len(thresholds); channel++ {
			for prefix := 1; prefix <= len(thresholds[channel]); prefix++ {
				applySegmentThresholds(candidate, features, channel, thresholds[channel], prefix)
				evaluate()
			}
		}
	}

	if !found || bestSegments < 1 || bestSegments > segmentAssignmentMaxSegments {
		return result
	}
	plan := deriveSegmentMapPlan(best)
	segmentation := frame.Segmentation{
		Enabled:    true,
		UpdateMap:  true,
		UpdateData: false,
		TreeProbs:  plan.Probs,
	}
	enabledCost := priceSegmentControl(segmentation, best)
	incrementalCost := enabledCost.TotalCost - result.DisabledCost.TotalCost
	if incrementalCost < 0 || gain <= incrementalCost {
		return result
	}

	copy(result.IDs, best)
	result.Segmentation = segmentation
	result.EnabledCost = enabledCost
	result.Enabled = true
	return result
}

func normalizeSegmentFeatures(features []segmentFeature) []normalizedSegmentFeature {
	normalized := make([]normalizedSegmentFeature, len(features))
	for i, feature := range features {
		normalized[i] = normalizedSegmentFeature{
			mean:     uint16(feature.Mean),
			variance: scaleSegmentFeature(feature.Variance, segmentVarianceMaximum),
			edge:     scaleSegmentFeature(feature.Edge, segmentEdgeMaximum),
		}
	}
	return normalized
}

func scaleSegmentFeature(value, maximum uint32) uint16 {
	if value > maximum {
		value = maximum
	}
	return uint16(uint64(value) * 255 / uint64(maximum))
}

func canonicalizeSegmentIDs(dst, src []uint8) (int, bool) {
	if len(dst) != len(src) {
		return 0, false
	}
	remap := [segmentAssignmentMaxSegments]int8{-1, -1, -1, -1}
	next := int8(0)
	for i, label := range src {
		if label >= segmentAssignmentMaxSegments {
			return 0, false
		}
		if remap[label] < 0 {
			remap[label] = next
			next++
		}
		dst[i] = uint8(remap[label])
	}
	return int(next), true
}

func collapseEquivalentSegmentClusters(ids []uint8, features []normalizedSegmentFeature, segments int) int {
	for pass := 0; pass < segmentAssignmentMaxSegments-1; pass++ {
		aggregates := segmentClusterAggregates(ids, features)
		lower, upper := -1, -1
		for candidateUpper := 1; candidateUpper < segments && lower < 0; candidateUpper++ {
			for candidateLower := 0; candidateLower < candidateUpper; candidateLower++ {
				if aggregates[candidateLower] == aggregates[candidateUpper] {
					lower, upper = candidateLower, candidateUpper
					break
				}
			}
		}
		if lower < 0 {
			break
		}
		for i := range ids {
			if ids[i] == uint8(upper) {
				ids[i] = uint8(lower)
			}
		}
		var ok bool
		segments, ok = canonicalizeSegmentIDs(ids, ids)
		if !ok {
			return 0
		}
	}
	return segments
}

func segmentClusterAggregates(ids []uint8, features []normalizedSegmentFeature) [segmentAssignmentMaxSegments]segmentClusterAggregate {
	var aggregates [segmentAssignmentMaxSegments]segmentClusterAggregate
	for i, feature := range features {
		aggregate := &aggregates[ids[i]]
		aggregate.count++
		aggregate.mean = saturatingSegmentAdd(aggregate.mean, uint64(feature.mean))
		aggregate.variance = saturatingSegmentAdd(aggregate.variance, uint64(feature.variance))
		aggregate.edge = saturatingSegmentAdd(aggregate.edge, uint64(feature.edge))
	}
	return aggregates
}

func segmentCandidateDispersion(ids []uint8, features []normalizedSegmentFeature, segments int) uint64 {
	aggregates := segmentClusterAggregates(ids, features)
	var centroids [segmentAssignmentMaxSegments]normalizedSegmentFeature
	for i := 0; i < segments; i++ {
		count := uint64(aggregates[i].count)
		if count == 0 {
			continue
		}
		centroids[i] = normalizedSegmentFeature{
			mean:     uint16(aggregates[i].mean / count),
			variance: uint16(aggregates[i].variance / count),
			edge:     uint16(aggregates[i].edge / count),
		}
	}
	var score uint64
	for i, feature := range features {
		centroid := centroids[ids[i]]
		score = saturatingSegmentAdd(score, segmentAbsDiff(feature.mean, centroid.mean))
		score = saturatingSegmentAdd(score, segmentAbsDiff(feature.variance, centroid.variance))
		score = saturatingSegmentAdd(score, segmentAbsDiff(feature.edge, centroid.edge))
	}
	return score
}

func saturatingSegmentAdd(left, right uint64) uint64 {
	if ^uint64(0)-left < right {
		return ^uint64(0)
	}
	return left + right
}

func segmentAbsDiff(left, right uint16) uint64 {
	if left >= right {
		return uint64(left - right)
	}
	return uint64(right - left)
}

func segmentIDLexLess(left, right []uint8) bool {
	for i := range left {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return false
}

func applySegmentThresholds(dst []uint8, features []segmentFeature, channel int, thresholds [3]uint32, prefix int) {
	for i, feature := range features {
		var value uint32
		switch channel {
		case 0:
			value = uint32(feature.Mean)
		case 1:
			value = feature.Variance
		default:
			value = feature.Edge
		}
		var label uint8
		for threshold := 0; threshold < prefix; threshold++ {
			if value >= thresholds[threshold] {
				label++
			}
		}
		dst[i] = label
	}
}
