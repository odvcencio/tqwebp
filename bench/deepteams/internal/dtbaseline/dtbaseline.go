// Package dtbaseline measures github.com/deepteams/webp, a black-box
// external pure-Go WebP encoder, across the tqwebp corpus. This package
// lives only in the bench/deepteams nested module: the root module's
// go.mod never lists deepteams/webp as a dependency.
//
// The comparison stays fair with the root module's own JPEG baseline
// (internal/baseline in the root module) because both go through
// m31labs.dev/tqwebp/oracle: the same PSNR/SSIM formulas, and for
// deepteams/webp's real WebP output, the same independent
// golang.org/x/image/webp decoder oracle.RoundTrip uses everywhere else.
package dtbaseline

import (
	"fmt"
	"image"
	"io"
	"strconv"

	deepteams "github.com/deepteams/webp"

	"m31labs.dev/tqwebp/internal/corpus"
	"m31labs.dev/tqwebp/oracle"
)

// Qualities are the deepteams/webp quality levels this harness measures.
// 75/82/90 line up with the root module's stdlib JPEG baseline; 95 adds
// the extra point the quality-plateau question needs (spec §4.3's
// coordinator numbers: q75->29.60dB, q95->30.75dB on a synthetic image).
var Qualities = []int{75, 82, 90, 95}

// Run measures deepteams/webp at every quality in Qualities across every
// image in images, and returns the results as a sorted oracle.Table.
func Run(images []corpus.Image) (*oracle.Table, error) {
	table := &oracle.Table{}
	for _, img := range images {
		for _, q := range Qualities {
			enc := deepteamsEncodeFunc(q)
			res, err := oracle.RoundTrip(enc, img.Img)
			if err != nil {
				return nil, fmt.Errorf("dtbaseline: %s at deepteams/webp q%d: %w", img.Spec.Name, q, err)
			}
			table.Add(oracle.Row{
				Image:   img.Spec.Name,
				Class:   string(img.Spec.Class),
				Codec:   "deepteams/webp",
				Quality: strconv.Itoa(q),
				Bytes:   res.EncodedBytes,
				YPSNR:   res.PSNR.Y,
				SSIM:    res.SSIM,
			})
		}
	}
	table.Sort()
	return table, nil
}

func deepteamsEncodeFunc(quality int) oracle.EncodeFunc {
	return func(w io.Writer, m image.Image) error {
		return deepteams.Encode(w, m, &deepteams.EncoderOptions{
			Quality: float32(quality),
			Method:  4,
		})
	}
}
