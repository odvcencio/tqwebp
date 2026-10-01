package webp

import "fmt"

// LimitError reports a caller's resource refusal. Actual is the computed
// amount, or -1 when it cannot be safely computed without completing refused
// work. Errors.Is matches ErrLimitExceeded and, for output_bytes only,
// ErrOutputTooLarge. Error text is diagnostic; use these fields or errors.Is.
type LimitError struct {
	Resource string
	Limit    int64
	Actual   int64
}

func (e *LimitError) Error() string {
	if e.Actual < 0 {
		return fmt.Sprintf("tqwebp: %s exceeds limit %d", e.Resource, e.Limit)
	}
	return fmt.Sprintf("tqwebp: %s %d exceeds limit %d", e.Resource, e.Actual, e.Limit)
}

func (e *LimitError) Is(target error) bool {
	return target == ErrLimitExceeded || e.Resource == "output_bytes" && target == ErrOutputTooLarge
}

func limitActual(a, b uint64) int64 {
	const max = uint64(1<<63 - 1)
	if b != 0 && a > max/b {
		return -1
	}
	return int64(a * b)
}
