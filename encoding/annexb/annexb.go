// Package annexb splits H.264 and HEVC Annex-B byte streams into NAL units
// and groups them into access units (one coded picture each), which is the
// unit a hwmediacodec decoder consumes.
package annexb

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// H.264 NAL unit types used here.
const (
	H264NALSlice    = 1
	H264NALSliceIDR = 5
	H264NALSEI      = 6
	H264NALSPS      = 7
	H264NALPPS      = 8
	H264NALAUD      = 9
	H264NALEndSeq   = 10
	H264NALEndStr   = 11
	H264NALFiller   = 12
)

// HEVC NAL unit types used here.
const (
	HEVCNALVPS       = 32
	HEVCNALSPS       = 33
	HEVCNALPPS       = 34
	HEVCNALAUD       = 35
	HEVCNALEOS       = 36
	HEVCNALEOB       = 37
	HEVCNALFiller    = 38
	HEVCNALPrefixSEI = 39
	HEVCNALSuffixSEI = 40
)

var startCode = []byte{0, 0, 1}

// Split returns the NAL units contained in data, without start codes and
// without trailing zero bytes. The returned slices alias data.
func Split(data []byte) [][]byte {
	var out [][]byte
	i := bytes.Index(data, startCode)
	for i >= 0 {
		start := i + 3
		next := bytes.Index(data[start:], startCode)
		var nal []byte
		if next < 0 {
			nal = data[start:]
			i = -1
		} else {
			nal = data[start : start+next]
			i = start + next
		}
		nal = trimZeros(nal)
		if len(nal) > 0 {
			out = append(out, nal)
		}
	}
	return out
}

func trimZeros(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}

// NALUnitType returns the nal_unit_type of a NAL unit (without start code),
// or -1 when the header is incomplete.
func NALUnitType(c codec.Codec, nal []byte) int {
	switch c {
	case codec.H264:
		if len(nal) < 1 {
			return -1
		}
		return int(nal[0] & 0x1f)
	case codec.HEVC:
		if len(nal) < 2 {
			return -1
		}
		return int(nal[0]>>1) & 0x3f
	}
	return -1
}

// IsVCL reports whether the NAL unit type carries slice data.
func IsVCL(c codec.Codec, t int) bool {
	switch c {
	case codec.H264:
		return t >= 1 && t <= 5
	case codec.HEVC:
		return t >= 0 && t <= 31
	}
	return false
}

// IsKeyframe reports whether a VCL NAL unit type starts a random access
// point: IDR for H.264, IRAP (BLA/IDR/CRA) for HEVC.
func IsKeyframe(c codec.Codec, t int) bool {
	switch c {
	case codec.H264:
		return t == H264NALSliceIDR
	case codec.HEVC:
		return t >= 16 && t <= 23
	}
	return false
}

// IsParameterSet reports whether the NAL unit type is a VPS, SPS or PPS.
func IsParameterSet(c codec.Codec, t int) bool {
	switch c {
	case codec.H264:
		return t == H264NALSPS || t == H264NALPPS
	case codec.HEVC:
		return t == HEVCNALVPS || t == HEVCNALSPS || t == HEVCNALPPS
	}
	return false
}

// IsSEI reports whether the NAL unit type carries SEI messages.
func IsSEI(c codec.Codec, t int) bool {
	switch c {
	case codec.H264:
		return t == H264NALSEI
	case codec.HEVC:
		return t == HEVCNALPrefixSEI || t == HEVCNALSuffixSEI
	}
	return false
}

// FirstSliceInPicture reports whether a VCL NAL unit begins a new picture
// (first_mb_in_slice == 0 for H.264, first_slice_segment_in_pic_flag for
// HEVC). It assumes the NAL unit type was already checked with IsVCL.
func FirstSliceInPicture(c codec.Codec, nal []byte) bool {
	switch c {
	case codec.H264:
		// first_mb_in_slice is ue(v); the value 0 is the single bit '1'.
		return len(nal) >= 2 && nal[1]&0x80 != 0
	case codec.HEVC:
		return len(nal) >= 3 && nal[2]&0x80 != 0
	}
	return false
}

// beginsAccessUnit reports whether nal must start a new access unit when the
// current one already contains slice data.
func beginsAccessUnit(c codec.Codec, t int, nal []byte) bool {
	switch c {
	case codec.H264:
		switch t {
		case H264NALAUD, H264NALSPS, H264NALPPS, H264NALSEI, 14, 15, 16, 17, 18:
			return true
		}
		return IsVCL(c, t) && FirstSliceInPicture(c, nal)
	case codec.HEVC:
		switch t {
		case HEVCNALVPS, HEVCNALSPS, HEVCNALPPS, HEVCNALAUD, HEVCNALPrefixSEI:
			return true
		}
		if (t >= 41 && t <= 44) || (t >= 48 && t <= 55) {
			return true
		}
		return IsVCL(c, t) && FirstSliceInPicture(c, nal)
	}
	return false
}

// Reader groups the NAL units of an Annex-B stream into access units.
type Reader struct {
	src   io.Reader
	codec codec.Codec
	buf   []byte
	off   int
	eof   bool
	err   error

	au     []byte
	hasVCL bool
	done   bool

	base    int64 // stream offset of buf[0]
	nalOff  int64 // stream offset of the start code of the NAL unit nextNAL returned last
	auOff   int64 // stream offset of the first NAL unit of the access unit being assembled
	lastOff int64 // stream offset of the access unit Next returned last
}

const readChunk = 64 << 10

// NewReader returns a Reader for c over r.
func NewReader(r io.Reader, c codec.Codec) *Reader {
	return &Reader{src: r, codec: c}
}

// Next returns the next complete access unit as an Annex-B byte string with
// 4-byte start codes. It returns io.EOF when the stream is exhausted. The
// returned slice is owned by the caller.
func (r *Reader) Next() ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.done {
		return nil, io.EOF
	}
	for {
		nal, err := r.nextNAL()
		if err != nil {
			if err != io.EOF {
				r.err = err
				return nil, err
			}
			r.done = true
			if len(r.au) > 0 {
				au := r.au
				r.lastOff = r.auOff
				r.au = nil
				return au, nil
			}
			return nil, io.EOF
		}
		t := NALUnitType(r.codec, nal)
		if t < 0 {
			continue
		}
		if r.hasVCL && beginsAccessUnit(r.codec, t, nal) {
			au := r.au
			r.lastOff = r.auOff
			r.au = nil
			r.hasVCL = false
			r.appendNAL(nal)
			r.hasVCL = IsVCL(r.codec, t)
			return au, nil
		}
		r.appendNAL(nal)
		if IsVCL(r.codec, t) {
			r.hasVCL = true
		}
	}
}

// Offset returns the byte offset in the underlying stream at which the
// access unit most recently returned by Next begins: the position of the
// three-byte start code of its first NAL unit (one byte into a four-byte
// start code). Seeking an io.ReadSeeker there and starting a new Reader
// yields that access unit again, which is how a player builds a keyframe
// index for seeking.
func (r *Reader) Offset() int64 { return r.lastOff }

func (r *Reader) appendNAL(nal []byte) {
	if len(r.au) == 0 {
		r.auOff = r.nalOff
	}
	r.au = append(r.au, 0, 0, 0, 1)
	r.au = append(r.au, nal...)
}

// nextNAL returns the next NAL unit (aliasing the internal buffer, valid
// until the next call) or io.EOF.
func (r *Reader) nextNAL() ([]byte, error) {
	for {
		// Locate the start code that begins the next NAL unit.
		i := bytes.Index(r.buf[r.off:], startCode)
		if i < 0 {
			if r.eof {
				r.off = len(r.buf)
				return nil, io.EOF
			}
			// Keep the last two bytes in case a start code straddles reads.
			if len(r.buf)-r.off > 2 {
				r.off = len(r.buf) - 2
			}
			if err := r.fill(); err != nil {
				return nil, err
			}
			continue
		}
		start := r.off + i + 3
		r.nalOff = r.base + int64(r.off+i)
		j := bytes.Index(r.buf[start:], startCode)
		if j < 0 {
			if !r.eof {
				if err := r.fill(); err != nil {
					return nil, err
				}
				continue
			}
			nal := trimZeros(r.buf[start:])
			r.off = len(r.buf)
			if len(nal) == 0 {
				return nil, io.EOF
			}
			return nal, nil
		}
		nal := trimZeros(r.buf[start : start+j])
		r.off = start + j
		if len(nal) == 0 {
			continue
		}
		return nal, nil
	}
}

func (r *Reader) fill() error {
	if r.off > 0 && r.off >= len(r.buf)/2 {
		n := copy(r.buf, r.buf[r.off:])
		r.buf = r.buf[:n]
		r.base += int64(r.off)
		r.off = 0
	}
	if cap(r.buf)-len(r.buf) < readChunk {
		nb := make([]byte, len(r.buf), len(r.buf)+readChunk)
		copy(nb, r.buf)
		r.buf = nb
	}
	n, err := r.src.Read(r.buf[len(r.buf):cap(r.buf)])
	r.buf = r.buf[:len(r.buf)+n]
	if err == io.EOF {
		r.eof = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("annexb: read: %w", err)
	}
	if n == 0 {
		return errors.New("annexb: reader returned no data and no error")
	}
	return nil
}

// AppendUnit appends nal to dst behind a four-byte start code: the Annex-B
// form of one NAL unit.
func AppendUnit(dst, nal []byte) []byte {
	dst = append(dst, 0, 0, 0, 1)
	return append(dst, nal...)
}

// AppendLengthPrefixed appends nal to dst behind its four-byte big-endian
// length: the form MP4 and HEIF samples store NAL units in (AVCC, HVCC).
func AppendLengthPrefixed(dst, nal []byte) []byte {
	n := len(nal)
	dst = append(dst, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	return append(dst, nal...)
}

// FromLengthPrefixed appends to dst the NAL units of a length-prefixed
// sample in Annex-B form. lengthSize is the size of the length fields in
// bytes (1 to 4; lengthSizeMinusOne + 1 of the avcC or hvcC record).
// Trailing bytes too short to hold a length are ignored.
func FromLengthPrefixed(dst, sample []byte, lengthSize int) ([]byte, error) {
	if lengthSize < 1 || lengthSize > 4 {
		return dst, fmt.Errorf("annexb: NAL unit length size %d", lengthSize)
	}
	for len(sample) >= lengthSize {
		n := 0
		for i := 0; i < lengthSize; i++ {
			n = n<<8 | int(sample[i])
		}
		sample = sample[lengthSize:]
		if n <= 0 || n > len(sample) {
			return dst, fmt.Errorf("annexb: NAL unit length %d out of range", n)
		}
		dst = AppendUnit(dst, sample[:n])
		sample = sample[n:]
	}
	return dst, nil
}
