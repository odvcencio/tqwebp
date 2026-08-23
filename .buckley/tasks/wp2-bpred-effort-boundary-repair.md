# Ox Alpha repair: B_PRED fixed-point RD effort boundary

Repository: `m31labs.dev/tqwebp`, MIT licensed at the repository root.
Return only a complete valid unified diff. Do not use Markdown fences or prose.

The exact-head WIP replaces coefficient-count B_PRED admission with exact Q8
syntax-rate scoring. Its new focused pricing/context tests, build, vet,
deepteams module, and G1/G2 release gates pass, but this established acceptance
test now fails deterministically on every run:

```text
--- FAIL: TestBPredSelectorEffortBoundary
    bpred_selector_test.go:52: method 1 selected 55 B_PRED macroblocks, want 51
```

Produce a small, principled production repair that makes the unchanged
acceptance test pass while retaining the exact-rate work. You may modify only:

- `internal/encoder/encoder.go`
- `internal/encoder/cost.go`
- `internal/encoder/bpred_selector_test.go`
- `internal/encoder/cost_test.go`

Prefer one production file plus narrowly targeted tests. Keep the response
under 140 net new Go lines. Do not edit generated files or other packages.

## Non-negotiable constraints

- Do not change `wantSelected = 51`, weaken/delete/skip any existing test, or
  special-case the fixture, dimensions, macroblock coordinates, selected
  count, or method/quality tuple.
- Do not restore coefficient-count admission or replace exact Q8 mode/token
  pricing with an approximation.
- Do not use floating point, randomness, goroutines, recursion, maps, global
  mutable state, new dependencies, or a convergence loop.
- Do not change public APIs or Method 0 behavior. Smooth Method 1 input must
  retain the whole-block bytes and keep `bPredModes == nil`.
- Preserve strict fixed-point RD ties: an exact score tie stays whole-block.
- Preserve reconstruction rollback on rejection, independent Y2 contexts,
  selected-neighbor B-mode contexts, skip pricing, and overflow fail-fast
  behavior.
- The rule must generalize to all images and quantizers. Any new threshold or
  margin must be derived from existing quantizer/rate/distortion values and
  justified by its local invariant, not tuned to the fixture count.
- Keep all work statically bounded per macroblock and deterministic across
  `GOMAXPROCS`.

## Required acceptance

The returned patch must be designed to pass, without changing existing
assertions:

```bash
go test ./internal/encoder -run 'TestBPredSelectorEffortBoundary|TestBPredSelectorKeepsSmoothWholePath|TestPreferBPredRDBoundaries|TestBPredReconstructionPruneBoundaries|TestBPredMacroblockCostMatchesSyntaxAndContexts|TestY2ContextsPreserveIndependentBPredInputs' -count=20
go test ./... -count=1
go vet ./...
go build ./...
```

Add focused table coverage for every new decision boundary. The test must show
strict behavior immediately below, at, and above the boundary, including an
overflow-safe extreme if arithmetic changes. Tests may call private helpers;
they must not encode knowledge of the corpus fixture or the number 51.

## Current exact production shape

Relevant constants in `encoder.go`:

```go
const (
    bPredMinMethod              = 1
    bPredPenaltyDivisor         = 4
    bPredTrialPenaltyMultiplier = 32
)
```

Macroblock selection in `encoder.go`:

```go
func (e *encoder) tryBPredLuma(mbx, mby, mbIndex int, mb *macroblock) {
    wholeDistortion := e.lumaMacroblockSSE(mbx, mby)
    penalty := e.bPredPenalty()
    if int64(wholeDistortion) <= penalty {
        return
    }

    wholeMB := *mb
    var wholeRecon [16 * 16]uint8
    copyLuma16(wholeRecon[:], 16, 0, 0, e.rec.Y, e.rec.YStride, mbx*16, mby*16)

    candidate := wholeMB
    var modes [16]predict.BMode
    e.codeBPredLuma(mbx, mby, &candidate, &modes, true)
    candidate.useBPred = true
    candidate.skip = macroblockSkipped(&candidate)
    bPredDistortion := e.lumaMacroblockSSE(mbx, mby)

    leftY2, upY2 := e.y2Contexts(mbx, mby)
    skipProb := e.skipProbability()
    wholeRate := e.lumaCandidateRateQ8(mbx, mby, &wholeMB, nil, false, skipProb, leftY2, upY2)
    bPredRate := e.lumaCandidateRateQ8(mbx, mby, &candidate, &modes, true, skipProb, leftY2, upY2)
    if !bPredImprovesReconstruction(wholeDistortion, bPredDistortion, penalty) ||
        !preferBPred(wholeDistortion, bPredDistortion, wholeRate, bPredRate, lumaLambda(e.q)) {
        *mb = wholeMB
        copyLuma16(e.rec.Y, e.rec.YStride, mbx*16, mby*16, wholeRecon[:], 16, 0, 0)
        return
    }

    *mb = candidate
    e.setBPredModes(mbIndex, modes)
}

func (e *encoder) shouldTrialBPred(wholePredictionSSE int32) bool {
    return int64(wholePredictionSSE) > e.bPredPenalty()*bPredTrialPenaltyMultiplier
}

func (e *encoder) bPredPenalty() int64 {
    step := int64(e.q.Y1.AC)
    penalty := step * step / bPredPenaltyDivisor
    if penalty < 1 {
        return 1
    }
    return penalty
}
```

Current private decision helpers in `cost.go`:

```go
type lumaRateQ8 struct {
    control uint64
    tokens  uint64
}

func (r lumaRateQ8) total() uint64 {
    if r.tokens > ^uint64(0)-r.control {
        panic("tqwebp: luma rate overflows uint64")
    }
    return r.control + r.tokens
}

func bPredImprovesReconstruction(wholeDistortion, bPredDistortion int32, minImprovement int64) bool {
    if minImprovement < 0 {
        panic("tqwebp: negative B_PRED distortion margin")
    }
    return int64(wholeDistortion)-int64(bPredDistortion) > minImprovement
}

func preferBPred(wholeDistortion, bPredDistortion int32, wholeRate, bPredRate lumaRateQ8, lambda uint64) bool {
    return rdScore(bPredDistortion, bPredRate.total(), lambda) <
        rdScore(wholeDistortion, wholeRate.total(), lambda)
}

func lumaLambda(q quantize.Quantizer) uint64 {
    if q.Y1.DC <= 0 || q.Y1.AC <= 0 {
        panic("tqwebp: non-positive luma quantizer")
    }
    qAverage := (uint64(q.Y1.DC) + 15*uint64(q.Y1.AC) + 8) >> 4
    lambda := qAverage * qAverage >> 7
    if lambda < 1 {
        return 1
    }
    return lambda
}

func rdScore(distortion int32, rateQ8, lambda uint64) uint64 {
    if distortion < 0 {
        panic("tqwebp: negative distortion")
    }
    distortionQ8 := uint64(distortion) * boolenc.CostScaleQ8
    if lambda != 0 && rateQ8 > (^uint64(0)-distortionQ8)/lambda {
        panic("tqwebp: RD score overflows uint64")
    }
    return distortionQ8 + rateQ8*lambda
}
```

`lumaCandidateRateQ8` already prices the fixed skip branch, exact whole or
B_PRED mode syntax, and exact Y/Y2 coefficient tokens against selected-neighbor
contexts. Treat that implementation and its tests as correct unless you can
demonstrate a concrete invariant violation with a new focused test.

## Existing boundary expectations

`TestPreferBPredRDBoundaries` requires lower reconstructed RD to win and exact
ties to stay whole. `TestBPredReconstructionPruneBoundaries` requires a
distortion improvement strictly greater than the supplied quantizer-derived
margin. `TestBPredSelectorEffortBoundary` uses the licensed
`screenshot_panel_grid` corpus image at quality 75: Method 0 must allocate no
B_PRED mode records; Method 1 must select exactly 51 valid B_PRED macroblocks,
emit bytes distinct from Method 0, and independently decode to the encoder's
reconstruction.

Do not merely update the count to 55. Repair the generalized decision boundary
and return the complete patch only.
