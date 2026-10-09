package bitstream

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestWriterRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	type op struct {
		kind int
		n    int
		v    int64
	}
	var ops []op
	w := NewWriter()
	for i := 0; i < 2000; i++ {
		switch rng.Intn(4) {
		case 0:
			n := 1 + rng.Intn(32)
			v := rng.Int63() & (1<<uint(n) - 1)
			w.WriteBits(uint64(v), n)
			ops = append(ops, op{0, n, v})
		case 1:
			v := rng.Int63n(100000)
			w.WriteUE(uint32(v))
			ops = append(ops, op{1, 0, v})
		case 2:
			v := rng.Int63n(100000) - 50000
			w.WriteSE(int32(v))
			ops = append(ops, op{2, 0, v})
		case 3:
			b := rng.Intn(2) == 1
			w.WriteFlag(b)
			ops = append(ops, op{3, 0, int64(b2i(b))})
		}
	}
	w.Trailing()
	if !w.ByteAligned() {
		t.Fatal("not byte aligned after trailing bits")
	}
	r := New(w.Bytes())
	for i, o := range ops {
		switch o.kind {
		case 0:
			v, err := r.ReadBits(o.n)
			if err != nil || int64(v) != o.v {
				t.Fatalf("op %d: ReadBits(%d) = %d, %v; want %d", i, o.n, v, err, o.v)
			}
		case 1:
			v, err := r.ReadUE()
			if err != nil || int64(v) != o.v {
				t.Fatalf("op %d: ReadUE = %d, %v; want %d", i, v, err, o.v)
			}
		case 2:
			v, err := r.ReadSE()
			if err != nil || int64(v) != o.v {
				t.Fatalf("op %d: ReadSE = %d, %v; want %d", i, v, err, o.v)
			}
		case 3:
			v, err := r.ReadFlag()
			if err != nil || int64(b2i(v)) != o.v {
				t.Fatalf("op %d: ReadFlag = %v, %v; want %d", i, v, err, o.v)
			}
		}
	}
	if r.MoreRBSPData() {
		t.Fatal("reader reports more data after the last element")
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestEscapeRoundTrip(t *testing.T) {
	cases := [][]byte{
		{},
		{0, 0, 0},
		{0, 0, 1},
		{0, 0, 3, 0, 0, 2},
		{0, 0, 0, 0, 0, 0, 0},
		{0xff, 0, 0, 4, 0, 0, 3},
	}
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 50; i++ {
		b := make([]byte, rng.Intn(200))
		for j := range b {
			b[j] = byte(rng.Intn(5))
		}
		cases = append(cases, b)
	}
	for _, c := range cases {
		esc := Escape(c)
		// No 00 00 00, 00 00 01 or 00 00 02 may survive (00 00 03 is the
		// emulation prevention byte itself).
		for i := 2; i < len(esc); i++ {
			if esc[i-2] == 0 && esc[i-1] == 0 && esc[i] <= 2 {
				t.Fatalf("escaped %x still contains a start-code prefix at %d", esc, i)
			}
		}
		if got := NewRBSP(esc).Bytes(); !bytes.Equal(got, c) {
			t.Fatalf("round trip of %x gave %x (escaped %x)", c, got, esc)
		}
	}
}

func TestLen(t *testing.T) {
	w := NewWriter()
	if w.Len() != 0 {
		t.Fatal("empty length")
	}
	w.WriteBits(5, 3)
	if w.Len() != 3 {
		t.Fatalf("Len = %d", w.Len())
	}
	w.WriteBits(0, 13)
	if w.Len() != 16 || !w.ByteAligned() {
		t.Fatalf("Len = %d aligned=%v", w.Len(), w.ByteAligned())
	}
}
