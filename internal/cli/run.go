package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"
	webp "m31labs.dev/tqwebp"
	"m31labs.dev/tqwebp/container"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Version is set by a release build with -ldflags. Unset builds are development.
var Version = "development"

type Result struct {
	SourceWidth       int      `json:"source_width,omitempty"`
	SourceHeight      int      `json:"source_height,omitempty"`
	Preset            string   `json:"preset"`
	Background        string   `json:"background,omitempty"`
	IgnoreOrientation bool     `json:"ignore_orientation"`
	Version           string   `json:"version"`
	Command           string   `json:"command"`
	OK                bool     `json:"ok"`
	Category          string   `json:"category"`
	Error             string   `json:"error,omitempty"`
	InputBytes        int64    `json:"input_bytes"`
	OutputBytes       *int64   `json:"output_bytes"`
	Width             int      `json:"width,omitempty"`
	Height            int      `json:"height,omitempty"`
	Animated          *bool    `json:"animated,omitempty"`
	Frames            *int     `json:"frames,omitempty"`
	Loops             *uint16  `json:"loop_count,omitempty"`
	Validated         bool     `json:"validated"`
	Codec             []string `json:"codec,omitempty"`
	Retained          []string `json:"metadata_retained,omitempty"`
	Removed           []string `json:"metadata_removed,omitempty"`
	Quality           int      `json:"quality"`
	Method            int      `json:"method"`
	MetadataPolicy    string   `json:"metadata_policy"`
	ElapsedMS         int64    `json:"elapsed_ms"`
}

// Run writes exactly one stderr JSON result when requested; binary data only
// reaches the chosen destination. stdoutTerminal is supplied by the entrypoint.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, stdoutTerminal bool) int {
	return run(ctx, args, stdin, stdout, stderr, stdoutTerminal, systemOutputOps())
}
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, terminal bool, ops outputOps) int {
	start := time.Now()
	o, e := parse(args)
	if e == flag.ErrHelp {
		_, err := io.WriteString(stdout, Help)
		if err != nil {
			return 5
		}
		return 0
	}
	// Parse errors still honor an explicitly requested JSON diagnostic mode.
	if e != nil {
		for _, a := range args {
			if a == "--json" || a == "--json=true" {
				o.json = true
			}
		}
		e = fail(2, "usage", e)
	}
	r := Result{Preset: o.preset, Background: o.background, IgnoreOrientation: o.ignoreOrientation, Version: Version, Command: o.command, Quality: o.quality, Method: o.method, MetadataPolicy: o.metadata}
	if e == nil && o.version {
		if o.json {
			r.OK = true
			r.Category = "ok"
			if json.NewEncoder(stderr).Encode(r) != nil {
				return 5
			}
		} else {
			if _, err := fmt.Fprintln(stdout, Version); err != nil {
				return 5
			}
		}
		return 0
	}
	if e == nil {
		e = ctx.Err()
	}
	if e == nil {
		e = execute(ctx, o, stdin, stdout, terminal, ops, &r)
	}
	if r.SourceWidth == 0 && r.Width > 0 {
		r.SourceWidth, r.SourceHeight = r.Width, r.Height
	}
	r.ElapsedMS = time.Since(start).Milliseconds()
	code := 0
	if e != nil {
		code, r.Category = classify(e)
		r.Error = e.Error()
		r.OutputBytes = nil
	} else {
		r.OK = true
		r.Category = "ok"
		if r.OutputBytes == nil {
			zero := int64(0)
			r.OutputBytes = &zero
		}
	}
	if o.json {
		if err := json.NewEncoder(stderr).Encode(r); err != nil {
			return 5
		}
	} else if e != nil {
		if _, err := fmt.Fprintf(stderr, "tqwebp: %s\n", e); err != nil {
			return 5
		}
	} else if o.command == "inspect" || o.command == "metadata" && o.output == "" {
		if err := json.NewEncoder(stdout).Encode(r); err != nil {
			return 5
		}
	} else {
		if _, err := fmt.Fprintf(stderr, "tqwebp: wrote %d bytes (%dx%d), quality=%d method=%d\n", *r.OutputBytes, r.Width, r.Height, o.quality, o.method); err != nil {
			return 5
		}
	}
	return code
}
func execute(ctx context.Context, o options, stdin io.Reader, stdout io.Writer, terminal bool, ops outputOps, r *Result) error {
	if o.output == "-" && terminal && !o.force {
		return fail(2, "usage", errors.New("binary terminal stdout requires --force"))
	}
	if o.output != "" && o.output != "-" && o.input != "-" {
		a, e := filepath.Abs(o.input)
		if e != nil {
			return fail(5, "io", e)
		}
		b, e := filepath.Abs(o.output)
		if e != nil {
			return fail(5, "io", e)
		}
		ai, ae := os.Stat(a)
		bi, be := os.Stat(b)
		if ae == nil && be == nil && os.SameFile(ai, bi) {
			return fail(2, "usage", errors.New("input and output identify the same file"))
		}
		if a == b {
			return fail(2, "usage", errors.New("in-place replacement is not supported; choose another output path"))
		}
	}
	data, e := readPath(ctx, o.input, stdin, o.inputBytes)
	if e != nil {
		return e
	}
	r.InputBytes = int64(len(data))
	switch o.command {
	case "inspect":
		return inspect(ctx, data, o, r)
	case "metadata":
		return metadataCommand(ctx, data, o, stdin, stdout, terminal, ops, r)
	}
	var doc *webp.Document
	var kind string
	if o.command == "encode" {
		doc, e = encodeManifest(ctx, data, o)
		kind = "manifest"
	} else {
		doc, kind, e = decodeDocument(ctx, data, o)
	}
	if e != nil {
		return e
	}
	r.Codec = []string{kind}
	r.SourceWidth, r.SourceHeight = doc.Canvas.X, doc.Canvas.Y
	if o.command != "encode" {
		r.Retained, r.Removed, e = transform(ctx, doc, o)
		if e != nil {
			return e
		}
	} else {
		r.Retained = metadataNames(doc.Metadata)
	}
	if o.frame >= 0 {
		if o.frame >= len(doc.Frames) {
			return fail(3, "invalid_input", errors.New("frame index outside stored sequence"))
		}
		doc.Frames = []webp.Frame{{Pixels: doc.Frames[o.frame].Pixels}}
		doc.Animated = false
		doc.LoopCount = 0
		doc.Background = color.NRGBA{}
	}
	r.Width, r.Height = doc.Canvas.X, doc.Canvas.Y
	animated := doc.Animated
	r.Animated = &animated
	n := len(doc.Frames)
	r.Frames = &n
	loops := doc.LoopCount
	r.Loops = &loops
	r.Validated = true
	if o.outDir != "" {
		written, err := extractFrames(ctx, doc, o, ops)
		if err == nil {
			r.OutputBytes = &written
		}
		return err
	}
	var out []byte
	if o.command == "decode" {
		if doc.Animated {
			return fail(3, "unsupported", errors.New("animated input requires --out-dir or explicit --frame N; refusing to flatten"))
		}
		out, e = encodePNG(ctx, doc.Frames[0].Pixels, doc.Metadata, o)
	} else {
		out, e = encodeDocument(ctx, doc, o)
	}
	if e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = outputBytes(ctx, o.output, out, o, stdout, terminal, ops); e != nil {
		return e
	}
	written := int64(len(out))
	r.OutputBytes = &written
	return nil
}
func metadataNames(m webp.Metadata) []string {
	var n []string
	for _, v := range []struct {
		name string
		b    []byte
	}{{"icc", m.ICC}, {"exif", m.EXIF}, {"xmp", m.XMP}} {
		if len(v.b) > 0 {
			n = append(n, v.name)
		}
	}
	return n
}
func containerLimits(o options) container.Limits {
	return container.Limits{MaxInputBytes: o.inputBytes, MaxOutputBytes: o.outputBytes, MaxChunks: 4096, MaxFrames: o.frames, MaxMetadataBytes: o.metadataBytes, MaxRetainedBytes: o.inputBytes}
}
func translateContainer(e error) error {
	var l *container.LimitError
	if errors.As(e, &l) {
		return &webp.LimitError{Resource: l.Resource, Limit: l.Limit, Actual: l.Actual}
	}
	return e
}
func inspect(ctx context.Context, data []byte, o options, r *Result) error {
	if !webpData(data) {
		if o.validate {
			d, kind, e := decodeDocument(ctx, data, o)
			if e != nil {
				return e
			}
			r.Width, r.Height = d.Canvas.X, d.Canvas.Y
			r.Codec = []string{kind}
			n := 1
			r.Frames = &n
			r.Retained = metadataNames(d.Metadata)
			r.Validated = true
			return nil
		}
		c, kind, e := image.DecodeConfig(bytes.NewReader(data))
		if e != nil {
			return fail(3, "invalid_input", e)
		}
		if e = checkDimensions(c.Width, c.Height, o); e != nil {
			return e
		}
		r.Width, r.Height = c.Width, c.Height
		r.Codec = []string{kind}
		return nil
	}
	op, cancel := codecContext(ctx, o)
	defer cancel()
	reader, e := webp.NewReader(op, bytes.NewReader(data), readLimits(o))
	if e != nil {
		return e
	}
	defer reader.Close()
	info := reader.Info()
	if e = checkDimensions(info.Canvas.X, info.Canvas.Y, o); e != nil {
		return e
	}
	r.Width, r.Height = info.Canvas.X, info.Canvas.Y
	animated := info.Animated
	r.Animated = &animated
	loops := info.LoopCount
	r.Loops = &loops
	r.Codec = []string{"webp"}
	if !o.validate {
		return nil
	}
	for {
		_, e = reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	metadata, e := reader.Metadata()
	if e != nil {
		return e
	}
	r.Retained = metadataNames(metadata)
	n := int(reader.Info().Completion.Frames)
	r.Frames = &n
	r.Validated = true
	f, e := container.Demux(op, bytes.NewReader(data), containerLimits(o))
	if e != nil {
		return translateContainer(e)
	}
	r.Retained = containerMetadataNames(f)
	r.Codec = nil
	seen := map[string]bool{}
	for _, frame := range f.Frames {
		k := "VP8"
		if frame.VP8L != nil {
			k = "VP8L"
		}
		if !seen[k] {
			seen[k] = true
			r.Codec = append(r.Codec, k)
		}
	}
	return nil
}
func metadataCommand(ctx context.Context, data []byte, o options, stdin io.Reader, stdout io.Writer, terminal bool, ops outputOps, r *Result) error {
	if !webpData(data) {
		return fail(3, "unsupported", errors.New("raw metadata operations require WebP input"))
	}
	op, cancel := codecContext(ctx, o)
	f, e := container.Demux(op, bytes.NewReader(data), containerLimits(o))
	cancel()
	if e != nil {
		return translateContainer(e)
	}
	r.Width, r.Height = f.Canvas.X, f.Canvas.Y
	animated := f.Animated
	r.Animated = &animated
	n := len(f.Frames)
	r.Frames = &n
	loops := f.LoopCount
	r.Loops = &loops
	// Structural validity is established; entropy/pixels were intentionally not decoded.
	r.Validated = false
	r.MetadataPolicy = "raw preservation"
	remove := map[string]bool{}
	if o.strip != "" {
		for _, name := range strings.Split(o.strip, ",") {
			switch name {
			case "all":
				remove["ICCP"] = true
				remove["EXIF"] = true
				remove["XMP "] = true
			case "icc":
				remove["ICCP"] = true
			case "exif":
				remove["EXIF"] = true
			case "xmp":
				remove["XMP "] = true
			default:
				return fail(2, "usage", errors.New("strip categories must be icc,exif,xmp or all"))
			}
		}
	}
	replacements := []container.Chunk{}
	for _, entry := range []struct{ tag, path string }{{"ICCP", o.icc}, {"EXIF", o.exif}, {"XMP ", o.xmp}} {
		if entry.path != "" {
			if entry.path == "-" {
				return fail(2, "usage", errors.New("metadata replacement requires a local file"))
			}
			raw, e := readPath(ctx, entry.path, stdin, o.metadataBytes)
			if e != nil {
				var le *webp.LimitError
				if errors.As(e, &le) {
					return &webp.LimitError{Resource: "metadata_bytes", Limit: o.metadataBytes, Actual: -1}
				}
				return e
			}
			remove[entry.tag] = true
			replacements = append(replacements, container.Chunk{FourCC: entry.tag, Data: raw})
		}
	}
	old := containerMetadataNames(f)
	kept := f.MetadataChunks[:0]
	for _, c := range f.MetadataChunks {
		if !remove[c.FourCC] {
			kept = append(kept, c)
		}
	}
	f.MetadataChunks = append(kept, replacements...)
	r.Retained = containerMetadataNames(f)
	for _, name := range old {
		found := false
		for _, keep := range r.Retained {
			found = found || keep == name
		}
		if !found {
			r.Removed = append(r.Removed, name)
		}
	}
	if o.output == "" {
		return nil
	}
	op, cancel = codecContext(ctx, o)
	out := &cappedBuffer{max: o.outputBytes, ctx: op}
	e = container.Mux(op, out, f, containerLimits(o))
	cancel()
	if e != nil {
		return translateContainer(e)
	}
	if e = outputBytes(ctx, o.output, out.data, o, stdout, terminal, ops); e != nil {
		return e
	}
	written := int64(len(out.data))
	r.OutputBytes = &written
	return nil
}

func containerMetadataNames(f *container.File) []string {
	var names []string
	seen := map[string]bool{}
	for _, c := range f.MetadataChunks {
		name := ""
		switch c.FourCC {
		case "ICCP":
			name = "icc"
		case "EXIF":
			name = "exif"
		case "XMP ":
			name = "xmp"
		}
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}
