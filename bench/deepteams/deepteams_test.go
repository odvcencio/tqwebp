package deepteams

import (
	"os"
	"testing"

	"m31labs.dev/tqwebp/bench/deepteams/internal/dtbaseline"
	"m31labs.dev/tqwebp/internal/corpus"
)

const goldenDeepteamsBaseline = "testdata/golden/deepteams_baseline.txt"

// TestBaseline_MatchesGolden is bench/deepteams's own regeneration-check:
// it recomputes the deepteams/webp baseline table from the shared tqwebp
// corpus and the current dtbaseline code, and fails if the result differs
// from the committed golden file. A failure means either the corpus, the
// measured qualities, or the measurement code changed without
// regenerating the golden file: run `go generate` here and commit the
// result.
func TestBaseline_MatchesGolden(t *testing.T) {
	images, err := corpus.LoadAll("../..")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	table, err := dtbaseline.Run(images)
	if err != nil {
		t.Fatalf("dtbaseline.Run: %v", err)
	}
	got := table.String()

	want, err := os.ReadFile(goldenDeepteamsBaseline)
	if err != nil {
		t.Fatalf("read golden file %s: %v", goldenDeepteamsBaseline, err)
	}
	if got != string(want) {
		t.Errorf("deepteams/webp baseline table does not match %s.\nRun `go generate` from bench/deepteams and commit the result.\n\n--- got ---\n%s\n--- want ---\n%s", goldenDeepteamsBaseline, got, string(want))
	}
}
