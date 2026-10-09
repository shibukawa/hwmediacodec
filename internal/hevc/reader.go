package hevc

import "github.com/shibukawa/hwmediacodec/internal/bitstream"

// reader wraps a bitstream.Reader with a sticky error so that the syntax
// functions read like the tables of the specification. After the first
// error every read returns zero; callers bound their loops by validated
// values and check err once.
type reader struct {
	r   *bitstream.Reader
	err error
}

func newReader(payload []byte) *reader {
	return &reader{r: bitstream.NewRBSP(payload)}
}

// fail records a syntax error unless one is already pending.
func (r *reader) fail(format string, args ...any) {
	if r.err == nil {
		r.err = syntaxErr(format, args...)
	}
}

func (r *reader) u(n int) uint32 {
	if r.err != nil {
		return 0
	}
	v, err := r.r.ReadBits(n)
	if err != nil {
		r.err = wrap(err)
		return 0
	}
	return uint32(v)
}

func (r *reader) u64(n int) uint64 {
	if r.err != nil {
		return 0
	}
	v, err := r.r.ReadBits(n)
	if err != nil {
		r.err = wrap(err)
		return 0
	}
	return v
}

func (r *reader) flag() bool { return r.u(1) == 1 }

func (r *reader) ue() uint32 {
	if r.err != nil {
		return 0
	}
	v, err := r.r.ReadUE()
	if err != nil {
		r.err = wrap(err)
		return 0
	}
	return v
}

// ueMax reads ue(v) and fails when the value exceeds max.
func (r *reader) ueMax(name string, max uint32) uint32 {
	v := r.ue()
	if r.err == nil && v > max {
		r.fail("%s %d out of range", name, v)
		return 0
	}
	return v
}

func (r *reader) se() int32 {
	if r.err != nil {
		return 0
	}
	v, err := r.r.ReadSE()
	if err != nil {
		r.err = wrap(err)
		return 0
	}
	return v
}

// seRange reads se(v) and fails when the value is outside [lo, hi].
func (r *reader) seRange(name string, lo, hi int32) int32 {
	v := r.se()
	if r.err == nil && (v < lo || v > hi) {
		r.fail("%s %d out of range", name, v)
		return 0
	}
	return v
}

func (r *reader) skip(n int) {
	if r.err != nil {
		return
	}
	if err := r.r.SkipBits(n); err != nil {
		r.err = wrap(err)
	}
}

func (r *reader) pos() int { return r.r.Pos() }

// ceilLog2 returns Ceil(Log2(n)) for n >= 1.
func ceilLog2(n int) int {
	bits := 0
	for (1 << uint(bits)) < n {
		bits++
	}
	return bits
}
