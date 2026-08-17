package quantize

// IndexForQuality maps the public quality knob, 0 to 100, onto a VP8 base
// quantizer index, 127 down to 0. The map is data, not logic: the anchors
// below are measured on the corpus and can be re-measured without any
// change to the public interface (tqwebp specification section 7.4).
//
// Two properties matter more than any single anchor:
//
//   - The map must fall as quality rises, with no flat step. A flat step
//     is the failure the deepteams/webp curve shows, where more quality
//     buys bytes but no decibels (specification fact F54, gate G2b).
//   - The map must keep reaching the quantizer at the top of the range.
//     Quality 100 must land on index 0, the finest step the format has.
func IndexForQuality(quality int) Index {
	if quality <= 0 {
		return Index(qualityAnchors[0].index)
	}
	if quality >= 100 {
		return Index(qualityAnchors[len(qualityAnchors)-1].index)
	}
	for i := 1; i < len(qualityAnchors); i++ {
		hi := qualityAnchors[i]
		if quality > hi.quality {
			continue
		}
		lo := qualityAnchors[i-1]
		span := hi.quality - lo.quality
		step := lo.index - hi.index
		// Round to the nearest index, away from zero, so the map never
		// repeats an index across a wide quality span.
		return Index(lo.index - (2*(quality-lo.quality)*step+span)/(2*span))
	}
	return Index(qualityAnchors[len(qualityAnchors)-1].index)
}

// qualityAnchors carries the calibration points. Quality must rise and
// index must fall down the list; TestQualityMapIsMonotone pins that.
var qualityAnchors = []struct {
	quality int
	index   int
}{
	{0, 127},
	{10, 100},
	{25, 76},
	{50, 52},
	{75, 30},
	{85, 20},
	{90, 14},
	{95, 7},
	{100, 0},
}
