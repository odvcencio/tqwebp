package token

import "m31labs.dev/tqwebp/internal/boolenc"

// BlockCostQ8 returns the exact fixed-point entropy cost of the decisions
// WriteBlock emits and the same non-zero-context result. The walk is kept
// separate from Writer so tests can compare the model against independently
// recorded syntax decisions.
func BlockCostQ8(probs *Probs, plane, ctx, first int, levels *[16]int16) (uint64, bool) {
	last := -1
	for i := 15; i >= first; i-- {
		if levels[i] != 0 {
			last = i
			break
		}
	}

	planeProbs := &probs[plane]
	n := first
	p := &planeProbs[Bands[n]][ctx]
	if last < 0 {
		return uint64(boolenc.DecisionCostQ8(p[0], false)), false
	}
	cost := uint64(boolenc.DecisionCostQ8(p[0], true))

	for n < 16 {
		v := levels[n]
		if v == 0 {
			cost += uint64(boolenc.DecisionCostQ8(p[1], false))
			n++
			p = &planeProbs[Bands[n]][0]
			continue
		}

		cost += uint64(boolenc.DecisionCostQ8(p[1], true))
		mag := int(v)
		if mag < 0 {
			mag = -mag
		}
		if mag > MaxLevel {
			panic("tqwebp/token: level exceeds the largest codable magnitude")
		}

		nextCtx := 2
		if mag == 1 {
			cost += uint64(boolenc.DecisionCostQ8(p[2], false))
			nextCtx = 1
		} else {
			cost += uint64(boolenc.DecisionCostQ8(p[2], true))
			cost += magnitudeCostQ8(p, mag)
		}
		cost += uint64(boolenc.DecisionCostQ8(128, v < 0))

		n++
		if n == 16 {
			return cost, true
		}
		p = &planeProbs[Bands[n]][nextCtx]
		if n > last {
			cost += uint64(boolenc.DecisionCostQ8(p[0], false))
			return cost, true
		}
		cost += uint64(boolenc.DecisionCostQ8(p[0], true))
	}
	return cost, true
}

func magnitudeCostQ8(p *[NumProbs]uint8, mag int) uint64 {
	decision := func(prob uint8, bit bool) uint64 {
		return uint64(boolenc.DecisionCostQ8(prob, bit))
	}

	switch {
	case mag <= 4:
		cost := decision(p[3], false)
		if mag == 2 {
			return cost + decision(p[4], false)
		}
		cost += decision(p[4], true)
		return cost + decision(p[5], mag == 4)

	case mag <= 10:
		cost := decision(p[3], true) + decision(p[6], false)
		if mag <= 6 {
			return cost + decision(p[7], false) + decision(cat1Prob, mag == 6)
		}
		return cost + decision(p[7], true) +
			decision(cat2Prob0, (mag-7)>>1 == 1) +
			decision(cat2Prob1, (mag-7)&1 == 1)

	default:
		cat := categoryOf(mag)
		b1 := cat >> 1
		b0 := cat & 1
		cost := decision(p[3], true) +
			decision(p[6], true) +
			decision(p[8], b1 == 1) +
			decision(p[9+b1], b0 == 1)

		bits := extraBits[cat]
		rest := mag - categoryBase(cat)
		for i := 0; i < bits; i++ {
			cost += decision(ExtraProbs[cat][i], rest>>(bits-1-i)&1 == 1)
		}
		return cost
	}
}

// ProbUpdateCostQ8 prices one update gate and its eight-bit replacement
// literal when present.
func ProbUpdateCostQ8(updateProb uint8, update bool) uint64 {
	cost := uint64(boolenc.DecisionCostQ8(updateProb, update))
	if update {
		cost += boolenc.LiteralCostQ8(8)
	}
	return cost
}

// ProbUpdatesCostQ8 returns the exact header cost of retaining every default
// coefficient probability, matching WriteProbUpdates.
func ProbUpdatesCostQ8() uint64 {
	var cost uint64
	for i := 0; i < NumPlanes; i++ {
		for j := 0; j < NumBands; j++ {
			for k := 0; k < NumContexts; k++ {
				for l := 0; l < NumProbs; l++ {
					cost += ProbUpdateCostQ8(UpdateProbs[i][j][k][l], false)
				}
			}
		}
	}
	return cost
}
