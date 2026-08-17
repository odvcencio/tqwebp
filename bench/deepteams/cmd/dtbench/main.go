// Command dtbench runs the deepteams/webp black-box baseline: encoding
// the tqwebp corpus at q75/82/90/95 and scoring the result through
// m31labs.dev/tqwebp/oracle. It prints a stable text table, and with
// -update it overwrites the committed golden file.
//
// Usage:
//
//	go run ./cmd/dtbench [-root DIR] [-out FILE] [-update]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"m31labs.dev/tqwebp/bench/deepteams/internal/dtbaseline"
	"m31labs.dev/tqwebp/internal/corpus"
)

func main() {
	root := flag.String("root", "../..", "tqwebp module root to load testdata/corpus from")
	out := flag.String("out", "testdata/golden/deepteams_baseline.txt", "path to write the golden table to, relative to the bench/deepteams module root when -update is set")
	update := flag.Bool("update", false, "write the table to -out instead of printing to stdout")
	flag.Parse()

	images, err := corpus.LoadAll(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dtbench:", err)
		os.Exit(1)
	}

	table, err := dtbaseline.Run(images)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dtbench:", err)
		os.Exit(1)
	}

	if *update {
		path := filepath.Join(".", *out)
		if err := os.WriteFile(path, []byte(table.String()), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "dtbench:", err)
			os.Exit(1)
		}
		fmt.Printf("dtbench: wrote %s\n", path)
		return
	}

	fmt.Print(table.String())
}
