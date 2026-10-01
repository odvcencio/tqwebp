// Package webpwire implements the shared, frame-incremental WebP container
// state machine. It has no dependency on either public package or pixel codecs.
package webpwire

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"time"
)

// ErrInvalidReader identifies a Reader that violates the byte-count contract.
var ErrInvalidReader = errors.New("webp: invalid Reader count")

type Kind uint8

const (
	Invalid Kind = iota
	Unsupported
	IO
)

// Fault is translated at a public API boundary, preserving Err through wrapping.
type Fault struct {
	Offset int64
	Chunk  string
	Frame  int
	Kind   Kind
	Err    error
}

func (e *Fault) Error() string {
	return fmt.Sprintf("webp: offset %d chunk %q frame %d: %v", e.Offset, e.Chunk, e.Frame, e.Err)
}
func (e *Fault) Unwrap() error { return e.Err }

type ResourceError struct {
	Resource      string
	Limit, Actual int64
}

func (e *ResourceError) Error() string {
	return fmt.Sprintf("webp: %s %d exceeds %d", e.Resource, e.Actual, e.Limit)
}

// Working is shared by the cursor, decoder and compositor. A zero Limit is
// unlimited. Reserve precedes allocation; Release follows dropping ownership.
// It measures live backing storage, not runtime overhead, stacks or process RSS.
type Working struct{ Limit, Used int64 }

func (w *Working) Reserve(n int64) error {
	if n < 0 || n > int64(^uint64(0)>>1)-w.Used {
		return &ResourceError{"working_bytes", w.Limit, -1}
	}
	next := w.Used + n
	if w.Limit > 0 && next > w.Limit {
		return &ResourceError{"working_bytes", w.Limit, next}
	}
	w.Used = next
	return nil
}

func (w *Working) Release(n int64) {
	if n < 0 || n > w.Used {
		panic("webpwire: invalid working reservation release")
	}
	w.Used -= n
}

// Limits are already normalized by the facade. Zero disables an internal
// limit; public zero/default policy remains the responsibility of that facade.
type Limits struct {
	MaxInputBytes, MaxChunks, MaxFrames, MaxMetadataBytes, MaxRetainedBytes int64
	MaxCanvasPixels, MaxFramePixels, MaxDecodedPixels                       int64
	MaxDuration                                                             time.Duration
}

type Policy struct {
	// Preserve retains all metadata and unknown chunks. Otherwise only the
	// first metadata occurrence is kept, and unknown payloads are drained.
	Preserve bool
	Limits   Limits
	Working  *Working
}

type Chunk struct {
	FourCC string
	Data   []byte
}

type Header struct {
	Canvas                            image.Point
	Extended, Animated, AlphaDeclared bool
	LoopCount                         uint16
	Background                        color.NRGBA
	Declared                          byte
}

// Frame owns its byte slices. The caller releases OwnedBytes after discarding
// all slices; Cursor does not retain another reference to the returned frame.
// Blend 0/1 means over/replace; Dispose 0/1 means none/background.
type Frame struct {
	Index                                     int
	Rect                                      image.Rectangle
	Duration                                  time.Duration
	Blend, Dispose                            uint8
	VP8, VP8L, ALPH                           []byte
	UnknownChunks                             []Chunk
	Offset, VP8Offset, VP8LOffset, ALPHOffset int64
	OwnedBytes                                int64
}

// Summary exists only after successful RIFF finalization. DurationMilliseconds
// avoids narrowing a container-only aggregate into time.Duration. Root reading
// always supplies MaxDuration, so its accepted sum can safely be converted.
type Summary struct {
	Frames, DecodedPixels, DurationMilliseconds int64
	Observed                                    byte
}

const MaxRIFFSize int64 = 1<<32 - 10

func Check(resource string, actual, limit int64) error {
	if actual < 0 || limit > 0 && actual > limit {
		return &ResourceError{resource, limit, actual}
	}
	return nil
}

func Known(id string) bool {
	switch id {
	case "VP8X", "VP8 ", "VP8L", "ALPH", "ICCP", "EXIF", "XMP ", "ANIM", "ANMF":
		return true
	}
	return false
}
