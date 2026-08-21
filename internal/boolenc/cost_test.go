package boolenc

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/rand/v2"
	"testing"
)

func TestEntropyCostTablePin(t *testing.T) {
	buf := make([]byte, 2*len(entropyCostQ8))
	for i, cost := range entropyCostQ8 {
		binary.BigEndian.PutUint16(buf[2*i:], cost)
	}
	got := sha256.Sum256(buf)
	const want = "2ae1fe74dd1a3802190611b5b5bb4d6252be31043723ddb5ba6d6e263c34d3ba"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("entropy cost table SHA-256 = %x, want %s", got, want)
	}
}

func TestDecisionCostQ8EveryProbabilityAndBit(t *testing.T) {
	anchors := map[uint8][2]uint16{
		1:   {1792, 4},
		64:  {512, 110},
		128: {256, 256},
		145: {211, 312},
		254: {4, 1792},
		255: {3, 1792},
	}
	for prob := 1; prob <= 255; prob++ {
		p := uint8(prob)
		falseCost := DecisionCostQ8(p, false)
		trueCost := DecisionCostQ8(p, true)
		if falseCost != entropyCostQ8[p] {
			t.Fatalf("probability %d false cost = %d, want table[%d] = %d", p, falseCost, p, entropyCostQ8[p])
		}
		if trueCost != entropyCostQ8[255-p] {
			t.Fatalf("probability %d true cost = %d, want table[%d] = %d", p, trueCost, 255-p, entropyCostQ8[255-p])
		}
		var writer CostWriter
		writer.WriteBool(p, false)
		writer.WriteBool(p, true)
		if got, want := writer.CostQ8(), uint64(falseCost)+uint64(trueCost); got != want {
			t.Fatalf("probability %d writer cost = %d, want %d", p, got, want)
		}
		writer.Reset()
		if writer.CostQ8() != 0 {
			t.Fatalf("probability %d reset left cost %d", p, writer.CostQ8())
		}
	}
	for prob, want := range anchors {
		if got := [2]uint16{DecisionCostQ8(prob, false), DecisionCostQ8(prob, true)}; got != want {
			t.Errorf("probability %d costs = %v, want %v", prob, got, want)
		}
	}
}

func TestDecisionCostRejectsZeroProbability(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("DecisionCostQ8 accepted probability zero")
		}
	}()
	DecisionCostQ8(0, false)
}

func TestLiteralAndByteCosts(t *testing.T) {
	for bits := 0; bits <= 64; bits++ {
		if got, want := LiteralCostQ8(bits), uint64(bits)*256; got != want {
			t.Errorf("LiteralCostQ8(%d) = %d, want %d", bits, got, want)
		}
	}
	for bytes := uint64(0); bytes <= 4096; bytes++ {
		cost := BytesCostQ8(bytes)
		if got := CostBytesCeil(cost); got != bytes {
			t.Fatalf("byte count %d round-tripped through cost as %d", bytes, got)
		}
		if bytes > 0 {
			if got := CostBytesCeil(cost - 1); got != bytes {
				t.Fatalf("cost one Q8 unit below %d bytes rounded to %d", bytes, got)
			}
		}
		if got := CostBytesCeil(cost + 1); got != bytes+1 {
			t.Fatalf("cost one Q8 unit above %d bytes rounded to %d", bytes, got)
		}
	}
	max := ^uint64(0)
	want := max/ByteCostQ8 + 1
	if got := CostBytesCeil(max); got != want {
		t.Fatalf("CostBytesCeil(max uint64) = %d, want %d", got, want)
	}
}

func TestLiteralCostRejectsNegativeWidth(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("LiteralCostQ8 accepted a negative width")
		}
	}()
	LiteralCostQ8(-1)
}

func TestLiteralCostRejectsOverflow(t *testing.T) {
	bits := int(^uint(0) >> 1)
	if uint64(bits) <= ^uint64(0)/CostScaleQ8 {
		t.Skip("platform int cannot express an overflowing literal width")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("LiteralCostQ8 accepted an overflowing width")
		}
	}()
	LiteralCostQ8(bits)
}

func TestBytesCostRejectsOverflow(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("BytesCostQ8 accepted an overflowing byte count")
		}
	}()
	BytesCostQ8(^uint64(0)/ByteCostQ8 + 1)
}

func TestProjectedLenMatchesFinishAtEveryState(t *testing.T) {
	rng := rand.New(rand.NewPCG(23, 29))
	enc := New(0)
	for i := 0; i < 20000; i++ {
		clone := *enc
		clone.out = append([]byte(nil), enc.out...)
		if got, want := len(clone.Finish()), enc.ProjectedLen(); got != want {
			t.Fatalf("decision %d: projected length %d, Finish length %d", i, want, got)
		}
		enc.WriteBool(uint8(1+rng.IntN(255)), rng.IntN(2) != 0)
	}
	projected := enc.ProjectedLen()
	if got := len(enc.Finish()); got != projected {
		t.Fatalf("final projected length %d, Finish length %d", projected, got)
	}
	if got := enc.ProjectedLen(); got != projected {
		t.Fatalf("finished projected length %d, want %d", got, projected)
	}
}
