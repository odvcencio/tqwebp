package gate

import (
	"fmt"
	"sort"
	"strings"
)

// String renders the report as fixed-width text: the measurement table
// first, then one block per gate. Two runs over the same measurements
// render identically.
func (rep *Report) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%-22s %-10s %7s %9s %10s %10s %8s %10s\n",
		"image", "class", "quality", "bytes", "coded_y_db", "shown_y_db", "ssim", "ms_per_mp")
	points := append([]Point(nil), rep.Points...)
	sort.Slice(points, func(i, j int) bool {
		if points[i].Image != points[j].Image {
			return points[i].Image < points[j].Image
		}
		return points[i].Quality < points[j].Quality
	})
	for _, p := range points {
		fmt.Fprintf(&b, "%-22s %-10s %7d %9d %10.2f %10.2f %8.4f %10.1f\n",
			p.Image, p.Class, p.Quality, p.Bytes, p.CodedYPSNR, p.DisplayYPSNR, p.SSIM, p.MillisPerMegapixel)
	}

	fmt.Fprintf(&b, "\nG1 correctness: %s\n", verdict(rep.G1.Pass))
	fmt.Fprintf(&b, "   %d encodes over %d images, %d exact reconstructions\n",
		rep.G1.Encodes, rep.G1.Images, rep.G1.ExactMatches)
	for _, note := range rep.G1.DecodeErrors {
		fmt.Fprintf(&b, "   decode error: %s\n", note)
	}
	for _, note := range rep.G1.MismatchNotes {
		fmt.Fprintf(&b, "   mismatch: %s\n", note)
	}

	fmt.Fprintf(&b, "\nG2 bytes against stdlib JPEG at equal shown luma PSNR: %s\n", verdict(rep.G2.Pass))
	fmt.Fprintf(&b, "   median ratio %.3f (bar 0.900), worst ratio %.3f (bar 1.100)\n",
		rep.G2.MedianRatio, rep.G2.WorstRatio)
	for _, m := range rep.G2.Matches {
		fmt.Fprintf(&b, "   %-22s jpeg q%d %8d bytes at %6.2f dB -> tqwebp %9.0f bytes (q%d..q%d) ratio %.3f\n",
			m.Image, m.JPEGQuality, m.JPEGBytes, m.JPEGYPSNR, m.WebPBytes, m.WebPQualityL, m.WebPQualityH, m.Ratio)
		if m.Note != "" {
			fmt.Fprintf(&b, "      note: %s\n", m.Note)
		}
	}

	fmt.Fprintf(&b, "\nG2b quality curve shape: %s (reported; -strict gates it)\n", verdict(rep.G2b.Pass))
	fmt.Fprintf(&b, "   median gain q75 to q90 %.2f dB (bar 2.50), median byte ratio %.2f (bar 2.20), strictly increasing %v\n",
		rep.G2b.MedianGain75To90, rep.G2b.MedianByteRatio, rep.G2b.StrictlyIncreases)
	if rep.G2b.ControlCodec != "" {
		fmt.Fprintf(&b, "   control %s on the same images: gain %.2f dB, byte ratio %.2f\n",
			rep.G2b.ControlCodec, rep.G2b.ControlGain, rep.G2b.ControlByteRatio)
	}
	for _, im := range rep.G2b.PerImage {
		fmt.Fprintf(&b, "   %-22s gain %5.2f dB, bytes x%.2f, increasing %v\n",
			im.Image, im.GainDB, im.ByteRatio, im.Increasing)
	}

	fmt.Fprintf(&b, "\nG3 speed and libwebp comparison (informative)\n")
	fmt.Fprintf(&b, "   median %.1f ms per megapixel, worst %.1f ms per megapixel\n",
		rep.G3.MedianMillisPerMegapixel, rep.G3.WorstMillisPerMegapixel)
	if rep.G3.Note != "" {
		fmt.Fprintf(&b, "   %s\n", rep.G3.Note)
	}
	if len(rep.G3.LibwebpMatches) > 0 {
		fmt.Fprintf(&b, "   fixture %s, median byte ratio against libwebp %.3f\n",
			rep.G3.LibwebpFixture, rep.G3.LibwebpMedianRatio)
		for _, m := range rep.G3.LibwebpMatches {
			fmt.Fprintf(&b, "   %-22s libwebp q%d %8d bytes at %6.2f dB -> tqwebp %9.0f bytes ratio %.3f\n",
				m.Image, m.JPEGQuality, m.JPEGBytes, m.JPEGYPSNR, m.WebPBytes, m.Ratio)
			if m.Note != "" {
				fmt.Fprintf(&b, "      note: %s\n", m.Note)
			}
		}
	}

	fmt.Fprintf(&b, "\nG4b against the deepteams/webp fixture (reported, gated at WP-2)\n")
	if !rep.G4b.Available {
		fmt.Fprintf(&b, "   %s\n", rep.G4b.Note)
	} else {
		fmt.Fprintf(&b, "   median gain at the same file size: photos %.2f dB, whole corpus %.2f dB\n",
			rep.G4b.MedianGainAtBytes, rep.G4b.MedianGainAtBytesAll)
		fmt.Fprintf(&b, "   median gain at the same quality number: %.2f dB\n", rep.G4b.MedianGain)
		for _, c := range rep.G4b.Comparisons {
			fmt.Fprintf(&b, "   %-22s q%-3d tqwebp %8d bytes %6.2f dB, deepteams %8d bytes %6.2f dB, gain at same bytes %6.2f dB\n",
				c.Image, c.Quality, c.TQWebPBytes, c.TQWebPYPSNR, c.DeepteamsBytes, c.DeepteamsYPSNR, c.GainAtSameBytes)
			if c.AtSameBytesNote != "" {
				fmt.Fprintf(&b, "      note: %s\n", c.AtSameBytesNote)
			}
		}
	}
	return b.String()
}

func verdict(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}
