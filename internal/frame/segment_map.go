package frame

import (
	"fmt"

	"m31labs.dev/tqwebp/internal/boolenc"
)

// DefaultSegmentTreeProbs holds the segment-map tree probabilities a key
// frame starts from when it does not refresh the map (RFC 6386 section
// 9.3). Each node stays at 255 until an UpdateMap frame replaces it.
var DefaultSegmentTreeProbs = [3]uint8{255, 255, 255}

// EffectiveSegmentTreeProbs returns the probabilities the segment-map
// tree is coded against after this segmentation has been applied. It
// starts from the key-frame defaults and, only when the frame both
// enables segmentation and refreshes the map, takes over each TreeProbs
// entry whose Update gate is on. Entries left at their defaults keep the
// inherited probability, exactly as the decoder merges the updates.
func EffectiveSegmentTreeProbs(s Segmentation) [3]uint8 {
	probs := DefaultSegmentTreeProbs
	if !s.Enabled || !s.UpdateMap {
		return probs
	}
	for i, p := range s.TreeProbs {
		if p.Update {
			probs[i] = p.Value
		}
	}
	return probs
}

// WriteSegmentID codes one segment id with the segment-map tree of RFC
// 6386 section 9.3, walking the same nodes as cost.SegmentIDCost prices:
// the root bit id != 0 under treeProbs[0], then id != 1 under
// treeProbs[1], then id != 2 under treeProbs[2]. It panics when id is
// above 3, before anything reaches the stream.
func WriteSegmentID(enc *boolenc.Encoder, treeProbs *[3]uint8, id uint8) {
	if id > 3 {
		panic(fmt.Sprintf("tqwebp/frame: segment id %d is out of range 0..3", id))
	}
	if id != 0 {
		enc.WriteBool(treeProbs[0], true)
		if id != 1 {
			enc.WriteBool(treeProbs[1], true)
			enc.WriteBool(treeProbs[2], id != 2)
		} else {
			enc.WriteBool(treeProbs[1], false)
		}
	} else {
		enc.WriteBool(treeProbs[0], false)
	}
}
