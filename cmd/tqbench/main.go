// Command tqbench runs the WP-0 baseline harness: stdlib JPEG at
// q75/82/90, measured across the generated corpus through the oracle
// package. It prints a stable text table, and with -update it overwrites
// the committed golden file so `go generate` can keep the golden table in
// sync with the corpus and the oracle's own measurement code.
//
// Usage:
//
//	go run ./cmd/tqbench [-root DIR] [-out FILE] [-update]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"m31labs.dev/tqwebp/internal/baseline"
	"m31labs.dev/tqwebp/internal/corpus"
)

func main() {
	root := flag.String("root", ".", "module root to load testdata/corpus from")
	out := flag.String("out", "testdata/golden/jpeg_baseline.txt", "path to write the golden table to, relative to -root when -update is set")
	update := flag.Bool("update", false, "write the table to -out instead of printing to stdout")
	flag.Parse()

	images, err := corpus.LoadAll(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tqbench:", err)
		os.Exit(1)
	}

	table, err := baseline.Run(images)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tqbench:", err)
		os.Exit(1)
	}

	if *update {
		path := filepath.Join(*root, *out)
		if err := os.WriteFile(path, []byte(table.String()), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "tqbench:", err)
			os.Exit(1)
		}
		fmt.Printf("tqbench: wrote %s\n", path)
		return
	}

	fmt.Print(table.String())
}
