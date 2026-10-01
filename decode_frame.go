package webp

import (
	"bytes"
	"context"
	"image"
	vp8 "m31labs.dev/tqwebp/internal/vp8decode"
	vp8l "m31labs.dev/tqwebp/internal/vp8ldecode"
)

// decodePixels is the shared dimension-checked still/frame codec adapter.
// The caller admits compressed storage separately. Every codec/output backing
// allocation is conservatively charged through reserve before allocation.
func decodePixels(ctx context.Context, width, height int, lossy, lossless, alpha []byte, reserve func(int64) error) (*image.NRGBA, error) {
	w, ht := int64(width), int64(height)
	pixels := w * ht
	var err error
	if len(lossless) > 0 {
		out, e := vp8l.Decode(ctx, lossless, int(w), int(ht), false, reserve)
		if e != nil {
			return nil, losslessError(ctx, "VP8L", e)
		}
		return out, nil
	}
	// 384 bytes/MB for padded YUV, four filter bytes/MB, six predictor
	// bytes/column, <= compressed payload bytes for partition copies, output.
	mw, mh := (w+15)/16, (ht+15)/16
	if err = reserve(388*mw*mh + 6*mw + int64(len(lossy)) + 4*pixels); err != nil {
		return nil, err
	}
	d := vp8.NewDecoder()
	d.Init(bytes.NewReader(lossy), len(lossy))
	fh, err := d.DecodeFrameHeader()
	if err != nil {
		return nil, decodeError(-1, "VP8 ", err)
	}
	if !fh.KeyFrame || !fh.ShowFrame || int64(fh.Width) != w || int64(fh.Height) != ht {
		return nil, decodeError(-1, "VP8 ", ErrInvalidFormat)
	}
	yuv, err := d.DecodeFrame(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, decodeError(-1, "VP8 ", err)
	}
	out := image.NewNRGBA(image.Rect(0, 0, int(w), int(ht)))
	for y := 0; y < int(ht); y++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < int(w); x++ {
			yy := int(yuv.Y[y*yuv.YStride+x])
			uv := (y/2)*yuv.CStride + x/2
			rr, gg, bb := limitedRGB(yy, int(yuv.Cb[uv]), int(yuv.Cr[uv]))
			i := y*out.Stride + 4*x
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = rr, gg, bb, 255
		}
	}
	if len(alpha) > 0 {
		if alpha[0]&3 == 0 {
			err = decodeRawAlpha(ctx, out, alpha)
		} else {
			var residual *image.NRGBA
			residual, err = vp8l.Decode(ctx, alpha[1:], int(w), int(ht), true, reserve)
			if err != nil {
				return nil, losslessError(ctx, "ALPH", err)
			}
			err = unfilterAlpha(ctx, out, alpha[0], residual.Pix[1:], 4)
		}
		if err != nil {
			return nil, err
		}
	}

	return out, ctx.Err()
}
