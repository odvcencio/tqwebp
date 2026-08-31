package container

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestExtendedLayoutIsExact pins every field of the extended container
// against the WebP container specification, byte by byte.
func TestExtendedLayoutIsExact(t *testing.T) {
	alphaChunk := []byte{0x00, 0x11, 0x22} // header byte plus a 3-pixel plane
	payload := []byte{0xaa, 0xbb}

	var buf bytes.Buffer
	if err := WriteExtendedLossyAlpha(&buf, alphaChunk, payload, 3, 1); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := buf.Bytes()

	want := []byte{
		'R', 'I', 'F', 'F',
		0, 0, 0, 0, // the RIFF size, checked below
		'W', 'E', 'B', 'P',
		'V', 'P', '8', 'X',
		10, 0, 0, 0,
		0x10,    // the alpha flag, and nothing else
		0, 0, 0, // reserved
		2, 0, 0, // width minus one
		0, 0, 0, // height minus one
		'A', 'L', 'P', 'H',
		3, 0, 0, 0,
		0x00, 0x11, 0x22,
		0, // the ALPH pad byte, because the chunk has an odd size
		'V', 'P', '8', ' ',
		2, 0, 0, 0,
		0xaa, 0xbb,
	}
	binary.LittleEndian.PutUint32(want[4:8], uint32(len(want)-8))

	if !bytes.Equal(got, want) {
		t.Fatalf("file bytes are\n%x\nwant\n%x", got, want)
	}
	if got, want := len(got), ExtendedSize(len(alphaChunk), len(payload)); got != want {
		t.Errorf("file is %d bytes, ExtendedSize says %d", got, want)
	}
}

// TestExtendedRiffSizeCountsEveryLaterByte proves the RIFF size field
// counts every byte after itself, pad bytes included, at each of the
// four odd and even length combinations.
func TestExtendedRiffSizeCountsEveryLaterByte(t *testing.T) {
	for _, alphaLen := range []int{4, 5} {
		for _, payloadLen := range []int{6, 7} {
			var buf bytes.Buffer
			err := WriteExtendedLossyAlpha(&buf,
				make([]byte, alphaLen), make([]byte, payloadLen), 2, 2)
			if err != nil {
				t.Fatalf("alpha %d payload %d: %v", alphaLen, payloadLen, err)
			}
			data := buf.Bytes()
			size := binary.LittleEndian.Uint32(data[4:8])
			if int(size) != len(data)-8 {
				t.Errorf("alpha %d payload %d: RIFF size is %d, want %d",
					alphaLen, payloadLen, size, len(data)-8)
			}
			if len(data) != ExtendedSize(alphaLen, payloadLen) {
				t.Errorf("alpha %d payload %d: file is %d bytes, ExtendedSize says %d",
					alphaLen, payloadLen, len(data), ExtendedSize(alphaLen, payloadLen))
			}
			if len(data)%2 != 0 {
				t.Errorf("alpha %d payload %d: file length %d is odd", alphaLen, payloadLen, len(data))
			}
		}
	}
}

// TestCanvasFieldsCarryTheSizeMinusOne pins the 24-bit minus-one canvas
// fields, including their largest legal value.
func TestCanvasFieldsCarryTheSizeMinusOne(t *testing.T) {
	cases := []struct{ w, h int }{
		{1, 1},
		{16, 16},
		{641, 487},
		{MaxCanvas, MaxCanvas},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		if err := WriteExtendedLossyAlpha(&buf, []byte{0}, []byte{0}, c.w, c.h); err != nil {
			t.Fatalf("%dx%d: %v", c.w, c.h, err)
		}
		data := buf.Bytes()
		gotW := int(data[24]) | int(data[25])<<8 | int(data[26])<<16
		gotH := int(data[27]) | int(data[28])<<8 | int(data[29])<<16
		if gotW != c.w-1 || gotH != c.h-1 {
			t.Errorf("%dx%d wrote canvas fields %d and %d, want %d and %d",
				c.w, c.h, gotW, gotH, c.w-1, c.h-1)
		}
	}
}

// TestExtendedRefusesAnImpossibleCanvas proves the writer refuses a
// canvas the 24-bit fields cannot state.
func TestExtendedRefusesAnImpossibleCanvas(t *testing.T) {
	cases := []struct{ w, h int }{
		{0, 4},
		{4, 0},
		{-1, 4},
		{MaxCanvas + 1, 4},
		{4, MaxCanvas + 1},
	}
	for _, c := range cases {
		if err := WriteExtendedLossyAlpha(&bytes.Buffer{}, []byte{0}, []byte{0}, c.w, c.h); err == nil {
			t.Errorf("a canvas of %dx%d was accepted", c.w, c.h)
		}
	}
}

// TestSimpleLayoutIsUnchanged pins the simple container, so the opaque
// path keeps the bytes every earlier release wrote.
func TestSimpleLayoutIsUnchanged(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSimpleLossy(&buf, []byte{1, 2, 3}); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := []byte{
		'R', 'I', 'F', 'F',
		16, 0, 0, 0,
		'W', 'E', 'B', 'P',
		'V', 'P', '8', ' ',
		3, 0, 0, 0,
		1, 2, 3, 0,
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("file bytes are\n%x\nwant\n%x", buf.Bytes(), want)
	}
	if got := Size(3); got != len(want) {
		t.Errorf("Size says %d, the file is %d bytes", got, len(want))
	}
}

// TestExtendedReportsAShortWrite proves a failing writer surfaces its
// error instead of leaving a truncated file behind in silence.
func TestExtendedReportsAShortWrite(t *testing.T) {
	total := ExtendedSize(3, 2)
	for limit := 0; limit < total; limit++ {
		w := &failAfter{limit: limit}
		err := WriteExtendedLossyAlpha(w, []byte{0, 1, 2}, []byte{3, 4}, 3, 1)
		if err == nil {
			t.Errorf("a writer that fails after %d of %d bytes reported success", limit, total)
		}
	}
}

type failAfter struct {
	limit, written int
}

func (w *failAfter) Write(p []byte) (int, error) {
	if w.written+len(p) > w.limit {
		return 0, errFull
	}
	w.written += len(p)
	return len(p), nil
}

var errFull = errWriter("container_test: the writer is full")

type errWriter string

func (e errWriter) Error() string { return string(e) }
