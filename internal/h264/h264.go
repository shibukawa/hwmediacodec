// Package h264 parses the parts of H.264 (ITU-T H.264 / ISO/IEC 14496-10)
// parameter sets and slice headers that are needed to derive picture order
// counts, so that frames a decoder emits in decode order can be reordered
// into display order.
package h264

import (
	"errors"
	"fmt"

	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// NAL unit types.
const (
	NALSlice    = 1
	NALSliceIDR = 5
	NALSPS      = 7
	NALPPS      = 8
)

// ErrNotSlice is returned when a NAL unit is not a primary coded slice.
var ErrNotSlice = errors.New("h264: not a slice NAL unit")

// SPS holds the sequence parameter set fields used here.
type SPS struct {
	ID              uint32
	ProfileIDC      uint8
	ConstraintFlags uint8 // constraint_set0_flag in bit 7 ... constraint_set5_flag in bit 2
	LevelIDC        uint8

	ChromaFormatIDC     uint32
	SeparateColourPlane bool

	Log2MaxFrameNum           int
	POCType                   uint32
	Log2MaxPOCLsb             int
	DeltaPicOrderAlwaysZero   bool
	OffsetForNonRefPic        int32
	OffsetForTopToBottomField int32
	OffsetForRefFrame         []int32

	MaxNumRefFrames     uint32
	PicWidthInMbs       int
	PicHeightInMapUnits int
	FrameMbsOnly        bool

	// VUI fields. HasBitstreamRestriction reports whether the reorder bounds
	// were present in the stream.
	HasBitstreamRestriction bool
	MaxNumReorderFrames     uint32
	MaxDecFrameBuffering    uint32
	HasTiming               bool
	NumUnitsInTick          uint32
	TimeScale               uint32
}

// ConstraintSet3 reports constraint_set3_flag.
func (s *SPS) ConstraintSet3() bool { return s.ConstraintFlags&0x10 != 0 }

// Width and Height return the coded picture size in pixels (before
// cropping).
func (s *SPS) Width() int { return s.PicWidthInMbs * 16 }

// Height returns the coded frame height in pixels (before cropping).
func (s *SPS) Height() int { return s.FrameHeightInMbs() * 16 }

// FrameHeightInMbs returns the frame height in macroblocks.
func (s *SPS) FrameHeightInMbs() int {
	if s.FrameMbsOnly {
		return s.PicHeightInMapUnits
	}
	return 2 * s.PicHeightInMapUnits
}

// PPS holds the picture parameter set fields used here.
type PPS struct {
	ID    uint32
	SPSID uint32
	// BottomFieldPicOrderInFramePresent is pic_order_present_flag.
	BottomFieldPicOrderInFramePresent bool
}

// SliceHeader holds the leading slice header fields of a primary coded
// picture, up to and including the picture order count syntax.
type SliceHeader struct {
	NALType   int
	NalRefIDC uint8
	IDR       bool

	FirstMbInSlice uint32
	SliceType      uint32
	PPSID          uint32
	FrameNum       uint32
	FieldPic       bool
	BottomField    bool
	IDRPicID       uint32
	POCLsb         uint32
	DeltaPOCBottom int32
	DeltaPOC       [2]int32
}

// IsReference reports whether the picture is a reference picture.
func (h *SliceHeader) IsReference() bool { return h.NalRefIDC != 0 }

var highProfiles = map[uint8]bool{100: true, 110: true, 122: true, 244: true, 44: true, 83: true, 86: true, 118: true, 128: true, 138: true, 139: true, 134: true, 135: true}

// ParseSPS parses a sequence parameter set NAL unit (with its one-byte
// header, without start code).
func ParseSPS(nal []byte) (*SPS, error) {
	if len(nal) < 4 || nal[0]&0x1f != NALSPS {
		return nil, errors.New("h264: not an SPS NAL unit")
	}
	s := &SPS{ProfileIDC: nal[1], ConstraintFlags: nal[2], LevelIDC: nal[3], ChromaFormatIDC: 1}
	r := bitstream.NewRBSP(nal[4:])
	var err error
	if s.ID, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	if highProfiles[s.ProfileIDC] {
		if s.ChromaFormatIDC, err = r.ReadUE(); err != nil {
			return nil, wrap(err)
		}
		if s.ChromaFormatIDC == 3 {
			if s.SeparateColourPlane, err = readFlag(r); err != nil {
				return nil, wrap(err)
			}
		}
		if _, err = r.ReadUE(); err != nil { // bit_depth_luma_minus8
			return nil, wrap(err)
		}
		if _, err = r.ReadUE(); err != nil { // bit_depth_chroma_minus8
			return nil, wrap(err)
		}
		if err = r.SkipBits(1); err != nil { // qpprime_y_zero_transform_bypass_flag
			return nil, wrap(err)
		}
		scaling, err := readFlag(r)
		if err != nil {
			return nil, wrap(err)
		}
		if scaling {
			n := 8
			if s.ChromaFormatIDC == 3 {
				n = 12
			}
			for i := 0; i < n; i++ {
				present, err := readFlag(r)
				if err != nil {
					return nil, wrap(err)
				}
				if present {
					size := 16
					if i >= 6 {
						size = 64
					}
					if err := skipScalingList(r, size); err != nil {
						return nil, wrap(err)
					}
				}
			}
		}
	}
	v, err := r.ReadUE()
	if err != nil {
		return nil, wrap(err)
	}
	s.Log2MaxFrameNum = int(v) + 4
	if s.POCType, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	switch s.POCType {
	case 0:
		if v, err = r.ReadUE(); err != nil {
			return nil, wrap(err)
		}
		s.Log2MaxPOCLsb = int(v) + 4
	case 1:
		if s.DeltaPicOrderAlwaysZero, err = readFlag(r); err != nil {
			return nil, wrap(err)
		}
		if s.OffsetForNonRefPic, err = r.ReadSE(); err != nil {
			return nil, wrap(err)
		}
		if s.OffsetForTopToBottomField, err = r.ReadSE(); err != nil {
			return nil, wrap(err)
		}
		n, err := r.ReadUE()
		if err != nil {
			return nil, wrap(err)
		}
		if n > 255 {
			return nil, errors.New("h264: num_ref_frames_in_pic_order_cnt_cycle out of range")
		}
		s.OffsetForRefFrame = make([]int32, n)
		for i := range s.OffsetForRefFrame {
			if s.OffsetForRefFrame[i], err = r.ReadSE(); err != nil {
				return nil, wrap(err)
			}
		}
	case 2:
	default:
		return nil, fmt.Errorf("h264: pic_order_cnt_type %d out of range", s.POCType)
	}
	if s.MaxNumRefFrames, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	if err = r.SkipBits(1); err != nil { // gaps_in_frame_num_value_allowed_flag
		return nil, wrap(err)
	}
	if v, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	s.PicWidthInMbs = int(v) + 1
	if v, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	s.PicHeightInMapUnits = int(v) + 1
	if s.FrameMbsOnly, err = readFlag(r); err != nil {
		return nil, wrap(err)
	}
	if !s.FrameMbsOnly {
		if err = r.SkipBits(1); err != nil { // mb_adaptive_frame_field_flag
			return nil, wrap(err)
		}
	}
	if err = r.SkipBits(1); err != nil { // direct_8x8_inference_flag
		return nil, wrap(err)
	}
	cropping, err := readFlag(r)
	if err != nil {
		return nil, wrap(err)
	}
	if cropping {
		for i := 0; i < 4; i++ {
			if _, err = r.ReadUE(); err != nil {
				return nil, wrap(err)
			}
		}
	}
	vui, err := readFlag(r)
	if err != nil {
		return nil, wrap(err)
	}
	if vui {
		// A truncated or malformed VUI must not reject the SPS: everything
		// the decoder needs has been read, the VUI only refines the reorder
		// bound.
		_ = parseVUI(r, s)
	}
	return s, nil
}

func parseVUI(r *bitstream.Reader, s *SPS) error {
	flag, err := readFlag(r) // aspect_ratio_info_present_flag
	if err != nil {
		return err
	}
	if flag {
		idc, err := r.ReadBits(8)
		if err != nil {
			return err
		}
		if idc == 255 { // Extended_SAR
			if err := r.SkipBits(32); err != nil {
				return err
			}
		}
	}
	if flag, err = readFlag(r); err != nil { // overscan_info_present_flag
		return err
	}
	if flag {
		if err := r.SkipBits(1); err != nil {
			return err
		}
	}
	if flag, err = readFlag(r); err != nil { // video_signal_type_present_flag
		return err
	}
	if flag {
		if err := r.SkipBits(4); err != nil { // video_format, video_full_range_flag
			return err
		}
		if flag, err = readFlag(r); err != nil { // colour_description_present_flag
			return err
		}
		if flag {
			if err := r.SkipBits(24); err != nil {
				return err
			}
		}
	}
	if flag, err = readFlag(r); err != nil { // chroma_loc_info_present_flag
		return err
	}
	if flag {
		if _, err := r.ReadUE(); err != nil {
			return err
		}
		if _, err := r.ReadUE(); err != nil {
			return err
		}
	}
	if flag, err = readFlag(r); err != nil { // timing_info_present_flag
		return err
	}
	if flag {
		units, err := r.ReadBits(32)
		if err != nil {
			return err
		}
		scale, err := r.ReadBits(32)
		if err != nil {
			return err
		}
		if err := r.SkipBits(1); err != nil { // fixed_frame_rate_flag
			return err
		}
		s.HasTiming, s.NumUnitsInTick, s.TimeScale = true, uint32(units), uint32(scale)
	}
	nalHRD, err := readFlag(r)
	if err != nil {
		return err
	}
	if nalHRD {
		if err := skipHRD(r); err != nil {
			return err
		}
	}
	vclHRD, err := readFlag(r)
	if err != nil {
		return err
	}
	if vclHRD {
		if err := skipHRD(r); err != nil {
			return err
		}
	}
	if nalHRD || vclHRD {
		if err := r.SkipBits(1); err != nil { // low_delay_hrd_flag
			return err
		}
	}
	if err := r.SkipBits(1); err != nil { // pic_struct_present_flag
		return err
	}
	if flag, err = readFlag(r); err != nil { // bitstream_restriction_flag
		return err
	}
	if !flag {
		return nil
	}
	if err := r.SkipBits(1); err != nil { // motion_vectors_over_pic_boundaries_flag
		return err
	}
	for i := 0; i < 4; i++ { // max_bytes_per_pic_denom .. log2_max_mv_length_vertical
		if _, err := r.ReadUE(); err != nil {
			return err
		}
	}
	reorder, err := r.ReadUE()
	if err != nil {
		return err
	}
	dpb, err := r.ReadUE()
	if err != nil {
		return err
	}
	s.HasBitstreamRestriction = true
	s.MaxNumReorderFrames = reorder
	s.MaxDecFrameBuffering = dpb
	return nil
}

func skipHRD(r *bitstream.Reader) error {
	cpbCnt, err := r.ReadUE()
	if err != nil {
		return err
	}
	if cpbCnt > 31 {
		return errors.New("h264: cpb_cnt_minus1 out of range")
	}
	if err := r.SkipBits(8); err != nil { // bit_rate_scale, cpb_size_scale
		return err
	}
	for i := uint32(0); i <= cpbCnt; i++ {
		if _, err := r.ReadUE(); err != nil {
			return err
		}
		if _, err := r.ReadUE(); err != nil {
			return err
		}
		if err := r.SkipBits(1); err != nil {
			return err
		}
	}
	return r.SkipBits(20)
}

func skipScalingList(r *bitstream.Reader, size int) error {
	last, next := 8, 8
	for j := 0; j < size; j++ {
		if next != 0 {
			delta, err := r.ReadSE()
			if err != nil {
				return err
			}
			next = (last + int(delta) + 256) % 256
		}
		if next != 0 {
			last = next
		}
	}
	return nil
}

// ParsePPS parses a picture parameter set NAL unit.
func ParsePPS(nal []byte) (*PPS, error) {
	if len(nal) < 2 || nal[0]&0x1f != NALPPS {
		return nil, errors.New("h264: not a PPS NAL unit")
	}
	r := bitstream.NewRBSP(nal[1:])
	p := &PPS{}
	var err error
	if p.ID, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	if p.SPSID, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	if err = r.SkipBits(1); err != nil { // entropy_coding_mode_flag
		return nil, wrap(err)
	}
	if p.BottomFieldPicOrderInFramePresent, err = readFlag(r); err != nil {
		return nil, wrap(err)
	}
	return p, nil
}

// ParseSliceHeader parses the leading fields of a slice NAL unit (types 1
// and 5). lookup resolves a PPS id to the PPS and its SPS; it returns nil
// when either is unknown.
func ParseSliceHeader(nal []byte, lookup func(ppsID uint32) (*PPS, *SPS)) (*SliceHeader, error) {
	if len(nal) < 2 {
		return nil, ErrNotSlice
	}
	t := int(nal[0] & 0x1f)
	if t != NALSlice && t != NALSliceIDR {
		return nil, ErrNotSlice
	}
	h := &SliceHeader{NALType: t, NalRefIDC: (nal[0] >> 5) & 3, IDR: t == NALSliceIDR}
	r := bitstream.NewRBSP(nal[1:])
	var err error
	if h.FirstMbInSlice, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	if h.SliceType, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	if h.PPSID, err = r.ReadUE(); err != nil {
		return nil, wrap(err)
	}
	pps, sps := lookup(h.PPSID)
	if pps == nil || sps == nil {
		return nil, fmt.Errorf("h264: slice refers to unknown PPS %d", h.PPSID)
	}
	if sps.SeparateColourPlane {
		if err = r.SkipBits(2); err != nil { // colour_plane_id
			return nil, wrap(err)
		}
	}
	v, err := r.ReadBits(sps.Log2MaxFrameNum)
	if err != nil {
		return nil, wrap(err)
	}
	h.FrameNum = uint32(v)
	if !sps.FrameMbsOnly {
		if h.FieldPic, err = readFlag(r); err != nil {
			return nil, wrap(err)
		}
		if h.FieldPic {
			if h.BottomField, err = readFlag(r); err != nil {
				return nil, wrap(err)
			}
		}
	}
	if h.IDR {
		if h.IDRPicID, err = r.ReadUE(); err != nil {
			return nil, wrap(err)
		}
	}
	switch sps.POCType {
	case 0:
		if v, err = r.ReadBits(sps.Log2MaxPOCLsb); err != nil {
			return nil, wrap(err)
		}
		h.POCLsb = uint32(v)
		if pps.BottomFieldPicOrderInFramePresent && !h.FieldPic {
			if h.DeltaPOCBottom, err = r.ReadSE(); err != nil {
				return nil, wrap(err)
			}
		}
	case 1:
		if !sps.DeltaPicOrderAlwaysZero {
			if h.DeltaPOC[0], err = r.ReadSE(); err != nil {
				return nil, wrap(err)
			}
			if pps.BottomFieldPicOrderInFramePresent && !h.FieldPic {
				if h.DeltaPOC[1], err = r.ReadSE(); err != nil {
					return nil, wrap(err)
				}
			}
		}
	}
	return h, nil
}

// POCState carries the decoding-order state needed to derive the picture
// order count of successive pictures (clause 8.2.1).
type POCState struct {
	prevPOCMsb         int32
	prevPOCLsb         uint32
	prevFrameNum       uint32
	prevFrameNumOffset int32
	started            bool
}

// Reset forgets the previous pictures; the next picture must be an IDR.
func (s *POCState) Reset() { *s = POCState{} }

// Next derives the picture order count of the picture whose first slice is
// h and advances the state. Memory management control operation 5 is not
// detected (it would need the full slice header), so streams that reset
// the POC that way are reordered as if the reset were absent.
func (s *POCState) Next(sps *SPS, h *SliceHeader) int32 {
	var top, bottom int32
	maxFrameNum := int32(1) << uint(sps.Log2MaxFrameNum)
	switch sps.POCType {
	case 0:
		maxLsb := int32(1) << uint(sps.Log2MaxPOCLsb)
		var msb int32
		lsb := int32(h.POCLsb)
		if h.IDR || !s.started {
			s.prevPOCMsb, s.prevPOCLsb = 0, 0
		}
		prevLsb := int32(s.prevPOCLsb)
		switch {
		case lsb < prevLsb && prevLsb-lsb >= maxLsb/2:
			msb = s.prevPOCMsb + maxLsb
		case lsb > prevLsb && lsb-prevLsb > maxLsb/2:
			msb = s.prevPOCMsb - maxLsb
		default:
			msb = s.prevPOCMsb
		}
		if !h.FieldPic {
			top = msb + lsb
			bottom = top + h.DeltaPOCBottom
		} else if !h.BottomField {
			top = msb + lsb
			bottom = top
		} else {
			bottom = msb + lsb
			top = bottom
		}
		if h.IsReference() {
			s.prevPOCMsb, s.prevPOCLsb = msb, h.POCLsb
		}
	case 1, 2:
		var frameNumOffset int32
		switch {
		case h.IDR || !s.started:
			frameNumOffset = 0
		case s.prevFrameNum > h.FrameNum:
			frameNumOffset = s.prevFrameNumOffset + maxFrameNum
		default:
			frameNumOffset = s.prevFrameNumOffset
		}
		s.prevFrameNumOffset = frameNumOffset
		if sps.POCType == 1 {
			cycle := int32(len(sps.OffsetForRefFrame))
			var absFrameNum int32
			if cycle != 0 {
				absFrameNum = frameNumOffset + int32(h.FrameNum)
			}
			if !h.IsReference() && absFrameNum > 0 {
				absFrameNum--
			}
			var expected int32
			if absFrameNum > 0 {
				cycleCnt := (absFrameNum - 1) / cycle
				inCycle := (absFrameNum - 1) % cycle
				var perCycle int32
				for _, o := range sps.OffsetForRefFrame {
					perCycle += o
				}
				expected = cycleCnt * perCycle
				for i := int32(0); i <= inCycle; i++ {
					expected += sps.OffsetForRefFrame[i]
				}
			}
			if !h.IsReference() {
				expected += sps.OffsetForNonRefPic
			}
			switch {
			case !h.FieldPic:
				top = expected + h.DeltaPOC[0]
				bottom = top + sps.OffsetForTopToBottomField + h.DeltaPOC[1]
			case !h.BottomField:
				top = expected + h.DeltaPOC[0]
				bottom = top
			default:
				bottom = expected + sps.OffsetForTopToBottomField + h.DeltaPOC[0]
				top = bottom
			}
		} else {
			var temp int32
			if !h.IDR {
				temp = 2 * (frameNumOffset + int32(h.FrameNum))
				if !h.IsReference() {
					temp--
				}
			}
			top, bottom = temp, temp
		}
	}
	s.prevFrameNum = h.FrameNum
	s.started = true
	if top < bottom {
		return top
	}
	return bottom
}

// maxDpbMbs maps level_idc to MaxDpbMbs (table A-1). Level 1b shares the
// value of level 1.
var maxDpbMbs = map[uint8]int{
	9: 396, 10: 396, 11: 900, 12: 2376, 13: 2376, 20: 2376, 21: 4752, 22: 8100,
	30: 8100, 31: 18000, 32: 20480, 40: 32768, 41: 32768, 42: 34816,
	50: 110400, 51: 184320, 52: 184320, 60: 696320, 61: 696320, 62: 696320,
}

// MaxReorderFrames returns how many frames may precede a frame in decoding
// order and follow it in output order: max_num_reorder_frames when the VUI
// carries it, 0 when the stream cannot reorder (pic_order_cnt_type 2,
// intra-only, or a constrained profile), and otherwise the DPB size the
// level allows for the picture size (clause A.3.1 and E.2.1).
func (s *SPS) MaxReorderFrames() int {
	if s.HasBitstreamRestriction {
		n := int(s.MaxNumReorderFrames)
		if dpb := int(s.MaxDecFrameBuffering); dpb < n {
			n = dpb
		}
		if n > 16 {
			n = 16
		}
		return n
	}
	if s.POCType == 2 || s.MaxNumRefFrames == 0 {
		return 0
	}
	switch s.ProfileIDC {
	case 44, 86, 100, 110, 122, 244:
		if s.ConstraintSet3() {
			return 0
		}
	}
	level := s.LevelIDC
	if level == 11 && s.ConstraintSet3() && (s.ProfileIDC == 66 || s.ProfileIDC == 77 || s.ProfileIDC == 88) {
		level = 9 // level 1b
	}
	mbs, ok := maxDpbMbs[level]
	if !ok {
		return 16
	}
	frameMbs := s.PicWidthInMbs * s.FrameHeightInMbs()
	if frameMbs <= 0 {
		return 16
	}
	n := mbs / frameMbs
	if n > 16 {
		n = 16
	}
	if n < 1 {
		n = 1
	}
	return n
}

func readFlag(r *bitstream.Reader) (bool, error) {
	b, err := r.ReadBit()
	return b == 1, err
}

func wrap(err error) error { return fmt.Errorf("h264: %w", err) }
