// Package hevc parses and writes HEVC (ITU-T H.265 / ISO/IEC 23008-2)
// parameter sets and slice segment headers and keeps the decoded picture
// buffer bookkeeping of the decoding process: picture order counts,
// reference picture sets and reference picture lists.
//
// Two levels of parsing are offered. ParseSPSHead, ParsePPSHead and
// ParseSliceHead read only the leading fields that picture order counts
// depend on; the reorder layer uses them for every backend, so they stay
// tolerant of streams whose later syntax this package does not model.
// ParseSPS, ParsePPS and ParseSliceHeader read the complete structures a
// slice-level decoder API (VA-API) needs.
package hevc

import (
	"errors"
	"fmt"
)

// NAL unit types.
const (
	NALTrailN    = 0
	NALTrailR    = 1
	NALRADLN     = 6
	NALRADLR     = 7
	NALRASLN     = 8
	NALRASLR     = 9
	NALBLAWLP    = 16
	NALBLAWRADL  = 17
	NALBLANLP    = 18
	NALIDRWRADL  = 19
	NALIDRNLP    = 20
	NALCRA       = 21
	NALVPS       = 32
	NALSPS       = 33
	NALPPS       = 34
	NALAUD       = 35
	NALEOS       = 36
	NALEOB       = 37
	NALPrefixSEI = 39
)

// ErrNotSlice is returned when a NAL unit is not a coded slice segment.
var ErrNotSlice = errors.New("hevc: not a slice segment NAL unit")

// ErrNotFirstSegment is returned by ParseSliceHead for slice segments that
// do not begin a picture; only the first segment carries the fields it
// reads.
var ErrNotFirstSegment = errors.New("hevc: not the first slice segment of a picture")

// Errors returned when a slice refers to parameter sets that have not
// arrived.
var (
	ErrMissingSPS = errors.New("hevc: slice refers to an SPS that was not received")
	ErrMissingPPS = errors.New("hevc: slice refers to a PPS that was not received")
)

// Slice types (slice_type).
const (
	SliceB = 0
	SliceP = 1
	SliceI = 2
)

func syntaxErr(format string, args ...any) error {
	return fmt.Errorf("hevc: "+format, args...)
}

// Type returns nal_unit_type of a NAL unit (two-byte header), or -1.
func Type(nal []byte) int {
	if len(nal) < 2 {
		return -1
	}
	return int(nal[0]>>1) & 0x3f
}

// TemporalID returns TemporalId of a NAL unit, or -1.
func TemporalID(nal []byte) int {
	if len(nal) < 2 {
		return -1
	}
	return int(nal[1]&7) - 1
}

// IsVCL reports whether the type is a coded slice segment.
func IsVCL(t int) bool { return t >= 0 && t <= 31 }

// IsSlice reports whether the type is a coded slice segment this
// specification version defines (the reserved VCL types are excluded).
func IsSlice(t int) bool { return (t >= 0 && t <= NALRASLR) || (t >= NALBLAWLP && t <= NALCRA) }

// IsIRAP reports whether the type is an intra random access point (BLA,
// IDR, CRA or a reserved IRAP type).
func IsIRAP(t int) bool { return t >= NALBLAWLP && t <= 23 }

// IsIDR reports whether the type is an IDR picture.
func IsIDR(t int) bool { return t == NALIDRWRADL || t == NALIDRNLP }

// IsBLA reports whether the type is a BLA picture.
func IsBLA(t int) bool { return t >= NALBLAWLP && t <= NALBLANLP }

// IsRASL reports whether the type is a RASL (skippable leading) picture.
func IsRASL(t int) bool { return t == NALRASLN || t == NALRASLR }

// IsRADL reports whether the type is a RADL (decodable leading) picture.
func IsRADL(t int) bool { return t == NALRADLN || t == NALRADLR }

// IsSubLayerNonReference reports whether the picture is a sub-layer
// non-reference picture (even types below 16).
func IsSubLayerNonReference(t int) bool { return t >= 0 && t < 16 && t%2 == 0 }

// POCState carries the decoding-order state needed to derive picture order
// counts (clause 8.3.1).
type POCState struct {
	prevTid0POC int32
}

// Reset forgets the previous pictures.
func (s *POCState) Reset() { *s = POCState{} }

// Next derives the picture order count of the picture whose first slice
// segment is h and advances the state. noRaslOutputFlag is NoRaslOutputFlag
// of an IRAP picture (true for IDR and BLA pictures and for a CRA picture
// that starts the bitstream or follows an end-of-sequence NAL unit).
func (s *POCState) Next(sps *SPS, h *SliceHeader, noRaslOutputFlag bool) int32 {
	maxLsb := int32(1) << uint(sps.Log2MaxPOCLsb)
	lsb := int32(h.POCLsb)
	var msb int32
	if IsIRAP(h.NALType) && noRaslOutputFlag {
		msb = 0
	} else {
		prevLsb := s.prevTid0POC & (maxLsb - 1)
		prevMsb := s.prevTid0POC - prevLsb
		switch {
		case lsb < prevLsb && prevLsb-lsb >= maxLsb/2:
			msb = prevMsb + maxLsb
		case lsb > prevLsb && lsb-prevLsb > maxLsb/2:
			msb = prevMsb - maxLsb
		default:
			msb = prevMsb
		}
	}
	poc := msb + lsb
	if h.TemporalID == 0 && !IsRASL(h.NALType) && !IsRADL(h.NALType) && !IsSubLayerNonReference(h.NALType) {
		s.prevTid0POC = poc
	}
	return poc
}

func wrap(err error) error { return fmt.Errorf("hevc: %w", err) }
