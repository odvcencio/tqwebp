// Package baseline computes the WP-0 baseline table: stdlib JPEG at fixed
// quality levels, measured across the generated corpus through the oracle
// package. cmd/tqbench and baseline_test.go both call Run, so the CLI and
// the regeneration-check test can never drift from each other.
package baseline

import (
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"strconv"

	"m31labs.dev/tqwebp/internal/corpus"
	"m31labs.dev/tqwebp/oracle"
)

// JPEGQualities are the stdlib JPEG quality levels the WP-0 harness
// measures across the corpus (spec item 4: q75/82/90).
var JPEGQualities = []int{75, 82, 90}

// Run measures stdlib image/jpeg at every quality in JPEGQualities across
// every image in images, and returns the results as a sorted oracle.Table.
func Run(images []corpus.Image) (*oracle.Table, error) {
	table := &oracle.Table{}
	for _, img := range images {
		for _, q := range JPEGQualities {
			enc := jpegEncodeFunc(q)
			res, err := oracle.RoundTripWith(enc, img.Img, jpeg.Decode)
			if err != nil {
				return nil, fmt.Errorf("baseline: %s at jpeg q%d: %w", img.Spec.Name, q, err)
			}
			table.Add(oracle.Row{
				Image:   img.Spec.Name,
				Class:   string(img.Spec.Class),
				Codec:   "jpeg",
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

func jpegEncodeFunc(quality int) oracle.EncodeFunc {
	return func(w io.Writer, m image.Image) error {
		return jpeg.Encode(w, m, &jpeg.Options{Quality: quality})
	}
}
