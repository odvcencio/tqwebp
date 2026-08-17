// Command genbt601 writes the source image of the BT.601 golden fixture,
// testdata/golden/bt601/ramp.png. The fixture pins tqwebp's colour
// conversion against libwebp's own output (tqwebp specification sections
// 7.2 and 11.3).
//
// The rest of the fixture comes from libwebp itself and is produced once,
// outside the Go toolchain, by the commands recorded in
// testdata/golden/bt601/manifest.json.
//
// Usage:
//
//	go run ./cmd/genbt601 [-root DIR]
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image/png"
	"os"
	"path/filepath"

	"m31labs.dev/tqwebp/internal/yuv"
)

func main() {
	root := flag.String("root", ".", "module root to write testdata into")
	flag.Parse()

	dir := filepath.Join(*root, "testdata", "golden", "bt601")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "genbt601:", err)
		os.Exit(1)
	}

	var buf bytes.Buffer
	enc := &png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, yuv.ColorRamp()); err != nil {
		fmt.Fprintln(os.Stderr, "genbt601:", err)
		os.Exit(1)
	}
	path := filepath.Join(dir, "ramp.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "genbt601:", err)
		os.Exit(1)
	}
	fmt.Printf("genbt601: wrote %s\n", path)
}
