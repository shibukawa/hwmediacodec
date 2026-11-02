package sys

// AV1 encode attributes (va.h).
const (
	ConfigAttribEncAV1     int32 = 52
	ConfigAttribEncAV1Ext1 int32 = 53
	ConfigAttribEncAV1Ext2 int32 = 54
)

// Fields of VAConfigAttribValEncAV1Ext2.
const (
	EncAV1Ext2TileSizeBytesMinus1Mask uint32 = 0x3
	EncAV1Ext2TxModeSupportShift             = 4
	EncAV1Ext2TxModeSupportMask       uint32 = 0x7
	EncAV1Ext2MaxTileNumMinus1Shift          = 7
	EncAV1Ext2MaxTileNumMinus1Mask    uint32 = 0x1fff

	// Bits of tx_mode_support.
	EncAV1TxModeSupportLargest uint32 = 0x2
	EncAV1TxModeSupportSelect  uint32 = 0x4
)

// Bits of EncSequenceParameterBufferAV1.SeqFields.
const (
	AV1EncSeqEnableOrderHint uint32 = 1 << 8
	AV1EncSeqEnableCDEF      uint32 = 1 << 12
	AV1EncSeqSubsamplingX    uint32 = 1 << 17
	AV1EncSeqSubsamplingY    uint32 = 1 << 18
)

// EncSequenceParameterBufferAV1 mirrors VAEncSequenceParameterBufferAV1.
type EncSequenceParameterBufferAV1 struct {
	SeqProfile       uint8
	SeqLevelIdx      uint8
	SeqTier          uint8
	HierarchicalFlag uint8
	IntraPeriod      uint32
	IPPeriod         uint32
	BitsPerSecond    uint32
	// SeqFields packs still_picture (bit 0) to mono_chrome (19); see the
	// AV1EncSeq* constants.
	SeqFields           uint32
	OrderHintBitsMinus1 uint8
	_                   [3]uint8
	_                   [16]uint32
}

// EncSegParamAV1 mirrors VAEncSegParamAV1.
type EncSegParamAV1 struct {
	SegFlags      uint8
	SegmentNumber uint8
	FeatureData   [8][8]int16
	FeatureMask   [8]uint8
	_             [2]uint8
	_             [4]uint32
}

// EncWarpedMotionParamsAV1 mirrors VAEncWarpedMotionParamsAV1.
type EncWarpedMotionParamsAV1 struct {
	WMType  uint32
	WMMat   [8]int32
	Invalid uint8
	_       [3]uint8
	_       [4]uint32
}

// Fields of EncPictureParameterBufferAV1.PictureFlags.
const (
	AV1EncPicFrameTypeMask         uint32 = 0x3
	AV1EncPicErrorResilientMode    uint32 = 1 << 2
	AV1EncPicAllowHighPrecisionMV  uint32 = 1 << 5
	AV1EncPicEnableFrameOBU        uint32 = 1 << 9
	AV1EncPicDisableFrameRecon     uint32 = 1 << 11
	AV1EncPicAllowScreenContent    uint32 = 1 << 14
	AV1EncModeControlTxModeShift          = 7
	AV1EncModeControlRefModeShift         = 9
	AV1EncTileGroupOBUHasSizeField uint8  = 1 << 1
)

// EncPictureParameterBufferAV1 mirrors VAEncPictureParameterBufferAV1.
type EncPictureParameterBufferAV1 struct {
	FrameWidthMinus1      uint16
	FrameHeightMinus1     uint16
	ReconstructedFrame    uint32
	CodedBuf              uint32
	ReferenceFrames       [8]uint32
	RefFrameIdx           [7]uint8
	HierarchicalLevelPlus uint8
	PrimaryRefFrame       uint8
	OrderHint             uint8
	RefreshFrameFlags     uint8
	_                     uint8
	// RefFrameCtrlL0 and L1 hold the reference frames the encoder may
	// search, three bits per entry (1 LAST to 7 ALTREF, 0 for none).
	RefFrameCtrlL0 uint32
	RefFrameCtrlL1 uint32
	// PictureFlags packs frame_type (bits 0-1) to force_integer_mv (15);
	// see the AV1EncPic* constants.
	PictureFlags             uint32
	SegIDBlockSize           uint8
	NumTileGroupsMinus1      uint8
	TemporalID               uint8
	FilterLevel              [2]uint8
	FilterLevelU             uint8
	FilterLevelV             uint8
	LoopFilterFlags          uint8
	SuperresScaleDenominator uint8
	InterpolationFilter      uint8
	RefDeltas                [8]int8
	ModeDeltas               [2]int8
	BaseQIndex               uint8
	YDcDeltaQ                int8
	UDcDeltaQ                int8
	UAcDeltaQ                int8
	VDcDeltaQ                int8
	VAcDeltaQ                int8
	MinBaseQIndex            uint8
	MaxBaseQIndex            uint8
	QMatrixFlags             uint16
	_                        uint16
	// ModeControlFlags packs delta_q_present (bit 0) to skip_mode_present
	// (11); tx_mode sits at bit 7 and reference_mode at bit 9.
	ModeControlFlags     uint32
	Segments             EncSegParamAV1
	TileCols             uint8
	TileRows             uint8
	_                    uint16
	WidthInSbsMinus1     [63]uint16
	HeightInSbsMinus1    [63]uint16
	ContextUpdateTileID  uint16
	CDEFDampingMinus3    uint8
	CDEFBits             uint8
	CDEFYStrengths       [8]uint8
	CDEFUVStrengths      [8]uint8
	LoopRestorationFlags uint16
	_                    uint16
	WM                   [7]EncWarpedMotionParamsAV1
	// Bit positions inside the packed frame header (counted from the
	// start of the OBU) of fields the driver rewrites under bitrate
	// control.
	BitOffsetQIndex           uint32
	BitOffsetSegmentation     uint32
	BitOffsetLoopfilterParams uint32
	BitOffsetCDEFParams       uint32
	SizeInBitsCDEFParams      uint32
	ByteOffsetFrameHdrOBUSize uint32
	SizeInBitsFrameHdrOBU     uint32
	TileGroupOBUHdrInfo       uint8
	NumberSkipFrames          uint8
	_                         uint16
	SkipFramesReducedSize     int32
	_                         [16]uint32
}

// EncTileGroupBufferAV1 mirrors VAEncTileGroupBufferAV1; it is passed as the
// slice parameter buffer.
type EncTileGroupBufferAV1 struct {
	TgStart uint8
	TgEnd   uint8
	_       [2]uint8
	_       [4]uint32
}
