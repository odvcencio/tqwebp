package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"image"
	"image/draw"
	_ "image/png"
	"io"
	"os"
	"runtime"
	"sort"
	"time"

	native "github.com/HugoSmits86/nativewebp"
	tq "m31labs.dev/tqwebp"
)

func main() {
	codec := flag.String("codec", "tq4", "encoder")
	input := flag.String("input", "", "source PNG")
	output := flag.String("output", "", "output WebP")
	quality := flag.Int("quality", 75, "quality")
	repeats := flag.Int("repeats", 5, "timed repeats after warmup")
	flag.Parse()
	if *repeats < 1 || *repeats > 100 || *input == "" || *output == "" {
		panic("input and output are required; repeats must be 1 to 100")
	}
	runtime.GOMAXPROCS(1)
	f, err := os.Open(*input)
	check(err)
	im, _, err := image.Decode(f)
	check(err)
	check(f.Close())
	m := image.NewNRGBA(image.Rect(0, 0, im.Bounds().Dx(), im.Bounds().Dy()))
	draw.Draw(m, m.Bounds(), im, im.Bounds().Min, draw.Src)
	var encode func(io.Writer) error
	switch *codec {
	case "tq4", "tq5", "tq6":
		method := int((*codec)[2] - '0')
		encode = func(w io.Writer) error { return tq.Encode(w, m, &tq.Options{Quality: *quality, Method: method}) }
	case "chai", "bep":
		encode = cgoEncoder(*codec, m, *quality)
	case "native":
		encode = func(w io.Writer) error { return native.Encode(w, m, nil) }
	default:
		panic("unknown codec")
	}
	var b bytes.Buffer
	check(encode(&b))
	expected := append([]byte(nil), b.Bytes()...)
	times := make([]float64, *repeats)
	for i := range times {
		b.Reset()
		runtime.GC()
		start := time.Now()
		check(encode(&b))
		times[i] = float64(time.Since(start).Nanoseconds()) / 1e6
		if !bytes.Equal(expected, b.Bytes()) {
			panic("nondeterministic output")
		}
	}
	b.Reset()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	check(encode(&b))
	runtime.ReadMemStats(&after)
	check(os.WriteFile(*output, b.Bytes(), 0644))
	sort.Float64s(times)
	check(json.NewEncoder(os.Stdout).Encode(map[string]any{"codec": *codec, "quality": *quality, "bytes": b.Len(), "ms": times[len(times)/2], "times_ms": times, "alloc_bytes": after.TotalAlloc - before.TotalAlloc, "allocs": after.Mallocs - before.Mallocs, "width": m.Bounds().Dx(), "height": m.Bounds().Dy()}))
}
func check(err error) {
	if err != nil {
		panic(err)
	}
}
