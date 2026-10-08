package bitstream

import "testing"

func TestEmulationPreventionRemoval(t *testing.T) {
	r := NewRBSP([]byte{0x00, 0x00, 0x03, 0x01, 0x00, 0x00, 0x03, 0x00, 0x03})
	want := []byte{0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x03}
	if string(r.data) != string(want) {
		t.Fatalf("got %x want %x", r.data, want)
	}
}

func TestReadUE(t *testing.T) {
	// Codes: 1 -> 0, 010 -> 1, 011 -> 2, 00100 -> 3, 00111 -> 6, 0001000 -> 7
	// Bits: 1 010 011 00100 00111 0001000 = 1010 0110 0100 0011 1000 1000 (+pad)
	r := New([]byte{0xA6, 0x43, 0x88})
	want := []uint32{0, 1, 2, 3, 6, 7}
	for i, w := range want {
		got, err := r.ReadUE()
		if err != nil {
			t.Fatalf("code %d: %v", i, err)
		}
		if got != w {
			t.Fatalf("code %d: got %d want %d", i, got, w)
		}
	}
}

func TestReadSE(t *testing.T) {
	// ue codes 1,2,3,4 -> se +1,-1,+2,-2 : bits 010 011 00100 00101
	r := New([]byte{0x4C, 0x85})
	want := []int32{1, -1, 2, -2}
	for i, w := range want {
		got, err := r.ReadSE()
		if err != nil {
			t.Fatalf("code %d: %v", i, err)
		}
		if got != w {
			t.Fatalf("code %d: got %d want %d", i, got, w)
		}
	}
}

func TestReadBitsEOF(t *testing.T) {
	r := New([]byte{0xFF})
	if _, err := r.ReadBits(9); err != ErrEOF {
		t.Fatalf("expected ErrEOF, got %v", err)
	}
	if v, err := r.ReadBits(8); err != nil || v != 0xFF {
		t.Fatalf("got %d, %v", v, err)
	}
}
