package encoder

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"runtime"
	"testing"

	"m31labs.dev/tqwebp/internal/cost"
	"m31labs.dev/tqwebp/internal/frame"
)

const segmentAssignmentTestGain = cost.Cost(1 << 60)

type segmentAssignmentTestLCG struct{ state uint64 }

func (l *segmentAssignmentTestLCG) next(modulus uint64) uint64 {
	l.state = l.state*6364136223846793005 + 1442695040888963407
	return (l.state >> 33) % modulus
}

type oracleSegmentFeature struct {
	mean, variance, edge uint16
}

type oracleSegmentAggregate struct {
	count                int
	mean, variance, edge uint64
}

type oracleSegmentAssignment struct {
	ids      []uint8
	score    uint64
	segments int
}

func oracleNormalizeSegmentFeatures(features []segmentFeature) []oracleSegmentFeature {
	result := make([]oracleSegmentFeature, len(features))
	for i, feature := range features {
		variance := feature.Variance
		if variance > 1065369600 {
			variance = 1065369600
		}
		edge := feature.Edge
		if edge > 122400 {
			edge = 122400
		}
		result[i] = oracleSegmentFeature{
			mean:     uint16(feature.Mean),
			variance: uint16(uint64(variance) * 255 / 1065369600),
			edge:     uint16(uint64(edge) * 255 / 122400),
		}
	}
	return result
}

func oracleCanonicalSegmentIDs(ids []uint8) ([]uint8, int) {
	result := make([]uint8, len(ids))
	remap := [4]int{-1, -1, -1, -1}
	next := 0
	for i, id := range ids {
		if id > 3 {
			panic("oracle received an invalid segment id")
		}
		if remap[id] < 0 {
			remap[id] = next
			next++
		}
		result[i] = uint8(remap[id])
	}
	return result, next
}

func oracleSegmentAggregates(ids []uint8, features []oracleSegmentFeature) [4]oracleSegmentAggregate {
	var aggregates [4]oracleSegmentAggregate
	for i, feature := range features {
		aggregate := &aggregates[ids[i]]
		aggregate.count++
		aggregate.mean += uint64(feature.mean)
		aggregate.variance += uint64(feature.variance)
		aggregate.edge += uint64(feature.edge)
	}
	return aggregates
}

func oracleCollapseEquivalentSegments(ids []uint8, features []oracleSegmentFeature, segments int) ([]uint8, int) {
	result := append([]uint8(nil), ids...)
	for pass := 0; pass < 3; pass++ {
		aggregates := oracleSegmentAggregates(result, features)
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
		for i := range result {
			if result[i] == uint8(upper) {
				result[i] = uint8(lower)
			}
		}
		result, segments = oracleCanonicalSegmentIDs(result)
	}
	return result, segments
}

func oracleSegmentDispersion(ids []uint8, features []oracleSegmentFeature, segments int) uint64 {
	aggregates := oracleSegmentAggregates(ids, features)
	var centroids [4]oracleSegmentFeature
	for i := 0; i < segments; i++ {
		count := uint64(aggregates[i].count)
		centroids[i] = oracleSegmentFeature{
			mean:     uint16(aggregates[i].mean / count),
			variance: uint16(aggregates[i].variance / count),
			edge:     uint16(aggregates[i].edge / count),
		}
	}
	var score uint64
	for i, feature := range features {
		centroid := centroids[ids[i]]
		score += oracleSegmentDifference(feature.mean, centroid.mean)
		score += oracleSegmentDifference(feature.variance, centroid.variance)
		score += oracleSegmentDifference(feature.edge, centroid.edge)
	}
	return score
}

func oracleSegmentDifference(left, right uint16) uint64 {
	if left >= right {
		return uint64(left - right)
	}
	return uint64(right - left)
}

func oracleSegmentLexLess(left, right []uint8) bool {
	for i := range left {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return false
}

func oracleBestSegmentAssignment(features []segmentFeature) oracleSegmentAssignment {
	normalized := oracleNormalizeSegmentFeatures(features)
	labels := make([]uint8, len(features))
	var best oracleSegmentAssignment
	found := false
	for {
		canonical, segments := oracleCanonicalSegmentIDs(labels)
		canonical, segments = oracleCollapseEquivalentSegments(canonical, normalized, segments)
		score := oracleSegmentDispersion(canonical, normalized, segments)
		if !found || score < best.score ||
			(score == best.score && segments < best.segments) ||
			(score == best.score && segments == best.segments && oracleSegmentLexLess(canonical, best.ids)) {
			best = oracleSegmentAssignment{append([]uint8(nil), canonical...), score, segments}
			found = true
		}
		position := len(labels) - 1
		for position >= 0 && labels[position] == 3 {
			labels[position] = 0
			position--
		}
		if position < 0 {
			return best
		}
		labels[position]++
	}
}

func TestSegmentAssignmentMatchesIndependentExhaustiveOracle(t *testing.T) {
	means := []uint8{0, 1, 63, 64, 127, 128, 191, 192, 255}
	variances := []uint32{0, 1, 266342399, 266342400, 532684800, 799027200, 1065369600}
	edges := []uint32{0, 1, 30599, 30600, 61200, 91800, 122400}
	random := segmentAssignmentTestLCG{state: 0x5eed1234}
	for trial := 0; trial < 64; trial++ {
		length := 1 + int(random.next(5))
		features := make([]segmentFeature, length)
		for i := range features {
			features[i] = segmentFeature{
				Mean:     means[random.next(uint64(len(means)))],
				Variance: variances[random.next(uint64(len(variances)))],
				Edge:     edges[random.next(uint64(len(edges)))],
			}
		}
		got := assignSegments(features, segmentAssignmentTestGain)
		want := oracleBestSegmentAssignment(features)
		if !got.Enabled {
			t.Fatalf("trial %d: high-gain plan was disabled", trial)
		}
		if !reflect.DeepEqual(got.IDs, want.ids) {
			t.Fatalf("trial %d: IDs=%v, want %v", trial, got.IDs, want.ids)
		}
		if got.CandidatesEvaluated != 1<<(2*uint(length)) {
			t.Fatalf("trial %d: candidates=%d, want %d", trial, got.CandidatesEvaluated, 1<<(2*uint(length)))
		}
		if !got.ExhaustiveSearch {
			t.Fatalf("trial %d: length %d did not use exhaustive search", trial, length)
		}
	}
}

func TestSegmentAssignmentLedgerParityAndSingularDerivation(t *testing.T) {
	features := []segmentFeature{
		{Mean: 0, Variance: 0, Edge: 0},
		{Mean: 255, Variance: 1065369600, Edge: 122400},
		{Mean: 64, Variance: 266342400, Edge: 30600},
		{Mean: 192, Variance: 799027200, Edge: 91800},
	}
	result := assignSegments(features, segmentAssignmentTestGain)
	if !result.Enabled {
		t.Fatal("high-gain plan was disabled")
	}
	plan := deriveSegmentMapPlan(result.IDs)
	wantSegmentation := frame.Segmentation{Enabled: true, UpdateMap: true, TreeProbs: plan.Probs}
	if !reflect.DeepEqual(result.Segmentation, wantSegmentation) {
		t.Fatalf("segmentation=%+v, want %+v", result.Segmentation, wantSegmentation)
	}
	wantCost := priceSegmentControl(wantSegmentation, result.IDs)
	if result.EnabledCost != wantCost {
		t.Fatalf("enabled ledger=%+v, want %+v", result.EnabledCost, wantCost)
	}
	if result.EnabledCost.TotalCost != result.EnabledCost.HeaderCost+result.EnabledCost.IDCost {
		t.Fatal("enabled total is not header plus IDs")
	}
	if result.DisabledCost != priceSegmentControl(frame.Segmentation{}, nil) {
		t.Fatal("disabled ledger mismatch")
	}

	parsed, err := parser.ParseFile(token.NewFileSet(), "segment_assignment.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "assignSegments" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if identifier, ok := call.Fun.(*ast.Ident); ok {
				calls[identifier.Name]++
			}
			return true
		})
	}
	if calls["deriveSegmentMapPlan"] != 1 || calls["priceSegmentControl"] != 2 {
		t.Fatalf("assignSegments calls derive=%d price=%d, want 1 and 2", calls["deriveSegmentMapPlan"], calls["priceSegmentControl"])
	}
}

func TestSegmentAssignmentStrictPayoffBoundaries(t *testing.T) {
	features := []segmentFeature{{Mean: 12, Edge: 100}, {Mean: 240, Edge: 5}, {Mean: 130, Variance: 500000000}}
	selected := assignSegments(features, segmentAssignmentTestGain)
	if !selected.Enabled {
		t.Fatal("high-gain plan was disabled")
	}
	delta := selected.EnabledCost.TotalCost - selected.DisabledCost.TotalCost
	if delta <= 0 {
		t.Fatalf("incremental cost=%d, want positive", delta)
	}
	for _, test := range []struct {
		name    string
		gain    cost.Cost
		enabled bool
	}{
		{name: "minus one", gain: delta - 1},
		{name: "equal", gain: delta},
		{name: "plus one", gain: delta + 1, enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := assignSegments(features, test.gain)
			if result.Enabled != test.enabled {
				t.Fatalf("Enabled=%v, want %v", result.Enabled, test.enabled)
			}
			if test.enabled {
				if !reflect.DeepEqual(result.IDs, selected.IDs) || result.EnabledCost != selected.EnabledCost {
					t.Fatal("gain changed the selected assignment")
				}
				return
			}
			if !reflect.DeepEqual(result.IDs, make([]uint8, len(features))) ||
				result.Segmentation != (frame.Segmentation{}) || result.EnabledCost != (segmentControlCost{}) {
				t.Fatalf("disabled result retained enabled state: %+v", result)
			}
		})
	}
}

func TestSegmentAssignmentDegenerateAndAdversarialInputs(t *testing.T) {
	empty := assignSegments(nil, segmentAssignmentTestGain)
	if empty.Enabled || len(empty.IDs) != 0 || empty.CandidatesEvaluated != 0 {
		t.Fatalf("empty result=%+v", empty)
	}

	singleton := assignSegments([]segmentFeature{{Mean: 255, Variance: ^uint32(0), Edge: ^uint32(0)}}, segmentAssignmentTestGain)
	if !singleton.Enabled || !reflect.DeepEqual(singleton.IDs, []uint8{0}) || singleton.CandidatesEvaluated != 4 {
		t.Fatalf("singleton result=%+v", singleton)
	}

	identicalFeatures := make([]segmentFeature, 6)
	for i := range identicalFeatures {
		identicalFeatures[i] = segmentFeature{Mean: 77, Variance: 123456, Edge: 789}
	}
	identical := assignSegments(identicalFeatures, segmentAssignmentTestGain)
	if !identical.Enabled || !reflect.DeepEqual(identical.IDs, make([]uint8, len(identicalFeatures))) {
		t.Fatalf("identical IDs=%v", identical.IDs)
	}

	large := []segmentFeature{
		{}, {Mean: 63, Variance: 266342399, Edge: 30599},
		{Mean: 64, Variance: 266342400, Edge: 30600},
		{Mean: 127, Variance: 532684799, Edge: 61199},
		{Mean: 128, Variance: 532684800, Edge: 61200},
		{Mean: 192, Variance: 799027200, Edge: 91800},
		{Mean: 255, Variance: ^uint32(0), Edge: ^uint32(0)},
	}
	adversarial := assignSegments(large, segmentAssignmentTestGain)
	if !adversarial.Enabled || adversarial.ExhaustiveSearch || adversarial.CandidatesEvaluated != 10 {
		t.Fatalf("large result=%+v", adversarial)
	}
	for _, id := range adversarial.IDs {
		if id > 3 {
			t.Fatalf("invalid selected ID %d", id)
		}
	}

	canonical := make([]uint8, 4)
	segments, ok := canonicalizeSegmentIDs(canonical, []uint8{3, 3, 1, 3})
	if !ok || segments != 2 || !reflect.DeepEqual(canonical, []uint8{0, 0, 1, 0}) {
		t.Fatalf("empty-label canonicalization=%v, %d, %v", canonical, segments, ok)
	}
	if _, ok := canonicalizeSegmentIDs(canonical[:1], []uint8{4}); ok {
		t.Fatal("out-of-range internal label was accepted")
	}
}

func TestSegmentAssignmentDeterminismAndInputImmutability(t *testing.T) {
	features := []segmentFeature{
		{Mean: 2, Variance: 10, Edge: 100}, {Mean: 220, Variance: 900000000, Edge: 100000},
		{Mean: 80, Variance: 200000000, Edge: 20000}, {Mean: 140, Variance: 600000000, Edge: 70000},
		{Mean: 33, Variance: 1000000, Edge: 1000}, {Mean: 200, Variance: 800000000, Edge: 90000},
		{Mean: 110, Variance: 400000000, Edge: 50000},
	}
	original := append([]segmentFeature(nil), features...)
	baseline := assignSegments(features, segmentAssignmentTestGain)
	oldProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(oldProcs)
	for _, procs := range []int{1, 2, 4} {
		runtime.GOMAXPROCS(procs)
		for repeat := 0; repeat < 25; repeat++ {
			if got := assignSegments(features, segmentAssignmentTestGain); !reflect.DeepEqual(got, baseline) {
				t.Fatalf("GOMAXPROCS=%d repeat=%d result drifted", procs, repeat)
			}
		}
	}
	if !reflect.DeepEqual(features, original) {
		t.Fatalf("input mutated: got %+v, want %+v", features, original)
	}
}

var segmentAssignmentAllocationSink segmentAssignmentResult

func TestSegmentAssignmentAllocationAndFixedWorkBounds(t *testing.T) {
	exhaustive := make([]segmentFeature, 6)
	for i := range exhaustive {
		exhaustive[i] = segmentFeature{Mean: uint8(i * 40), Variance: uint32(i) * 100000000, Edge: uint32(i) * 10000}
	}
	if allocations := testing.AllocsPerRun(10, func() {
		segmentAssignmentAllocationSink = assignSegments(exhaustive, segmentAssignmentTestGain)
	}); allocations > 8 {
		t.Fatalf("exhaustive allocations=%v, want <=8", allocations)
	}
	if segmentAssignmentAllocationSink.CandidatesEvaluated != 4096 {
		t.Fatalf("exhaustive candidates=%d, want 4096", segmentAssignmentAllocationSink.CandidatesEvaluated)
	}

	bounded := append(append([]segmentFeature(nil), exhaustive...), segmentFeature{Mean: 255})
	if allocations := testing.AllocsPerRun(50, func() {
		segmentAssignmentAllocationSink = assignSegments(bounded, segmentAssignmentTestGain)
	}); allocations > 8 {
		t.Fatalf("bounded allocations=%v, want <=8", allocations)
	}
	if segmentAssignmentAllocationSink.CandidatesEvaluated != 10 {
		t.Fatalf("bounded candidates=%d, want 10", segmentAssignmentAllocationSink.CandidatesEvaluated)
	}
}
