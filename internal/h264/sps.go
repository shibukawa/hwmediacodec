// Package h264 parses the H.264 (Rec. ITU-T H.264 / ISO/IEC 14496-10)
// parameter sets and slice headers and implements the decoding-process
// bookkeeping (picture order count, reference picture marking and reference
// list construction) that slice-level hardware decode APIs such as VA-API
// leave to the application.
package h264

import (
	"errors"
	"fmt"

	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// NAL unit types (Table 7-1).
const (
	NALSlice        = 1
	NALSliceDPA     = 2
	NALSliceDPB     = 3
	NALSliceDPC     = 4
	NALSliceIDR     = 5
	NALSEI          = 6
	NALSPS          = 7
	NALPPS          = 8
	NALAUD          = 9
	NALEndSeq       = 10
	NALEndStream    = 11
	NALFiller       = 12
	NALSPSExt       = 13
	NALPrefix       = 14
	NALSubsetSPS    = 15
	NALSliceAux     = 19
	NALSliceExt     = 20
	NALSliceExt3D   = 21
	maxSPSID        = 31
	maxPPSID        = 255
	maxRefFrames    = 16
	maxRefIdxActive = 32
)

// ErrSyntax is wrapped by every parse error.
var ErrSyntax = errors.New("h264: bitstream syntax error")

func syntaxErr(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrSyntax}, args...)...)
}

// NALHeader splits the first byte of a NAL unit.
func NALHeader(nal []byte) (refIdc, typ int, ok bool) {
	if len(nal) == 0 || nal[0]&0x80 != 0 {
		return 0, 0, false
	}
	return int(nal[0]>>5) & 3, int(nal[0] & 0x1f), true
}

// HRD mirrors hrd_parameters() (E.1.2); only the fields that matter for
// buffering are kept.
type HRD struct {
	CpbCnt                             int
	BitRateScale, CpbSizeScale         uint8
	InitialCpbRemovalDelayLengthMinus1 uint8
	CpbRemovalDelayLengthMinus1        uint8
	DpbOutputDelayLengthMinus1         uint8
	TimeOffsetLength                   uint8
}

// VUI mirrors vui_parameters() (E.1.1).
type VUI struct {
	AspectRatioInfoPresent bool
	AspectRatioIDC         uint8
	SarWidth, SarHeight    uint16

	OverscanInfoPresent, OverscanAppropriate bool

	VideoSignalTypePresent   bool
	VideoFormat              uint8
	VideoFullRange           bool
	ColourDescriptionPresent bool
	ColourPrimaries          uint8
	TransferCharacteristics  uint8
	MatrixCoefficients       uint8

	ChromaLocInfoPresent                              bool
	ChromaSampleLocTypeTop, ChromaSampleLocTypeBottom uint32

	TimingInfoPresent bool
	NumUnitsInTick    uint32
	TimeScale         uint32
	FixedFrameRate    bool

	NalHRDPresent, VclHRDPresent bool
	NalHRD, VclHRD               HRD
	LowDelayHRD                  bool
	PicStructPresent             bool

	BitstreamRestriction           bool
	MotionVectorsOverPicBoundaries bool
	MaxBytesPerPicDenom            uint32
	MaxBitsPerMbDenom              uint32
	Log2MaxMvLengthHorizontal      uint32
	Log2MaxMvLengthVertical        uint32
	MaxNumReorderFrames            uint32
	MaxDecFrameBuffering           uint32
}

// SPS is a parsed sequence parameter set (7.3.2.1.1).
type SPS struct {
	ProfileIDC      uint8
	ConstraintFlags uint8 // constraint_set0_flag in bit 7 ... constraint_set5_flag in bit 2
	LevelIDC        uint8
	ID              uint32

	ChromaFormatIDC             uint32
	SeparateColourPlane         bool
	BitDepthLuma                uint32
	BitDepthChroma              uint32
	QpprimeYZeroTransformBypass bool
	ScalingMatrixPresent        bool
	ScalingLists                ScalingLists // resolved; flat when absent

	Log2MaxFrameNum           uint32
	PicOrderCntType           uint32
	Log2MaxPicOrderCntLsb     uint32
	DeltaPicOrderAlwaysZero   bool
	OffsetForNonRefPic        int32
	OffsetForTopToBottomField int32
	OffsetForRefFrame         []int32

	MaxNumRefFrames       uint32
	GapsInFrameNumAllowed bool
	PicWidthInMbs         uint32
	PicHeightInMapUnits   uint32
	FrameMbsOnly          bool
	MbAdaptiveFrameField  bool
	Direct8x8Inference    bool

	FrameCropping                            bool
	CropLeft, CropRight, CropTop, CropBottom uint32

	VUIPresent bool
	VUI        VUI
}

// ConstraintSet reports constraint_set<n>_flag.
func (s *SPS) ConstraintSet(n int) bool { return s.ConstraintFlags&(0x80>>uint(n)) != 0 }

// ChromaArrayType is chroma_format_idc, or 0 for separate colour planes.
func (s *SPS) ChromaArrayType() uint32 {
	if s.SeparateColourPlane {
		return 0
	}
	return s.ChromaFormatIDC
}

// MaxFrameNum is 2^(log2_max_frame_num_minus4 + 4).
func (s *SPS) MaxFrameNum() int { return 1 << s.Log2MaxFrameNum }

// MaxPicOrderCntLsb is 2^(log2_max_pic_order_cnt_lsb_minus4 + 4).
func (s *SPS) MaxPicOrderCntLsb() int { return 1 << s.Log2MaxPicOrderCntLsb }

// FrameHeightInMbs is PicHeightInMapUnits * (2 - frame_mbs_only_flag).
func (s *SPS) FrameHeightInMbs() int {
	if s.FrameMbsOnly {
		return int(s.PicHeightInMapUnits)
	}
	return 2 * int(s.PicHeightInMapUnits)
}

// CodedWidth and CodedHeight are the dimensions in luma samples of the
// decoded picture before cropping.
func (s *SPS) CodedWidth() int  { return int(s.PicWidthInMbs) * 16 }
func (s *SPS) CodedHeight() int { return s.FrameHeightInMbs() * 16 }

// cropUnits returns CropUnitX and CropUnitY (7.4.2.1.1).
func (s *SPS) cropUnits() (int, int) {
	subW, subH := 1, 1
	switch s.ChromaArrayType() {
	case 1:
		subW, subH = 2, 2
	case 2:
		subW, subH = 2, 1
	}
	if s.FrameMbsOnly {
		return subW, subH
	}
	return subW, subH * 2
}

// Crop returns the cropping rectangle in luma samples: the offsets of the
// left and top edges and the output width and height.
func (s *SPS) Crop() (left, top, width, height int) {
	width, height = s.CodedWidth(), s.CodedHeight()
	if !s.FrameCropping {
		return 0, 0, width, height
	}
	ux, uy := s.cropUnits()
	left = ux * int(s.CropLeft)
	top = uy * int(s.CropTop)
	width -= ux * int(s.CropLeft+s.CropRight)
	height -= uy * int(s.CropTop+s.CropBottom)
	if width <= 0 || height <= 0 || left < 0 || top < 0 {
		return 0, 0, s.CodedWidth(), s.CodedHeight()
	}
	return left, top, width, height
}

// Width and Height are the cropped output dimensions.
func (s *SPS) Width() int  { _, _, w, _ := s.Crop(); return w }
func (s *SPS) Height() int { _, _, _, h := s.Crop(); return h }

// hasChromaInfo reports whether profile_idc carries the chroma/bit-depth
// syntax (the "high" profiles).
func hasChromaInfo(profileIDC uint8) bool {
	switch profileIDC {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		return true
	}
	return false
}

// ParseSPS parses a sequence parameter set NAL unit (including the NAL
// header byte).
func ParseSPS(nal []byte) (*SPS, error) {
	if _, typ, ok := NALHeader(nal); !ok || typ != NALSPS {
		return nil, syntaxErr("not an SPS NAL unit")
	}
	if len(nal) < 4 {
		return nil, syntaxErr("SPS too short")
	}
	r := bitstream.NewRBSP(nal[1:])
	s := &SPS{ChromaFormatIDC: 1, BitDepthLuma: 8, BitDepthChroma: 8}
	s.ScalingLists = flatScalingLists()

	v, err := r.ReadBits(8)
	if err != nil {
		return nil, err
	}
	s.ProfileIDC = uint8(v)
	if v, err = r.ReadBits(8); err != nil {
		return nil, err
	}
	s.ConstraintFlags = uint8(v) & 0xfc
	if v, err = r.ReadBits(8); err != nil {
		return nil, err
	}
	s.LevelIDC = uint8(v)
	if s.ID, err = r.ReadUE(); err != nil {
		return nil, err
	}
	if s.ID > maxSPSID {
		return nil, syntaxErr("seq_parameter_set_id %d out of range", s.ID)
	}

	if hasChromaInfo(s.ProfileIDC) {
		if s.ChromaFormatIDC, err = r.ReadUE(); err != nil {
			return nil, err
		}
		if s.ChromaFormatIDC > 3 {
			return nil, syntaxErr("chroma_format_idc %d out of range", s.ChromaFormatIDC)
		}
		if s.ChromaFormatIDC == 3 {
			if s.SeparateColourPlane, err = r.ReadFlag(); err != nil {
				return nil, err
			}
		}
		if s.BitDepthLuma, err = r.ReadUE(); err != nil {
			return nil, err
		}
		if s.BitDepthChroma, err = r.ReadUE(); err != nil {
			return nil, err
		}
		s.BitDepthLuma += 8
		s.BitDepthChroma += 8
		if s.BitDepthLuma > 14 || s.BitDepthChroma > 14 {
			return nil, syntaxErr("bit depth out of range")
		}
		if s.QpprimeYZeroTransformBypass, err = r.ReadFlag(); err != nil {
			return nil, err
		}
		if s.ScalingMatrixPresent, err = r.ReadFlag(); err != nil {
			return nil, err
		}
		if s.ScalingMatrixPresent {
			count := 8
			if s.ChromaFormatIDC == 3 {
				count = 12
			}
			if s.ScalingLists, err = parseScalingMatrix(r, count, nil); err != nil {
				return nil, err
			}
		}
	}

	if s.Log2MaxFrameNum, err = r.ReadUE(); err != nil {
		return nil, err
	}
	if s.Log2MaxFrameNum > 12 {
		return nil, syntaxErr("log2_max_frame_num_minus4 %d out of range", s.Log2MaxFrameNum)
	}
	s.Log2MaxFrameNum += 4
	if s.PicOrderCntType, err = r.ReadUE(); err != nil {
		return nil, err
	}
	switch s.PicOrderCntType {
	case 0:
		if s.Log2MaxPicOrderCntLsb, err = r.ReadUE(); err != nil {
			return nil, err
		}
		if s.Log2MaxPicOrderCntLsb > 12 {
			return nil, syntaxErr("log2_max_pic_order_cnt_lsb_minus4 %d out of range", s.Log2MaxPicOrderCntLsb)
		}
		s.Log2MaxPicOrderCntLsb += 4
	case 1:
		if s.DeltaPicOrderAlwaysZero, err = r.ReadFlag(); err != nil {
			return nil, err
		}
		if s.OffsetForNonRefPic, err = r.ReadSE(); err != nil {
			return nil, err
		}
		if s.OffsetForTopToBottomField, err = r.ReadSE(); err != nil {
			return nil, err
		}
		n, err := r.ReadUE()
		if err != nil {
			return nil, err
		}
		if n > 255 {
			return nil, syntaxErr("num_ref_frames_in_pic_order_cnt_cycle %d out of range", n)
		}
		s.OffsetForRefFrame = make([]int32, n)
		for i := range s.OffsetForRefFrame {
			if s.OffsetForRefFrame[i], err = r.ReadSE(); err != nil {
				return nil, err
			}
		}
	case 2:
	default:
		return nil, syntaxErr("pic_order_cnt_type %d out of range", s.PicOrderCntType)
	}

	if s.MaxNumRefFrames, err = r.ReadUE(); err != nil {
		return nil, err
	}
	if s.MaxNumRefFrames > maxRefFrames {
		return nil, syntaxErr("max_num_ref_frames %d out of range", s.MaxNumRefFrames)
	}
	if s.GapsInFrameNumAllowed, err = r.ReadFlag(); err != nil {
		return nil, err
	}
	if s.PicWidthInMbs, err = r.ReadUE(); err != nil {
		return nil, err
	}
	if s.PicHeightInMapUnits, err = r.ReadUE(); err != nil {
		return nil, err
	}
	s.PicWidthInMbs++
	s.PicHeightInMapUnits++
	if s.PicWidthInMbs > 1024 || s.PicHeightInMapUnits > 1024 {
		return nil, syntaxErr("picture size %dx%d macroblocks out of range", s.PicWidthInMbs, s.PicHeightInMapUnits)
	}
	if s.FrameMbsOnly, err = r.ReadFlag(); err != nil {
		return nil, err
	}
	if !s.FrameMbsOnly {
		if s.MbAdaptiveFrameField, err = r.ReadFlag(); err != nil {
			return nil, err
		}
	}
	if s.Direct8x8Inference, err = r.ReadFlag(); err != nil {
		return nil, err
	}
	if s.FrameCropping, err = r.ReadFlag(); err != nil {
		return nil, err
	}
	if s.FrameCropping {
		for _, p := range []*uint32{&s.CropLeft, &s.CropRight, &s.CropTop, &s.CropBottom} {
			if *p, err = r.ReadUE(); err != nil {
				return nil, err
			}
		}
	}
	if s.VUIPresent, err = r.ReadFlag(); err != nil {
		return nil, err
	}
	if s.VUIPresent {
		// VUI only carries display hints; a truncated VUI (seen in the
		// wild) must not reject the stream.
		if err := parseVUI(r, &s.VUI); err != nil && !errors.Is(err, bitstream.ErrEOF) {
			return nil, err
		}
	}
	return s, nil
}

func parseHRD(r *bitstream.Reader, h *HRD) error {
	n, err := r.ReadUE()
	if err != nil {
		return err
	}
	if n > 31 {
		return syntaxErr("cpb_cnt_minus1 %d out of range", n)
	}
	h.CpbCnt = int(n) + 1
	v, err := r.ReadBits(8)
	if err != nil {
		return err
	}
	h.BitRateScale, h.CpbSizeScale = uint8(v>>4), uint8(v&0xf)
	for i := 0; i < h.CpbCnt; i++ {
		if _, err := r.ReadUE(); err != nil { // bit_rate_value_minus1
			return err
		}
		if _, err := r.ReadUE(); err != nil { // cpb_size_value_minus1
			return err
		}
		if _, err := r.ReadBit(); err != nil { // cbr_flag
			return err
		}
	}
	for _, p := range []*uint8{&h.InitialCpbRemovalDelayLengthMinus1, &h.CpbRemovalDelayLengthMinus1, &h.DpbOutputDelayLengthMinus1, &h.TimeOffsetLength} {
		v, err := r.ReadBits(5)
		if err != nil {
			return err
		}
		*p = uint8(v)
	}
	return nil
}

func parseVUI(r *bitstream.Reader, v *VUI) error {
	var err error
	if v.AspectRatioInfoPresent, err = r.ReadFlag(); err != nil {
		return err
	}
	if v.AspectRatioInfoPresent {
		b, err := r.ReadBits(8)
		if err != nil {
			return err
		}
		v.AspectRatioIDC = uint8(b)
		if v.AspectRatioIDC == 255 { // Extended_SAR
			w, err := r.ReadBits(16)
			if err != nil {
				return err
			}
			h, err := r.ReadBits(16)
			if err != nil {
				return err
			}
			v.SarWidth, v.SarHeight = uint16(w), uint16(h)
		}
	}
	if v.OverscanInfoPresent, err = r.ReadFlag(); err != nil {
		return err
	}
	if v.OverscanInfoPresent {
		if v.OverscanAppropriate, err = r.ReadFlag(); err != nil {
			return err
		}
	}
	if v.VideoSignalTypePresent, err = r.ReadFlag(); err != nil {
		return err
	}
	if v.VideoSignalTypePresent {
		b, err := r.ReadBits(3)
		if err != nil {
			return err
		}
		v.VideoFormat = uint8(b)
		if v.VideoFullRange, err = r.ReadFlag(); err != nil {
			return err
		}
		if v.ColourDescriptionPresent, err = r.ReadFlag(); err != nil {
			return err
		}
		if v.ColourDescriptionPresent {
			for _, p := range []*uint8{&v.ColourPrimaries, &v.TransferCharacteristics, &v.MatrixCoefficients} {
				b, err := r.ReadBits(8)
				if err != nil {
					return err
				}
				*p = uint8(b)
			}
		}
	}
	if v.ChromaLocInfoPresent, err = r.ReadFlag(); err != nil {
		return err
	}
	if v.ChromaLocInfoPresent {
		if v.ChromaSampleLocTypeTop, err = r.ReadUE(); err != nil {
			return err
		}
		if v.ChromaSampleLocTypeBottom, err = r.ReadUE(); err != nil {
			return err
		}
	}
	if v.TimingInfoPresent, err = r.ReadFlag(); err != nil {
		return err
	}
	if v.TimingInfoPresent {
		b, err := r.ReadBits(32)
		if err != nil {
			return err
		}
		v.NumUnitsInTick = uint32(b)
		if b, err = r.ReadBits(32); err != nil {
			return err
		}
		v.TimeScale = uint32(b)
		if v.FixedFrameRate, err = r.ReadFlag(); err != nil {
			return err
		}
	}
	if v.NalHRDPresent, err = r.ReadFlag(); err != nil {
		return err
	}
	if v.NalHRDPresent {
		if err := parseHRD(r, &v.NalHRD); err != nil {
			return err
		}
	}
	if v.VclHRDPresent, err = r.ReadFlag(); err != nil {
		return err
	}
	if v.VclHRDPresent {
		if err := parseHRD(r, &v.VclHRD); err != nil {
			return err
		}
	}
	if v.NalHRDPresent || v.VclHRDPresent {
		if v.LowDelayHRD, err = r.ReadFlag(); err != nil {
			return err
		}
	}
	if v.PicStructPresent, err = r.ReadFlag(); err != nil {
		return err
	}
	if v.BitstreamRestriction, err = r.ReadFlag(); err != nil {
		return err
	}
	if v.BitstreamRestriction {
		if v.MotionVectorsOverPicBoundaries, err = r.ReadFlag(); err != nil {
			return err
		}
		for _, p := range []*uint32{&v.MaxBytesPerPicDenom, &v.MaxBitsPerMbDenom, &v.Log2MaxMvLengthHorizontal, &v.Log2MaxMvLengthVertical, &v.MaxNumReorderFrames, &v.MaxDecFrameBuffering} {
			if *p, err = r.ReadUE(); err != nil {
				return err
			}
		}
	}
	return nil
}
