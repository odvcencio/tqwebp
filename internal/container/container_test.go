package container

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type testWriter struct {
	calls, failAt int
	err           error
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return len(p) - 1, w.err
	}
	return len(p), nil
}
func TestWriteFailures(t *testing.T) {
	sentinel := errors.New("writer failed")
	for _, at := range []int{1, 2, 3} {
		for _, cause := range []error{nil, sentinel} {
			w := &testWriter{failAt: at, err: cause}
			err := WriteSimpleLossy(w, []byte{1, 2, 3})
			want := cause
			if want == nil {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) {
				t.Fatalf("write %d: %v", at, err)
			}
			if w.calls != at {
				t.Fatal("continued after failure")
			}
		}
	}
}
func TestContainerLengths(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 256, 257} {
		var b bytes.Buffer
		if err := WriteSimpleLossy(&b, make([]byte, n)); err != nil {
			t.Fatal(err)
		}
		if b.Len() != Size(n) || int(binary.LittleEndian.Uint32(b.Bytes()[4:8])) != b.Len()-8 {
			t.Fatalf("length %d", n)
		}
		if int(binary.LittleEndian.Uint32(b.Bytes()[16:20])) != n {
			t.Fatal("chunk length")
		}
	}
}

func TestMaxPayloadIncludesPadding(t *testing.T) {
	const maxRIFF = uint64(1<<32 - 1)
	fits := func(n uint64) bool { return 12+n+n&1 <= maxRIFF }
	if !fits(MaxPayload) || fits(MaxPayload+1) {
		t.Fatal("incorrect RIFF payload/padding limit")
	}
}
