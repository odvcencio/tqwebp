package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	webp "m31labs.dev/tqwebp"
	"os"
	"path/filepath"
	"strings"
)

type failure struct {
	code     int
	category string
	err      error
}

func (e *failure) Error() string                    { return e.err.Error() }
func (e *failure) Unwrap() error                    { return e.err }
func fail(code int, category string, e error) error { return &failure{code, category, e} }
func classify(e error) (int, string) {
	var f *failure
	if errors.As(e, &f) {
		return f.code, f.category
	}
	switch {
	case errors.Is(e, context.Canceled):
		return 130, "interrupt"
	case errors.Is(e, context.DeadlineExceeded):
		return 6, "timeout"
	case errors.Is(e, webp.ErrInvalidMetadata):
		return 3, "metadata"
	case errors.Is(e, webp.ErrLimitExceeded):
		return 4, "limit"
	case errors.Is(e, webp.ErrUnsupportedFeature), errors.Is(e, webp.ErrAnimatedImage):
		return 3, "unsupported"
	case errors.Is(e, webp.ErrInvalidOptions), errors.Is(e, webp.ErrInvalidLimits):
		return 2, "usage"
	case errors.Is(e, webp.ErrInvalidDocument), errors.Is(e, webp.ErrInvalidImage), errors.Is(e, webp.ErrInvalidFormat), errors.Is(e, webp.ErrTooLarge):
		return 3, "invalid_input"
	default:
		return 5, "io"
	}
}
func readBounded(ctx context.Context, r io.Reader, limit int64) ([]byte, error) {
	var out []byte
	var buf [32768]byte
	empty := 0
	for {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		count := int64(len(buf))
		remaining := limit - int64(len(out))
		if remaining < 0 {
			return nil, webp.ErrInvalidLimits
		}
		// Add the overflow-detection byte only after proving the sum is small.
		if remaining < count {
			count = remaining + 1
		}
		n, e := r.Read(buf[:int(count)])
		if n < 0 || n > int(count) {
			return nil, fail(5, "io", webp.ErrInvalidReader)
		}
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		if int64(len(out))+int64(n) > limit {
			return nil, &webp.LimitError{Resource: "input_bytes", Limit: limit, Actual: int64(len(out)) + int64(n)}
		}
		if e != nil && e != io.EOF {
			return nil, fail(5, "io", e)
		}
		out = append(out, buf[:n]...)
		if e == io.EOF {
			return out, nil
		}
		if n == 0 {
			empty++
			if empty >= 100 {
				return nil, fail(5, "io", io.ErrNoProgress)
			}
		} else {
			empty = 0
		}
	}
}
func readPath(ctx context.Context, path string, stdin io.Reader, limit int64) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if strings.Contains(path, "://") {
		return nil, fail(2, "usage", errors.New("remote URLs are not supported"))
	}
	if path == "-" {
		return readBounded(ctx, stdin, limit)
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, fail(5, "io", e)
	}
	b, e := readBounded(ctx, f, limit)
	closeErr := f.Close()
	if e != nil {
		return nil, e
	}
	if closeErr != nil {
		return nil, fail(5, "io", closeErr)
	}
	return b, nil
}

type tempOutput interface {
	io.WriteCloser
	Name() string
}
type outputOps struct {
	create       func(string, string) (tempOutput, error)
	link, rename func(string, string) error
	remove       func(string) error
}

func systemOutputOps() outputOps {
	return outputOps{
		create: func(dir, pattern string) (tempOutput, error) { return os.CreateTemp(dir, pattern) },
		link:   os.Link, rename: os.Rename, remove: os.Remove,
	}
}

// atomicFile prepares a same-directory temporary and checks Close before commit.
// Link is the atomic no-clobber operation; it never follows/replaces destination.
// Force uses platform Rename semantics, without claiming cross-platform atomicity.
func atomicFile(ctx context.Context, path string, data []byte, force bool, ops outputOps) (result error) {
	if e := ctx.Err(); e != nil {
		return e
	}
	f, e := ops.create(filepath.Dir(path), ".tqwebp-*")
	if e != nil {
		return fail(5, "io", e)
	}
	name := f.Name()
	defer func() {
		if e := ops.remove(name); e != nil && !os.IsNotExist(e) {
			warning := fmt.Errorf("temporary cleanup failed: %w", e)
			if result == nil {
				result = fail(5, "io", fmt.Errorf("output may already be committed; %w", warning))
			} else {
				code, category := classify(result)
				result = fail(code, category, errors.Join(result, warning))
			}
		}
	}()
	n, e := f.Write(data)
	closeErr := f.Close()
	if e != nil {
		return fail(5, "io", e)
	}
	if n != len(data) {
		return fail(5, "io", io.ErrShortWrite)
	}
	if closeErr != nil {
		return fail(5, "io", closeErr)
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if force {
		e = ops.rename(name, path)
	} else {
		e = ops.link(name, path)
	}
	if e != nil {
		return fail(5, "io", fmt.Errorf("output commit: %w", e))
	}
	return nil
}
func outputBytes(ctx context.Context, path string, data []byte, o options, stdout io.Writer, terminal bool, ops outputOps) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if int64(len(data)) > o.outputBytes {
		return &webp.LimitError{Resource: "output_bytes", Limit: o.outputBytes, Actual: int64(len(data))}
	}
	if path == "-" {
		if terminal && !o.force {
			return fail(2, "usage", errors.New("binary terminal stdout requires --force"))
		}
		n, e := stdout.Write(data)
		if e != nil {
			return fail(5, "io", e)
		}
		if n != len(data) {
			return fail(5, "io", io.ErrShortWrite)
		}
		return nil
	}
	return atomicFile(ctx, path, data, o.force, ops)
}

type cappedBuffer struct {
	data []byte
	max  int64
	ctx  context.Context
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	if e := w.ctx.Err(); e != nil {
		return 0, e
	}
	if int64(len(p)) > w.max-int64(len(w.data)) {
		return 0, &webp.LimitError{Resource: "output_bytes", Limit: w.max, Actual: -1}
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

// contextReader deliberately does not implement ReadByte, so compressed metadata
// cannot bypass its checkpoints through a byte-reader fast path.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	if len(p) > 4096 {
		p = p[:4096]
	}
	n, e := r.r.Read(p)
	if ce := r.ctx.Err(); ce != nil {
		return n, ce
	}
	return n, e
}
