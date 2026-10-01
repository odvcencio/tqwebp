package webp_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	webp "m31labs.dev/tqwebp"
	"os"
)

func ExampleNewReader() {
	encoded, err := os.ReadFile("testdata/animation/blend-dispose.webp")
	if err != nil {
		panic(err)
	}
	r, err := webp.NewReader(context.Background(), bytes.NewReader(encoded), webp.ReadLimits{})
	if err != nil {
		panic(err)
	}
	defer r.Close()
	count := 0
	for {
		frame, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(err)
		}
		// Use the borrowed full canvas now; copy it before advancing to retain it.
		_ = frame.Pixels
		_ = frame.Duration
		count++
	}
	metadata, err := r.Metadata()
	if err != nil {
		panic(err)
	}
	fmt.Println(count, r.Info().Completion.Frames, string(metadata.EXIF))
	// Output: 5 5 early-exif
}
func ExampleDecodeAll() {
	encoded, err := os.ReadFile("testdata/animation/one-frame-animation.webp")
	if err != nil {
		panic(err)
	}
	doc, err := webp.DecodeAll(context.Background(), bytes.NewReader(encoded), webp.ReadLimits{})
	if err != nil {
		panic(err)
	}
	// Frames and metadata are owned. Background is a hint, not a compositing fill.
	// VP8 retains nearest-chroma sampling; VP8L channels are exact.
	fmt.Println(doc.Animated, len(doc.Frames), doc.LoopCount, doc.Canvas)
	// Output: true 1 2 (5,5)
}
