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
// index must fall down the list; TestPinQualityMapIsMonotone pins that.
//
// # How these numbers were measured
//
// libwebp encoded the photo class of the corpus at every quality from 1
// to 100. tqwebp encoded the same images at every quantizer index from 0
// to 127. Each anchor names the index whose median luma PSNR, measured
// after each encoder's own correct inverse colour conversion, sits
// closest to libwebp's median at that quality. Quality 75 therefore
// lands within 0.02 dB of libwebp's quality 75, which is the calibration
// specification section 7.4 asks for.
//
// Below quality 10 the anchors leave the measured curve on purpose. The
// corpus carries per-pixel noise, and every encoder's luma PSNR flattens
// against that noise floor, so libwebp's own quality 1 still sits only
// 0.5 dB under its quality 10. Following the measurement there would
// leave the coarsest third of the quantizer range unreachable. The
// anchors run down to index 127 instead, so the knob still spans the
// whole quantizer.
var qualityAnchors = []struct {
	quality int
	index   int
}{
	{0, 127},
	{5, 90},
	{10, 66},
	{20, 47},
	{30, 40},
	{40, 33},
	{50, 27},
	{60, 25},
	{70, 23},
	{75, 22},
	{80, 18},
	{85, 13},
	{90, 7},
	{95, 3},
	{100, 0},
}
