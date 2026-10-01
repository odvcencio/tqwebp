package encoder

import (
	"context"
	"errors"
	"testing"

	"m31labs.dev/tqwebp/internal/token"
	"m31labs.dev/tqwebp/internal/yuv"
)

// Deterministic internal checkpoints avoid relying on scheduler timing or
// wall-clock performance to prove a long phase actually observes cancellation.
type checkpointContext struct {
	context.Context
	stop   func() bool
	checks int
}

func (c *checkpointContext) Err() error {
	c.checks++
	if c.stop() {
		return context.Canceled
	}
	return nil
}

func TestCancellationAcrossMethod6Reconsideration(t *testing.T) {
	e := newEncoder(yuv.Convert(probOptFixture()), Config{Quality: 75, Method: 6})
	c := &checkpointContext{Context: context.Background(), stop: func() bool { return e.rateProbs != &token.DefaultProbs }}
	e.ctx = c
	e.runFrame()
	if e.err != context.Canceled || e.rateProbs == &token.DefaultProbs || e.ctx != c {
		t.Fatalf("second-pass checkpoint/context lost: err=%v checks=%d", e.err, c.checks)
	}
}

func TestCancellationBeforeCoefficientTrellis(t *testing.T) {
	e := newEncoder(yuv.Convert(probOptFixture()), Config{Quality: 75, Method: 6})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.ctx = ctx
	levels := [16]int16{17, 3, -2}
	e.refineBlockLevels(0, &levels, func(_ *[16]int16) int64 { cancel(); return 0 })
	if e.err != context.Canceled || e.rd.TrellisBlocksSearched != 0 {
		t.Fatalf("trellis ran after cancellation: err=%v searches=%d", e.err, e.rd.TrellisBlocksSearched)
	}
}

func TestCancellationDuringProbabilityAndSerialization(t *testing.T) {
	for _, phase := range []string{"histogram", "first partition", "token partition"} {
		t.Run(phase, func(t *testing.T) {
			e := newEncoder(yuv.Convert(probOptFixture()), Config{Quality: 75, Method: 6})
			e.runFrame()
			if e.err != nil {
				t.Fatal(e.err)
			}
			var c *checkpointContext
			c = &checkpointContext{Context: context.Background(), stop: func() bool { return c.checks >= 3 }}
			e.ctx = c
			switch phase {
			case "histogram":
				if e.optimizeTokenProbs() != nil {
					t.Fatal("cancelled histogram returned probabilities")
				}
			case "first partition":
				if data, err := e.frameBytes(); err != context.Canceled || data != nil {
					t.Fatalf("got %d bytes/%v", len(data), err)
				}
			case "token partition":
				if data, err := e.writeTokensBounded(e.frozenTokenProbs, -1); err != context.Canceled || data != nil {
					t.Fatalf("got %d bytes/%v", len(data), err)
				}
			}
			if e.err != context.Canceled {
				t.Fatalf("%s missed cancellation: %v", phase, e.err)
			}
		})
	}
}

func TestPartitionBudgetRetainsNoUnboundedHint(t *testing.T) {
	e := partitionEncoder(1<<30, 3)
	for i := 0; i < 10000; i++ {
		e.WriteFlag(i%2 == 0)
	}
	data := e.Finish()
	if len(data) > 3 || cap(data) > 3 || e.Err() == nil {
		t.Fatalf("budget escaped: len%d cap%d err%v", len(data), cap(data), e.Err())
	}
}

func TestOutputCapBridge(t *testing.T) {
	e := newEncoder(yuv.Convert(probOptFixture()), Config{Quality: 75, Method: 6})
	e.runFrame()
	e.maxOutputBytes = 31
	data, err := e.frameBytes()
	var detail *OutputLimitError
	if data != nil || !errors.As(err, &detail) || detail.Limit != 31 || detail.Actual != -1 {
		t.Fatalf("got %d bytes, %#v", len(data), err)
	}
}
