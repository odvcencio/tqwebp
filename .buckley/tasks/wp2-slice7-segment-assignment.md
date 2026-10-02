# Ox Alpha task: bounded segment assignment kernel

Repository: `m31labs.dev/tqwebp` (MIT licensed at the repository root).
Source base before this task-only checkpoint: `4579db9020f18c2331f6cf13ae83c9971d5a002a`.
Your patch applies to the exact clean commit containing this tracked task file.

Return only a valid unified diff. Do not include prose or Markdown fences.

## Scope

Add exactly these two files and modify nothing else:

- `internal/encoder/segment_assignment.go`
- `internal/encoder/segment_assignment_test.go`

This is a dormant, byte-inert planning kernel. Do not activate segmentation in
`encoder.go`; do not change serialization, frame writing, encoded bytes, public
APIs, dependencies, documentation, or any existing file.

## Required behavior

Implement a small unexported assignment API in package `encoder`. You may choose
the exact private result type and helper names, but the primary function must
accept a raster-ordered `[]segmentFeature` plus an explicitly supplied
`cost.Cost` content-score gain and return the chosen IDs together with the
resulting `frame.Segmentation` and enough cost-plan evidence for exact tests.

The kernel must satisfy all of these properties:

1. Assign the input deterministically to between one and four segments using
   integer arithmetic only. Input order is macroblock raster order.
2. Work is statically bounded for a given input length: fixed candidate counts
   and fixed iteration counts only. No randomness, floating point, maps,
   goroutines, recursion, or convergence-until-stable loops.
3. Every tie selects the lower segment ID. Canonicalize nonempty labels by the
   first raster occurrence, then collapse empty or feature-equivalent clusters.
   Repeated and equivalent inputs must produce the same canonical IDs on every
   `GOMAXPROCS` setting.
4. Derive the selected IDs with `deriveSegmentMapPlan` exactly once. Transfer its
   three `Probs` entries into `frame.Segmentation.TreeProbs`; an enabled plan has
   `Enabled=true`, `UpdateMap=true`, and `UpdateData=false`.
5. Price the enabled plan with `priceSegmentControl(segmentation, ids)` exactly
   once. The control ledger already includes map update gates, probability
   literals, and ID records. Never add `segmentMapPlan.SignalCost` or `IDCost`
   again.
6. Price the disabled baseline as
   `priceSegmentControl(frame.Segmentation{}, nil)`. Compare the supplied
   `cost.Cost` content gain directly with
   `enabled.TotalCost - disabled.TotalCost`; these values are all Q8 rate units.
   Enable only on a strict gain. Equality and loss must fall back to one segment.
7. Disabled output must contain an ID slice of the same length filled with zero,
   a zero-value `frame.Segmentation`, and no enabled-plan cost/map state that a
   caller could accidentally apply.
8. Do not mutate the input slice. Keep allocation count and candidate work
   explicitly bounded and test those bounds without fragile wall-clock timing.
9. Empty and one-element inputs must be defined, deterministic, and panic-free.
   Invalid states produced internally must fail closed to the disabled result.

## Required tests

The new test file must independently prove the implementation, not repeat its
helpers:

- a slow exhaustive oracle over small feature grids and all feasible 1--4
  segment labelings, including canonical-label and lower-ID tie rules;
- exact map/control ledger parity against `deriveSegmentMapPlan` and
  `priceSegmentControl`, proving `SignalCost` is not double counted;
- strict payoff boundaries at incremental cost minus one, equal, and plus one;
- empty, singleton, identical, empty-cluster, and adversarial extreme features;
- repeated-run and cross-`GOMAXPROCS` determinism;
- input non-mutation;
- allocation and fixed-work bounds using deterministic counters or bounded
  candidate statistics exposed only through private result/test-visible state.

Tests must use names containing `SegmentAssignment` so focused validation can
select them.

## Existing APIs and exact units

```go
// internal/encoder/segment_features.go
type segmentFeature struct {
    Mean     uint8
    Variance uint32
    Edge     uint32
}

func extractSegmentFeature(src *yuv.Planes, mbx, mby int) segmentFeature
```

The ranges are `Mean <= 255`, `Variance <= 1_065_369_600`, and
`Edge <= 122_400`.

```go
// internal/encoder/segment_map_cost.go
type segmentMapPlan struct {
    Probs      [3]frame.SegmentProbability
    Effective  [3]uint8
    SignalCost cost.Cost
    IDCost     cost.Cost
    TotalCost  cost.Cost
}

func deriveSegmentMapPlan(ids []uint8) segmentMapPlan
```

`deriveSegmentMapPlan` validates IDs in `0..3`, derives the optimal probability
updates, and sets `TotalCost = SignalCost + IDCost`. Its signal charge is already
covered by the full control ledger below.

```go
// internal/encoder/segment_control_cost.go
type segmentControlCost struct {
    HeaderCost cost.Cost
    IDCost     cost.Cost
    TotalCost  cost.Cost
}

func priceSegmentControl(s frame.Segmentation, ids []uint8) segmentControlCost
```

`priceSegmentControl` charges every section 9.3 header decision plus all ID
records. IDs are legal only when both `Enabled` and `UpdateMap` are true.

```go
// internal/frame/frame.go
type SegmentFeature struct {
    Enabled bool
    Value   int
}

type SegmentProbability struct {
    Update bool
    Value  uint8
}

type Segmentation struct {
    Enabled    bool
    UpdateMap  bool
    UpdateData bool
    Absolute   bool
    Quantizer  [4]SegmentFeature
    LoopFilter [4]SegmentFeature
    TreeProbs  [3]SegmentProbability
}
```

The zero `Segmentation` is the disabled representation.

```go
// internal/cost/cost.go
type Cost int64

const Unit Cost = 256 // one bit is 256 Q8 cost units
```

All comparisons in this task are rate-domain comparisons in `cost.Cost`; do not
introduce a lambda or convert to bytes/bits.

## Validation target

The resulting patch should pass:

```sh
go test ./internal/encoder -run 'Test.*SegmentAssignment' -count=20
go test -race ./internal/encoder -run 'Test.*SegmentAssignment' -count=1
go test ./internal/encoder -run 'Test(ExtractSegmentFeature|DeriveSegmentMapPlan|PriceSegmentControl|.*SegmentAssignment)' -count=1
go test ./... -count=1
go vet ./...
go build ./...
git diff --check
```
