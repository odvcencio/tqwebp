package yuv

// This file implements the default effort tier's sharp RGB-to-4:2:0
// conversion. A decoder must reuse one U/V pair for four RGB pixels. Directly
// averaging RGB before conversion is fast, but fixed-point inverse rounding
// and channel clipping can make that pair needlessly inaccurate on saturated
// edges. We search the small, deterministic Y/U/V neighborhood in decoder
// space and retain the representation with the lowest luma-weighted RGB
// squared error, never accepting a box whose displayed luma error increases.

const (
	sharpEdgeRange  = 64
	sharpCacheLimit = 4096
	sharpLumaWeight = 8
)

type sharpRGB struct{ r, g, b, y uint8 }

type sharpBoxKey [4]uint32

type sharpBox struct {
	y         [4]uint8
	u, v      uint8
	score     int64
	lumaScore int64
}

func sharpenBox(src [4]sharpRGB, fastU, fastV uint8, cache map[sharpBoxKey]sharpBox) sharpBox {
	key := sharpKey(src)
	if cached, ok := cache[key]; ok {
		return cached
	}
	if len(cache) >= sharpCacheLimit {
		return sharpUnchangedBox(src, fastU, fastV)
	}

	baseline := sharpFastBox(src, fastU, fastV)
	best := baseline
	if candidate := sharpScoreUV(src, fastU, fastV); sharpBoxLess(candidate, best, fastU, fastV, baseline.lumaScore) {
		best = candidate
	}
	for _, step := range [...]int{16, 8, 4, 2, 1} {
		for {
			beforeU, beforeV := best.u, best.v
			for _, d := range [...][2]int{
				{-step, -step}, {0, -step}, {step, -step},
				{-step, 0}, {step, 0},
				{-step, step}, {0, step}, {step, step},
			} {
				u, okU := sharpAdd(best.u, d[0])
				v, okV := sharpAdd(best.v, d[1])
				if !okU || !okV {
					continue
				}
				candidate := sharpScoreUV(src, u, v)
				if sharpBoxLess(candidate, best, fastU, fastV, baseline.lumaScore) {
					best = candidate
				}
			}
			if best.u == beforeU && best.v == beforeV {
				break
			}
		}
	}
	cache[key] = best
	return best
}

func sharpEligible(src [4]sharpRGB, hardEdges bool) bool {
	if src[0] == src[1] && src[0] == src[2] && src[0] == src[3] {
		return true
	}
	if !hardEdges {
		return false
	}
	minR, maxR := src[0].r, src[0].r
	minG, maxG := src[0].g, src[0].g
	minB, maxB := src[0].b, src[0].b
	for i := 1; i < len(src); i++ {
		minR, maxR = min(minR, src[i].r), max(maxR, src[i].r)
		minG, maxG = min(minG, src[i].g), max(maxG, src[i].g)
		minB, maxB = min(minB, src[i].b), max(maxB, src[i].b)
	}
	return int(maxR-minR) >= sharpEdgeRange || int(maxG-minG) >= sharpEdgeRange || int(maxB-minB) >= sharpEdgeRange
}

func sharpKey(src [4]sharpRGB) (key sharpBoxKey) {
	for i, p := range src {
		key[i] = uint32(p.r)<<16 | uint32(p.g)<<8 | uint32(p.b)
	}
	return key
}

func sharpScoreUV(src [4]sharpRGB, u, v uint8) sharpBox {
	out := sharpBox{u: u, v: v}
	for i, p := range src {
		nominal := p.y
		y, score, lumaScore := sharpBestY(p, u, v, nominal)
		out.y[i] = y
		out.score += score
		out.lumaScore += lumaScore
	}
	return out
}

func sharpFastBox(src [4]sharpRGB, u, v uint8) sharpBox {
	out := sharpBox{u: u, v: v}
	for i, p := range src {
		out.y[i] = p.y
		got := sharpYUVToRGB(out.y[i], u, v)
		luma := sharpLumaError(p, got)
		out.score += sharpRGBError(p, got) + sharpLumaWeight*luma
		out.lumaScore += luma
	}
	return out
}

func sharpUnchangedBox(src [4]sharpRGB, u, v uint8) sharpBox {
	out := sharpBox{u: u, v: v}
	for i, p := range src {
		out.y[i] = p.y
	}
	return out
}

func sharpBestY(want sharpRGB, u, v, nominal uint8) (uint8, int64, int64) {
	bestY := nominal
	bestRGB := sharpYUVToRGB(bestY, u, v)
	bestLuma := sharpLumaError(want, bestRGB)
	bestScore := sharpRGBError(want, bestRGB) + sharpLumaWeight*bestLuma
	for _, step := range [...]int{64, 32, 16, 8, 4, 2, 1} {
		for {
			before := bestY
			for _, delta := range [...]int{-step, step} {
				y, ok := sharpAdd(bestY, delta)
				if !ok {
					continue
				}
				got := sharpYUVToRGB(y, u, v)
				luma := sharpLumaError(want, got)
				score := sharpRGBError(want, got) + sharpLumaWeight*luma
				if score < bestScore || score == bestScore && (luma < bestLuma || luma == bestLuma && sharpYLess(y, bestY, nominal)) {
					bestY, bestScore, bestLuma = y, score, luma
				}
			}
			if bestY == before {
				break
			}
		}
	}
	return bestY, bestScore, bestLuma
}

func sharpYUVToRGB(y, u, v uint8) sharpRGB {
	yy := int32(y) * 19077 >> 8
	return sharpRGB{
		r: sharpClipFix6(yy + (int32(v) * 26149 >> 8) - 14234),
		g: sharpClipFix6(yy - (int32(u) * 6419 >> 8) - (int32(v) * 13320 >> 8) + 8708),
		b: sharpClipFix6(yy + (int32(u) * 33050 >> 8) - 17685),
	}
}

func sharpClipFix6(v int32) uint8 {
	if v < 0 {
		return 0
	}
	v >>= 6
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func sharpRGBError(a, b sharpRGB) int64 {
	dr := int64(a.r) - int64(b.r)
	dg := int64(a.g) - int64(b.g)
	db := int64(a.b) - int64(b.b)
	return dr*dr + dg*dg + db*db
}

func sharpLumaError(a, b sharpRGB) int64 {
	d := int64(sharpRGBLuma(a) - sharpRGBLuma(b))
	return d * d
}

func sharpRGBLuma(p sharpRGB) int32 {
	return (19595*int32(p.r) + 38470*int32(p.g) + 7471*int32(p.b) + 32768) >> 16
}

func sharpBoxLess(a, b sharpBox, fastU, fastV uint8, maxLumaScore int64) bool {
	if a.lumaScore > maxLumaScore {
		return false
	}
	if a.score != b.score {
		return a.score < b.score
	}
	if a.lumaScore != b.lumaScore {
		return a.lumaScore < b.lumaScore
	}
	aDelta := absInt(int(a.u)-int(fastU)) + absInt(int(a.v)-int(fastV))
	bDelta := absInt(int(b.u)-int(fastU)) + absInt(int(b.v)-int(fastV))
	if aDelta != bDelta {
		return aDelta < bDelta
	}
	return a.u < b.u || a.u == b.u && a.v < b.v
}

func sharpYLess(a, b, nominal uint8) bool {
	aDelta := absInt(int(a) - int(nominal))
	bDelta := absInt(int(b) - int(nominal))
	return aDelta < bDelta || aDelta == bDelta && a < b
}

func sharpAdd(v uint8, delta int) (uint8, bool) {
	n := int(v) + delta
	if n < 0 || n > 255 {
		return 0, false
	}
	return uint8(n), true
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
