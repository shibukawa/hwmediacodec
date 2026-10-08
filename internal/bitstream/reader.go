// Package bitstream reads bit fields and Exp-Golomb codes from H.264/HEVC
// raw byte sequence payloads.
package bitstream

import "errors"

// ErrEOF is returned when a read runs past the end of the payload.
var ErrEOF = errors.New("bitstream: unexpected end of data")

// Reader reads bits most-significant first.
type Reader struct {
	data []byte
	pos  int // bit position
}

// NewRBSP returns a reader over a NAL unit payload after removing emulation
// prevention bytes (00 00 03 -> 00 00).
func NewRBSP(payload []byte) *Reader {
	out := make([]byte, 0, len(payload))
	zeros := 0
	for _, b := range payload {
		if zeros >= 2 && b == 3 {
			zeros = 0
			continue
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return &Reader{data: out}
}

// New returns a reader over data without emulation prevention removal.
func New(data []byte) *Reader { return &Reader{data: data} }

// ReadBit reads one bit.
func (r *Reader) ReadBit() (uint8, error) {
	if r.pos >= len(r.data)*8 {
		return 0, ErrEOF
	}
	b := (r.data[r.pos>>3] >> (7 - uint(r.pos&7))) & 1
	r.pos++
	return b, nil
}

// ReadBits reads n (0..64) bits as an unsigned integer.
func (r *Reader) ReadBits(n int) (uint64, error) {
	if n < 0 || n > 64 {
		return 0, errors.New("bitstream: bit count out of range")
	}
	if r.pos+n > len(r.data)*8 {
		return 0, ErrEOF
	}
	var v uint64
	for i := 0; i < n; i++ {
		b := (r.data[r.pos>>3] >> (7 - uint(r.pos&7))) & 1
		v = v<<1 | uint64(b)
		r.pos++
	}
	return v, nil
}

// SkipBits advances the position by n bits.
func (r *Reader) SkipBits(n int) error {
	if n < 0 || r.pos+n > len(r.data)*8 {
		return ErrEOF
	}
	r.pos += n
	return nil
}

// ReadUE reads an unsigned Exp-Golomb code ue(v).
func (r *Reader) ReadUE() (uint32, error) {
	zeros := 0
	for {
		b, err := r.ReadBit()
		if err != nil {
			return 0, err
		}
		if b == 1 {
			break
		}
		zeros++
		if zeros > 31 {
			return 0, errors.New("bitstream: exp-golomb code too long")
		}
	}
	if zeros == 0 {
		return 0, nil
	}
	v, err := r.ReadBits(zeros)
	if err != nil {
		return 0, err
	}
	return uint32((uint64(1) << uint(zeros)) - 1 + v), nil
}

// ReadSE reads a signed Exp-Golomb code se(v).
func (r *Reader) ReadSE() (int32, error) {
	k, err := r.ReadUE()
	if err != nil {
		return 0, err
	}
	if k&1 == 1 {
		return int32((k + 1) / 2), nil
	}
	return -int32(k / 2), nil
}

// Pos returns the current bit position.
func (r *Reader) Pos() int { return r.pos }
