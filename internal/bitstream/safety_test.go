package bitstream

import "testing"

func TestSkipBitsOverflow(t *testing.T) {
	for _, r := range []*Reader{New([]byte{0xff}), NewRBSP([]byte{0xff})} {
		if _, err := r.ReadBit(); err != nil {
			t.Fatal(err)
		}
		if err := r.SkipBits(int(^uint(0) >> 1)); err != ErrEOF {
			t.Fatalf("overflowing skip: %v, want ErrEOF", err)
		}
		if r.Pos() != 1 {
			t.Fatalf("failed skip changed position to %d", r.Pos())
		}
		if v, err := r.ReadBits(7); err != nil || v != 0x7f {
			t.Fatalf("read after overflowing skip = %#x, %v", v, err)
		}
	}
}

func TestReadBitsEveryAlignmentAndWidth(t *testing.T) {
	data := []byte{0xf0, 0x81, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	for offset := 0; offset < 8; offset++ {
		for width := 0; width <= 64; width++ {
			r := New(data)
			if err := r.SkipBits(offset); err != nil {
				t.Fatal(err)
			}
			var want uint64
			for pos := offset; pos < offset+width; pos++ {
				want = want<<1 | uint64(data[pos/8]>>uint(7-pos%8)&1)
			}
			got, err := r.ReadBits(width)
			if err != nil || got != want || r.Pos() != offset+width {
				t.Fatalf("offset %d width %d: got %#x, %v at %d; want %#x", offset, width, got, err, r.Pos(), want)
			}
		}
	}
}
