package sys

// Encode entry points and attributes.
const (
	EntrypointEncSlice   int32 = 6
	EntrypointEncSliceLP int32 = 8

	ConfigAttribRateControl      int32 = 5
	ConfigAttribEncPackedHeaders int32 = 10
	ConfigAttribEncMaxRefFrames  int32 = 13
	ConfigAttribEncMaxSlices     int32 = 14
	ConfigAttribEncQualityRange  int32 = 21
)

// VA_RC_* rate control modes (bit mask values of VAConfigAttribRateControl).
const (
	RCNone uint32 = 0x01
	RCCBR  uint32 = 0x02
	RCVBR  uint32 = 0x04
	RCCQP  uint32 = 0x10
	RCICQ  uint32 = 0x40
	RCQVBR uint32 = 0x400
)

// VA_ENC_PACKED_HEADER_* flags.
const (
	EncPackedHeaderSequence uint32 = 0x1
	EncPackedHeaderPicture  uint32 = 0x2
	EncPackedHeaderSlice    uint32 = 0x4
	EncPackedHeaderMisc     uint32 = 0x8
)

// Encode buffer types.
const (
	EncCodedBufferType                 int32 = 21
	EncSequenceParameterBufferType     int32 = 22
	EncPictureParameterBufferType      int32 = 23
	EncSliceParameterBufferType        int32 = 24
	EncPackedHeaderParameterBufferType int32 = 25
	EncPackedHeaderDataBufferType      int32 = 26
	EncMiscParameterBufferType         int32 = 27
)

// VAEncMiscParameterType values.
const (
	EncMiscParameterTypeFrameRate    int32 = 0
	EncMiscParameterTypeRateControl  int32 = 1
	EncMiscParameterTypeHRD          int32 = 5
	EncMiscParameterTypeQualityLevel int32 = 6
)

// VAEncPackedHeaderType values.
const (
	EncPackedHeaderTypeSequence uint32 = 1
	EncPackedHeaderTypePicture  uint32 = 2
	EncPackedHeaderTypeSlice    uint32 = 3
)

// Coded buffer status bits.
const (
	CodedBufStatusSliceOverflow uint32 = 0x200
	CodedBufStatusBadBitstream  uint32 = 0x8000
)

// TimeoutInfinite is VA_TIMEOUT_INFINITE for vaSyncBuffer.
const TimeoutInfinite uint64 = ^uint64(0)

// EncSequenceParameterBufferH264 mirrors VAEncSequenceParameterBufferH264.
type EncSequenceParameterBufferH264 struct {
	SeqParameterSetID              uint8
	LevelIDC                       uint8
	_                              uint16
	IntraPeriod                    uint32
	IntraIDRPeriod                 uint32
	IPPeriod                       uint32
	BitsPerSecond                  uint32
	MaxNumRefFrames                uint32
	PictureWidthInMbs              uint16
	PictureHeightInMbs             uint16
	SeqFields                      uint32
	BitDepthLumaMinus8             uint8
	BitDepthChromaMinus8           uint8
	NumRefFramesInPicOrderCntCycle uint8
	_                              uint8
	OffsetForNonRefPic             int32
	OffsetForTopToBottomField      int32
	OffsetForRefFrame              [256]int32
	FrameCroppingFlag              uint8
	_                              [3]uint8
	FrameCropLeftOffset            uint32
	FrameCropRightOffset           uint32
	FrameCropTopOffset             uint32
	FrameCropBottomOffset          uint32
	VUIParametersPresentFlag       uint8
	_                              [3]uint8
	VUIFields                      uint32
	AspectRatioIDC                 uint8
	_                              [3]uint8
	SarWidth                       uint32
	SarHeight                      uint32
	NumUnitsInTick                 uint32
	TimeScale                      uint32
	_                              [4]uint32
}

// Bit positions in EncSequenceParameterBufferH264.SeqFields.
const (
	EncSeqChromaFormatIDCShift             = 0 // 2 bits
	EncSeqFrameMbsOnlyFlag                 = 1 << 2
	EncSeqMbAdaptiveFrameFieldFlag         = 1 << 3
	EncSeqScalingMatrixPresentFlag         = 1 << 4
	EncSeqDirect8x8InferenceFlag           = 1 << 5
	EncSeqLog2MaxFrameNumMinus4Shift       = 6  // 4 bits
	EncSeqPicOrderCntTypeShift             = 10 // 2 bits
	EncSeqLog2MaxPicOrderCntLsbMinus4Shift = 12 // 4 bits
	EncSeqDeltaPicOrderAlwaysZeroFlag      = 1 << 16
)

// Bit positions in EncSequenceParameterBufferH264.VUIFields.
const (
	EncVUIAspectRatioInfoPresentFlag         = 1 << 0
	EncVUITimingInfoPresentFlag              = 1 << 1
	EncVUIBitstreamRestrictionFlag           = 1 << 2
	EncVUILog2MaxMvLengthHorizontalShift     = 3 // 5 bits
	EncVUILog2MaxMvLengthVerticalShift       = 8 // 5 bits
	EncVUIFixedFrameRateFlag                 = 1 << 13
	EncVUILowDelayHRDFlag                    = 1 << 14
	EncVUIMotionVectorsOverPicBoundariesFlag = 1 << 15
)

// EncPictureParameterBufferH264 mirrors VAEncPictureParameterBufferH264.
type EncPictureParameterBufferH264 struct {
	CurrPic                   PictureH264
	ReferenceFrames           [16]PictureH264
	CodedBuf                  uint32
	PicParameterSetID         uint8
	SeqParameterSetID         uint8
	LastPicture               uint8
	_                         uint8
	FrameNum                  uint16
	PicInitQp                 uint8
	NumRefIdxL0ActiveMinus1   uint8
	NumRefIdxL1ActiveMinus1   uint8
	ChromaQpIndexOffset       int8
	SecondChromaQpIndexOffset int8
	_                         uint8
	PicFields                 uint32
	_                         [4]uint32
}

// Bit positions in EncPictureParameterBufferH264.PicFields.
const (
	EncPicIdrPicFlag                         = 1 << 0
	EncPicReferencePicFlagShift              = 1 // 2 bits
	EncPicEntropyCodingModeFlag              = 1 << 3
	EncPicWeightedPredFlag                   = 1 << 4
	EncPicWeightedBipredIdcShift             = 5 // 2 bits
	EncPicConstrainedIntraPredFlag           = 1 << 7
	EncPicTransform8x8ModeFlag               = 1 << 8
	EncPicDeblockingFilterControlPresentFlag = 1 << 9
	EncPicRedundantPicCntPresentFlag         = 1 << 10
	EncPicPicOrderPresentFlag                = 1 << 11
	EncPicPicScalingMatrixPresentFlag        = 1 << 12
)

// EncSliceParameterBufferH264 mirrors VAEncSliceParameterBufferH264.
type EncSliceParameterBufferH264 struct {
	MacroblockAddress           uint32
	NumMacroblocks              uint32
	MacroblockInfo              uint32
	SliceType                   uint8
	PicParameterSetID           uint8
	IdrPicID                    uint16
	PicOrderCntLsb              uint16
	_                           uint16
	DeltaPicOrderCntBottom      int32
	DeltaPicOrderCnt            [2]int32
	DirectSpatialMvPredFlag     uint8
	NumRefIdxActiveOverrideFlag uint8
	NumRefIdxL0ActiveMinus1     uint8
	NumRefIdxL1ActiveMinus1     uint8
	RefPicList0                 [32]PictureH264
	RefPicList1                 [32]PictureH264
	LumaLog2WeightDenom         uint8
	ChromaLog2WeightDenom       uint8
	LumaWeightL0Flag            uint8
	_                           uint8
	LumaWeightL0                [32]int16
	LumaOffsetL0                [32]int16
	ChromaWeightL0Flag          uint8
	_                           uint8
	ChromaWeightL0              [32][2]int16
	ChromaOffsetL0              [32][2]int16
	LumaWeightL1Flag            uint8
	_                           uint8
	LumaWeightL1                [32]int16
	LumaOffsetL1                [32]int16
	ChromaWeightL1Flag          uint8
	_                           uint8
	ChromaWeightL1              [32][2]int16
	ChromaOffsetL1              [32][2]int16
	CabacInitIdc                uint8
	SliceQpDelta                int8
	DisableDeblockingFilterIdc  uint8
	SliceAlphaC0OffsetDiv2      int8
	SliceBetaOffsetDiv2         int8
	_                           uint8
	_                           [4]uint32
}

// The misc parameter buffers below include the leading VAEncMiscParameterType
// of VAEncMiscParameterBuffer so that one struct is handed to vaCreateBuffer.

// EncMiscParameterRateControl is VAEncMiscParameterBuffer +
// VAEncMiscParameterRateControl.
type EncMiscParameterRateControl struct {
	Type             int32
	BitsPerSecond    uint32
	TargetPercentage uint32
	WindowSize       uint32
	InitialQp        uint32
	MinQp            uint32
	BasicUnitSize    uint32
	RcFlags          uint32
	ICQQualityFactor uint32
	MaxQp            uint32
	QualityFactor    uint32
	TargetFrameSize  uint32
	_                [4]uint32
}

// Bits of EncMiscParameterRateControl.RcFlags.
const (
	RcFlagReset              uint32 = 1 << 0
	RcFlagDisableFrameSkip   uint32 = 1 << 1
	RcFlagDisableBitStuffing uint32 = 1 << 2
)

// EncMiscParameterFrameRate is VAEncMiscParameterBuffer +
// VAEncMiscParameterFrameRate. Framerate holds numerator | denominator<<16.
type EncMiscParameterFrameRate struct {
	Type      int32
	Framerate uint32
	Flags     uint32
	_         [4]uint32
}

// EncMiscParameterHRD is VAEncMiscParameterBuffer + VAEncMiscParameterHRD.
type EncMiscParameterHRD struct {
	Type                  int32
	InitialBufferFullness uint32
	BufferSize            uint32
	_                     [4]uint32
}

// EncMiscParameterQualityLevel is VAEncMiscParameterBuffer +
// VAEncMiscParameterBufferQualityLevel.
type EncMiscParameterQualityLevel struct {
	Type         int32
	QualityLevel uint32
	_            [4]uint32
}

// EncPackedHeaderParameterBuffer mirrors VAEncPackedHeaderParameterBuffer.
type EncPackedHeaderParameterBuffer struct {
	Type              uint32
	BitLength         uint32
	HasEmulationBytes uint8
	_                 [3]uint8
	_                 [4]uint32
}

// CodedBufferSegment mirrors VACodedBufferSegment (64-bit layout).
type CodedBufferSegment struct {
	Size      uint32
	BitOffset uint32
	Status    uint32
	Reserved  uint32
	Buf       *byte
	Next      *CodedBufferSegment
	_         [4]uint32
}
