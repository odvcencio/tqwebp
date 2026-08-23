package encoder

import (
	"m31labs.dev/tqwebp/internal/yuv"
)

// This file holds the segment-feature probe used by the segmentation
// stage: three cheap integer statistics over one padded 16x16 luma
// macroblock. Everything here is integer-only and deterministic, reads
// the padded block exactly as it sits in the plane (the padding repeats
// the nearest visible pixel, so padded blocks measure like flat-ish
// blocks by construction), and allocates nothing.
//
// # The features
//
//   - Mean is floor(sum/256), the truncated average of the 256 luma
//     samples. It separates dark from bright regions.
//   - Variance is the population-variance numerator kept undivided:
//     sum((sample-mean)^2)*256 == sumSquares*256 - sum*sum, an exact
//     identity because the mean enters rationally. Its maximum,
//     reached when half the samples are 0 and half are 255, is
//     1,065,369,600 (= sumSquares 8,323,200 and sum 32,640 there).
//     It separates flat from detailed regions.
//   - Edge totals absolute differences over the 240 horizontal
//     adjacent pairs (15 per row, 16 rows) and the 240 vertical
//     adjacent pairs (15 per column, 16 columns); each difference is
//     at most 255, so its maximum is 480*255 = 122,400. It responds
//     to oriented structure that plain variance cannot see.
//
// Every intermediate value stays inside uint32 without wrapping: the
// largest possible sums-of-squares term is 16,646,400*256 =
// 4,261,478,400 and the largest squared sum is 65,280^2 =
// 4,261,478,400, both below 2^32, and their difference is never
// negative because sumSquares*256 >= sum*sum holds identically.

// segmentFeature carries the three statistics extractSegmentFeature
// measures over one macroblock's luma.
type segmentFeature struct {
	// Mean is floor(sum/256) over the block's 256 luma samples.
	Mean uint8

	// Variance is the undivided population-variance numerator
	// sumSquares*256 - sum*sum; see this file's comment for the
	// maximum 1,065,369,600.
	Variance uint32

	// Edge is the total absolute difference over the 240 horizontal
	// and 240 vertical adjacent sample pairs; maximum 122,400.
	Edge uint32
}

// extractSegmentFeature measures Mean, Variance, and Edge over the
// padded 16x16 luma macroblock at macroblock coordinates (mbx, mby)
// of the frame's planes. It indexes src.Y through src.YStride and
// touches nothing else, performs integer arithmetic only, and
// allocates nothing.
func extractSegmentFeature(src *yuv.Planes, mbx, mby int) segmentFeature {
	if src == nil {
		panic("tqwebp/encoder: extractSegmentFeature called with nil planes")
	}
	if mbx < 0 || mbx >= src.MBW {
		panic("tqwebp/encoder: macroblock column out of range")
	}
	if mby < 0 || mby >= src.MBH {
		panic("tqwebp/encoder: macroblock row out of range")
	}

	var sum, sumSquares, edge uint32
	base := (mby*16)*src.YStride + mbx*16
	for dy := 0; dy < 16; dy++ {
		row := base + dy*src.YStride
		for dx := 0; dx < 16; dx++ {
			v := uint32(src.Y[row+dx])
			sum += v
			sumSquares += v * v
			if dx > 0 {
				edge += absSampleDiff(src.Y[row+dx-1], src.Y[row+dx])
			}
		}
	}
	for dx := 0; dx < 16; dx++ {
		for dy := 1; dy < 16; dy++ {
			up := base + (dy-1)*src.YStride + dx
			down := up + src.YStride
			edge += absSampleDiff(src.Y[up], src.Y[down])
		}
	}

	return segmentFeature{
		Mean:     uint8(sum >> 8),
		Variance: sumSquares*256 - sum*sum,
		Edge:     edge,
	}
}

// absSampleDiff returns |a-b| for two luma samples.
func absSampleDiff(a, b uint8) uint32 {
	if a > b {
		return uint32(a) - uint32(b)
	}
	return uint32(b) - uint32(a)
}
