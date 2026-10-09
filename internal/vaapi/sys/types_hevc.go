package sys

// HEVC additions to the configuration constants.
const (
	// RTFormatYUV420_10 is VA_RT_FORMAT_YUV420_10 (10-bit 4:2:0 surfaces).
	RTFormatYUV420_10 uint32 = 0x00000100

	ConfigAttribPredictionDirection int32 = 39
	ConfigAttribEncHEVCFeatures     int32 = 50
	ConfigAttribEncHEVCBlockSizes   int32 = 51
)

// VA_PREDICTION_DIRECTION_* bits of VAConfigAttribPredictionDirection.
const (
	PredictionDirectionPrevious   uint32 = 0x1
	PredictionDirectionFuture     uint32 = 0x2
	PredictionDirectionBiNotEmpty uint32 = 0x4
)

// VAPictureHEVC flags.
const (
	PictureHEVCInvalid           uint32 = 0x01
	PictureHEVCFieldPic          uint32 = 0x02
	PictureHEVCBottomField       uint32 = 0x04
	PictureHEVCLongTermReference uint32 = 0x08
	PictureHEVCRPSStCurrBefore   uint32 = 0x10
	PictureHEVCRPSStCurrAfter    uint32 = 0x20
	PictureHEVCRPSLtCurr         uint32 = 0x40
)

// PictureHEVC mirrors VAPictureHEVC.
type PictureHEVC struct {
	PictureID   uint32
	PicOrderCnt int32
	Flags       uint32
	_           [4]uint32
}

// InvalidPictureHEVC is the "empty slot" value for reference arrays.
var InvalidPictureHEVC = PictureHEVC{PictureID: InvalidSurface, Flags: PictureHEVCInvalid}

// PictureParameterBufferHEVC mirrors VAPictureParameterBufferHEVC.
// PicFields and SliceParsingFields hold the C bit-fields packed
// least-significant bit first.
type PictureParameterBufferHEVC struct {
	CurrPic                              PictureHEVC
	ReferenceFrames                      [15]PictureHEVC
	PicWidthInLumaSamples                uint16
	PicHeightInLumaSamples               uint16
	PicFields                            uint32
	SpsMaxDecPicBufferingMinus1          uint8
	BitDepthLumaMinus8                   uint8
	BitDepthChromaMinus8                 uint8
	PcmSampleBitDepthLumaMinus1          uint8
	PcmSampleBitDepthChromaMinus1        uint8
	Log2MinLumaCodingBlockSizeMinus3     uint8
	Log2DiffMaxMinLumaCodingBlockSize    uint8
	Log2MinTransformBlockSizeMinus2      uint8
	Log2DiffMaxMinTransformBlockSize     uint8
	Log2MinPcmLumaCodingBlockSizeMinus3  uint8
	Log2DiffMaxMinPcmLumaCodingBlockSize uint8
	MaxTransformHierarchyDepthIntra      uint8
	MaxTransformHierarchyDepthInter      uint8
	InitQpMinus26                        int8
	DiffCuQpDeltaDepth                   uint8
	PpsCbQpOffset                        int8
	PpsCrQpOffset                        int8
	Log2ParallelMergeLevelMinus2         uint8
	NumTileColumnsMinus1                 uint8
	NumTileRowsMinus1                    uint8
	ColumnWidthMinus1                    [19]uint16
	RowHeightMinus1                      [21]uint16
	SliceParsingFields                   uint32
	Log2MaxPicOrderCntLsbMinus4          uint8
	NumShortTermRefPicSets               uint8
	NumLongTermRefPicSps                 uint8
	NumRefIdxL0DefaultActiveMinus1       uint8
	NumRefIdxL1DefaultActiveMinus1       uint8
	PpsBetaOffsetDiv2                    int8
	PpsTcOffsetDiv2                      int8
	NumExtraSliceHeaderBits              uint8
	StRpsBits                            uint32
	_                                    [8]uint32
}

// Bit positions inside PictureParameterBufferHEVC.PicFields.
const (
	HEVCPicChromaFormatIDCShift                 = 0 // 2 bits
	HEVCPicSeparateColourPlaneFlag              = 1 << 2
	HEVCPicPcmEnabledFlag                       = 1 << 3
	HEVCPicScalingListEnabledFlag               = 1 << 4
	HEVCPicTransformSkipEnabledFlag             = 1 << 5
	HEVCPicAmpEnabledFlag                       = 1 << 6
	HEVCPicStrongIntraSmoothingEnabledFlag      = 1 << 7
	HEVCPicSignDataHidingEnabledFlag            = 1 << 8
	HEVCPicConstrainedIntraPredFlag             = 1 << 9
	HEVCPicCuQpDeltaEnabledFlag                 = 1 << 10
	HEVCPicWeightedPredFlag                     = 1 << 11
	HEVCPicWeightedBipredFlag                   = 1 << 12
	HEVCPicTransquantBypassEnabledFlag          = 1 << 13
	HEVCPicTilesEnabledFlag                     = 1 << 14
	HEVCPicEntropyCodingSyncEnabledFlag         = 1 << 15
	HEVCPicPpsLoopFilterAcrossSlicesEnabledFlag = 1 << 16
	HEVCPicLoopFilterAcrossTilesEnabledFlag     = 1 << 17
	HEVCPicPcmLoopFilterDisabledFlag            = 1 << 18
	HEVCPicNoPicReorderingFlag                  = 1 << 19
	HEVCPicNoBiPredFlag                         = 1 << 20
)

// Bit positions inside PictureParameterBufferHEVC.SliceParsingFields.
const (
	HEVCSliceParsingListsModificationPresentFlag    = 1 << 0
	HEVCSliceParsingLongTermRefPicsPresentFlag      = 1 << 1
	HEVCSliceParsingSpsTemporalMvpEnabledFlag       = 1 << 2
	HEVCSliceParsingCabacInitPresentFlag            = 1 << 3
	HEVCSliceParsingOutputFlagPresentFlag           = 1 << 4
	HEVCSliceParsingDependentSliceSegmentsEnabled   = 1 << 5
	HEVCSliceParsingPpsSliceChromaQpOffsetsPresent  = 1 << 6
	HEVCSliceParsingSampleAdaptiveOffsetEnabled     = 1 << 7
	HEVCSliceParsingDeblockingFilterOverrideEnabled = 1 << 8
	HEVCSliceParsingPpsDisableDeblockingFilter      = 1 << 9
	HEVCSliceParsingSliceSegmentHeaderExtPresent    = 1 << 10
	HEVCSliceParsingRapPicFlag                      = 1 << 11
	HEVCSliceParsingIdrPicFlag                      = 1 << 12
	HEVCSliceParsingIntraPicFlag                    = 1 << 13
)

// SliceParameterBufferHEVC mirrors VASliceParameterBufferHEVC. RefPicList
// entries index PictureParameterBufferHEVC.ReferenceFrames; 0xff marks an
// unused entry.
type SliceParameterBufferHEVC struct {
	SliceDataSize              uint32
	SliceDataOffset            uint32
	SliceDataFlag              uint32
	SliceDataByteOffset        uint32
	SliceSegmentAddress        uint32
	RefPicList                 [2][15]uint8
	_                          [2]uint8
	LongSliceFlags             uint32
	CollocatedRefIdx           uint8
	NumRefIdxL0ActiveMinus1    uint8
	NumRefIdxL1ActiveMinus1    uint8
	SliceQpDelta               int8
	SliceCbQpOffset            int8
	SliceCrQpOffset            int8
	SliceBetaOffsetDiv2        int8
	SliceTcOffsetDiv2          int8
	LumaLog2WeightDenom        uint8
	DeltaChromaLog2WeightDenom int8
	DeltaLumaWeightL0          [15]int8
	LumaOffsetL0               [15]int8
	DeltaChromaWeightL0        [15][2]int8
	ChromaOffsetL0             [15][2]int8
	DeltaLumaWeightL1          [15]int8
	LumaOffsetL1               [15]int8
	DeltaChromaWeightL1        [15][2]int8
	ChromaOffsetL1             [15][2]int8
	FiveMinusMaxNumMergeCand   uint8
	_                          uint8
	NumEntryPointOffsets       uint16
	EntryOffsetToSubsetArray   uint16
	SliceDataNumEmuPrevnBytes  uint16
	_                          uint16
	_                          [2]uint32
}

// Bit positions inside SliceParameterBufferHEVC.LongSliceFlags.
const (
	HEVCSliceLastSliceOfPic                    = 1 << 0
	HEVCSliceDependentSliceSegmentFlag         = 1 << 1
	HEVCSliceSliceTypeShift                    = 2 // 2 bits
	HEVCSliceColorPlaneIDShift                 = 4 // 2 bits
	HEVCSliceSaoLumaFlag                       = 1 << 6
	HEVCSliceSaoChromaFlag                     = 1 << 7
	HEVCSliceMvdL1ZeroFlag                     = 1 << 8
	HEVCSliceCabacInitFlag                     = 1 << 9
	HEVCSliceTemporalMvpEnabledFlag            = 1 << 10
	HEVCSliceDeblockingFilterDisabledFlag      = 1 << 11
	HEVCSliceCollocatedFromL0Flag              = 1 << 12
	HEVCSliceLoopFilterAcrossSlicesEnabledFlag = 1 << 13
)

// HEVCRefPicListUnused marks an unused SliceParameterBufferHEVC.RefPicList
// entry.
const HEVCRefPicListUnused uint8 = 0xff

// IQMatrixBufferHEVC mirrors VAIQMatrixBufferHEVC. The lists are in coded
// (up-right diagonal scan) order.
type IQMatrixBufferHEVC struct {
	ScalingList4x4     [6][16]uint8
	ScalingList8x8     [6][64]uint8
	ScalingList16x16   [6][64]uint8
	ScalingList32x32   [2][64]uint8
	ScalingListDC16x16 [6]uint8
	ScalingListDC32x32 [2]uint8
	_                  [4]uint32
}

// EncSequenceParameterBufferHEVC mirrors VAEncSequenceParameterBufferHEVC.
type EncSequenceParameterBufferHEVC struct {
	GeneralProfileIDC                   uint8
	GeneralLevelIDC                     uint8
	GeneralTierFlag                     uint8
	_                                   uint8
	IntraPeriod                         uint32
	IntraIDRPeriod                      uint32
	IPPeriod                            uint32
	BitsPerSecond                       uint32
	PicWidthInLumaSamples               uint16
	PicHeightInLumaSamples              uint16
	SeqFields                           uint32
	Log2MinLumaCodingBlockSizeMinus3    uint8
	Log2DiffMaxMinLumaCodingBlockSize   uint8
	Log2MinTransformBlockSizeMinus2     uint8
	Log2DiffMaxMinTransformBlockSize    uint8
	MaxTransformHierarchyDepthInter     uint8
	MaxTransformHierarchyDepthIntra     uint8
	_                                   uint16
	PcmSampleBitDepthLumaMinus1         uint32
	PcmSampleBitDepthChromaMinus1       uint32
	Log2MinPcmLumaCodingBlockSizeMinus3 uint32
	Log2MaxPcmLumaCodingBlockSizeMinus3 uint32
	VUIParametersPresentFlag            uint8
	_                                   [3]uint8
	VUIFields                           uint32
	AspectRatioIDC                      uint8
	_                                   [3]uint8
	SarWidth                            uint32
	SarHeight                           uint32
	VUINumUnitsInTick                   uint32
	VUITimeScale                        uint32
	MinSpatialSegmentationIDC           uint16
	MaxBytesPerPicDenom                 uint8
	MaxBitsPerMinCuDenom                uint8
	SccFields                           uint32
	_                                   [7]uint32
}

// Bit positions in EncSequenceParameterBufferHEVC.SeqFields and VUIFields.
const (
	HEVCEncSeqChromaFormatIDCShift            = 0 // 2 bits
	HEVCEncSeqSeparateColourPlaneFlag         = 1 << 2
	HEVCEncSeqBitDepthLumaMinus8Shift         = 3 // 3 bits
	HEVCEncSeqBitDepthChromaMinus8Shift       = 6 // 3 bits
	HEVCEncSeqScalingListEnabledFlag          = 1 << 9
	HEVCEncSeqStrongIntraSmoothingEnabledFlag = 1 << 10
	HEVCEncSeqAmpEnabledFlag                  = 1 << 11
	HEVCEncSeqSampleAdaptiveOffsetEnabledFlag = 1 << 12
	HEVCEncSeqPcmEnabledFlag                  = 1 << 13
	HEVCEncSeqPcmLoopFilterDisabledFlag       = 1 << 14
	HEVCEncSeqSpsTemporalMvpEnabledFlag       = 1 << 15
	HEVCEncSeqLowDelaySeq                     = 1 << 16
	HEVCEncSeqHierachicalFlag                 = 1 << 17

	HEVCEncVUIAspectRatioInfoPresentFlag     = 1 << 0
	HEVCEncVUINeutralChromaIndicationFlag    = 1 << 1
	HEVCEncVUIFieldSeqFlag                   = 1 << 2
	HEVCEncVUITimingInfoPresentFlag          = 1 << 3
	HEVCEncVUIBitstreamRestrictionFlag       = 1 << 4
	HEVCEncVUITilesFixedStructureFlag        = 1 << 5
	HEVCEncVUIMotionVectorsOverPicBoundaries = 1 << 6
	HEVCEncVUIRestrictedRefPicListsFlag      = 1 << 7
	HEVCEncVUILog2MaxMvLengthHorizontalShift = 8  // 5 bits
	HEVCEncVUILog2MaxMvLengthVerticalShift   = 13 // 5 bits
)

// EncPictureParameterBufferHEVC mirrors VAEncPictureParameterBufferHEVC.
type EncPictureParameterBufferHEVC struct {
	DecodedCurrPic                 PictureHEVC
	ReferenceFrames                [15]PictureHEVC
	CodedBuf                       uint32
	CollocatedRefPicIndex          uint8
	LastPicture                    uint8
	PicInitQp                      uint8
	DiffCuQpDeltaDepth             uint8
	PpsCbQpOffset                  int8
	PpsCrQpOffset                  int8
	NumTileColumnsMinus1           uint8
	NumTileRowsMinus1              uint8
	ColumnWidthMinus1              [19]uint8
	RowHeightMinus1                [21]uint8
	Log2ParallelMergeLevelMinus2   uint8
	CtuMaxBitsizeAllowed           uint8
	NumRefIdxL0DefaultActiveMinus1 uint8
	NumRefIdxL1DefaultActiveMinus1 uint8
	SlicePicParameterSetID         uint8
	NalUnitType                    uint8
	_                              uint16
	PicFields                      uint32
	HierarchicalLevelPlus1         uint8
	_                              uint8
	SccFields                      uint16
	_                              [15]uint32
}

// Bit positions in EncPictureParameterBufferHEVC.PicFields.
const (
	HEVCEncPicIdrPicFlag                           = 1 << 0
	HEVCEncPicCodingTypeShift                      = 1 // 3 bits: 1 I, 2 P, 3 B
	HEVCEncPicReferencePicFlag                     = 1 << 4
	HEVCEncPicDependentSliceSegmentsEnabledFlag    = 1 << 5
	HEVCEncPicSignDataHidingEnabledFlag            = 1 << 6
	HEVCEncPicConstrainedIntraPredFlag             = 1 << 7
	HEVCEncPicTransformSkipEnabledFlag             = 1 << 8
	HEVCEncPicCuQpDeltaEnabledFlag                 = 1 << 9
	HEVCEncPicWeightedPredFlag                     = 1 << 10
	HEVCEncPicWeightedBipredFlag                   = 1 << 11
	HEVCEncPicTransquantBypassEnabledFlag          = 1 << 12
	HEVCEncPicTilesEnabledFlag                     = 1 << 13
	HEVCEncPicEntropyCodingSyncEnabledFlag         = 1 << 14
	HEVCEncPicLoopFilterAcrossTilesEnabledFlag     = 1 << 15
	HEVCEncPicPpsLoopFilterAcrossSlicesEnabledFlag = 1 << 16
	HEVCEncPicScalingListDataPresentFlag           = 1 << 17
	HEVCEncPicScreenContentFlag                    = 1 << 18
	HEVCEncPicEnableGpuWeightedPrediction          = 1 << 19
	HEVCEncPicNoOutputOfPriorPicsFlag              = 1 << 20
)

// EncSliceParameterBufferHEVC mirrors VAEncSliceParameterBufferHEVC.
type EncSliceParameterBufferHEVC struct {
	SliceSegmentAddress        uint32
	NumCtuInSlice              uint32
	SliceType                  uint8
	SlicePicParameterSetID     uint8
	NumRefIdxL0ActiveMinus1    uint8
	NumRefIdxL1ActiveMinus1    uint8
	RefPicList0                [15]PictureHEVC
	RefPicList1                [15]PictureHEVC
	LumaLog2WeightDenom        uint8
	DeltaChromaLog2WeightDenom int8
	DeltaLumaWeightL0          [15]int8
	LumaOffsetL0               [15]int8
	DeltaChromaWeightL0        [15][2]int8
	ChromaOffsetL0             [15][2]int8
	DeltaLumaWeightL1          [15]int8
	LumaOffsetL1               [15]int8
	DeltaChromaWeightL1        [15][2]int8
	ChromaOffsetL1             [15][2]int8
	MaxNumMergeCand            uint8
	SliceQpDelta               int8
	SliceCbQpOffset            int8
	SliceCrQpOffset            int8
	SliceBetaOffsetDiv2        int8
	SliceTcOffsetDiv2          int8
	SliceFields                uint32
	PredWeightTableBitOffset   uint32
	PredWeightTableBitLength   uint32
	_                          [6]uint32
}

// Bit positions in EncSliceParameterBufferHEVC.SliceFields.
const (
	HEVCEncSliceLastSliceOfPicFlag                = 1 << 0
	HEVCEncSliceDependentSliceSegmentFlag         = 1 << 1
	HEVCEncSliceColourPlaneIDShift                = 2 // 2 bits
	HEVCEncSliceTemporalMvpEnabledFlag            = 1 << 4
	HEVCEncSliceSaoLumaFlag                       = 1 << 5
	HEVCEncSliceSaoChromaFlag                     = 1 << 6
	HEVCEncSliceNumRefIdxActiveOverrideFlag       = 1 << 7
	HEVCEncSliceMvdL1ZeroFlag                     = 1 << 8
	HEVCEncSliceCabacInitFlag                     = 1 << 9
	HEVCEncSliceDeblockingFilterDisabledFlagShift = 10 // 2 bits
	HEVCEncSliceLoopFilterAcrossSlicesEnabledFlag = 1 << 12
	HEVCEncSliceCollocatedFromL0Flag              = 1 << 13
)

// EncHEVCFeature extracts one two-bit field of
// VAConfigAttribValEncHEVCFeatures: 0 not supported, 1 supported, 2 always
// enabled by the driver.
func EncHEVCFeature(value uint32, shift uint) uint32 { return (value >> shift) & 3 }

// Field positions inside VAConfigAttribValEncHEVCFeatures.
const (
	HEVCFeatureSeparateColourPlanesShift    = 0
	HEVCFeatureScalingListsShift            = 2
	HEVCFeatureAmpShift                     = 4
	HEVCFeatureSaoShift                     = 6
	HEVCFeaturePcmShift                     = 8
	HEVCFeatureTemporalMvpShift             = 10
	HEVCFeatureStrongIntraSmoothingShift    = 12
	HEVCFeatureDependentSlicesShift         = 14
	HEVCFeatureSignDataHidingShift          = 16
	HEVCFeatureConstrainedIntraPredShift    = 18
	HEVCFeatureTransformSkipShift           = 20
	HEVCFeatureCuQpDeltaShift               = 22
	HEVCFeatureWeightedPredictionShift      = 24
	HEVCFeatureTransquantBypassShift        = 26
	HEVCFeatureDeblockingFilterDisableShift = 28
)

// Field positions inside VAConfigAttribValEncHEVCBlockSizes (two bits each).
const (
	HEVCBlockLog2MaxCodingTreeBlockSizeMinus3Shift    = 0
	HEVCBlockLog2MinCodingTreeBlockSizeMinus3Shift    = 2
	HEVCBlockLog2MinLumaCodingBlockSizeMinus3Shift    = 4
	HEVCBlockLog2MaxLumaTransformBlockSizeMinus2Shift = 6
	HEVCBlockLog2MinLumaTransformBlockSizeMinus2Shift = 8
	HEVCBlockMaxMaxTransformHierarchyDepthInterShift  = 10
	HEVCBlockMinMaxTransformHierarchyDepthInterShift  = 12
	HEVCBlockMaxMaxTransformHierarchyDepthIntraShift  = 14
	HEVCBlockMinMaxTransformHierarchyDepthIntraShift  = 16
)
