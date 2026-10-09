package bitstream

// Writer assembles a raw byte sequence payload bit by bit, most-significant
// bit first.
type Writer struct {
	data []byte
	nbit int // bits used in the last byte (0 when data ends on a boundary)
}

// NewWriter returns an empty writer.
func NewWriter() *Writer { return &Writer{} }

// WriteBits appends the low n bits of v (n <= 64).
func (w *Writer) WriteBits(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.WriteBit(uint8(v>>uint(i)) & 1)
	}
}

// WriteBit appends one bit.
func (w *Writer) WriteBit(b uint8) {
	if w.nbit == 0 {
		w.data = append(w.data, 0)
	}
	if b != 0 {
		w.data[len(w.data)-1] |= 0x80 >> uint(w.nbit)
	}
	w.nbit = (w.nbit + 1) & 7
}

// WriteFlag appends one bit from a boolean.
func (w *Writer) WriteFlag(b bool) {
	if b {
		w.WriteBit(1)
	} else {
		w.WriteBit(0)
	}
}

// WriteUE appends an unsigned Exp-Golomb code ue(v).
func (w *Writer) WriteUE(v uint32) {
	x := uint64(v) + 1
	n := 0
	for t := x; t > 1; t >>= 1 {
		n++
	}
	w.WriteBits(0, n)
	w.WriteBits(x, n+1)
}

// WriteSE appends a signed Exp-Golomb code se(v).
func (w *Writer) WriteSE(v int32) {
	if v > 0 {
		w.WriteUE(uint32(2*v - 1))
	} else {
		w.WriteUE(uint32(-2 * v))
	}
}

// Trailing appends rbsp_trailing_bits(): a one bit and zero bits up to the
// next byte boundary.
func (w *Writer) Trailing() {
	w.WriteBit(1)
	for w.nbit != 0 {
		w.WriteBit(0)
	}
}

// Len returns the number of bits written.
func (w *Writer) Len() int {
	if w.nbit == 0 {
		return len(w.data) * 8
	}
	return (len(w.data)-1)*8 + w.nbit
}

// Bytes returns the payload; a partial last byte is zero padded.
func (w *Writer) Bytes() []byte { return w.data }

// ByteAligned reports whether the next bit starts a byte.
func (w *Writer) ByteAligned() bool { return w.nbit == 0 }

// Escape returns the RBSP with emulation prevention bytes inserted: every
// 00 00 followed by a byte of 03 or less gets a 03 in between.
func Escape(rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp)+len(rbsp)/64+4)
	zeros := 0
	for _, b := range rbsp {
		if zeros >= 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}
