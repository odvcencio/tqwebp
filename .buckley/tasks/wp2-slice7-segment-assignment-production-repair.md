# Ox Alpha repair: production-only segment assignment kernel

Repository: `m31labs.dev/tqwebp`, MIT licensed at the repository root.
Return only a valid unified diff. Do not use Markdown fences or prose.

Add exactly one new file and modify nothing else:

- `internal/encoder/segment_assignment.go`

This is a dormant, byte-inert planning kernel. Do not activate it, alter encoded
bytes, edit existing files, add dependencies, or create tests in this response.
Keep the complete new file compact enough to finish in one response (target at
most 280 Go source lines; concise comments only).

## Repair context

An earlier candidate response was rejected because it was truncated, selected
candidates only by a single plan-wide gain minus control rate (which makes the
all-zero labeling win regardless of features), swapped the variance and edge
threshold scales, and called `deriveSegmentMapPlan` and `priceSegmentControl`
for every candidate instead of exactly once for the selected IDs.

Produce a fresh implementation, not a continuation of the rejected response.

## Required private API

Implement an unexported function:

```go
func assignSegments(features []segmentFeature, gain cost.Cost) segmentAssignmentResult
```

The private result must expose, at minimum, same-length `IDs`, the resulting
`frame.Segmentation`, `Enabled`, the supplied `Gain`, enabled and disabled
`segmentControlCost` ledgers, number of candidates evaluated, and whether the
exhaustive path ran. Disabled output has all-zero IDs, zero segmentation, and a
zero enabled ledger.

## Deterministic assignment objective

Use this exact objective so the feature slice materially affects the plan:

1. Normalize each feature channel to integer 0..255, clamping raw values to the
   documented maximum first:
   - mean: `Mean` directly;
   - variance: `min(Variance, 1065369600) * 255 / 1065369600` using `uint64`;
   - edge: `min(Edge, 122400) * 255 / 122400` using `uint64`.
2. Canonicalize nonempty labels to 0,1,2,... by first raster occurrence.
3. Collapse feature-equivalent clusters. Two clusters are equivalent only when
   their counts and sums of all three normalized channels are identical. Merge
   into the lower ID, recanonicalize, and use at most three fixed merge passes.
4. Score a candidate without calling any map/control-cost API. For each
   nonempty cluster and channel, compute the integer centroid `sum/count`; the
   candidate dispersion is the saturating `uint64` sum over members of
   `abs(normalizedValue-centroid)`. Lower dispersion wins.
5. Dispersion ties prefer fewer nonempty segments, then lexicographically lower
   canonical raster IDs. These rules are the only candidate tie breakers.

No maps, randomness, floating point, recursion, goroutines, or
convergence-until-stable loops. Do not mutate `features`. Reuse bounded scratch
slices/arrays rather than allocating once per candidate. Saturate score
addition at `math.MaxUint64` (or an equivalent local constant) so every input
length/value is defined without wraparound.

## Candidate sets and fixed work

- For input length 1..6, enumerate exactly `4^n` raw labelings using a fixed
  mixed-radix counter. Canonicalize/collapse/score every labeling.
- For length greater than 6, evaluate exactly ten candidates: one all-zero
  labeling, then three threshold-prefix candidates for each channel.
- Raw threshold triplets are:
  - mean: 64, 128, 192;
  - variance: 266342400, 532684800, 799027200;
  - edge: 30600, 61200, 91800.
- For each channel evaluate prefix lengths 1, 2, and 3; a value greater than or
  equal to a used threshold increments its label. Evaluate duplicates too so
  the candidate counter stays exactly ten.

## Selected-plan cost and enablement

After candidate selection only:

1. Call `deriveSegmentMapPlan(selectedIDs)` exactly once.
2. Build `frame.Segmentation` with `Enabled=true`, `UpdateMap=true`,
   `UpdateData=false`, and copy the plan's three `Probs` into `TreeProbs`.
3. Call `priceSegmentControl(segmentation, selectedIDs)` exactly once.
4. Price the disabled baseline exactly as
   `priceSegmentControl(frame.Segmentation{}, nil)`.
5. Compare `gain` directly to
   `enabled.TotalCost-disabled.TotalCost` in Q8 rate units. Enable only when
   gain is strictly greater. Equality or loss returns the disabled form.
6. Never add `segmentMapPlan.SignalCost` or `IDCost` to the control ledger.

Empty input is deterministic and disabled. One-element input is defined.
Internally invalid labels/state fail closed. All loops and candidate work are
statically bounded for a given input length.

## Existing exact types

```go
type segmentFeature struct { Mean uint8; Variance uint32; Edge uint32 }

type segmentMapPlan struct {
    Probs [3]frame.SegmentProbability
    Effective [3]uint8
    SignalCost, IDCost, TotalCost cost.Cost
}
func deriveSegmentMapPlan(ids []uint8) segmentMapPlan

type segmentControlCost struct { HeaderCost, IDCost, TotalCost cost.Cost }
func priceSegmentControl(s frame.Segmentation, ids []uint8) segmentControlCost

type Segmentation struct {
    Enabled, UpdateMap, UpdateData, Absolute bool
    Quantizer, LoopFilter [4]frame.SegmentFeature
    TreeProbs [3]frame.SegmentProbability
}

type Cost int64
```

The implementation must format with `gofmt`, build in package `encoder`, and
remain private and byte-inert.
