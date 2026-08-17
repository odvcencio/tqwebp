// Command gencorpus renders the WP-0 test corpus and writes it to
// testdata/corpus as PNG files. It is the checked-in generator behind the
// committed corpus: run it again from the module root and it reproduces
// the same files byte for byte, because internal/corpus uses only a
// self-contained deterministic generator.
//
// Usage:
//
//	go run ./cmd/gencorpus [-root DIR]
package main

import (
	"flag"
	"fmt"
	"os"

	"m31labs.dev/tqwebp/internal/corpus"
)

func main() {
	root := flag.String("root", ".", "module root to write testdata/corpus under")
	flag.Parse()

	if err := corpus.WriteAll(*root); err != nil {
		fmt.Fprintln(os.Stderr, "gencorpus:", err)
		os.Exit(1)
	}

	fmt.Printf("gencorpus: wrote %d images to %s\n", len(corpus.Manifest), corpus.Dir)
}
