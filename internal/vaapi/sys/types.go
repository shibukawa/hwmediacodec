// Package sys binds libva (VA-API) through purego so that no cgo is
// required. The struct definitions mirror va.h; layout_test.go checks their
// sizes and offsets against values measured with a C compiler.
package sys

// Status codes (VAStatus).
const (
	StatusSuccess                     int32 = 0x00
	StatusErrorOperationFailed        int32 = 0x01
	StatusErrorAllocationFailed       int32 = 0x02
	StatusErrorInvalidDisplay         int32 = 0x03
	StatusErrorInvalidConfig          int32 = 0x04
	StatusErrorInvalidContext         int32 = 0x05
	StatusErrorInvalidSurface         int32 = 0x06
	StatusErrorInvalidBuffer          int32 = 0x07
	StatusErrorInvalidImage           int32 = 0x08
	StatusErrorInvalidSubpicture      int32 = 0x09
	StatusErrorAttrNotSupported       int32 = 0x0a
	StatusErrorMaxNumExceeded         int32 = 0x0b
	StatusErrorUnsupportedProfile     int32 = 0x0c
	StatusErrorUnsupportedEntrypoint  int32 = 0x0d
	StatusErrorUnsupportedRTFormat    int32 = 0x0e
	StatusErrorUnsupportedBufferType  int32 = 0x0f
	StatusErrorSurfaceBusy            int32 = 0x10
	StatusErrorFlagNotSupported       int32 = 0x11
	StatusErrorInvalidParameter       int32 = 0x12
	StatusErrorResolutionNotSupported int32 = 0x13
	StatusErrorUnimplemented          int32 = 0x14
	StatusErrorSurfaceInDisplaying    int32 = 0x15
	StatusErrorInvalidImageFormat     int32 = 0x16
	StatusErrorDecodingError          int32 = 0x17
	StatusErrorEncodingError          int32 = 0x18
	StatusErrorInvalidValue           int32 = 0x19
	StatusErrorUnsupportedFilter      int32 = 0x20
	StatusErrorInvalidFilterChain     int32 = 0x21
	StatusErrorHWBusy                 int32 = 0x22
	StatusErrorUnsupportedMemoryType  int32 = 0x24
	StatusErrorNotEnoughBuffer        int32 = 0x25
	StatusErrorTimedout               int32 = 0x26
)

// VAProfile values.
const (
	ProfileNone                    int32 = -1
	ProfileH264Main                int32 = 6
	ProfileH264High                int32 = 7
	ProfileH264ConstrainedBaseline int32 = 13
	ProfileHEVCMain                int32 = 17
	ProfileHEVCMain10              int32 = 18
	ProfileAV1Profile0             int32 = 32
	ProfileH264High10              int32 = 36
)

// VAEntrypoint values.
const (
	EntrypointVLD int32 = 1
)

// VAConfigAttribType values.
const (
	ConfigAttribRTFormat         int32 = 0
	ConfigAttribMaxPictureWidth  int32 = 18
	ConfigAttribMaxPictureHeight int32 = 19
)

// AttribNotSupported is the value a driver returns for an unsupported
// config attribute.
const AttribNotSupported uint32 = 0x80000000

// VA_RT_FORMAT_* values.
const (
	RTFormatYUV420 uint32 = 0x00000001
)

// VASurfaceAttribType values.
const (
	SurfaceAttribNone        int32 = 0
	SurfaceAttribPixelFormat int32 = 1
	SurfaceAttribMinWidth    int32 = 2
	SurfaceAttribMaxWidth    int32 = 3
	SurfaceAttribMinHeight   int32 = 4
	SurfaceAttribMaxHeight   int32 = 5
)

// VAGenericValueType values and surface attribute flags.
const (
	GenericValueTypeInteger int32  = 1
	SurfaceAttribGettable   uint32 = 0x1
	SurfaceAttribSettable   uint32 = 0x2
)

// VABufferType values.
const (
	PictureParameterBufferType int32 = 0
	IQMatrixBufferType         int32 = 1
	SliceParameterBufferType   int32 = 4
	SliceDataBufferType        int32 = 5
)

// Miscellaneous constants.
const (
	InvalidID        uint32 = 0xffffffff
	InvalidSurface   uint32 = InvalidID
	Progressive      int32  = 0x1
	FourccNV12       uint32 = 0x3231564E
	LSBFirst         uint32 = 1
	SliceDataFlagAll uint32 = 0
)

// VAPictureH264 flags.
const (
	PictureH264Invalid            uint32 = 0x01
	PictureH264TopField           uint32 = 0x02
	PictureH264BottomField        uint32 = 0x04
	PictureH264ShortTermReference uint32 = 0x08
	PictureH264LongTermReference  uint32 = 0x10
	PictureH264NonExisting        uint32 = 0x20
)

// ConfigAttrib mirrors VAConfigAttrib.
type ConfigAttrib struct {
	Type  int32
	Value uint32
}

// GenericValue mirrors VAGenericValue. Value holds the union; for integer
// attributes the low 32 bits carry the value.
type GenericValue struct {
	Type  int32
	_     int32
	Value uint64
}

// SurfaceAttrib mirrors VASurfaceAttrib.
type SurfaceAttrib struct {
	Type  int32
	Flags uint32
	Value GenericValue
}

// IntegerAttrib builds a settable integer surface attribute.
func IntegerAttrib(typ int32, v int32) SurfaceAttrib {
	return SurfaceAttrib{Type: typ, Flags: SurfaceAttribSettable, Value: GenericValue{Type: GenericValueTypeInteger, Value: uint64(uint32(v))}}
}

// Int returns the integer payload of a generic value.
func (g GenericValue) Int() int32 { return int32(uint32(g.Value)) }

// ImageFormat mirrors VAImageFormat.
type ImageFormat struct {
	Fourcc       uint32
	ByteOrder    uint32
	BitsPerPixel uint32
	Depth        uint32
	RedMask      uint32
	GreenMask    uint32
	BlueMask     uint32
	AlphaMask    uint32
	_            [4]uint32
}

// Image mirrors VAImage.
type Image struct {
	ImageID           uint32
	Format            ImageFormat
	Buf               uint32
	Width             uint16
	Height            uint16
	DataSize          uint32
	NumPlanes         uint32
	Pitches           [3]uint32
	Offsets           [3]uint32
	NumPaletteEntries int32
	EntryBytes        int32
	ComponentOrder    [4]int8
	_                 [4]uint32
}

// PictureH264 mirrors VAPictureH264.
type PictureH264 struct {
	PictureID           uint32
	FrameIdx            uint32
	Flags               uint32
	TopFieldOrderCnt    int32
	BottomFieldOrderCnt int32
	_                   [4]uint32
}

// InvalidPictureH264 is the "empty slot" value for reference arrays.
var InvalidPictureH264 = PictureH264{PictureID: InvalidSurface, Flags: PictureH264Invalid}

// PictureParameterBufferH264 mirrors VAPictureParameterBufferH264. SeqFields
// and PicFields hold the C bit-fields packed least-significant bit first.
type PictureParameterBufferH264 struct {
	CurrPic                    PictureH264
	ReferenceFrames            [16]PictureH264
	PictureWidthInMbsMinus1    uint16
	PictureHeightInMbsMinus1   uint16
	BitDepthLumaMinus8         uint8
	BitDepthChromaMinus8       uint8
	NumRefFrames               uint8
	_                          uint8
	SeqFields                  uint32
	NumSliceGroupsMinus1       uint8
	SliceGroupMapType          uint8
	SliceGroupChangeRateMinus1 uint16
	PicInitQpMinus26           int8
	PicInitQsMinus26           int8
	ChromaQpIndexOffset        int8
	SecondChromaQpIndexOffset  int8
	PicFields                  uint32
	FrameNum                   uint16
	_                          uint16
	_                          [8]uint32
}

// Bit positions inside PictureParameterBufferH264.SeqFields.
const (
	SeqChromaFormatIDCShift             = 0 // 2 bits
	SeqResidualColourTransformFlag      = 1 << 2
	SeqGapsInFrameNumValueAllowedFlag   = 1 << 3
	SeqFrameMbsOnlyFlag                 = 1 << 4
	SeqMbAdaptiveFrameFieldFlag         = 1 << 5
	SeqDirect8x8InferenceFlag           = 1 << 6
	SeqMinLumaBiPredSize8x8             = 1 << 7
	SeqLog2MaxFrameNumMinus4Shift       = 8  // 4 bits
	SeqPicOrderCntTypeShift             = 12 // 2 bits
	SeqLog2MaxPicOrderCntLsbMinus4Shift = 14 // 4 bits
	SeqDeltaPicOrderAlwaysZeroFlag      = 1 << 18
)

// Bit positions inside PictureParameterBufferH264.PicFields.
const (
	PicEntropyCodingModeFlag              = 1 << 0
	PicWeightedPredFlag                   = 1 << 1
	PicWeightedBipredIdcShift             = 2 // 2 bits
	PicTransform8x8ModeFlag               = 1 << 4
	PicFieldPicFlag                       = 1 << 5
	PicConstrainedIntraPredFlag           = 1 << 6
	PicPicOrderPresentFlag                = 1 << 7
	PicDeblockingFilterControlPresentFlag = 1 << 8
	PicRedundantPicCntPresentFlag         = 1 << 9
	PicReferencePicFlag                   = 1 << 10
)

// IQMatrixBufferH264 mirrors VAIQMatrixBufferH264 (raster scan order).
type IQMatrixBufferH264 struct {
	ScalingList4x4 [6][16]uint8
	ScalingList8x8 [2][64]uint8
	_              [4]uint32
}

// SliceParameterBufferH264 mirrors VASliceParameterBufferH264.
type SliceParameterBufferH264 struct {
	SliceDataSize              uint32
	SliceDataOffset            uint32
	SliceDataFlag              uint32
	SliceDataBitOffset         uint16
	FirstMbInSlice             uint16
	SliceType                  uint8
	DirectSpatialMvPredFlag    uint8
	NumRefIdxL0ActiveMinus1    uint8
	NumRefIdxL1ActiveMinus1    uint8
	CabacInitIdc               uint8
	SliceQpDelta               int8
	DisableDeblockingFilterIdc uint8
	SliceAlphaC0OffsetDiv2     int8
	SliceBetaOffsetDiv2        int8
	_                          [3]uint8
	RefPicList0                [32]PictureH264
	RefPicList1                [32]PictureH264
	LumaLog2WeightDenom        uint8
	ChromaLog2WeightDenom      uint8
	LumaWeightL0Flag           uint8
	_                          uint8
	LumaWeightL0               [32]int16
	LumaOffsetL0               [32]int16
	ChromaWeightL0Flag         uint8
	_                          uint8
	ChromaWeightL0             [32][2]int16
	ChromaOffsetL0             [32][2]int16
	LumaWeightL1Flag           uint8
	_                          uint8
	LumaWeightL1               [32]int16
	LumaOffsetL1               [32]int16
	ChromaWeightL1Flag         uint8
	_                          uint8
	ChromaWeightL1             [32][2]int16
	ChromaOffsetL1             [32][2]int16
	_                          uint16
	_                          [4]uint32
}
