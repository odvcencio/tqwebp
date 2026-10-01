package boolenc

import (
	"bytes"
	"errors"
	"testing"
)

func TestBoundedEncoderIncludesFinishAndCapsCapacity(t *testing.T) {
	plain := New(0)
	for i := 0; i < 4000; i++ {
		plain.WriteBool(uint8(i%255+1), i%3 == 0)
	}
	want := plain.Finish()
	for _, limit := range []int{0, 1, 3, 4, len(want) - 1, len(want), len(want) + 1} {
		bounded := NewBounded(1<<30, limit)
		if cap(bounded.out) > limit {
			t.Fatal("initial hint escaped cap")
		}
		for i := 0; i < 4000; i++ {
			bounded.WriteBool(uint8(i%255+1), i%3 == 0)
		}
		got := bounded.Finish()
		if len(got) > limit || cap(got) > limit {
			t.Fatalf("limit%d: len=%d cap=%d", limit, len(got), cap(got))
		}
		if limit < len(want) {
			if !errors.Is(bounded.Err(), ErrOutputLimit) {
				t.Fatalf("limit%d: missing error", limit)
			}
		} else if bounded.Err() != nil || !bytes.Equal(got, want) {
			t.Fatalf("limit%d: successful bytes changed: %v", limit, bounded.Err())
		}
	}
}

func TestBoundedFinishOnly(t *testing.T) {
	for limit := 0; limit <= 5; limit++ {
		e := NewBounded(0, limit)
		out := e.Finish()
		if len(out) > limit || cap(out) > limit {
			t.Fatal("flush escaped bound")
		}
		if (limit < 4) != (e.Err() != nil) {
			t.Fatalf("limit%d flush error=%v", limit, e.Err())
		}
	}
}
