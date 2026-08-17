package tqwebp

import (
	"os"
	"testing"

	"m31labs.dev/tqwebp/internal/baseline"
	"m31labs.dev/tqwebp/internal/corpus"
)

// goldenJPEGBaseline is the committed golden file cmd/tqbench -update
// writes and this test checks against. It holds the stdlib JPEG portion of
// the WP-0 baseline table: the part this module can regenerate on its own,
// without the deepteams/webp black-box baseline that lives in the
// bench/deepteams nested module (see bench/deepteams/README.md).
const goldenJPEGBaseline = "testdata/golden/jpeg_baseline.txt"

// TestBaseline_MatchesGolden is the WP-0 CI gate: "the baseline goldens'
// regeneration-check (table matches committed)". It recomputes the stdlib
// JPEG baseline table from the committed corpus and the current oracle
// code, and fails if the result differs from testdata/golden/jpeg_baseline.txt
// byte for byte. A failure here means either the corpus, the JPEG
// qualities, or the oracle's measurement code changed without
// regenerating the golden file: run `go generate` and commit the result.
func TestBaseline_MatchesGolden(t *testing.T) {
	images, err := corpus.LoadAll(".")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	table, err := baseline.Run(images)
	if err != nil {
		t.Fatalf("baseline.Run: %v", err)
	}
	got := table.String()

	want, err := os.ReadFile(goldenJPEGBaseline)
	if err != nil {
		t.Fatalf("read golden file %s: %v", goldenJPEGBaseline, err)
	}
	if got != string(want) {
		t.Errorf("baseline table does not match %s.\nRun `go generate ./...` from the module root and commit the result.\n\n--- got ---\n%s\n--- want ---\n%s", goldenJPEGBaseline, got, string(want))
	}
}

// TestBaseline_QualityRaisesBytesAndPSNR pins the monotonicity property
// (spec test T4) directly on the committed corpus: for every image, JPEG
// q90 must not undercut q75 on either bytes or Y-PSNR.
func TestBaseline_QualityRaisesBytesAndPSNR(t *testing.T) {
	images, err := corpus.LoadAll(".")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	table, err := baseline.Run(images)
	if err != nil {
		t.Fatalf("baseline.Run: %v", err)
	}

	byImageQuality := map[string]map[string]struct {
		bytes int
		psnr  float64
	}{}
	for _, row := range table.Rows {
		if byImageQuality[row.Image] == nil {
			byImageQuality[row.Image] = map[string]struct {
				bytes int
				psnr  float64
			}{}
		}
		byImageQuality[row.Image][row.Quality] = struct {
			bytes int
			psnr  float64
		}{row.Bytes, row.YPSNR}
	}

	for image, byQuality := range byImageQuality {
		q75, ok75 := byQuality["75"]
		q90, ok90 := byQuality["90"]
		if !ok75 || !ok90 {
			t.Fatalf("%s: missing q75 or q90 measurement", image)
		}
		if q90.bytes < q75.bytes {
			t.Errorf("%s: q90 bytes (%d) < q75 bytes (%d)", image, q90.bytes, q75.bytes)
		}
		if q90.psnr < q75.psnr {
			t.Errorf("%s: q90 Y-PSNR (%v) < q75 Y-PSNR (%v)", image, q90.psnr, q75.psnr)
		}
	}
}
