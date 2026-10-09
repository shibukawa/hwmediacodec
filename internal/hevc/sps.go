package hevc

// SPS is a sequence parameter set (7.3.2.2). ParseSPSHead fills the fields
// up to and including the sub-layer ordering information; ParseSPS fills
// everything.
type SPS struct {
	ID                  uint32
	VPSID               uint32
	MaxSubLayersMinus1  int
	TemporalIDNesting   bool
	PTL                 ProfileTierLevel
	ChromaFormatIDC     uint32
	SeparateColourPlane bool
	// Width and Height are pic_width_in_luma_samples and
	// pic_height_in_luma_samples (the coded size).
	Width             int
	Height            int
	ConformanceWindow bool
	ConfWinLeft       uint32
	ConfWinRight      uint32
	ConfWinTop        uint32
	ConfWinBottom     uint32
	BitDepthLuma      uint32
	BitDepthChroma    uint32
	Log2MaxPOCLsb     int
	// Per sub-layer, index 0 .. MaxSubLayersMinus1 (lower entries are
	// inferred from the highest when the stream omits them).
	SubLayerOrderingInfoPresent bool
	MaxDecPicBufferingMinus1    []uint32
	MaxNumReorderPics           []uint32
	MaxLatencyIncreasePlus1     []uint32

	Log2MinLumaCodingBlockSizeMinus3     uint32
	Log2DiffMaxMinLumaCodingBlockSize    uint32
	Log2MinLumaTransformBlockSizeMinus2  uint32
	Log2DiffMaxMinLumaTransformBlockSize uint32
	MaxTransformHierarchyDepthInter      uint32
	MaxTransformHierarchyDepthIntra      uint32

	ScalingListEnabled     bool
	ScalingListDataPresent bool
	// ScalingList holds the lists of this SPS when ScalingListEnabled is
	// set: the coded ones, or the defaults when no data is present.
	ScalingList ScalingList

	AMPEnabled bool
	SAOEnabled bool
	PCMEnabled bool
	// PCM fields, valid when PCMEnabled is set.
	PCMSampleBitDepthLumaMinus1          uint32
	PCMSampleBitDepthChromaMinus1        uint32
	Log2MinPCMLumaCodingBlockSizeMinus3  uint32
	Log2DiffMaxMinPCMLumaCodingBlockSize uint32
	PCMLoopFilterDisabled                bool

	ShortTermRPS []ShortTermRPS

	LongTermRefPicsPresent bool
	LtRefPicPOCLsbSPS      []uint32
	UsedByCurrPicLtSPS     []bool

	TemporalMVPEnabled          bool
	StrongIntraSmoothingEnabled bool

	VUIPresent bool
	VUI        VUI

	ExtensionPresent    bool
	RangeExtension      bool
	MultilayerExtension bool
	Extension3D         bool
	SCCExtension        bool
	Extension4Bits      uint8
	Range               SPSRangeExtension
}

// SPSRangeExtension is sps_range_extension() (7.3.2.2.2).
type SPSRangeExtension struct {
	TransformSkipRotationEnabled    bool
	TransformSkipContextEnabled     bool
	ImplicitRDPCMEnabled            bool
	ExplicitRDPCMEnabled            bool
	ExtendedPrecisionProcessing     bool
	IntraSmoothingDisabled          bool
	HighPrecisionOffsetsEnabled     bool
	PersistentRiceAdaptationEnabled bool
	CabacBypassAlignmentEnabled     bool
}

// VUI is vui_parameters() (E.2.1). HRD parameters are parsed and dropped.
type VUI struct {
	AspectRatioInfoPresent         bool
	AspectRatioIDC                 uint8
	SarWidth                       uint16
	SarHeight                      uint16
	OverscanInfoPresent            bool
	OverscanAppropriate            bool
	VideoSignalTypePresent         bool
	VideoFormat                    uint8
	VideoFullRange                 bool
	ColourDescriptionPresent       bool
	ColourPrimaries                uint8
	TransferCharacteristics        uint8
	MatrixCoeffs                   uint8
	ChromaLocInfoPresent           bool
	ChromaSampleLocTypeTopField    uint32
	ChromaSampleLocTypeBottomField uint32
	NeutralChromaIndication        bool
	FieldSeq                       bool
	FrameFieldInfoPresent          bool
	DefaultDisplayWindow           bool
	DefDispWinLeft                 uint32
	DefDispWinRight                uint32
	DefDispWinTop                  uint32
	DefDispWinBottom               uint32
	TimingInfoPresent              bool
	NumUnitsInTick                 uint32
	TimeScale                      uint32
	POCProportionalToTiming        bool
	NumTicksPOCDiffOneMinus1       uint32
	HRDParametersPresent           bool
	BitstreamRestriction           bool
	TilesFixedStructure            bool
	MotionVectorsOverPicBoundaries bool
	RestrictedRefPicLists          bool
	MinSpatialSegmentationIDC      uint32
	MaxBytesPerPicDenom            uint32
	MaxBitsPerMinCuDenom           uint32
	Log2MaxMvLengthHorizontal      uint32
	Log2MaxMvLengthVertical        uint32
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

// MaxDecPicBuffering returns sps_max_dec_pic_buffering_minus1 + 1 for the
// highest temporal sub-layer: the number of pictures the decoded picture
// buffer holds, the current one included.
func (s *SPS) MaxDecPicBuffering() int {
	return int(s.MaxDecPicBufferingMinus1[len(s.MaxDecPicBufferingMinus1)-1]) + 1
}

// MinCbLog2Size is MinCbLog2SizeY.
func (s *SPS) MinCbLog2Size() int { return int(s.Log2MinLumaCodingBlockSizeMinus3) + 3 }

// CtbLog2Size is CtbLog2SizeY.
func (s *SPS) CtbLog2Size() int {
	return s.MinCbLog2Size() + int(s.Log2DiffMaxMinLumaCodingBlockSize)
}

// PicWidthInCtbs is PicWidthInCtbsY.
func (s *SPS) PicWidthInCtbs() int {
	ctb := 1 << uint(s.CtbLog2Size())
	return (s.Width + ctb - 1) / ctb
}

// PicHeightInCtbs is PicHeightInCtbsY.
func (s *SPS) PicHeightInCtbs() int {
	ctb := 1 << uint(s.CtbLog2Size())
	return (s.Height + ctb - 1) / ctb
}

// PicSizeInCtbs is PicSizeInCtbsY.
func (s *SPS) PicSizeInCtbs() int { return s.PicWidthInCtbs() * s.PicHeightInCtbs() }

// chromaSubsampling returns SubWidthC and SubHeightC.
func (s *SPS) chromaSubsampling() (int, int) {
	if s.SeparateColourPlane {
		return 1, 1
	}
	switch s.ChromaFormatIDC {
	case 1:
		return 2, 2
	case 2:
		return 2, 1
	}
	return 1, 1
}

// Crop returns the conformance window in luma samples: the offset of its
// top-left corner inside the coded picture and its size.
func (s *SPS) Crop() (x, y, width, height int) {
	if !s.ConformanceWindow {
		return 0, 0, s.Width, s.Height
	}
	sw, sh := s.chromaSubsampling()
	x = sw * int(s.ConfWinLeft)
	y = sh * int(s.ConfWinTop)
	width = s.Width - x - sw*int(s.ConfWinRight)
	height = s.Height - y - sh*int(s.ConfWinBottom)
	return x, y, width, height
}

const (
	maxSPSID = 15
	maxPPSID = 63
)

// ParseSPSHead parses the leading part of a sequence parameter set NAL unit
// (with its two-byte header, without start code): everything up to and
// including the sub-layer ordering information.
func ParseSPSHead(nal []byte) (*SPS, error) {
	r, err := spsReader(nal)
	if err != nil {
		return nil, err
	}
	s := &SPS{}
	r.spsHead(s)
	if r.err != nil {
		return nil, r.err
	}
	return s, nil
}

// ParseSPS parses a complete sequence parameter set NAL unit.
func ParseSPS(nal []byte) (*SPS, error) {
	r, err := spsReader(nal)
	if err != nil {
		return nil, err
	}
	s := &SPS{}
	r.spsHead(s)
	r.spsBody(s)
	if r.err != nil {
		return nil, r.err
	}
	return s, nil
}

func spsReader(nal []byte) (*reader, error) {
	if Type(nal) != NALSPS || len(nal) < 4 {
		return nil, syntaxErr("not an SPS NAL unit")
	}
	return newReader(nal[2:]), nil
}

func (r *reader) spsHead(s *SPS) {
	s.VPSID = r.u(4)
	s.MaxSubLayersMinus1 = int(r.u(3))
	if r.err == nil && s.MaxSubLayersMinus1 > 6 {
		r.fail("sps_max_sub_layers_minus1 %d out of range", s.MaxSubLayersMinus1)
		return
	}
	s.TemporalIDNesting = r.flag()
	s.PTL = r.profileTierLevel(s.MaxSubLayersMinus1)
	s.ID = r.ueMax("sps_seq_parameter_set_id", maxSPSID)
	s.ChromaFormatIDC = r.ueMax("chroma_format_idc", 3)
	if s.ChromaFormatIDC == 3 {
		s.SeparateColourPlane = r.flag()
	}
	s.Width = int(r.ueMax("pic_width_in_luma_samples", 1<<16))
	s.Height = int(r.ueMax("pic_height_in_luma_samples", 1<<16))
	s.ConformanceWindow = r.flag()
	if s.ConformanceWindow {
		s.ConfWinLeft = r.ue()
		s.ConfWinRight = r.ue()
		s.ConfWinTop = r.ue()
		s.ConfWinBottom = r.ue()
	}
	s.BitDepthLuma = r.ueMax("bit_depth_luma_minus8", 8) + 8
	s.BitDepthChroma = r.ueMax("bit_depth_chroma_minus8", 8) + 8
	s.Log2MaxPOCLsb = int(r.ueMax("log2_max_pic_order_cnt_lsb_minus4", 12)) + 4
	s.SubLayerOrderingInfoPresent = r.flag()
	if r.err != nil {
		return
	}
	n := s.MaxSubLayersMinus1 + 1
	s.MaxDecPicBufferingMinus1 = make([]uint32, n)
	s.MaxNumReorderPics = make([]uint32, n)
	s.MaxLatencyIncreasePlus1 = make([]uint32, n)
	start := s.MaxSubLayersMinus1
	if s.SubLayerOrderingInfoPresent {
		start = 0
	}
	for i := start; i <= s.MaxSubLayersMinus1; i++ {
		s.MaxDecPicBufferingMinus1[i] = r.ue()
		s.MaxNumReorderPics[i] = r.ue()
		s.MaxLatencyIncreasePlus1[i] = r.ue()
	}
	for i := 0; i < start; i++ {
		s.MaxDecPicBufferingMinus1[i] = s.MaxDecPicBufferingMinus1[start]
		s.MaxNumReorderPics[i] = s.MaxNumReorderPics[start]
		s.MaxLatencyIncreasePlus1[i] = s.MaxLatencyIncreasePlus1[start]
	}
}

func (r *reader) spsBody(s *SPS) {
	if r.err != nil {
		return
	}
	cx, cy, cw, ch := s.Crop()
	if s.Width == 0 || s.Height == 0 || cx < 0 || cy < 0 || cw <= 0 || ch <= 0 {
		r.fail("picture size %dx%d with an invalid conformance window", s.Width, s.Height)
		return
	}
	for _, v := range s.MaxDecPicBufferingMinus1 {
		if v > 15 {
			r.fail("sps_max_dec_pic_buffering_minus1 %d out of range", v)
			return
		}
	}
	s.Log2MinLumaCodingBlockSizeMinus3 = r.ueMax("log2_min_luma_coding_block_size_minus3", 3)
	s.Log2DiffMaxMinLumaCodingBlockSize = r.ueMax("log2_diff_max_min_luma_coding_block_size", 3)
	s.Log2MinLumaTransformBlockSizeMinus2 = r.ueMax("log2_min_luma_transform_block_size_minus2", 3)
	s.Log2DiffMaxMinLumaTransformBlockSize = r.ueMax("log2_diff_max_min_luma_transform_block_size", 3)
	s.MaxTransformHierarchyDepthInter = r.ueMax("max_transform_hierarchy_depth_inter", 4)
	s.MaxTransformHierarchyDepthIntra = r.ueMax("max_transform_hierarchy_depth_intra", 4)
	if r.err != nil {
		return
	}
	if s.CtbLog2Size() < 4 || s.CtbLog2Size() > 6 {
		r.fail("coding tree block size 2^%d out of range", s.CtbLog2Size())
		return
	}
	if minCb := 1 << uint(s.MinCbLog2Size()); s.Width%minCb != 0 || s.Height%minCb != 0 {
		r.fail("picture size %dx%d is not a multiple of the minimum coding block size %d", s.Width, s.Height, minCb)
		return
	}

	s.ScalingListEnabled = r.flag()
	if s.ScalingListEnabled {
		s.ScalingList = DefaultScalingList()
		s.ScalingListDataPresent = r.flag()
		if s.ScalingListDataPresent {
			r.scalingListData(&s.ScalingList)
		}
	}
	s.AMPEnabled = r.flag()
	s.SAOEnabled = r.flag()
	s.PCMEnabled = r.flag()
	if s.PCMEnabled {
		s.PCMSampleBitDepthLumaMinus1 = r.u(4)
		s.PCMSampleBitDepthChromaMinus1 = r.u(4)
		s.Log2MinPCMLumaCodingBlockSizeMinus3 = r.ueMax("log2_min_pcm_luma_coding_block_size_minus3", 2)
		s.Log2DiffMaxMinPCMLumaCodingBlockSize = r.ueMax("log2_diff_max_min_pcm_luma_coding_block_size", 2)
		s.PCMLoopFilterDisabled = r.flag()
	}

	numRPS := int(r.ueMax("num_short_term_ref_pic_sets", 64))
	if r.err != nil {
		return
	}
	s.ShortTermRPS = make([]ShortTermRPS, 0, numRPS)
	for i := 0; i < numRPS; i++ {
		rps := r.shortTermRPS(i, s.ShortTermRPS, false)
		if r.err != nil {
			return
		}
		s.ShortTermRPS = append(s.ShortTermRPS, rps)
	}

	s.LongTermRefPicsPresent = r.flag()
	if s.LongTermRefPicsPresent {
		n := int(r.ueMax("num_long_term_ref_pics_sps", 32))
		if r.err != nil {
			return
		}
		s.LtRefPicPOCLsbSPS = make([]uint32, n)
		s.UsedByCurrPicLtSPS = make([]bool, n)
		for i := 0; i < n; i++ {
			s.LtRefPicPOCLsbSPS[i] = r.u(s.Log2MaxPOCLsb)
			s.UsedByCurrPicLtSPS[i] = r.flag()
		}
	}
	s.TemporalMVPEnabled = r.flag()
	s.StrongIntraSmoothingEnabled = r.flag()
	s.VUIPresent = r.flag()
	if s.VUIPresent {
		r.vui(&s.VUI, s.MaxSubLayersMinus1)
	}
	s.ExtensionPresent = r.flag()
	if s.ExtensionPresent {
		s.RangeExtension = r.flag()
		s.MultilayerExtension = r.flag()
		s.Extension3D = r.flag()
		s.SCCExtension = r.flag()
		s.Extension4Bits = uint8(r.u(4))
	}
	if s.RangeExtension {
		e := &s.Range
		e.TransformSkipRotationEnabled = r.flag()
		e.TransformSkipContextEnabled = r.flag()
		e.ImplicitRDPCMEnabled = r.flag()
		e.ExplicitRDPCMEnabled = r.flag()
		e.ExtendedPrecisionProcessing = r.flag()
		e.IntraSmoothingDisabled = r.flag()
		e.HighPrecisionOffsetsEnabled = r.flag()
		e.PersistentRiceAdaptationEnabled = r.flag()
		e.CabacBypassAlignmentEnabled = r.flag()
	}
	// The multilayer, 3D and screen content extensions are not modelled;
	// nothing this package derives depends on what follows.
}

func (r *reader) vui(v *VUI, maxSubLayersMinus1 int) {
	v.AspectRatioInfoPresent = r.flag()
	if v.AspectRatioInfoPresent {
		v.AspectRatioIDC = uint8(r.u(8))
		if v.AspectRatioIDC == 255 {
			v.SarWidth = uint16(r.u(16))
			v.SarHeight = uint16(r.u(16))
		}
	}
	v.OverscanInfoPresent = r.flag()
	if v.OverscanInfoPresent {
		v.OverscanAppropriate = r.flag()
	}
	v.VideoSignalTypePresent = r.flag()
	if v.VideoSignalTypePresent {
		v.VideoFormat = uint8(r.u(3))
		v.VideoFullRange = r.flag()
		v.ColourDescriptionPresent = r.flag()
		if v.ColourDescriptionPresent {
			v.ColourPrimaries = uint8(r.u(8))
			v.TransferCharacteristics = uint8(r.u(8))
			v.MatrixCoeffs = uint8(r.u(8))
		}
	}
	v.ChromaLocInfoPresent = r.flag()
	if v.ChromaLocInfoPresent {
		v.ChromaSampleLocTypeTopField = r.ue()
		v.ChromaSampleLocTypeBottomField = r.ue()
	}
	v.NeutralChromaIndication = r.flag()
	v.FieldSeq = r.flag()
	v.FrameFieldInfoPresent = r.flag()
	v.DefaultDisplayWindow = r.flag()
	if v.DefaultDisplayWindow {
		v.DefDispWinLeft = r.ue()
		v.DefDispWinRight = r.ue()
		v.DefDispWinTop = r.ue()
		v.DefDispWinBottom = r.ue()
	}
	v.TimingInfoPresent = r.flag()
	if v.TimingInfoPresent {
		v.NumUnitsInTick = r.u(32)
		v.TimeScale = r.u(32)
		v.POCProportionalToTiming = r.flag()
		if v.POCProportionalToTiming {
			v.NumTicksPOCDiffOneMinus1 = r.ue()
		}
		v.HRDParametersPresent = r.flag()
		if v.HRDParametersPresent {
			r.skipHRD(true, maxSubLayersMinus1)
		}
	}
	v.BitstreamRestriction = r.flag()
	if v.BitstreamRestriction {
		v.TilesFixedStructure = r.flag()
		v.MotionVectorsOverPicBoundaries = r.flag()
		v.RestrictedRefPicLists = r.flag()
		v.MinSpatialSegmentationIDC = r.ue()
		v.MaxBytesPerPicDenom = r.ue()
		v.MaxBitsPerMinCuDenom = r.ue()
		v.Log2MaxMvLengthHorizontal = r.ue()
		v.Log2MaxMvLengthVertical = r.ue()
	}
}

// skipHRD reads hrd_parameters(commonInfPresent, maxNumSubLayersMinus1)
// (E.2.2) without keeping it.
func (r *reader) skipHRD(commonInfPresent bool, maxSubLayersMinus1 int) {
	var nal, vcl, subPic bool
	if commonInfPresent {
		nal = r.flag()
		vcl = r.flag()
		if nal || vcl {
			subPic = r.flag()
			if subPic {
				r.skip(8 + 5 + 1 + 5)
			}
			r.skip(4 + 4) // bit_rate_scale, cpb_size_scale
			if subPic {
				r.skip(4) // cpb_size_du_scale
			}
			r.skip(5 + 5 + 5)
		}
	}
	for i := 0; i <= maxSubLayersMinus1 && r.err == nil; i++ {
		fixedGeneral := r.flag()
		fixedWithinCVS := true
		if !fixedGeneral {
			fixedWithinCVS = r.flag()
		}
		lowDelay := false
		if fixedWithinCVS {
			r.ue() // elemental_duration_in_tc_minus1
		} else {
			lowDelay = r.flag()
		}
		cpbCnt := 1
		if !lowDelay {
			cpbCnt = int(r.ueMax("cpb_cnt_minus1", 31)) + 1
		}
		for _, present := range []bool{nal, vcl} {
			if !present {
				continue
			}
			for j := 0; j < cpbCnt && r.err == nil; j++ {
				r.ue() // bit_rate_value_minus1
				r.ue() // cpb_size_value_minus1
				if subPic {
					r.ue() // cpb_size_du_value_minus1
					r.ue() // bit_rate_du_value_minus1
				}
				r.skip(1) // cbr_flag
			}
		}
	}
}
