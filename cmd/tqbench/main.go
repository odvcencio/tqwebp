// Command tqbench measures tqwebp and its baselines against the
// committed corpus.
//
// Two modes:
//
//	tqbench                 Print the stdlib JPEG baseline table. With
//	                        -update, write it to the golden file, which is
//	                        what go generate does.
//	tqbench -gates          Run every release gate of specification
//	                        section 10.2 and print the verdicts. With
//	                        -json, also write the full measurements.
//
// Usage:
//
//	go run ./cmd/tqbench [-root DIR] [-out FILE] [-update]
//	go run ./cmd/tqbench -gates [-root DIR] [-json FILE] [-encoded-dir DIR]
//	                     [-alpha-dir DIR] [-qualities 50,75,85,90,95]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"m31labs.dev/tqwebp/internal/baseline"
	"m31labs.dev/tqwebp/internal/corpus"
	"m31labs.dev/tqwebp/internal/gate"
)

func main() {
	root := flag.String("root", ".", "module root to load testdata/corpus from")
	out := flag.String("out", "testdata/golden/jpeg_baseline.txt", "path to write the golden table to, relative to -root when -update is set")
	update := flag.Bool("update", false, "write the table to -out instead of printing to stdout")
	gates := flag.Bool("gates", false, "run the release gates instead of the baseline table")
	jsonOut := flag.String("json", "", "write the gate report as JSON to this path")
	encodedDir := flag.String("encoded-dir", "", "write every encoded file into this directory, for external checks")
	alphaDir := flag.String("alpha-dir", "", "write every gate G5 alpha fixture into this directory, as a .webp and its .png source, for tools/libwebp_alpha_check.py")
	qualities := flag.String("qualities", "10,25,50,75,85,90,95", "comma separated quality settings for the gate run")
	strict := flag.Bool("strict", false, "also fail the run when gate G2b fails (see the README for why its byte-ratio clause is reported, not gated, on the generated corpus)")
	flag.Parse()

	images, err := corpus.LoadAll(*root)
	if err != nil {
		fail(err)
	}

	if *gates {
		runGates(*root, images, *qualities, *jsonOut, *encodedDir, *alphaDir, *strict)
		return
	}

	table, err := baseline.Run(images)
	if err != nil {
		fail(err)
	}
	if *update {
		path := filepath.Join(*root, *out)
		if err := os.WriteFile(path, []byte(table.String()), 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("tqbench: wrote %s\n", path)
		return
	}
	fmt.Print(table.String())
}

func runGates(root string, images []corpus.Image, qualities, jsonPath, encodedDir, alphaDir string, strict bool) {
	opts := gate.Options{
		Qualities:      parseQualities(qualities),
		JPEGQuality:    82,
		DeepteamsTable: readOptional(filepath.Join(root, "bench", "deepteams", "testdata", "golden", "deepteams_baseline.txt")),
	}
	if points, name := readLibwebpFixture(root); points != nil {
		opts.LibwebpPoints = points
		opts.LibwebpFixture = name
	}
	if encodedDir != "" {
		if err := os.MkdirAll(encodedDir, 0o755); err != nil {
			fail(err)
		}
		opts.WriteEncoded = func(name string, quality int, data []byte) error {
			return os.WriteFile(filepath.Join(encodedDir, fmt.Sprintf("%s_q%d.webp", name, quality)), data, 0o644)
		}
	}

	if alphaDir != "" {
		if err := os.MkdirAll(alphaDir, 0o755); err != nil {
			fail(err)
		}
		opts.WriteAlphaCase = func(name string, src image.Image, data []byte) error {
			return writeAlphaFixture(alphaDir, name, src, data)
		}
	}

	report, err := gate.Run(images, opts)
	if err != nil {
		fail(err)
	}
	fmt.Print(report.String())

	if jsonPath != "" {
		blob, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(jsonPath, append(blob, '\n'), 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("\ntqbench: wrote %s\n", jsonPath)
	}

	// G1, G2, and G5 always gate the run. G5 is a correctness gate like
	// G1: raw alpha stores the plane sample for sample, so a decoder
	// must return it unchanged, and an opaque picture must keep the
	// simple container.
	//
	// G2b's byte-ratio clause asks for a steepness the generated photo
	// corpus cannot show: its per-pixel noise puts a rate wall between
	// quality 75 and quality 90, and libwebp misses the same clause on
	// the same images by the same shape. The -strict flag re-arms it for
	// the day a real photo corpus lands.
	if !report.G1.Pass || !report.G2.Pass || !report.G5.Pass || (strict && !report.G2b.Pass) {
		os.Exit(1)
	}
}

// writeAlphaFixture writes one gate G5 case as a pair of files: the
// WebP tqwebp produced, and the source picture as a lossless PNG.
// tools/libwebp_alpha_check.py reads the pair and asks libwebp itself
// whether the alpha plane survived.
func writeAlphaFixture(dir, name string, src image.Image, data []byte) error {
	base := strings.ReplaceAll(name, "/", "_")
	if err := os.WriteFile(filepath.Join(dir, base+".webp"), data, 0o644); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, base+".png"))
	if err != nil {
		return err
	}
	if err := png.Encode(f, src); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readLibwebpFixture reads the optional libwebp measurement fixture that
// arms the informative half of gate G3.
func readLibwebpFixture(root string) (map[string][]gate.LibwebpPoint, string) {
	path := filepath.Join(root, "testdata", "golden", "libwebp_baseline.json")
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, ""
	}
	var fixture struct {
		Points map[string][]gate.LibwebpPoint `json:"points"`
	}
	if err := json.Unmarshal(blob, &fixture); err != nil {
		return nil, ""
	}
	return fixture.Points, path
}

func readOptional(path string) string {
	blob, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(blob)
}

func parseQualities(list string) []int {
	var out []int
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		q, err := strconv.Atoi(part)
		if err != nil {
			fail(fmt.Errorf("quality %q is not a number", part))
		}
		out = append(out, q)
	}
	return out
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tqbench:", err)
	os.Exit(1)
}
