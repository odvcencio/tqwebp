// Package cli implements the tqwebp command through the public codec/container APIs.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

type options struct {
	command, input, output, outDir, preset, metadata, background        string
	icc, exif, xmp, strip                                               string
	quality, method, frame                                              int
	width, height                                                       int
	pixels, inputBytes, outputBytes, frames, totalPixels, metadataBytes int64
	timeout, durationLimit                                              time.Duration
	force, json, version, validate, ignoreOrientation                   bool
	qualitySet, methodSet, metadataSet                                  bool
}

func parse(args []string) (options, error) {
	o := options{command: "convert"}
	if len(args) > 0 {
		switch args[0] {
		case "inspect", "decode", "encode", "metadata":
			o.command = args[0]
			args = args[1:]
		}
	}
	fs := flag.NewFlagSet("tqwebp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.output, "o", "", "output path or -")
	fs.StringVar(&o.outDir, "out-dir", "", "new frame extraction directory")
	fs.StringVar(&o.preset, "preset", "standard", "standard, compact, smallest")
	fs.StringVar(&o.metadata, "metadata", "color", "none, color, all")
	fs.StringVar(&o.background, "background", "", "explicit #RRGGBB flattening")
	fs.StringVar(&o.icc, "icc", "", "replacement ICC file")
	fs.StringVar(&o.exif, "exif", "", "replacement EXIF file")
	fs.StringVar(&o.xmp, "xmp", "", "replacement XMP file")
	fs.StringVar(&o.strip, "strip", "", "metadata categories: icc,exif,xmp,all")
	fs.IntVar(&o.quality, "quality", 75, "lossy quality 1..100")
	fs.IntVar(&o.method, "method", 4, "effort 1..6")
	fs.IntVar(&o.frame, "frame", -1, "explicit zero-based displayed frame")
	fs.IntVar(&o.width, "max-width", 8192, "visible width bound; zero unlimited")
	fs.IntVar(&o.height, "max-height", 8192, "visible height bound; zero unlimited")
	fs.Int64Var(&o.pixels, "max-pixels", 16_000_000, "per-frame pixel bound")
	fs.Int64Var(&o.inputBytes, "max-input-bytes", 64<<20, "compressed input bound")
	fs.Int64Var(&o.outputBytes, "max-output-bytes", 32<<20, "complete per-output byte bound")
	fs.Int64Var(&o.frames, "max-frames", 1000, "stored frame bound")
	fs.Int64Var(&o.totalPixels, "max-total-pixels", 256_000_000, "aggregate pixel bound")
	fs.Int64Var(&o.metadataBytes, "max-metadata-bytes", 4<<20, "metadata payload bound")
	fs.DurationVar(&o.durationLimit, "max-duration", 10*time.Minute, "aggregate stored frame duration")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Second, "active codec budget; zero disables")
	fs.BoolVar(&o.force, "force", false, "replace output / allow binary terminal stdout")
	fs.BoolVar(&o.json, "json", false, "one JSON result on stderr")
	fs.BoolVar(&o.version, "version", false, "print version")
	fs.BoolVar(&o.validate, "validate", false, "fully validate inspection input")
	fs.BoolVar(&o.ignoreOrientation, "ignore-orientation", false, "leave orientation unapplied")
	// Support flags after positional paths without treating a flag value as input.
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			positionals = append(positionals, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		name, _, hasValue := strings.Cut(name, "=")
		if name == "h" || name == "help" {
			return o, flag.ErrHelp
		}
		f := fs.Lookup(name)
		if f == nil {
			return o, fmt.Errorf("unknown option %q", name)
		}
		flags = append(flags, a)
		isBool := false
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
			isBool = b.IsBoolFlag()
		}
		if !hasValue && !isBool {
			if i+1 >= len(args) {
				return o, fmt.Errorf("missing value for %s", a)
			}
			i++
			flags = append(flags, args[i])
		}
	}
	if e := fs.Parse(flags); e != nil {
		return o, e
	}
	fs.Visit(func(f *flag.Flag) {
		o.metadataSet = o.metadataSet || f.Name == "metadata"
		o.qualitySet = o.qualitySet || f.Name == "quality"
		o.methodSet = o.methodSet || f.Name == "method"
	})
	if o.version {
		if len(positionals) != 0 {
			return o, errors.New("--version takes no input")
		}
		return o, nil
	}
	if len(positionals) != 1 {
		return o, errors.New("exactly one local input path or - is required")
	}
	o.input = positionals[0]
	switch o.preset {
	case "standard":
	case "compact":
		if !o.methodSet {
			o.method = 5
		}
	case "smallest":
		if !o.methodSet {
			o.method = 6
		}
	default:
		return o, errors.New("unknown preset")
	}
	if o.quality < 1 || o.quality > 100 || o.method < 1 || o.method > 6 {
		return o, errors.New("quality must be 1..100 and method 1..6")
	}
	if o.timeout < 0 || o.durationLimit <= 0 || o.width < 0 || o.height < 0 || o.pixels <= 0 || o.inputBytes <= 0 || o.inputBytes > int64(int(^uint(0)>>1))-1 || o.outputBytes <= 0 || o.frames <= 0 || o.totalPixels <= 0 || o.metadataBytes <= 0 {
		return o, errors.New("invalid resource limit")
	}
	if o.metadata != "none" && o.metadata != "color" && o.metadata != "all" {
		return o, errors.New("metadata must be none, color or all")
	}
	if o.frame < -1 {
		return o, errors.New("frame index must be nonnegative")
	}
	if o.background != "" {
		if _, e := parseBackground(o.background); e != nil {
			return o, e
		}
	}
	if o.output != "" && o.outDir != "" {
		return o, errors.New("-o and --out-dir conflict")
	}
	if o.command != "decode" && o.outDir != "" {
		return o, errors.New("--out-dir requires decode")
	}
	if o.command != "metadata" && (o.icc != "" || o.exif != "" || o.xmp != "" || o.strip != "") {
		return o, errors.New("metadata edits require metadata command")
	}
	if o.command == "inspect" && (o.output != "" || o.frame >= 0 || o.background != "") {
		return o, errors.New("inspect cannot transform or write image output")
	}
	if o.command == "metadata" && (o.frame >= 0 || o.background != "") {
		return o, errors.New("metadata remux cannot transform pixels")
	}
	if o.command == "convert" || o.command == "encode" || o.command == "decode" {
		if o.output == "" && o.outDir == "" {
			return o, errors.New("-o output or decode --out-dir is required")
		}
	}
	if o.command == "metadata" && o.metadataSet {
		return o, errors.New("raw metadata remux uses --strip, not pixel --metadata policy")
	}
	if o.command == "metadata" && o.output == "" && (o.icc != "" || o.exif != "" || o.xmp != "" || o.strip != "") {
		return o, errors.New("metadata edits require -o")
	}
	if o.outDir != "" && o.frame >= 0 {
		return o, errors.New("--frame and --out-dir conflict")
	}
	if o.command == "encode" && o.frame >= 0 {
		return o, errors.New("manifest encode does not accept --frame")
	}
	return o, nil
}

const Help = `tqwebp [input.jpg|input.png|input.webp|-] -o output.webp [options]
  inspect input [--validate]       Header facts, or complete validation
  decode input -o output.png       Still or explicit --frame N
  decode input --out-dir frames    Every frame plus interchange manifest
  encode manifest.json -o out.webp Full-canvas frame manifest
  metadata input [-o out.webp] [--strip icc,exif,xmp|all] [--icc file ...]
Options: --quality 1..100 --method 1..6 --preset standard|compact|smallest
  compact/smallest are experimental effort recipes, not size guarantees
  --metadata none|color|all (default color) --ignore-orientation
  --background '#RRGGBB' --timeout 30s (0 disables active codec timeout)
  --max-width 8192 --max-height 8192 --max-pixels 16000000
  --max-input-bytes 67108864 --max-output-bytes 33554432
  --max-frames 1000 --max-total-pixels 256000000 --max-metadata-bytes 4194304
  --force --json --version
Input reads and ordinary PNG/JPEG decoding are not interrupted by --timeout.
First interrupt cancels cooperative work; a second forces exit130.
Binary output never shares stdout with diagnostics. --json writes one stderr result.
Default conversion preserves every WebP animation frame; --frame N is explicit extraction.
No remote URLs are fetched. Raw metadata remux does not transform profiles or pixels.
`
