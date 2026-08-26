package cost

import "m31labs.dev/tqwebp/internal/token"

// BlockCost prices exactly what token.Writer.WriteBlock writes for the
// same arguments: one 4x4 coefficient block of plane (token.YAfterY2,
// token.Y2, token.UV, or token.YWithDC) against neighbour context ctx
// (0, 1, or 2 counted nonzeros among above and left), starting at scan
// position first (1 for a luma block whose direct-current value travels
// in the Y2 block, 0 elsewhere), under probability table probs -- the
// same table the frame signalled, defaults included.
//
// The walk mirrors the writer decision for decision: end-of-block flags,
// nonzero flags with their context updates, the single-vs-multiple
// split, the magnitude categories with their extra bits, and the sign
// flag at even odds. It panics on an out-of-range plane, context, or
// start position, and on any level beyond token.MaxLevel, mirroring the
// writer's own refusals.
func BlockCost(plane, ctx, first int, levels *[16]int16, probs *token.Probs) Cost {
	c, _ := BlockCostAndNonZero(plane, ctx, first, levels, probs)
	return c
}

// BlockCostAndNonZero returns BlockCost's exact rate together with the
// nonzero flag the same coefficient scan discovers. RD callers need both to
// thread neighbour contexts; returning the flag avoids immediately scanning
// the block a second time.
func BlockCostAndNonZero(plane, ctx, first int, levels *[16]int16, probs *token.Probs) (Cost, bool) {
	if plane < 0 || plane >= token.NumPlanes {
		panic("tqwebp/cost: unknown coefficient plane")
	}
	if ctx < 0 || ctx > 2 {
		panic("tqwebp/cost: neighbour context out of range")
	}
	if first < 0 || first > 15 {
		panic("tqwebp/cost: start position out of range")
	}

	last := -1
	for i := 15; i >= first; i-- {
		if levels[i] != 0 {
			last = i
			break
		}
	}
	nonZero := last >= 0
	// Encoder context records describe the whole coefficient vector even
	// when coding starts after DC. Normally those skipped positions are zero,
	// but preserve BlockCost callers' established full-vector semantics for
	// hand-built records and invariant tests as well.
	if !nonZero {
		for i := 0; i < first; i++ {
			if levels[i] != 0 {
				nonZero = true
				break
			}
		}
	}

	planeProbs := &probs[plane]
	n := first
	p := &planeProbs[token.Bands[n]][ctx]

	var c Cost
	if last < 0 {
		return c + BitCost(p[0], false), nonZero
	}
	c += BitCost(p[0], true)

	for n < 16 {
		v := levels[n]
		if v == 0 {
			c += BitCost(p[1], false)
			n++
			p = &planeProbs[token.Bands[n]][0]
			continue
		}

		c += BitCost(p[1], true)
		mag := int(v)
		if mag < 0 {
			mag = -mag
		}
		if mag > token.MaxLevel {
			panic("tqwebp/cost: level exceeds the largest codable magnitude")
		}

		nextCtx := 2
		if mag == 1 {
			c += BitCost(p[2], false)
			nextCtx = 1
		} else {
			c += BitCost(p[2], true)
			c += magnitudeCost(p, mag)
		}
		c += BitCost(128, v < 0)

		n++
		if n == 16 {
			return c, true
		}
		p = &planeProbs[token.Bands[n]][nextCtx]
		if n > last {
			c += BitCost(p[0], false)
			return c, true
		}
		c += BitCost(p[0], true)
	}
	return c, true
}

// magnitudeCost prices the magnitude subtree of one coefficient whose
// value is two or larger, RFC 6386 section 13.2. The category boundaries
// and extra-bit widths derive from token.ExtraProbs exactly as the
// writer's own helpers do.
func magnitudeCost(p *[token.NumProbs]uint8, mag int) Cost {
	switch {
	case mag <= 4:
		c := BitCost(p[3], false)
		if mag == 2 {
			return c + BitCost(p[4], false)
		}
		return c + BitCost(p[4], true) + BitCost(p[5], mag == 4)

	case mag <= 10:
		c := BitCost(p[3], true) + BitCost(p[6], false)
		if mag <= 6 {
			// Category 1 covers 5 and 6.
			return c + BitCost(p[7], false) + BitCost(token.Cat1Prob, mag == 6)
		}
		// Category 2 covers 7 to 10.
		m := mag - 7
		return c + BitCost(p[7], true) +
			BitCost(token.Cat2Prob0, m>>1 == 1) +
			BitCost(token.Cat2Prob1, m&1 == 1)

	default:
		// Categories 3 to 6 cover 11 to 2114.
		cat := categoryOf(mag)
		b1 := cat >> 1
		b0 := cat & 1
		c := BitCost(p[3], true) + BitCost(p[6], true) +
			BitCost(p[8], b1 == 1) + BitCost(p[9+b1], b0 == 1)
		bits := extraBitCount(cat)
		rest := mag - categoryBase(cat)
		for i := 0; i < bits; i++ {
			c += BitCost(token.ExtraProbs[cat][i], rest>>(bits-1-i)&1 == 1)
		}
		return c
	}
}

// categoryBase returns the smallest magnitude of large category cat.
// It mirrors the writer's helper of the same shape.
func categoryBase(cat int) int { return 3 + (8 << uint(cat)) }

// categoryOf returns the large category, 0 to 3, that holds mag.
func categoryOf(mag int) int {
	for cat := 0; cat < 4; cat++ {
		if mag < categoryBase(cat)+(1<<uint(extraBitCount(cat))) {
			return cat
		}
	}
	return 3
}

// extraBitCount returns how many extra bits large category cat carries.
var extraBitCounts = [4]int{3, 4, 5, 11}

func extraBitCount(cat int) int { return extraBitCounts[cat] }

// ProbUpdateCost prices one coefficient-probability update decision,
// RFC 6386 section 13.4: a frame writes one such decision per entry of
// its probability tables, gated by the update probability the decoder
// derives from the same table. An update continues with the new 8-bit
// probability coded as an even-odds literal, which Literal(8) prices.
func ProbUpdateCost(entry uint8, update bool) Cost {
	c := BitCost(entry, update)
	if update {
		c += Literal(8)
	}
	return c
}
