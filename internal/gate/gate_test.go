package gate

import (
	"math"
	"testing"

	"m31labs.dev/tqwebp/internal/corpus"
)

// TestMatchBytesAtPSNR pins the interpolation gate G2 depends on: bytes
// move geometrically between two measured qualities, and a target outside
// the sweep produces a bound with a note attached.
func TestMatchBytesAtPSNR(t *testing.T) {
	points := []Point{
		{Image: "a", Quality: 50, Bytes: 1000, DisplayYPSNR: 30},
		{Image: "a", Quality: 75, Bytes: 4000, DisplayYPSNR: 36},
		{Image: "a", Quality: 90, Bytes: 16000, DisplayYPSNR: 42},
	}

	got := matchBytesAtPSNR(points, "a", 33)
	if got.Note != "" {
		t.Errorf("unexpected note: %s", got.Note)
	}
	if want := 2000.0; math.Abs(got.WebPBytes-want) > 1 {
		t.Errorf("bytes at 33 dB = %.1f, want %.1f", got.WebPBytes, want)
	}
	if got.WebPQualityL != 50 || got.WebPQualityH != 75 {
		t.Errorf("bracket is q%d..q%d, want q50..q75", got.WebPQualityL, got.WebPQualityH)
	}

	above := matchBytesAtPSNR(points, "a", 50)
	if above.Note == "" {
		t.Error("a target above the sweep produced no note")
	}
	if above.WebPBytes != 16000 {
		t.Errorf("bytes above the sweep = %.0f, want the largest measured file", above.WebPBytes)
	}

	below := matchBytesAtPSNR(points, "a", 20)
	if below.Note == "" {
		t.Error("a target below the sweep produced no note")
	}
}

// TestPSNRAtBytes pins the reading gate G4b uses: quality interpolated to
// a chosen file size.
func TestPSNRAtBytes(t *testing.T) {
	points := []Point{
		{Image: "a", Quality: 50, Bytes: 1000, DisplayYPSNR: 30},
		{Image: "a", Quality: 75, Bytes: 4000, DisplayYPSNR: 36},
	}
	got, note := psnrAtBytes(points, "a", 2000)
	if note != "" {
		t.Errorf("unexpected note: %s", note)
	}
	if want := 33.0; math.Abs(got-want) > 0.01 {
		t.Errorf("PSNR at 2000 bytes = %.3f, want %.3f", got, want)
	}
	if _, note := psnrAtBytes(points, "a", 100); note == "" {
		t.Error("a size below the sweep produced no note")
	}
}

// TestParseTable pins the fixture reader against the exact text an
// oracle.Table renders.
func TestParseTable(t *testing.T) {
	text := "image                  class       codec           quality       bytes   y_psnr_db      ssim\n" +
		"edge_prime_dims        screenshot  deepteams/webp  75             2938     47.0213    0.9970\n" +
		"edge_prime_dims        screenshot  deepteams/webp  90             4480     53.8412    0.9995\n"
	rows, err := parseTable(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("parsed %d rows, want 2", len(rows))
	}
	row := rows[tableKey{image: "edge_prime_dims", quality: 90}]
	if row.bytes != 4480 || math.Abs(row.psnr-53.8412) > 1e-9 {
		t.Errorf("row = %+v, want bytes 4480 at 53.8412 dB", row)
	}
	if _, err := parseTable("nothing here"); err == nil {
		t.Error("an empty table parsed without an error")
	}
}

// TestMedian pins the summary statistic every gate reports.
func TestMedian(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{3}, 3},
		{[]float64{3, 1}, 2},
		{[]float64{5, 1, 3}, 3},
		{[]float64{4, 1, 3, 2}, 2.5},
	}
	for _, c := range cases {
		if got := median(c.in); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("median(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestRunSmall runs the whole gate machinery over two small generated
// images, so the wiring stays covered without the cost of the corpus.
func TestRunSmall(t *testing.T) {
	images := []corpus.Image{
		{
			Spec: corpus.Spec{Name: "small_photo", Class: corpus.Photo, Width: 64, Height: 48, Seed: 1},
			Img:  corpus.Generate(corpus.Spec{Name: "small_photo", Class: corpus.Photo, Width: 64, Height: 48, Seed: 1}),
		},
		{
			Spec: corpus.Spec{Name: "small_flat", Class: corpus.Flat, Width: 48, Height: 48, Seed: 2},
			Img:  corpus.Generate(corpus.Spec{Name: "small_flat", Class: corpus.Flat, Width: 48, Height: 48, Seed: 2}),
		},
	}

	rep, err := Run(images, Options{Qualities: []int{50, 90}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.Points) != 4 {
		t.Fatalf("measured %d points, want 4", len(rep.Points))
	}
	if !rep.G1.Pass {
		t.Errorf("gate G1 failed: %v %v", rep.G1.DecodeErrors, rep.G1.MismatchNotes)
	}
	if rep.G1.ExactMatches != 4 {
		t.Errorf("%d exact reconstructions, want 4", rep.G1.ExactMatches)
	}
	for _, p := range rep.Points {
		if p.Bytes <= 0 || p.CodedYPSNR <= 0 || p.MillisPerMegapixel <= 0 {
			t.Errorf("point %+v carries an empty measurement", p)
		}
	}
	if rep.String() == "" {
		t.Error("the report rendered as an empty string")
	}
}
