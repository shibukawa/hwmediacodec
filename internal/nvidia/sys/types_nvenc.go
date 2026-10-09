package sys

import "unsafe"

// NVENC API version the structs below correspond to (Video Codec SDK 11.1,
// supported by driver 470.57 and newer). Newer drivers accept older struct
// versions, so this is the floor, not a pin.
const (
	APIMajorVersion = 11
	APIMinorVersion = 1
	// APIVersion is NVENCAPI_VERSION.
	APIVersion uint32 = APIMajorVersion | APIMinorVersion<<24
	// MaxSupportedVersionFloor is what NvEncodeAPIGetMaxSupportedVersion
	// must report at least: (major << 4) | minor.
	MaxSupportedVersionFloor uint32 = APIMajorVersion<<4 | APIMinorVersion
)

// structVersion is NVENCAPI_STRUCT_VERSION(ver).
const structVersionBase uint32 = APIVersion | 0x7<<28

// Struct version tags.
const (
	CapsParamVer                 uint32 = structVersionBase | 1<<16
	CreateInputBufferVer         uint32 = structVersionBase | 1<<16
	CreateBitstreamBufferVer     uint32 = structVersionBase | 1<<16
	RCParamsVer                  uint32 = structVersionBase | 1<<16
	ConfigVer                    uint32 = structVersionBase | 7<<16 | 1<<31
	InitializeParamsVer          uint32 = structVersionBase | 5<<16 | 1<<31
	PresetConfigVer              uint32 = structVersionBase | 4<<16 | 1<<31
	PicParamsVer                 uint32 = structVersionBase | 4<<16 | 1<<31
	LockBitstreamVer             uint32 = structVersionBase | 1<<16
	LockInputBufferVer           uint32 = structVersionBase | 1<<16
	SequenceParamPayloadVer      uint32 = structVersionBase | 1<<16
	OpenEncodeSessionExParamsVer uint32 = structVersionBase | 1<<16
	FunctionListVer              uint32 = structVersionBase | 2<<16
)

// GUID mirrors the Windows-style GUID used by nvEncodeAPI.h.
type GUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// Codec, profile and preset GUIDs.
var (
	CodecH264GUID = GUID{0x6bc82762, 0x4e63, 0x4ca4, [8]byte{0xaa, 0x85, 0x1e, 0x50, 0xf3, 0x21, 0xf6, 0xbf}}
	CodecHEVCGUID = GUID{0x790cdc88, 0x4522, 0x4d7b, [8]byte{0x94, 0x25, 0xbd, 0xa9, 0x97, 0x5f, 0x76, 0x3}}

	ProfileAutoselectGUID   = GUID{0xbfd6f8e7, 0x233c, 0x4341, [8]byte{0x8b, 0x3e, 0x48, 0x18, 0x52, 0x38, 0x3, 0xf4}}
	H264ProfileBaselineGUID = GUID{0x727bcaa, 0x78c4, 0x4c83, [8]byte{0x8c, 0x2f, 0xef, 0x3d, 0xff, 0x26, 0x7c, 0x6a}}
	H264ProfileMainGUID     = GUID{0x60b5c1d4, 0x67fe, 0x4790, [8]byte{0x94, 0xd5, 0xc4, 0x72, 0x6d, 0x7b, 0x6e, 0x6d}}
	H264ProfileHighGUID     = GUID{0xe7cbc309, 0x4f7a, 0x4b89, [8]byte{0xaf, 0x2a, 0xd5, 0x37, 0xc9, 0x2b, 0xe3, 0x10}}
	HEVCProfileMainGUID     = GUID{0xb514c39a, 0xb55b, 0x40fa, [8]byte{0x87, 0x8f, 0xf1, 0x25, 0x3b, 0x4d, 0xfd, 0xec}}
	HEVCProfileMain10GUID   = GUID{0xfa4d2b6c, 0x3a5b, 0x411a, [8]byte{0x80, 0x18, 0x0a, 0x3f, 0x5e, 0x3c, 0x9b, 0xe5}}

	PresetP1GUID = GUID{0xfc0a8d3e, 0x45f8, 0x4cf8, [8]byte{0x80, 0xc7, 0x29, 0x88, 0x71, 0x59, 0xe, 0xbf}}
	PresetP2GUID = GUID{0xf581cfb8, 0x88d6, 0x4381, [8]byte{0x93, 0xf0, 0xdf, 0x13, 0xf9, 0xc2, 0x7d, 0xab}}
	PresetP3GUID = GUID{0x36850110, 0x3a07, 0x441f, [8]byte{0x94, 0xd5, 0x36, 0x70, 0x63, 0x1f, 0x91, 0xf6}}
	PresetP4GUID = GUID{0x90a7b826, 0xdf06, 0x4862, [8]byte{0xb9, 0xd2, 0xcd, 0x6d, 0x73, 0xa0, 0x86, 0x81}}
	PresetP5GUID = GUID{0x21c6e6b4, 0x297a, 0x4cba, [8]byte{0x99, 0x8f, 0xb6, 0xcb, 0xde, 0x72, 0xad, 0xe3}}
	PresetP6GUID = GUID{0x8e75c279, 0x6299, 0x4ab6, [8]byte{0x83, 0x2, 0xb, 0x21, 0x5a, 0x33, 0x5c, 0xf5}}
	PresetP7GUID = GUID{0x84848c12, 0x6f71, 0x4c13, [8]byte{0x93, 0x1b, 0x53, 0xe2, 0x83, 0xf5, 0x79, 0x74}}
)

// NVENCSTATUS values.
const (
	StatusSuccess                   uint32 = 0
	StatusErrNoEncodeDevice         uint32 = 1
	StatusErrUnsupportedDevice      uint32 = 2
	StatusErrInvalidEncoderDevice   uint32 = 3
	StatusErrInvalidDevice          uint32 = 4
	StatusErrDeviceNotExist         uint32 = 5
	StatusErrInvalidPtr             uint32 = 6
	StatusErrInvalidEvent           uint32 = 7
	StatusErrInvalidParam           uint32 = 8
	StatusErrInvalidCall            uint32 = 9
	StatusErrOutOfMemory            uint32 = 10
	StatusErrEncoderNotInitialized  uint32 = 11
	StatusErrUnsupportedParam       uint32 = 12
	StatusErrLockBusy               uint32 = 13
	StatusErrNotEnoughBuffer        uint32 = 14
	StatusErrInvalidVersion         uint32 = 15
	StatusErrMapFailed              uint32 = 16
	StatusErrNeedMoreInput          uint32 = 17
	StatusErrEncoderBusy            uint32 = 18
	StatusErrEventNotRegistered     uint32 = 19
	StatusErrGeneric                uint32 = 20
	StatusErrIncompatibleClientKey  uint32 = 21
	StatusErrUnimplemented          uint32 = 22
	StatusErrResourceRegisterFailed uint32 = 23
	StatusErrResourceNotRegistered  uint32 = 24
	StatusErrResourceNotMapped      uint32 = 25
)

// StatusString names an NVENCSTATUS.
func StatusString(st uint32) string {
	names := [...]string{
		"NV_ENC_SUCCESS", "NV_ENC_ERR_NO_ENCODE_DEVICE", "NV_ENC_ERR_UNSUPPORTED_DEVICE", "NV_ENC_ERR_INVALID_ENCODERDEVICE",
		"NV_ENC_ERR_INVALID_DEVICE", "NV_ENC_ERR_DEVICE_NOT_EXIST", "NV_ENC_ERR_INVALID_PTR", "NV_ENC_ERR_INVALID_EVENT",
		"NV_ENC_ERR_INVALID_PARAM", "NV_ENC_ERR_INVALID_CALL", "NV_ENC_ERR_OUT_OF_MEMORY", "NV_ENC_ERR_ENCODER_NOT_INITIALIZED",
		"NV_ENC_ERR_UNSUPPORTED_PARAM", "NV_ENC_ERR_LOCK_BUSY", "NV_ENC_ERR_NOT_ENOUGH_BUFFER", "NV_ENC_ERR_INVALID_VERSION",
		"NV_ENC_ERR_MAP_FAILED", "NV_ENC_ERR_NEED_MORE_INPUT", "NV_ENC_ERR_ENCODER_BUSY", "NV_ENC_ERR_EVENT_NOT_REGISTERD",
		"NV_ENC_ERR_GENERIC", "NV_ENC_ERR_INCOMPATIBLE_CLIENT_KEY", "NV_ENC_ERR_UNIMPLEMENTED", "NV_ENC_ERR_RESOURCE_REGISTER_FAILED",
		"NV_ENC_ERR_RESOURCE_NOT_REGISTERED", "NV_ENC_ERR_RESOURCE_NOT_MAPPED",
	}
	if int(st) < len(names) {
		return names[st]
	}
	return "NVENCSTATUS(" + itoa(int(st)) + ")"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// NV_ENC_PARAMS_RC_MODE values.
const (
	RCConstQP uint32 = 0x0
	RCVBR     uint32 = 0x1
	RCCBR     uint32 = 0x2
)

// NV_ENC_PARAMS_FRAME_FIELD_MODE values.
const (
	FrameFieldModeFrame uint32 = 0x01
)

// NV_ENC_PIC_STRUCT values.
const (
	PicStructFrame uint32 = 0x01
)

// NV_ENC_PIC_TYPE values.
const (
	PicTypeP            uint32 = 0x0
	PicTypeB            uint32 = 0x01
	PicTypeI            uint32 = 0x02
	PicTypeIDR          uint32 = 0x03
	PicTypeBI           uint32 = 0x04
	PicTypeSkipped      uint32 = 0x05
	PicTypeIntraRefresh uint32 = 0x06
	PicTypeNonRefP      uint32 = 0x07
	PicTypeUnknown      uint32 = 0xFF
)

// NV_ENC_BUFFER_FORMAT values.
const (
	BufferFormatUndefined uint32 = 0x00000000
	BufferFormatNV12      uint32 = 0x00000001
	BufferFormatYV12      uint32 = 0x00000010
	BufferFormatIYUV      uint32 = 0x00000100
	BufferFormatYUV444    uint32 = 0x00001000
	// BufferFormatARGB is A8R8G8B8 as a little-endian word: B, G, R, A in
	// memory, that is the BGRA layout.
	BufferFormatARGB uint32 = 0x01000000
	// BufferFormatABGR is A8B8G8R8: R, G, B, A in memory, the RGBA layout.
	BufferFormatABGR uint32 = 0x10000000
)

// NV_ENC_PIC_FLAGS values.
const (
	PicFlagForceIntra   uint32 = 0x1
	PicFlagForceIDR     uint32 = 0x2
	PicFlagOutputSPSPPS uint32 = 0x4
	PicFlagEOS          uint32 = 0x8
)

// NV_ENC_MEMORY_HEAP values.
const (
	MemoryHeapAutoselect uint32 = 0
)

// NV_ENC_H264_ENTROPY_CODING_MODE values.
const (
	EntropyCodingAutoselect uint32 = 0x0
	EntropyCodingCABAC      uint32 = 0x1
	EntropyCodingCAVLC      uint32 = 0x2
)

// NV_ENC_DEVICE_TYPE values.
const (
	DeviceTypeDirectX uint32 = 0x0
	DeviceTypeCUDA    uint32 = 0x1
	DeviceTypeOpenGL  uint32 = 0x2
)

// NV_ENC_TUNING_INFO values.
const (
	TuningInfoUndefined       uint32 = 0
	TuningInfoHighQuality     uint32 = 1
	TuningInfoLowLatency      uint32 = 2
	TuningInfoUltraLowLatency uint32 = 3
	TuningInfoLossless        uint32 = 4
)

// NV_ENC_CAPS values used by the backend.
const (
	CapsNumMaxBFrames             uint32 = 0
	CapsSupportedRateControlModes uint32 = 1
	CapsSupportCABAC              uint32 = 7
	CapsWidthMax                  uint32 = 16
	CapsHeightMax                 uint32 = 17
	CapsSupportCustomVBVBufSize   uint32 = 26
	CapsMBNumMax                  uint32 = 31
	CapsSupportLookahead          uint32 = 37
	CapsSupportBFrameRefMode      uint32 = 43
	CapsWidthMin                  uint32 = 45
	CapsHeightMin                 uint32 = 46
	CapsNumEncoderEngines         uint32 = 49
)

// InfiniteGOPLength is NVENC_INFINITE_GOPLENGTH.
const InfiniteGOPLength uint32 = 0xffffffff

// CapsParam mirrors NV_ENC_CAPS_PARAM.
type CapsParam struct {
	Version     uint32
	CapsToQuery uint32
	Reserved    [62]uint32
}

// CreateInputBuffer mirrors NV_ENC_CREATE_INPUT_BUFFER.
type CreateInputBuffer struct {
	Version      uint32
	Width        uint32
	Height       uint32
	MemoryHeap   uint32
	BufferFmt    uint32
	Reserved     uint32
	InputBuffer  uintptr // out: NV_ENC_INPUT_PTR
	SysMemBuffer uintptr
	Reserved1    [57]uint32
	Reserved2    [63]uintptr
}

// CreateBitstreamBuffer mirrors NV_ENC_CREATE_BITSTREAM_BUFFER.
type CreateBitstreamBuffer struct {
	Version            uint32
	Size               uint32
	MemoryHeap         uint32
	Reserved           uint32
	BitstreamBuffer    uintptr // out: NV_ENC_OUTPUT_PTR
	BitstreamBufferPtr uintptr
	Reserved1          [58]uint32
	Reserved2          [64]uintptr
}

// QP mirrors NV_ENC_QP.
type QP struct {
	InterP uint32
	InterB uint32
	Intra  uint32
}

// Bits of RCParams.Flags.
const (
	RCFlagEnableMinQP       uint32 = 1 << 0
	RCFlagEnableMaxQP       uint32 = 1 << 1
	RCFlagEnableInitialRCQP uint32 = 1 << 2
	RCFlagEnableAQ          uint32 = 1 << 3
	RCFlagEnableLookahead   uint32 = 1 << 5
	RCFlagDisableIadapt     uint32 = 1 << 6
	RCFlagDisableBadapt     uint32 = 1 << 7
	RCFlagEnableTemporalAQ  uint32 = 1 << 8
	RCFlagZeroReorderDelay  uint32 = 1 << 9
	RCFlagEnableNonRefP     uint32 = 1 << 10
	RCFlagStrictGOPTarget   uint32 = 1 << 11
	RCFlagAQStrengthShift          = 12 // 4 bits
)

// RCParams mirrors NV_ENC_RC_PARAMS.
type RCParams struct {
	Version                uint32
	RateControlMode        uint32
	ConstQP                QP
	AverageBitRate         uint32
	MaxBitRate             uint32
	VBVBufferSize          uint32
	VBVInitialDelay        uint32
	Flags                  uint32 // see RCFlag*
	MinQP                  QP
	MaxQP                  QP
	InitialRCQP            QP
	TemporalLayerIdxMask   uint32
	TemporalLayerQP        [8]uint8
	TargetQuality          uint8
	TargetQualityLSB       uint8
	LookaheadDepth         uint16
	LowDelayKeyFrameScale  uint8
	Reserved1              [3]uint8
	QPMapMode              uint32
	MultiPass              uint32
	AlphaLayerBitrateRatio uint32
	CbQPIndexOffset        int8
	CrQPIndexOffset        int8
	Reserved2              uint16
	Reserved               [4]uint32
}

// VUIParameters mirrors NV_ENC_CONFIG_H264_VUI_PARAMETERS, which is also
// NV_ENC_CONFIG_HEVC_VUI_PARAMETERS.
type VUIParameters struct {
	OverscanInfoPresentFlag      uint32
	OverscanInfo                 uint32
	VideoSignalTypePresentFlag   uint32
	VideoFormat                  uint32
	VideoFullRangeFlag           uint32
	ColourDescriptionPresentFlag uint32
	ColourPrimaries              uint32
	TransferCharacteristics      uint32
	ColourMatrix                 uint32
	ChromaSampleLocationFlag     uint32
	ChromaSampleLocationTop      uint32
	ChromaSampleLocationBot      uint32
	BitstreamRestrictionFlag     uint32
	Reserved                     [15]uint32
}

// MEHintCounts mirrors NVENC_EXTERNAL_ME_HINT_COUNTS_PER_BLOCKTYPE.
type MEHintCounts struct {
	Flags     uint32
	Reserved1 [3]uint32
}

// Bits of ConfigH264.Flags.
const (
	H264FlagEnableTemporalSVC           uint32 = 1 << 0
	H264FlagEnableStereoMVC             uint32 = 1 << 1
	H264FlagHierarchicalPFrames         uint32 = 1 << 2
	H264FlagHierarchicalBFrames         uint32 = 1 << 3
	H264FlagOutputBufferingPeriodSEI    uint32 = 1 << 4
	H264FlagOutputPictureTimingSEI      uint32 = 1 << 5
	H264FlagOutputAUD                   uint32 = 1 << 6
	H264FlagDisableSPSPPS               uint32 = 1 << 7
	H264FlagOutputFramePackingSEI       uint32 = 1 << 8
	H264FlagOutputRecoveryPointSEI      uint32 = 1 << 9
	H264FlagEnableIntraRefresh          uint32 = 1 << 10
	H264FlagEnableConstrainedEncoding   uint32 = 1 << 11
	H264FlagRepeatSPSPPS                uint32 = 1 << 12
	H264FlagEnableVFR                   uint32 = 1 << 13
	H264FlagEnableLTR                   uint32 = 1 << 14
	H264FlagQPPrimeYZeroTransformBypass uint32 = 1 << 15
	H264FlagUseConstrainedIntraPred     uint32 = 1 << 16
	H264FlagEnableFillerDataInsertion   uint32 = 1 << 17
	H264FlagDisableSVCPrefixNalu        uint32 = 1 << 18
	H264FlagEnableScalabilityInfoSEI    uint32 = 1 << 19
	H264FlagSingleSliceIntraRefresh     uint32 = 1 << 20
)

// ConfigH264 mirrors NV_ENC_CONFIG_H264.
type ConfigH264 struct {
	Flags                      uint32 // see H264Flag*
	Level                      uint32
	IDRPeriod                  uint32
	SeparateColourPlaneFlag    uint32
	DisableDeblockingFilterIDC uint32
	NumTemporalLayers          uint32
	SPSID                      uint32
	PPSID                      uint32
	AdaptiveTransformMode      uint32
	FMOMode                    uint32
	BDirectMode                uint32
	EntropyCodingMode          uint32
	StereoMode                 uint32
	IntraRefreshPeriod         uint32
	IntraRefreshCnt            uint32
	MaxNumRefFrames            uint32
	SliceMode                  uint32
	SliceModeData              uint32
	VUI                        VUIParameters
	LTRNumFrames               uint32
	LTRTrustMode               uint32
	ChromaFormatIDC            uint32
	MaxTemporalLayers          uint32
	UseBFramesAsRef            uint32
	NumRefL0                   uint32
	NumRefL1                   uint32
	Reserved1                  [267]uint32
	Reserved2                  [64]uintptr
}

// Bits of ConfigHEVC.Flags.
const (
	HEVCFlagUseConstrainedIntraPred           uint32 = 1 << 0
	HEVCFlagDisableDeblockAcrossSliceBoundary uint32 = 1 << 1
	HEVCFlagOutputBufferingPeriodSEI          uint32 = 1 << 2
	HEVCFlagOutputPictureTimingSEI            uint32 = 1 << 3
	HEVCFlagOutputAUD                         uint32 = 1 << 4
	HEVCFlagEnableLTR                         uint32 = 1 << 5
	HEVCFlagDisableSPSPPS                     uint32 = 1 << 6
	HEVCFlagRepeatSPSPPS                      uint32 = 1 << 7
	HEVCFlagEnableIntraRefresh                uint32 = 1 << 8
	HEVCFlagChromaFormatIDCShift                     = 9  // 2 bits
	HEVCFlagPixelBitDepthMinus8Shift                 = 11 // 3 bits
	HEVCFlagEnableFillerDataInsertion         uint32 = 1 << 14
	HEVCFlagEnableConstrainedEncoding         uint32 = 1 << 15
	HEVCFlagEnableAlphaLayerEncoding          uint32 = 1 << 16
	HEVCFlagSingleSliceIntraRefresh           uint32 = 1 << 17
)

// ConfigHEVC mirrors NV_ENC_CONFIG_HEVC.
type ConfigHEVC struct {
	Level                   uint32
	Tier                    uint32
	MinCUSize               uint32
	MaxCUSize               uint32
	Flags                   uint32 // see HEVCFlag*
	IDRPeriod               uint32
	IntraRefreshPeriod      uint32
	IntraRefreshCnt         uint32
	MaxNumRefFramesInDPB    uint32
	LTRNumFrames            uint32
	VPSID                   uint32
	SPSID                   uint32
	PPSID                   uint32
	SliceMode               uint32
	SliceModeData           uint32
	MaxTemporalLayersMinus1 uint32
	VUI                     VUIParameters
	LTRTrustMode            uint32
	UseBFramesAsRef         uint32
	NumRefL0                uint32
	NumRefL1                uint32
	Reserved1               [214]uint32
	Reserved2               [64]uintptr
}

// CodecConfig mirrors the NV_ENC_CODEC_CONFIG union; H264 and HEVC view it
// as the codec-specific struct.
type CodecConfig struct {
	_   [0]uint64
	Raw [1792]byte
}

// Config mirrors NV_ENC_CONFIG.
type Config struct {
	Version            uint32
	ProfileGUID        GUID
	GOPLength          uint32
	FrameIntervalP     int32
	MonoChromeEncoding uint32
	FrameFieldMode     uint32
	MVPrecision        uint32
	RC                 RCParams
	CodecConfig        CodecConfig
	Reserved           [278]uint32
	Reserved2          [64]uintptr
}

// Bits of InitializeParams.Flags.
const (
	InitFlagReportSliceOffsets       uint32 = 1 << 0
	InitFlagEnableSubFrameWrite      uint32 = 1 << 1
	InitFlagEnableExternalMEHints    uint32 = 1 << 2
	InitFlagEnableMEOnlyMode         uint32 = 1 << 3
	InitFlagEnableWeightedPrediction uint32 = 1 << 4
	InitFlagEnableOutputInVidmem     uint32 = 1 << 5
)

// InitializeParams mirrors NV_ENC_INITIALIZE_PARAMS.
type InitializeParams struct {
	Version                 uint32
	EncodeGUID              GUID
	PresetGUID              GUID
	EncodeWidth             uint32
	EncodeHeight            uint32
	DARWidth                uint32
	DARHeight               uint32
	FrameRateNum            uint32
	FrameRateDen            uint32
	EnableEncodeAsync       uint32
	EnablePTD               uint32
	Flags                   uint32 // see InitFlag*
	PrivDataSize            uint32
	PrivData                uintptr
	EncodeConfig            *Config
	MaxEncodeWidth          uint32
	MaxEncodeHeight         uint32
	MaxMEHintCountsPerBlock [2]MEHintCounts
	TuningInfo              uint32
	BufferFormat            uint32
	Reserved                [287]uint32
	Reserved2               [64]uintptr
}

// PresetConfig mirrors NV_ENC_PRESET_CONFIG.
type PresetConfig struct {
	Version   uint32
	PresetCfg Config
	Reserved1 [255]uint32
	Reserved2 [64]uintptr
}

// PicParamsH264 mirrors NV_ENC_PIC_PARAMS_H264.
type PicParamsH264 struct {
	DisplayPOCSyntax              uint32
	Reserved3                     uint32
	RefPicFlag                    uint32
	ColourPlaneID                 uint32
	ForceIntraRefreshWithFrameCnt uint32
	Flags                         uint32
	SliceTypeData                 uintptr
	SliceTypeArrayCnt             uint32
	SEIPayloadArrayCnt            uint32
	SEIPayloadArray               uintptr
	SliceMode                     uint32
	SliceModeData                 uint32
	LTRMarkFrameIdx               uint32
	LTRUseFrameBitmap             uint32
	LTRUsageMode                  uint32
	ForceIntraSliceCount          uint32
	ForceIntraSliceIdx            uintptr
	H264ExtPicParams              [128]byte
	Reserved                      [210]uint32
	Reserved2                     [61]uintptr
}

// PicParamsHEVC mirrors NV_ENC_PIC_PARAMS_HEVC.
type PicParamsHEVC struct {
	DisplayPOCSyntax              uint32
	RefPicFlag                    uint32
	TemporalID                    uint32
	ForceIntraRefreshWithFrameCnt uint32
	Flags                         uint32
	SliceTypeData                 uintptr
	SliceTypeArrayCnt             uint32
	SliceMode                     uint32
	SliceModeData                 uint32
	LTRMarkFrameIdx               uint32
	LTRUseFrameBitmap             uint32
	LTRUsageMode                  uint32
	SEIPayloadArrayCnt            uint32
	Reserved                      uint32
	SEIPayloadArray               uintptr
	Reserved2                     [244]uint32
	Reserved3                     [61]uintptr
}

// CodecPicParams mirrors the NV_ENC_CODEC_PIC_PARAMS union.
type CodecPicParams struct {
	_   [0]uint64
	Raw [1536]byte
}

// PicParams mirrors NV_ENC_PIC_PARAMS.
type PicParams struct {
	Version              uint32
	InputWidth           uint32
	InputHeight          uint32
	InputPitch           uint32
	EncodePicFlags       uint32
	FrameIdx             uint32
	InputTimeStamp       uint64
	InputDuration        uint64
	InputBuffer          uintptr
	OutputBitstream      uintptr
	CompletionEvent      uintptr
	BufferFmt            uint32
	PictureStruct        uint32
	PictureType          uint32
	CodecPicParams       CodecPicParams
	MEHintCountsPerBlock [2]MEHintCounts
	MEExternalHints      uintptr
	Reserved1            [6]uint32
	Reserved2            [2]uintptr
	QPDeltaMap           uintptr
	QPDeltaMapSize       uint32
	ReservedBitFields    uint32
	MEHintRefPicDist     [2]uint16
	AlphaBuffer          uintptr
	Reserved3            [286]uint32
	Reserved4            [59]uintptr
}

// Bits of LockBitstream.Flags.
const (
	LockBitstreamDoNotWait  uint32 = 1 << 0
	LockBitstreamLTRFrame   uint32 = 1 << 1
	LockBitstreamGetRCStats uint32 = 1 << 2
)

// LockBitstream mirrors NV_ENC_LOCK_BITSTREAM.
type LockBitstream struct {
	Version               uint32
	Flags                 uint32 // see LockBitstream*
	OutputBitstream       uintptr
	SliceOffsets          uintptr
	FrameIdx              uint32
	HWEncodeStatus        uint32
	NumSlices             uint32
	BitstreamSizeInBytes  uint32
	OutputTimeStamp       uint64
	OutputDuration        uint64
	BitstreamBufferPtr    *byte
	PictureType           uint32
	PictureStruct         uint32
	FrameAvgQP            uint32
	FrameSatd             uint32
	LTRFrameIdx           uint32
	LTRFrameBitmap        uint32
	TemporalID            uint32
	Reserved              [12]uint32
	IntraMBCount          uint32
	InterMBCount          uint32
	AverageMVX            int32
	AverageMVY            int32
	AlphaLayerSizeInBytes uint32
	Reserved1             [218]uint32
	Reserved2             [64]uintptr
}

// LockInputBuffer mirrors NV_ENC_LOCK_INPUT_BUFFER.
type LockInputBuffer struct {
	Version       uint32
	Flags         uint32 // doNotWait:1
	InputBuffer   uintptr
	BufferDataPtr *byte
	Pitch         uint32
	Reserved1     [251]uint32
	Reserved2     [64]uintptr
}

// SequenceParamPayload mirrors NV_ENC_SEQUENCE_PARAM_PAYLOAD.
type SequenceParamPayload struct {
	Version              uint32
	InBufferSize         uint32
	SPSID                uint32
	PPSID                uint32
	SPSPPSBuffer         unsafe.Pointer
	OutSPSPPSPayloadSize *uint32
	Reserved             [250]uint32
	Reserved2            [64]uintptr
}

// OpenEncodeSessionExParams mirrors NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS.
type OpenEncodeSessionExParams struct {
	Version    uint32
	DeviceType uint32
	Device     uintptr
	Reserved   uintptr
	APIVersion uint32
	Reserved1  [253]uint32
	Reserved2  [64]uintptr
}

// FunctionList mirrors NV_ENCODE_API_FUNCTION_LIST: the table of entry
// points NvEncodeAPICreateInstance fills in.
type FunctionList struct {
	Version                   uint32
	Reserved                  uint32
	OpenEncodeSession         uintptr
	GetEncodeGUIDCount        uintptr
	GetEncodeProfileGUIDCount uintptr
	GetEncodeProfileGUIDs     uintptr
	GetEncodeGUIDs            uintptr
	GetInputFormatCount       uintptr
	GetInputFormats           uintptr
	GetEncodeCaps             uintptr
	GetEncodePresetCount      uintptr
	GetEncodePresetGUIDs      uintptr
	GetEncodePresetConfig     uintptr
	InitializeEncoder         uintptr
	CreateInputBuffer         uintptr
	DestroyInputBuffer        uintptr
	CreateBitstreamBuffer     uintptr
	DestroyBitstreamBuffer    uintptr
	EncodePicture             uintptr
	LockBitstream             uintptr
	UnlockBitstream           uintptr
	LockInputBuffer           uintptr
	UnlockInputBuffer         uintptr
	GetEncodeStats            uintptr
	GetSequenceParams         uintptr
	RegisterAsyncEvent        uintptr
	UnregisterAsyncEvent      uintptr
	MapInputResource          uintptr
	UnmapInputResource        uintptr
	DestroyEncoder            uintptr
	InvalidateRefFrames       uintptr
	OpenEncodeSessionEx       uintptr
	RegisterResource          uintptr
	UnregisterResource        uintptr
	ReconfigureEncoder        uintptr
	Reserved1                 uintptr
	CreateMVBuffer            uintptr
	DestroyMVBuffer           uintptr
	RunMotionEstimationOnly   uintptr
	GetLastErrorString        uintptr
	SetIOCudaStreams          uintptr
	GetEncodePresetConfigEx   uintptr
	GetSequenceParamEx        uintptr
	Reserved2                 [277]uintptr
}
