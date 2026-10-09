// Package hevc parses the parts of HEVC (ITU-T H.265 / ISO/IEC 23008-2)
// parameter sets and slice segment headers that are needed to derive
// picture order counts, so that frames a decoder emits in decode order can
// be reordered into display order.
package hevc

import (
	"errors"
	"fmt"

	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// NAL unit types.
const (
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
	NALEOS       = 36
	NALEOB       = 37
	NALPrefixSEI = 39
)

// ErrNotSlice is returned when a NAL unit is not a coded slice segment.
var ErrNotSlice = errors.New("hevc: not a slice segment NAL unit")

// ErrNotFirstSegment is returned for slice segments that do not begin a
// picture; only the first segment carries the fields parsed here.
var ErrNotFirstSegment = errors.New("hevc: not the first slice segment of a picture")

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

// SPS holds the sequence parameter set fields used here.
type SPS struct {
	ID                  uint32
	VPSID               uint32
	MaxSubLayersMinus1  int
	ChromaFormatIDC     uint32
	SeparateColourPlane bool
	Width               int
	Height              int
	Log2MaxPOCLsb       int
	// Per sub-layer, index 0 .. MaxSubLayersMinus1 (lower entries are
	// inferred from the highest when the stream omits them).
	MaxDecPicBufferingMinus1 []uint32
	MaxNumReorderPics        []uint32
	MaxLatencyIncreasePlus1  []uint32
}

// MaxReorderFrames returns sps_max_num_reorder_pics for the highest
// temporal sub-layer, capped at 16.
func (s *SPS) MaxReorderFrames() int {
	n := int(s.MaxNumReorderPics[len(s.MaxNumReorderPics)-1])
	if n > 16 {
		n = 16
	}
	return n
}

// PPS holds the picture parameter set fields used here.
type PPS struct {
	ID                            uint32
	SPSID                         uint32
	DependentSliceSegmentsEnabled bool
	OutputFlagPresent             bool
	NumExtraSliceHeaderBits       int
}

// SliceHeader holds the leading fields of the first slice segment of a
// picture.
type SliceHeader struct {
	NALType             int
	TemporalID          int
	NoOutputOfPriorPics bool
	PPSID               uint32
	SliceType           uint32
	PicOutput           bool
	POCLsb              uint32
}

// ParseSPS parses a sequence parameter set NAL unit (with its two-byte
// header, without start code).
func ParseSPS(nal []byte) (*SPS, error) {
	if Type(nal) != NALSPS || len(nal) < 4 {
		return nil, errors.New("hevc: not an SPS NAL unit")
	}
	r := bitstream.NewRBSP(nal[2:])
	s := &SPS{}
	v, err := r.ReadBits(4)
	if err != nil {
		return nil, wrap(err)
	}
	s.VPSID = uint32(v)
	if v, err = r.ReadBits(3); err != nil {
		return nil, wrap(err)
	}
	s.MaxSubLayersMinus1 = int(v)
	if err = r.SkipBits(1); err != nil { // sps_temporal_id_nesting_flag
		return nil, wrap(err)
	}
	if err = SkipProfileTierLevel(r, s.MaxSubLayersMinus1); err != nil {
		return nil, wrap(err)
	}
	if s.ID, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	if s.ChromaFormatIDC, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	if s.ChromaFormatIDC == 3 {
		if s.SeparateColourPlane, err = readFlag(r); err != nil {
			return nil, wrap(err)
		}
	}
	if v32, err := r.ReadUE(); err != nil {
		return nil, wrap(err)
	} else {
		s.Width = int(v32)
	}
	if v32, err := r.ReadUE(); err != nil {
		return nil, wrap(err)
	} else {
		s.Height = int(v32)
	}
	window, err := readFlag(r)
	if err != nil {
		return nil, wrap(err)
	}
	if window {
		for i := 0; i < 4; i++ {
			if _, err = r.ReadUE(); err != nil {
				return nil, wrap(err)
			}
		}
	}
	if _, err = r.ReadUE(); err != nil { // bit_depth_luma_minus8
		return nil, wrap(err)
	}
	if _, err = r.ReadUE(); err != nil { // bit_depth_chroma_minus8
		return nil, wrap(err)
	}
	v32, err := r.ReadUE()
	if err != nil {
		return nil, wrap(err)
	}
	if v32 > 12 {
		return nil, errors.New("hevc: log2_max_pic_order_cnt_lsb_minus4 out of range")
	}
	s.Log2MaxPOCLsb = int(v32) + 4
	orderingInfo, err := readFlag(r)
	if err != nil {
		return nil, wrap(err)
	}
	n := s.MaxSubLayersMinus1 + 1
	s.MaxDecPicBufferingMinus1 = make([]uint32, n)
	s.MaxNumReorderPics = make([]uint32, n)
	s.MaxLatencyIncreasePlus1 = make([]uint32, n)
	start := s.MaxSubLayersMinus1
	if orderingInfo {
		start = 0
	}
	for i := start; i <= s.MaxSubLayersMinus1; i++ {
		if s.MaxDecPicBufferingMinus1[i], err = r.ReadUE(); err != nil {
			return nil, wrap(err)
		}
		if s.MaxNumReorderPics[i], err = r.ReadUE(); err != nil {
			return nil, wrap(err)
		}
		if s.MaxLatencyIncreasePlus1[i], err = r.ReadUE(); err != nil {
			return nil, wrap(err)
		}
	}
	for i := 0; i < start; i++ {
		s.MaxDecPicBufferingMinus1[i] = s.MaxDecPicBufferingMinus1[start]
		s.MaxNumReorderPics[i] = s.MaxNumReorderPics[start]
		s.MaxLatencyIncreasePlus1[i] = s.MaxLatencyIncreasePlus1[start]
	}
	return s, nil
}

// SkipProfileTierLevel skips profile_tier_level(1, maxSubLayersMinus1).
func SkipProfileTierLevel(r *bitstream.Reader, maxSubLayersMinus1 int) error {
	// general_profile_space(2) tier(1) idc(5) compat(32) flags(48) = 88 bits,
	// then general_level_idc(8).
	if err := r.SkipBits(88 + 8); err != nil {
		return err
	}
	profilePresent := make([]bool, maxSubLayersMinus1)
	levelPresent := make([]bool, maxSubLayersMinus1)
	for i := 0; i < maxSubLayersMinus1; i++ {
		p, err := r.ReadBit()
		if err != nil {
			return err
		}
		l, err := r.ReadBit()
		if err != nil {
			return err
		}
		profilePresent[i] = p == 1
		levelPresent[i] = l == 1
	}
	if maxSubLayersMinus1 > 0 {
		if err := r.SkipBits(2 * (8 - maxSubLayersMinus1)); err != nil {
			return err
		}
	}
	for i := 0; i < maxSubLayersMinus1; i++ {
		if profilePresent[i] {
			if err := r.SkipBits(88); err != nil {
				return err
			}
		}
		if levelPresent[i] {
			if err := r.SkipBits(8); err != nil {
				return err
			}
		}
	}
	return nil
}

// ParsePPS parses a picture parameter set NAL unit.
func ParsePPS(nal []byte) (*PPS, error) {
	if Type(nal) != NALPPS || len(nal) < 3 {
		return nil, errors.New("hevc: not a PPS NAL unit")
	}
	r := bitstream.NewRBSP(nal[2:])
	p := &PPS{}
	var err error
	if p.ID, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	if p.SPSID, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	if p.DependentSliceSegmentsEnabled, err = readFlag(r); err != nil {
		return nil, wrap(err)
	}
	if p.OutputFlagPresent, err = readFlag(r); err != nil {
		return nil, wrap(err)
	}
	v, err := r.ReadBits(3)
	if err != nil {
		return nil, wrap(err)
	}
	p.NumExtraSliceHeaderBits = int(v)
	return p, nil
}

// ParseSliceHeader parses the leading fields of the first slice segment of
// a picture. lookup resolves a PPS id to the PPS and its SPS; it returns nil
// when either is unknown.
func ParseSliceHeader(nal []byte, lookup func(ppsID uint32) (*PPS, *SPS)) (*SliceHeader, error) {
	t := Type(nal)
	if !IsVCL(t) || len(nal) < 3 {
		return nil, ErrNotSlice
	}
	h := &SliceHeader{NALType: t, TemporalID: TemporalID(nal), PicOutput: true}
	r := bitstream.NewRBSP(nal[2:])
	first, err := readFlag(r)
	if err != nil {
		return nil, wrap(err)
	}
	if !first {
		return nil, ErrNotFirstSegment
	}
	if IsIRAP(t) {
		if h.NoOutputOfPriorPics, err = readFlag(r); err != nil {
			return nil, wrap(err)
		}
	}
	if h.PPSID, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	pps, sps := lookup(h.PPSID)
	if pps == nil || sps == nil {
		return nil, fmt.Errorf("hevc: slice refers to unknown PPS %d", h.PPSID)
	}
	if err = r.SkipBits(pps.NumExtraSliceHeaderBits); err != nil {
		return nil, wrap(err)
	}
	if h.SliceType, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	if pps.OutputFlagPresent {
		if h.PicOutput, err = readFlag(r); err != nil {
			return nil, wrap(err)
		}
	}
	if sps.SeparateColourPlane {
		if err = r.SkipBits(2); err != nil {
			return nil, wrap(err)
		}
	}
	if !IsIDR(t) {
		v, err := r.ReadBits(sps.Log2MaxPOCLsb)
		if err != nil {
			return nil, wrap(err)
		}
		h.POCLsb = uint32(v)
	}
	return h, nil
}

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

func readFlag(r *bitstream.Reader) (bool, error) {
	b, err := r.ReadBit()
	return b == 1, err
}

func wrap(err error) error { return fmt.Errorf("hevc: %w", err) }
