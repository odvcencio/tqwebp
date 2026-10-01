package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	webp "m31labs.dev/tqwebp"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type manifestFrame struct {
	Path       string `json:"path"`
	DurationMS int64  `json:"duration_ms"`
}
type manifestMetadata struct {
	ICC  string `json:"icc,omitempty"`
	EXIF string `json:"exif,omitempty"`
	XMP  string `json:"xmp,omitempty"`
}
type manifest struct {
	Version    int              `json:"version"`
	Canvas     [2]int           `json:"canvas"`
	Animated   bool             `json:"animated"`
	LoopCount  uint16           `json:"loop_count"`
	Background [4]uint8         `json:"background"`
	Frames     []manifestFrame  `json:"frames"`
	Metadata   manifestMetadata `json:"metadata"`
}

func parseManifest(b []byte, maxFrames int64) (manifest, error) {
	m := manifest{Version: 1, Animated: true}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return m, fail(3, "invalid_input", errors.New("frame manifest must be an object"))
	}
	seen := map[string]bool{}
	for d.More() {
		key, e := d.Token()
		if e != nil {
			return m, fail(3, "invalid_input", e)
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return m, fail(3, "invalid_input", errors.New("duplicate manifest field"))
		}
		seen[name] = true
		switch name {
		case "version":
			e = d.Decode(&m.Version)
		case "canvas":
			e = fixedNumbers(d, 2, func(i int, n int64) error {
				if n > int64(int(^uint(0)>>1)) || n < 0 {
					return errors.New("invalid canvas dimension")
				}
				m.Canvas[i] = int(n)
				return nil
			})
		case "animated":
			e = d.Decode(&m.Animated)
		case "loop_count":
			e = d.Decode(&m.LoopCount)
		case "background":
			e = fixedNumbers(d, 4, func(i int, n int64) error {
				if n < 0 || n > 255 {
					return errors.New("background channels must be0..255")
				}
				m.Background[i] = byte(n)
				return nil
			})
		case "metadata":
			e = smallObject(d, map[string]any{"icc": &m.Metadata.ICC, "exif": &m.Metadata.EXIF, "xmp": &m.Metadata.XMP})
		case "frames":
			t, err := d.Token()
			if err != nil || t != json.Delim('[') {
				return m, fail(3, "invalid_input", errors.New("frames must be an array"))
			}
			for d.More() {
				if int64(len(m.Frames)) >= maxFrames {
					return m, &webp.LimitError{Resource: "frames", Limit: maxFrames, Actual: int64(len(m.Frames)) + 1}
				}
				var f manifestFrame
				if err = smallObject(d, map[string]any{"path": &f.Path, "duration_ms": &f.DurationMS}); err != nil {
					return m, fail(3, "invalid_input", err)
				}
				m.Frames = append(m.Frames, f)
			}
			_, e = d.Token()
		default:
			return m, fail(3, "invalid_input", fmt.Errorf("unknown manifest field %q", name))
		}
		if e != nil {
			return m, fail(3, "invalid_input", e)
		}
	}
	if _, e = d.Token(); e != nil {
		return m, fail(3, "invalid_input", e)
	}
	if _, e = d.Token(); e != io.EOF {
		return m, fail(3, "invalid_input", errors.New("trailing manifest data"))
	}
	if m.Version != 1 || len(m.Frames) == 0 || m.Canvas[0] <= 0 || m.Canvas[1] <= 0 {
		return m, fail(3, "invalid_input", errors.New("invalid manifest version, canvas or frames"))
	}
	return m, nil
}
func encodeManifest(ctx context.Context, b []byte, o options) (*webp.Document, error) {
	m, e := parseManifest(b, o.frames)
	if e != nil {
		return nil, e
	}
	if e = checkDimensions(m.Canvas[0], m.Canvas[1], o); e != nil {
		return nil, e
	}
	if int64(m.Canvas[0]) > o.totalPixels/int64(m.Canvas[1])/int64(len(m.Frames)) {
		return nil, &webp.LimitError{Resource: "total_pixels", Limit: o.totalPixels, Actual: -1}
	}
	if m.Canvas[0] > 16383 || m.Canvas[1] > 16383 {
		return nil, webp.ErrTooLarge
	}
	if !m.Animated && (len(m.Frames) != 1 || m.LoopCount != 0 || m.Background != ([4]uint8{})) {
		return nil, fail(3, "invalid_input", webp.ErrInvalidDocument)
	}
	var admittedDuration int64
	for _, f := range m.Frames {
		if f.Path == "" || f.Path == "-" || strings.Contains(f.Path, "://") {
			return nil, fail(3, "invalid_input", errors.New("manifest paths must name local files"))
		}
		if f.DurationMS < 0 || f.DurationMS > 0xffffff || !m.Animated && f.DurationMS != 0 {
			return nil, fail(3, "invalid_input", errors.New("manifest duration is not representable"))
		}
		if f.DurationMS > int64(o.durationLimit/time.Millisecond)-admittedDuration {
			return nil, &webp.LimitError{Resource: "duration", Limit: int64(o.durationLimit), Actual: -1}
		}
		admittedDuration += f.DurationMS
	}
	doc := &webp.Document{Canvas: image.Pt(m.Canvas[0], m.Canvas[1]), Animated: m.Animated, LoopCount: m.LoopCount, Background: color.NRGBA{R: m.Background[0], G: m.Background[1], B: m.Background[2], A: m.Background[3]}}
	base := filepath.Dir(o.input)
	if o.input == "-" {
		base = "."
	}
	used := int64(len(b))
	metadataUsed := int64(0)
	totalDuration := int64(0)
	local := func(path string, metadata bool) ([]byte, error) {
		if path == "" || path == "-" || strings.Contains(path, "://") {
			return nil, fail(3, "invalid_input", errors.New("manifest paths must name local files"))
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		limit := o.inputBytes - used
		metadataBound := metadata && o.metadataBytes-metadataUsed <= limit
		if metadataBound {
			limit = o.metadataBytes - metadataUsed
		}
		data, e := readPath(ctx, path, nil, limit)
		if e != nil {
			var le *webp.LimitError
			if errors.As(e, &le) {
				if metadataBound {
					return nil, &webp.LimitError{Resource: "metadata_bytes", Limit: o.metadataBytes, Actual: -1}
				}
				return nil, &webp.LimitError{Resource: "input_bytes", Limit: o.inputBytes, Actual: -1}
			}
			return nil, e
		}
		used += int64(len(data))
		if metadata {
			metadataUsed += int64(len(data))
		}
		return data, nil
	}
	for _, entry := range []struct {
		path   string
		target *[]byte
	}{{m.Metadata.ICC, &doc.Metadata.ICC}, {m.Metadata.EXIF, &doc.Metadata.EXIF}, {m.Metadata.XMP, &doc.Metadata.XMP}} {
		if entry.path != "" {
			raw, e := local(entry.path, true)
			if e != nil {
				return nil, e
			}
			*entry.target = raw
		}
	}
	var sharedICC []byte
	for i, f := range m.Frames {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		if f.DurationMS < 0 || f.DurationMS > 0xffffff || !m.Animated && f.DurationMS != 0 {
			return nil, fail(3, "invalid_input", errors.New("manifest duration is not representable"))
		}
		if f.DurationMS > int64(o.durationLimit/time.Millisecond)-totalDuration {
			return nil, &webp.LimitError{Resource: "duration", Limit: int64(o.durationLimit), Actual: -1}
		}
		totalDuration += f.DurationMS
		raw, e := local(f.Path, false)
		if e != nil {
			return nil, e
		}
		if webpData(raw) {
			r, err := webp.NewReader(ctx, bytes.NewReader(raw), readLimits(o))
			if err != nil {
				return nil, err
			}
			animated := r.Info().Animated
			r.Close()
			if animated {
				return nil, fail(3, "invalid_input", errors.New("manifest frames must be still images"))
			}
		}
		frame, _, e := decodeDocument(ctx, raw, o)
		if e != nil {
			return nil, e
		}
		if frame.Animated || len(frame.Frames) != 1 {
			return nil, fail(3, "invalid_input", errors.New("each manifest path must contain one still image"))
		}
		if _, _, e = transform(ctx, frame, o); e != nil {
			return nil, e
		}
		if frame.Canvas != doc.Canvas {
			return nil, fail(3, "invalid_input", errors.New("manifest frame dimensions differ from canvas; no implicit crop or scale"))
		}
		if o.metadata == "all" && (len(frame.Metadata.EXIF) > 0 && !bytes.Equal(frame.Metadata.EXIF, doc.Metadata.EXIF) || len(frame.Metadata.XMP) > 0 && !bytes.Equal(frame.Metadata.XMP, doc.Metadata.XMP)) {
			return nil, invalidMetadata("per-frame EXIF/XMP differs from explicit global manifest metadata; use --metadata=color")
		}
		if i == 0 {
			sharedICC = frame.Metadata.ICC
		} else if !bytes.Equal(sharedICC, frame.Metadata.ICC) {
			return nil, invalidMetadata("frame profiles differ; no profile conversion is available")
		}
		doc.Frames = append(doc.Frames, webp.Frame{Pixels: frame.Frames[0].Pixels, Duration: time.Duration(f.DurationMS) * time.Millisecond})
	}
	if len(doc.Metadata.ICC) == 0 {
		doc.Metadata.ICC = sharedICC
	} else if len(sharedICC) > 0 && !bytes.Equal(sharedICC, doc.Metadata.ICC) {
		return nil, invalidMetadata("manifest profile differs from associated frame profile")
	}
	// Frames already received orientation/flattening. Apply only the selected
	// metadata policy to global explicitly supplied payloads.
	metaOptions := o
	metaOptions.ignoreOrientation = true
	metaOptions.background = ""
	_, _, e = transform(ctx, doc, metaOptions)
	if e != nil {
		return nil, e
	}
	return doc, nil
}
func extractFrames(ctx context.Context, doc *webp.Document, o options, ops outputOps) (int64, error) {
	// Create exclusively. Per-file commits are atomic/no-clobber; the directory
	// operation is not advertised as one atomic multi-file transaction.
	if e := os.Mkdir(o.outDir, 0755); e != nil {
		return 0, fail(5, "io", e)
	}
	created := []string{}
	success := false
	defer func() {
		if !success {
			for _, name := range created {
				_ = os.Remove(name)
			}
			_ = os.Remove(o.outDir)
		}
	}()
	m := manifest{Version: 1, Canvas: [2]int{doc.Canvas.X, doc.Canvas.Y}, Animated: doc.Animated, LoopCount: doc.LoopCount, Background: [4]uint8{doc.Background.R, doc.Background.G, doc.Background.B, doc.Background.A}}
	var total int64
	save := func(name string, data []byte) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if int64(len(data)) > o.outputBytes-total {
			return &webp.LimitError{Resource: "output_bytes", Limit: o.outputBytes, Actual: -1}
		}
		path := filepath.Join(o.outDir, name)
		if e := atomicFile(ctx, path, data, false, ops); e != nil {
			return e
		}
		created = append(created, path)
		total += int64(len(data))
		return nil
	}
	for i, f := range doc.Frames {
		name := fmt.Sprintf("frame-%06d.png", i)
		data, e := encodePNG(ctx, f.Pixels, doc.Metadata, o)
		if e != nil {
			return total, e
		}
		if e = save(name, data); e != nil {
			return total, e
		}
		m.Frames = append(m.Frames, manifestFrame{Path: name, DurationMS: int64(f.Duration / time.Millisecond)})
	}
	for _, entry := range []struct {
		name string
		data []byte
		path *string
	}{{"profile.icc", doc.Metadata.ICC, &m.Metadata.ICC}, {"metadata.exif", doc.Metadata.EXIF, &m.Metadata.EXIF}, {"metadata.xmp", doc.Metadata.XMP, &m.Metadata.XMP}} {
		if len(entry.data) > 0 {
			if e := save(entry.name, entry.data); e != nil {
				return total, e
			}
			*entry.path = entry.name
		}
	}
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return total, e
	}
	if e = save("manifest.json", append(b, '\n')); e != nil {
		return total, e
	}
	success = true
	return total, nil
}

func fixedNumbers(d *json.Decoder, n int, set func(int, int64) error) error {
	token, e := d.Token()
	if e != nil {
		return e
	}
	if token != json.Delim('[') {
		return errors.New("expected fixed-length array")
	}
	for i := 0; i < n; i++ {
		if !d.More() {
			return errors.New("array is too short")
		}
		var value int64
		if e = d.Decode(&value); e != nil {
			return e
		}
		if e = set(i, value); e != nil {
			return e
		}
	}
	if d.More() {
		return errors.New("array is too long")
	}
	_, e = d.Token()
	return e
}
func smallObject(d *json.Decoder, fields map[string]any) error {
	token, e := d.Token()
	if e != nil {
		return e
	}
	if token != json.Delim('{') {
		return errors.New("expected object")
	}
	seen := map[string]bool{}
	for d.More() {
		token, e = d.Token()
		if e != nil {
			return e
		}
		name, ok := token.(string)
		target, exists := fields[name]
		if !ok || !exists || seen[name] {
			return errors.New("unknown or duplicate object field")
		}
		seen[name] = true
		if e = d.Decode(target); e != nil {
			return e
		}
	}
	_, e = d.Token()
	return e
}
