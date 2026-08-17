package oracle

import (
	"fmt"
	"sort"
	"strings"
)

// Row is one measured (image, codec, quality) point in a Table.
type Row struct {
	// Image is the corpus image name (for example "photo_gradient_wide").
	Image string
	// Class is the corpus content class (for example "photo").
	Class string
	// Codec names the encoder under test (for example "jpeg", "deepteams/webp").
	Codec string
	// Quality is the codec's quality setting, formatted by the caller (for
	// example "75").
	Quality string
	// Bytes is the encoded size.
	Bytes int
	// YPSNR is luma PSNR in decibels.
	YPSNR float64
	// SSIM is luma SSIM.
	SSIM float64
}

// Table is a size/quality report: a stable, sorted set of Rows with a
// String method that renders fixed-width text. Two Tables built from the
// same measurements always render identically, which is what makes the
// golden-file regeneration check in CI meaningful.
type Table struct {
	Rows []Row
}

// Add appends r to the table.
func (t *Table) Add(r Row) {
	t.Rows = append(t.Rows, r)
}

// Sort orders Rows by image, then codec, then quality, so String's output
// does not depend on the order measurements were taken in.
func (t *Table) Sort() {
	sort.Slice(t.Rows, func(i, j int) bool {
		a, b := t.Rows[i], t.Rows[j]
		if a.Image != b.Image {
			return a.Image < b.Image
		}
		if a.Codec != b.Codec {
			return a.Codec < b.Codec
		}
		return a.Quality < b.Quality
	})
}

// String renders the table as a fixed-width, space-padded text grid, one
// row per line, columns in the order: image, class, codec, quality, bytes,
// Y-PSNR, SSIM. The table is sorted first, so the output is stable.
func (t *Table) String() string {
	t.Sort()

	header := Row{Image: "image", Class: "class", Codec: "codec", Quality: "quality"}
	widths := [4]int{
		len(header.Image), len(header.Class), len(header.Codec), len(header.Quality),
	}
	for _, r := range t.Rows {
		widths[0] = maxLen(widths[0], r.Image)
		widths[1] = maxLen(widths[1], r.Class)
		widths[2] = maxLen(widths[2], r.Codec)
		widths[3] = maxLen(widths[3], r.Quality)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %-*s  %10s  %10s  %8s\n",
		widths[0], header.Image, widths[1], header.Class, widths[2], header.Codec, widths[3], header.Quality,
		"bytes", "y_psnr_db", "ssim")
	for _, r := range t.Rows {
		fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %-*s  %10d  %10s  %8.4f\n",
			widths[0], r.Image, widths[1], r.Class, widths[2], r.Codec, widths[3], r.Quality,
			r.Bytes, formatPSNR(r.YPSNR), r.SSIM)
	}
	return b.String()
}

func formatPSNR(v float64) string {
	if v == PSNRInf {
		return "inf"
	}
	return fmt.Sprintf("%.4f", v)
}

func maxLen(w int, s string) int {
	if len(s) > w {
		return len(s)
	}
	return w
}
