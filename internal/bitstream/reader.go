// Package bitstream reads bit fields and Exp-Golomb codes from H.264/HEVC
// raw byte sequence payloads.
package bitstream

import (
	"errors"
	"math/bits"
)

// ErrEOF is returned when a read runs past the end of the payload.
var ErrEOF = errors.New("bitstream: unexpected end of data")

// Reader reads bits most-significant first.
//
// A reader created with NewRBSP removes emulation prevention bytes lazily,
// so parsing the header of a large slice NAL unit does not copy the whole
// slice.
type Reader struct {
	data []byte // unescaped bytes decoded so far
	pos  int    // bit position in data
	raw  []byte // escaped input not yet decoded (nil when no unescaping is done)
	zero int    // run of zero bytes at the end of data
}

// NewRBSP returns a reader over a NAL unit payload that removes emulation
// prevention bytes (00 00 03 -> 00 00) as it reads.
func NewRBSP(payload []byte) *Reader {
	n := len(payload)
	if n > 256 {
		n = 256
	}
	return &Reader{raw: payload, data: make([]byte, 0, n)}
}

// New returns a reader over data without emulation prevention removal.
func New(data []byte) *Reader { return &Reader{data: data} }

// fill decodes input until at least nbits bits past the current position
// are available, or the input is exhausted. It reports whether the bits are
// available.
func (r *Reader) fill(nbits int) bool {
	need := r.pos + nbits
	for len(r.data)*8 < need && len(r.raw) > 0 {
		b := r.raw[0]
		r.raw = r.raw[1:]
		if r.zero >= 2 && b == 3 {
			r.zero = 0
			continue
		}
		r.data = append(r.data, b)
		if b == 0 {
			r.zero++
		} else {
			r.zero = 0
		}
	}
	return len(r.data)*8 >= need
}

// fillAll decodes the whole input.
func (r *Reader) fillAll() {
	for len(r.raw) > 0 {
		r.fill(len(r.data)*8 + 8*len(r.raw))
	}
}

// Bytes returns the unescaped payload decoded so far. After MoreRBSPData or
// when the reader was created with New it is the whole payload.
func (r *Reader) Bytes() []byte {
	r.fillAll()
	return r.data
}

// ReadBit reads one bit.
func (r *Reader) ReadBit() (uint8, error) {
	if !r.fill(1) {
		return 0, ErrEOF
	}
	b := (r.data[r.pos>>3] >> (7 - uint(r.pos&7))) & 1
	r.pos++
	return b, nil
}

// ReadFlag reads one bit as a boolean.
func (r *Reader) ReadFlag() (bool, error) {
	b, err := r.ReadBit()
	return b == 1, err
}

// ReadBits reads n (0..64) bits as an unsigned integer.
func (r *Reader) ReadBits(n int) (uint64, error) {
	if n < 0 || n > 64 {
		return 0, errors.New("bitstream: bit count out of range")
	}
	if !r.fill(n) {
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
	if n < 0 || !r.fill(n) {
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

// Pos returns the current bit position in the unescaped payload.
func (r *Reader) Pos() int { return r.pos }

// MoreRBSPData implements more_rbsp_data(): it reports whether syntax
// elements remain before the rbsp_trailing_bits.
func (r *Reader) MoreRBSPData() bool {
	r.fillAll()
	last := len(r.data) - 1
	for last >= 0 && r.data[last] == 0 {
		last--
	}
	if last < 0 {
		return false
	}
	// The stop bit is the lowest set bit of the last non-zero byte.
	stop := last*8 + 7 - bits.TrailingZeros8(r.data[last])
	return r.pos < stop
}
