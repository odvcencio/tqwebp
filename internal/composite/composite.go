// Package composite implements the pinned libwebp1.5 non-premultiplied channel
// blending and transparent-clear policy, without runtime foreign code.
package composite

import (
	"context"
	"errors"
	"image"
)

var ErrFrame = errors.New("webp: invalid compositor frame")

type Control struct {
	Rect             image.Rectangle
	Replace, Dispose bool
}
type Canvas struct {
	pixels      *image.NRGBA
	previous    Control
	previousKey bool
	frames      int
	bytes       int64
}

// New reserves one canvas. The owner releases Bytes after dropping Canvas.
func New(ctx context.Context, w, h int, reserve func(int64) error) (*Canvas, error) {
	if ctx == nil || reserve == nil || w <= 0 || h <= 0 {
		return nil, ErrFrame
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if uint64(w) > uint64(int(^uint(0)>>1))/4/uint64(h) {
		return nil, ErrFrame
	}
	size := int64(w) * int64(h) * 4
	if err := reserve(size); err != nil {
		return nil, err
	}
	return &Canvas{pixels: image.NewNRGBA(image.Rect(0, 0, w, h)), bytes: size}, nil
}
func (c *Canvas) Bytes() int64 {
	if c == nil {
		return 0
	}
	return c.bytes
}
func (c *Canvas) Close() {
	if c != nil {
		c.pixels = nil
		c.bytes = 0
	}
}

// Apply invalidates the previous borrowed canvas. Inputs must have independent
// pixel storage; the decoder adapter satisfies that contract. Hidden RGB is
// preserved on replacement/keyframe paths. On error the caller stops reading.
func (c *Canvas) Apply(ctx context.Context, ctl Control, src *image.NRGBA) (*image.NRGBA, bool, error) {
	if ctx == nil || c == nil || c.pixels == nil || src == nil {
		return nil, false, ErrFrame
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if ctl.Rect.Min.X < 0 || ctl.Rect.Min.Y < 0 || ctl.Rect.Max.X <= ctl.Rect.Min.X || ctl.Rect.Max.Y <= ctl.Rect.Min.Y || ctl.Rect.Max.X > c.pixels.Rect.Max.X || ctl.Rect.Max.Y > c.pixels.Rect.Max.Y {
		return nil, false, ErrFrame
	}
	w, h := ctl.Rect.Dx(), ctl.Rect.Dy()
	if w <= 0 || h <= 0 || ctl.Rect.Min.X < 0 || ctl.Rect.Min.Y < 0 || !ctl.Rect.In(c.pixels.Rect) || src.Rect.Dx() != w || src.Rect.Dy() != h || src.Stride < 0 {
		return nil, false, ErrFrame
	}
	rowBytes := int64(w) * 4
	if int64(len(src.Pix)) < rowBytes || h > 1 && int64(src.Stride) > (int64(len(src.Pix))-rowBytes)/int64(h-1) {
		return nil, false, ErrFrame
	}
	needed := int64(h-1)*int64(src.Stride) + rowBytes
	if int64(src.Stride) < 4*int64(w) || needed < 0 || needed > int64(len(src.Pix)) {
		return nil, false, ErrFrame
	}
	alpha := false
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		for x := 0; x < w; x++ {
			if src.Pix[y*src.Stride+4*x+3] != 255 {
				alpha = true
			}
		}
	}
	full := ctl.Rect == c.pixels.Rect
	key := c.frames == 0 || full && (!alpha || ctl.Replace) || c.previous.Dispose && (c.previous.Rect == c.pixels.Rect || c.previousKey)
	// Disposal is applied exactly before the next frame. No full-canvas copy is
	// needed because previous outputs are borrowed only until this call.
	if c.frames > 0 && c.previous.Dispose {
		r := c.previous.Rect
		for y := r.Min.Y; y < r.Max.Y; y++ {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			row := c.pixels.Pix[y*c.pixels.Stride+4*r.Min.X : y*c.pixels.Stride+4*r.Max.X]
			for len(row) > 0 {
				n := len(row)
				if n > 4096 {
					n = 4096
				}
				clear(row[:n])
				row = row[n:]
				if err := ctx.Err(); err != nil {
					return nil, false, err
				}
			}
		}
	}
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		dy := ctl.Rect.Min.Y + y
		for x := 0; x < w; x++ {
			if x&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, false, err
				}
			}
			dx := ctl.Rect.Min.X + x
			si := y*src.Stride + 4*x
			di := dy*c.pixels.Stride + 4*dx
			s, d := src.Pix[si:si+4], c.pixels.Pix[di:di+4]
			disposed := c.frames > 0 && c.previous.Dispose && dx >= c.previous.Rect.Min.X && dx < c.previous.Rect.Max.X && dy >= c.previous.Rect.Min.Y && dy < c.previous.Rect.Max.Y
			if ctl.Replace || key || disposed || s[3] == 255 {
				copy(d, s)
			} else {
				blend(d, s)
			}
		}
	}
	c.previous = ctl
	c.previousKey = key
	c.frames++
	return c.pixels, alpha, ctx.Err()
}

// Arithmetic adapted from libwebp1.5.0 src/demux/anim_decode.c. Copyright2015
// Google Inc.; retained BSD notice is ../../third_party/libwebp-COPYING.
func blend(dst, src []byte) {
	a := uint32(src[3])
	if a == 0 {
		return
	}
	factor := uint32(dst[3]) * (256 - a) >> 8
	alpha := a + factor
	scale := uint32(1<<24) / alpha
	for k := 0; k < 3; k++ {
		dst[k] = uint8((uint32(src[k])*a + uint32(dst[k])*factor) * scale >> 24)
	}
	dst[3] = uint8(alpha)
}
