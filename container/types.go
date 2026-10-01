// Package container inspects and remuxes WebP still and animated containers
// without decoding pixels. Validation covers headers and container structure,
// not entropy coding or displayed-frame composition.
package container

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"reflect"
	"time"
)

var (
	ErrInvalidContext     = errors.New("tqwebp/container: invalid context")
	ErrInvalidReader      = errors.New("tqwebp/container: invalid reader")
	ErrInvalidWriter      = errors.New("tqwebp/container: invalid writer")
	ErrInvalidLimits      = errors.New("tqwebp/container: invalid limits")
	ErrInvalidFormat      = errors.New("tqwebp/container: invalid format")
	ErrUnsupportedFeature = errors.New("tqwebp/container: unsupported feature")
	ErrInvalidMetadata    = errors.New("tqwebp/container: invalid metadata")
	ErrLimitExceeded      = errors.New("tqwebp/container: resource limit exceeded")
)

// FormatError locates a structural error. Frame is -1 when not applicable.
// Unwrap preserves underlying I/O errors, including io.ErrUnexpectedEOF.
type FormatError struct {
	Offset int64
	Chunk  string
	Frame  int
	Err    error
}

func (e *FormatError) Error() string {
	return fmt.Sprintf("tqwebp/container: offset %d chunk %q: %v", e.Offset, e.Chunk, e.Err)
}
func (e *FormatError) Unwrap() error { return e.Err }

// LimitError distinguishes application policy from malformed input.
type LimitError struct {
	Resource      string
	Limit, Actual int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("tqwebp/container: %s %d exceeds %d", e.Resource, e.Actual, e.Limit)
}
func (e *LimitError) Is(target error) bool { return target == ErrLimitExceeded }

// Limits uses conservative defaults for zero fields; negative fields are invalid.
// Retained bytes counts payload bytes only, not Go object overhead or process RSS.
// No field imposes a CPU deadline or interrupts a blocked caller I/O method.
type Limits struct{ MaxInputBytes, MaxOutputBytes, MaxChunks, MaxFrames, MaxMetadataBytes, MaxRetainedBytes int64 }

func DefaultLimits() Limits {
	return Limits{MaxInputBytes: 64 << 20, MaxOutputBytes: 64 << 20, MaxChunks: 4096, MaxFrames: 1000, MaxMetadataBytes: 4 << 20, MaxRetainedBytes: 64 << 20}
}
func normalize(l Limits) (Limits, error) {
	d := DefaultLimits()
	p := []*int64{&l.MaxInputBytes, &l.MaxOutputBytes, &l.MaxChunks, &l.MaxFrames, &l.MaxMetadataBytes, &l.MaxRetainedBytes}
	v := []int64{d.MaxInputBytes, d.MaxOutputBytes, d.MaxChunks, d.MaxFrames, d.MaxMetadataBytes, d.MaxRetainedBytes}
	for i, x := range p {
		if *x < 0 {
			return l, ErrInvalidLimits
		}
		if *x == 0 {
			*x = v[i]
		}
	}
	return l, nil
}
func check(resource string, n, max int64) error {
	if n > max {
		return &LimitError{resource, max, n}
	}
	return nil
}
func nilValue(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return r.IsNil()
	}
	return false
}
func contextError(ctx context.Context) error {
	if nilValue(ctx) {
		return ErrInvalidContext
	}
	return ctx.Err()
}
func format(off int64, id string, err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = errors.Join(ErrInvalidFormat, err)
	}
	return &FormatError{off, id, -1, err}
}

// Metadata is a convenience view of the first occurrence of each category.
// Bytes are opaque: no profiles are applied, TIFF parsed, or XML executed.
type Metadata struct{ ICC, EXIF, XMP []byte }

// Chunk owns a FourCC and its unpadded payload. MetadataChunks permits ICCP,
// EXIF and XMP only; UnknownChunks permits no recognized WebP FourCC.
type Chunk struct {
	FourCC string
	Data   []byte
}

// Blend selects how an animation frame is drawn.
type Blend uint8

const (
	BlendOver Blend = iota
	BlendReplace
)

// Dispose selects post-frame animation disposal.
type Dispose uint8

const (
	DisposeNone Dispose = iota
	DisposeBackground
)

// EncodedFrame carries exactly one VP8 or VP8L payload; ALPH requires VP8.
// Stills require zero Duration, BlendOver, DisposeNone and the full canvas.
// Animated rectangles have even nonnegative origins and exact millisecond timing.
type EncodedFrame struct {
	Duration        time.Duration
	Blend           Blend
	Dispose         Dispose
	Rect            image.Rectangle
	VP8, VP8L, ALPH []byte
	// UnknownChunks are ANMF subchunks, preserved in their relative order.
	// Canonical Mux writes them after the frame codec; stills require none.
	UnknownChunks []Chunk
}

// File owns all payloads returned by Demux. Mux does not mutate them. Concurrent
// callers must not modify a File or its slices while it is being read.
// UnknownChunks remain in their relative order; canonical Mux places them last.
// Extended records whether VP8X was present. Mux also adds VP8X when required.
type File struct {
	// Animated distinguishes even a one-frame animation from a still.
	// LoopCount zero means infinite playback; nonzero means total plays.
	Animated   bool
	LoopCount  uint16
	Background color.NRGBA
	Canvas     image.Point
	Extended   bool
	// Alpha preserves the VP8X declaration; for VP8L the header hint alone
	// cannot determine actual transparency without entropy decoding.
	Alpha                         bool
	Frames                        []EncodedFrame
	MetadataChunks, UnknownChunks []Chunk
}

// Metadata returns borrowed views of the first occurrence, including empty
// chunks. Modify MetadataChunks explicitly to replace, remove or canonicalize.
func (f *File) Metadata() Metadata {
	var m Metadata
	var seen [3]bool
	if f == nil {
		return m
	}
	for _, c := range f.MetadataChunks {
		switch c.FourCC {
		case "ICCP":
			if !seen[0] {
				m.ICC = c.Data
				seen[0] = true
			}
		case "EXIF":
			if !seen[1] {
				m.EXIF = c.Data
				seen[1] = true
			}
		case "XMP ":
			if !seen[2] {
				m.XMP = c.Data
				seen[2] = true
			}
		}
	}
	return m
}
func known(id string) bool {
	switch id {
	case "VP8X", "VP8 ", "VP8L", "ALPH", "ICCP", "EXIF", "XMP ", "ANIM", "ANMF":
		return true
	}
	return false
}
