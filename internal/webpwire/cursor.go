package webpwire

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"time"
)

type chunkHeader struct {
	id             string
	off, size, end int64
}
type pendingFrame struct {
	chunk     chunkHeader
	frame     Frame
	prefix    [10]byte
	prefixLen int
}

// Cursor owns only parser state, retained metadata and file-level unknowns.
// Returned frames belong entirely to its caller. It never spools a RIFF or ANMF.
type Cursor struct {
	ctx                                                        context.Context
	r                                                          io.Reader
	policy                                                     Policy
	work                                                       *Working
	header                                                     Header
	off, end                                                   int64
	chunks, retained, metadataBytes                            int64
	metadata, unknown                                          []Chunk
	owned                                                      int64
	seenMetadata                                               byte
	reconstruction, animSeen, imageSeen, sawLossless, sawAlpha bool
	pending                                                    *pendingFrame
	stats                                                      Summary
	final                                                      *Summary
	terminal                                                   error
	closed                                                     bool
	scratch                                                    [4096]byte
}

func Open(ctx context.Context, r io.Reader, policy Policy) (*Cursor, error) {
	c := &Cursor{ctx: ctx, r: r, policy: policy, work: policy.Working}
	if c.work == nil {
		c.work = &Working{}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := Check("input_bytes", 12, policy.Limits.MaxInputBytes); err != nil {
		return nil, err
	}
	var h [12]byte
	if err := c.read(h[:]); err != nil {
		return nil, c.ioFault(0, "", -1, err)
	}
	n := int64(binary.LittleEndian.Uint32(h[4:8]))
	if string(h[:4]) != "RIFF" || string(h[8:]) != "WEBP" || n < 4 || n > MaxRIFFSize || n&1 != 0 {
		return nil, c.bad(0, "RIFF", -1)
	}
	c.end = n + 8
	if err := Check("input_bytes", c.end, policy.Limits.MaxInputBytes); err != nil {
		return nil, err
	}
	if err := c.prepare(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Cursor) Header() Header          { return c.header }
func (c *Cursor) MetadataChunks() []Chunk { return c.metadata }
func (c *Cursor) UnknownChunks() []Chunk  { return c.unknown }
func (c *Cursor) Summary() *Summary {
	if c.final == nil {
		return nil
	}
	s := *c.final
	return &s
}

// Close never consumes input and never closes the caller's reader. Returned
// frames are not cursor-owned and remain the caller's release responsibility.
func (c *Cursor) Close() {
	if c.closed {
		return
	}
	c.closed = true
	c.metadata = nil
	c.unknown = nil
	c.pending = nil
	c.r = nil
	c.work.Release(c.owned)
	c.owned = 0
}

func (c *Cursor) NextFrame() (Frame, error) {
	if c.closed {
		return Frame{}, io.ErrClosedPipe
	}
	if c.terminal != nil {
		return Frame{}, c.terminal
	}
	if c.final != nil {
		return Frame{}, io.EOF
	}
	if err := c.ctx.Err(); err != nil {
		c.terminal = err
		return Frame{}, err
	}
	if c.pending == nil {
		if err := c.prepare(); err != nil {
			if err != io.EOF {
				c.terminal = err
			}
			return Frame{}, err
		}
	}
	p := c.pending
	c.pending = nil
	f := p.frame
	var err error
	if c.header.Animated {
		err = c.readAnimated(p.chunk, &f)
	} else {
		err = c.readStill(p, &f)
	}
	if err != nil {
		c.work.Release(f.OwnedBytes)
		c.terminal = err
		return Frame{}, err
	}
	return f, nil
}

func (c *Cursor) bad(off int64, id string, frame int) error {
	return &Fault{off, id, frame, Invalid, malformed}
}
func (c *Cursor) ioFault(off int64, id string, frame int, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	k := IO
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		k = Invalid
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			err = errors.Join(io.ErrUnexpectedEOF, err)
		}
	}
	return &Fault{off, id, frame, k, err}
}
func locate(err error, off int64, id string, frame int) error {
	if f, ok := err.(*Fault); ok {
		copy := *f
		copy.Offset = off
		copy.Chunk = id
		copy.Frame = frame
		return &copy
	}
	return err
}

func (c *Cursor) read(p []byte) error {
	empty := 0
	for len(p) > 0 {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		n := len(p)
		if n > len(c.scratch) {
			n = len(c.scratch)
		}
		got, err := c.r.Read(p[:n])
		if got < 0 || got > n {
			return ErrInvalidReader
		}
		c.off += int64(got)
		p = p[got:]
		if cancel := c.ctx.Err(); cancel != nil {
			return cancel
		}
		if err != nil && err != io.EOF {
			return err
		}
		if len(p) == 0 {
			return nil
		}
		if err != nil {
			return io.ErrUnexpectedEOF
		}
		if got == 0 {
			empty++
			if empty >= 100 {
				return io.ErrNoProgress
			}
		} else {
			empty = 0
		}
	}
	return c.ctx.Err()
}

func (c *Cursor) nextChunk(end int64, frame int, top bool) (chunkHeader, error) {
	var ch chunkHeader
	if c.off == end {
		return ch, io.EOF
	}
	ch.off = c.off
	if end-c.off < 8 {
		return ch, c.ioFault(c.off, "", frame, io.ErrUnexpectedEOF)
	}
	if err := Check("chunk_count", c.chunks+1, c.policy.Limits.MaxChunks); err != nil {
		return ch, err
	}
	c.chunks++
	var h [8]byte
	if err := c.read(h[:]); err != nil {
		return ch, c.ioFault(ch.off, "", frame, err)
	}
	ch.id = string(h[:4])
	ch.size = int64(binary.LittleEndian.Uint32(h[4:]))
	if ch.size+(ch.size&1) > end-c.off {
		return ch, c.ioFault(ch.off, ch.id, frame, io.ErrUnexpectedEOF)
	}
	ch.end = c.off + ch.size
	if top {
		if err := Check("retained_bytes", c.retained+ch.size, c.policy.Limits.MaxRetainedBytes); err != nil {
			return ch, err
		}
		c.retained += ch.size
	}
	return ch, nil
}

func (c *Cursor) padding(ch chunkHeader, frame int) error {
	if c.off != ch.end {
		return c.bad(c.off, ch.id, frame)
	}
	if ch.size&1 != 0 {
		var b [1]byte
		if err := c.read(b[:]); err != nil {
			return c.ioFault(ch.end, ch.id, frame, err)
		}
		if b[0] != 0 {
			return c.bad(ch.end, ch.id, frame)
		}
	}
	return nil
}
func (c *Cursor) skip(ch chunkHeader, frame int) error {
	for c.off < ch.end {
		n := ch.end - c.off
		if n > int64(len(c.scratch)) {
			n = int64(len(c.scratch))
		}
		off := c.off
		if err := c.read(c.scratch[:int(n)]); err != nil {
			return c.ioFault(off, ch.id, frame, err)
		}
	}
	return c.padding(ch, frame)
}

func (c *Cursor) payload(ch chunkHeader, prefix []byte, frame int) ([]byte, error) {
	if ch.size > int64(int(^uint(0)>>1)) {
		return nil, &ResourceError{"working_bytes", int64(int(^uint(0) >> 1)), ch.size}
	}
	if err := c.work.Reserve(ch.size); err != nil {
		return nil, err
	}
	p := make([]byte, int(ch.size))
	copy(p, prefix)
	off := c.off
	if err := c.read(p[len(prefix):]); err != nil {
		c.work.Release(ch.size)
		return nil, c.ioFault(off, ch.id, frame, err)
	}
	if err := c.padding(ch, frame); err != nil {
		c.work.Release(ch.size)
		return nil, err
	}
	return p, nil
}

// A conservative 64-byte slot covers a Chunk backing element and its FourCC
// string on supported 32/64-bit targets. Reserve new backing before releasing
// old backing so append growth's overlap is admitted, not hidden.
func (c *Cursor) appendChunk(dst *[]Chunk, entry Chunk, owned *int64) error {
	if len(*dst) == cap(*dst) {
		old := cap(*dst)
		platform := int64(int(^uint(0) >> 1))
		if int64(old) > platform/128 {
			return &ResourceError{"working_bytes", platform, -1}
		}
		next := old * 2
		if next == 0 {
			next = 1
		}
		cost := int64(next) * 64
		if err := c.work.Reserve(cost); err != nil {
			return err
		}
		p := make([]Chunk, len(*dst), next)
		copy(p, *dst)
		*dst = p
		c.work.Release(int64(old) * 64)
		*owned += cost - int64(old)*64
	}
	*dst = append(*dst, entry)
	return nil
}

func metadataFlag(id string) byte {
	switch id {
	case "ICCP":
		return 0x20
	case "EXIF":
		return 8
	case "XMP ":
		return 4
	}
	return 0
}
func (c *Cursor) takeMetadata(ch chunkHeader, frame int) error {
	flag := metadataFlag(ch.id)
	if !c.header.Extended || ch.id == "ICCP" && c.reconstruction {
		return c.bad(ch.off, ch.id, frame)
	}
	if err := Check("metadata_bytes", c.metadataBytes+ch.size, c.policy.Limits.MaxMetadataBytes); err != nil {
		return err
	}
	c.metadataBytes += ch.size
	c.stats.Observed |= flag
	seen := c.seenMetadata&flag != 0
	c.seenMetadata |= flag
	if !c.policy.Preserve && seen {
		return c.skip(ch, frame)
	}
	p, err := c.payload(ch, nil, frame)
	if err != nil {
		return err
	}
	if err = c.appendChunk(&c.metadata, Chunk{ch.id, p}, &c.owned); err != nil {
		c.work.Release(ch.size)
		return err
	}
	c.owned += ch.size
	return nil
}
func (c *Cursor) takeUnknown(ch chunkHeader, frame int, f *Frame) error {
	if !c.policy.Preserve {
		return c.skip(ch, frame)
	}
	p, err := c.payload(ch, nil, frame)
	if err != nil {
		return err
	}
	dst, owned := &c.unknown, &c.owned
	if f != nil {
		dst, owned = &f.UnknownChunks, &f.OwnedBytes
	}
	if err = c.appendChunk(dst, Chunk{ch.id, p}, owned); err != nil {
		c.work.Release(ch.size)
		return err
	}
	*owned += ch.size
	return nil
}

func (c *Cursor) canvas() error {
	w, h := int64(c.header.Canvas.X), int64(c.header.Canvas.Y)
	if w <= 0 || h <= 0 || w*h > 1<<32-1 {
		return c.bad(12, "VP8X", -1)
	}
	return Check("canvas_pixels", w*h, c.policy.Limits.MaxCanvasPixels)
}
func (c *Cursor) admit(f Frame) error {
	l := c.policy.Limits
	pixels := int64(f.Rect.Dx()) * int64(f.Rect.Dy())
	if err := Check("frames", c.stats.Frames+1, l.MaxFrames); err != nil {
		return err
	}
	if err := Check("frame_pixels", pixels, l.MaxFramePixels); err != nil {
		return err
	}
	if pixels > int64(^uint64(0)>>1)-c.stats.DecodedPixels {
		return &ResourceError{"decoded_pixels", l.MaxDecodedPixels, -1}
	}
	if err := Check("decoded_pixels", c.stats.DecodedPixels+pixels, l.MaxDecodedPixels); err != nil {
		return err
	}
	ms := int64(f.Duration / time.Millisecond)
	if ms > int64(^uint64(0)>>1)-c.stats.DurationMilliseconds {
		return &ResourceError{"duration", int64(l.MaxDuration), -1}
	}
	next := c.stats.DurationMilliseconds + ms
	if l.MaxDuration > 0 && next > int64(l.MaxDuration/time.Millisecond) {
		actual := int64(-1)
		if next <= int64(^uint64(0)>>1)/int64(time.Millisecond) {
			actual = next * int64(time.Millisecond)
		}
		return &ResourceError{"duration", int64(l.MaxDuration), actual}
	}
	c.stats.Frames++
	c.stats.DecodedPixels += pixels
	c.stats.DurationMilliseconds = next
	return nil
}

func (c *Cursor) prepare() error {
	for {
		ch, err := c.nextChunk(c.end, -1, true)
		if err == io.EOF {
			return c.finish()
		}
		if err != nil {
			return err
		}
		switch ch.id {
		case "VP8X":
			if ch.off != 12 || c.header.Extended || ch.size < 10 {
				return c.bad(ch.off, ch.id, -1)
			}
			var p [10]byte
			if err = c.read(p[:]); err != nil {
				return c.ioFault(ch.off+8, ch.id, -1, err)
			}
			c.header.Extended = true
			c.header.Declared = p[0] & 0x3e
			c.header.Animated = p[0]&2 != 0
			c.header.AlphaDeclared = p[0]&0x10 != 0
			c.header.Canvas = image.Pt(int(U24(p[4:7]))+1, int(U24(p[7:10]))+1)
			if err = c.canvas(); err != nil {
				return err
			}
			if err = c.skip(ch, -1); err != nil {
				return err
			}
		case "ANIM":
			if !c.header.Extended {
				return c.bad(ch.off, ch.id, -1)
			}
			if !c.header.Animated {
				if err = c.skip(ch, -1); err != nil {
					return err
				}
				continue
			}
			if c.animSeen || c.stats.Frames != 0 || ch.size < 6 {
				return c.bad(ch.off, ch.id, -1)
			}
			var p [6]byte
			if err = c.read(p[:]); err != nil {
				return c.ioFault(ch.off+8, ch.id, -1, err)
			}
			c.header.Background = color.NRGBA{R: p[2], G: p[1], B: p[0], A: p[3]}
			c.header.LoopCount = binary.LittleEndian.Uint16(p[4:])
			c.animSeen = true
			c.reconstruction = true
			c.stats.Observed |= 2
			if err = c.skip(ch, -1); err != nil {
				return err
			}
		case "ANMF":
			index := int(c.stats.Frames)
			if !c.header.Animated || !c.animSeen || ch.size < 16 {
				return c.bad(ch.off, ch.id, index)
			}
			var p [16]byte
			if err = c.read(p[:]); err != nil {
				return c.ioFault(ch.off+8, ch.id, index, err)
			}
			x, y := int64(U24(p[0:3]))*2, int64(U24(p[3:6]))*2
			w, h := int64(U24(p[6:9]))+1, int64(U24(p[9:12]))+1
			if x+w > int64(c.header.Canvas.X) || y+h > int64(c.header.Canvas.Y) {
				return c.bad(ch.off, ch.id, index)
			}
			f := Frame{Index: index, Rect: image.Rect(int(x), int(y), int(x+w), int(y+h)), Duration: time.Duration(U24(p[12:15])) * time.Millisecond, Blend: p[15] >> 1 & 1, Dispose: p[15] & 1, Offset: ch.off, VP8Offset: -1, VP8LOffset: -1, ALPHOffset: -1}
			if err = c.admit(f); err != nil {
				return err
			}
			c.pending = &pendingFrame{chunk: ch, frame: f}
			return nil
		case "ALPH", "VP8 ", "VP8L":
			if c.header.Animated || c.imageSeen || !c.header.Extended && ch.off != 12 {
				return c.bad(ch.off, ch.id, 0)
			}
			if ch.id == "ALPH" && (!c.header.Extended || !c.header.AlphaDeclared) {
				return c.bad(ch.off, ch.id, 0)
			}
			p := &pendingFrame{chunk: ch, frame: Frame{Index: 0, Offset: ch.off, VP8Offset: -1, VP8LOffset: -1, ALPHOffset: -1}}
			if ch.id != "ALPH" {
				p.prefixLen = 10
				if ch.id == "VP8L" {
					p.prefixLen = 5
				}
				if ch.size < int64(p.prefixLen) {
					return c.bad(ch.off, ch.id, 0)
				}
				if err = c.read(p.prefix[:p.prefixLen]); err != nil {
					return c.ioFault(ch.off+8, ch.id, 0, err)
				}
				dim, _, e := Probe(ch.id, p.prefix[:p.prefixLen], ch.size)
				if e != nil {
					return locate(e, ch.off, ch.id, 0)
				}
				if c.header.Extended && dim != c.header.Canvas {
					return c.bad(ch.off, ch.id, 0)
				}
				c.header.Canvas = dim
				if err = c.canvas(); err != nil {
					return err
				}
			}
			p.frame.Rect = image.Rectangle{Max: c.header.Canvas}
			c.reconstruction = true
			if err = c.admit(p.frame); err != nil {
				return err
			}
			c.pending = p
			return nil
		case "ICCP", "EXIF", "XMP ":
			if err = c.takeMetadata(ch, -1); err != nil {
				return err
			}
		default:
			if !c.header.Extended && !c.imageSeen {
				return c.bad(ch.off, ch.id, -1)
			}
			if err = c.takeUnknown(ch, -1, nil); err != nil {
				return err
			}
		}
	}
}

func (c *Cursor) codec(ch chunkHeader, prefix []byte, f *Frame) error {
	if f.VP8 != nil || f.VP8L != nil || ch.id == "VP8L" && f.ALPH != nil {
		return c.bad(ch.off, ch.id, f.Index)
	}
	var h [10]byte
	if prefix == nil {
		n := 10
		if ch.id == "VP8L" {
			n = 5
		}
		if ch.size < int64(n) {
			return c.bad(ch.off, ch.id, f.Index)
		}
		if err := c.read(h[:n]); err != nil {
			return c.ioFault(ch.off+8, ch.id, f.Index, err)
		}
		prefix = h[:n]
	}
	dim, _, err := Probe(ch.id, prefix, ch.size)
	if err != nil {
		return locate(err, ch.off, ch.id, f.Index)
	}
	if dim != f.Rect.Size() {
		return c.bad(ch.off, ch.id, f.Index)
	}
	p, err := c.payload(ch, prefix, f.Index)
	if err != nil {
		return err
	}
	f.OwnedBytes += ch.size
	if ch.id == "VP8L" {
		f.VP8L = p
		f.VP8LOffset = ch.off
		c.sawLossless = true
	} else {
		f.VP8 = p
		f.VP8Offset = ch.off
	}
	return nil
}
func (c *Cursor) alpha(ch chunkHeader, f *Frame) error {
	if f.ALPH != nil || f.VP8 != nil || f.VP8L != nil || !c.header.AlphaDeclared || ch.size < 1 {
		return c.bad(ch.off, ch.id, f.Index)
	}
	var h [1]byte
	if err := c.read(h[:]); err != nil {
		return c.ioFault(ch.off+8, ch.id, f.Index, err)
	}
	if h[0]&3 > 1 || h[0]>>4&3 > 1 {
		return &Fault{ch.off, ch.id, f.Index, Unsupported, unsupported}
	}
	if h[0]&3 == 0 && ch.size-1 != int64(f.Rect.Dx())*int64(f.Rect.Dy()) || h[0]&3 == 1 && ch.size < 2 {
		return c.bad(ch.off, ch.id, f.Index)
	}
	p, err := c.payload(ch, h[:], f.Index)
	if err != nil {
		return err
	}
	f.ALPH = p
	f.ALPHOffset = ch.off
	f.OwnedBytes += ch.size
	c.sawAlpha = true
	c.stats.Observed |= 0x10
	return nil
}
func (c *Cursor) readAnimated(outer chunkHeader, f *Frame) error {
	for c.off < outer.end {
		ch, err := c.nextChunk(outer.end, f.Index, false)
		if err != nil {
			return err
		}
		switch ch.id {
		case "ALPH":
			err = c.alpha(ch, f)
		case "VP8 ", "VP8L":
			err = c.codec(ch, nil, f)
		default:
			if Known(ch.id) {
				return c.bad(ch.off, ch.id, f.Index)
			}
			err = c.takeUnknown(ch, f.Index, f)
		}
		if err != nil {
			return err
		}
	}
	if f.VP8 == nil && f.VP8L == nil {
		return c.bad(outer.off, "ANMF", f.Index)
	}
	return c.padding(outer, f.Index)
}
func (c *Cursor) readStill(p *pendingFrame, f *Frame) error {
	ch := p.chunk
	prefix := p.prefix[:p.prefixLen]
	for {
		var err error
		switch ch.id {
		case "ALPH":
			err = c.alpha(ch, f)
		case "VP8 ", "VP8L":
			if len(prefix) == 0 {
				prefix = nil
			}
			err = c.codec(ch, prefix, f)
			if err == nil {
				c.imageSeen = true
			}
			return err
		case "ICCP", "EXIF", "XMP ":
			err = c.takeMetadata(ch, 0)
		case "ANIM":
			err = c.skip(ch, 0) // Ignored when animation is unset.
		default:
			if Known(ch.id) {
				return c.bad(ch.off, ch.id, 0)
			}
			err = c.takeUnknown(ch, 0, nil)
		}
		if err != nil {
			return err
		}
		ch, err = c.nextChunk(c.end, 0, true)
		if err == io.EOF {
			return c.bad(c.off, "", 0)
		}
		if err != nil {
			return err
		}
		prefix = nil
	}
}
func (c *Cursor) finish() error {
	if c.stats.Frames == 0 || !c.header.Animated && !c.imageSeen || c.header.Animated && !c.animSeen {
		return c.bad(c.off, "", -1)
	}
	if c.header.Extended && c.header.Declared&0x2c != c.stats.Observed&0x2c {
		return c.bad(12, "VP8X", -1)
	}
	if c.header.Extended && !c.sawLossless && c.header.AlphaDeclared != c.sawAlpha {
		return c.bad(12, "VP8X", -1)
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	s := c.stats
	c.final = &s
	return io.EOF
}
