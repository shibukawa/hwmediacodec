// Package sys binds the Intel VPL dispatcher (libvpl) through purego so that
// no cgo is required. The dispatcher loads the GPU runtime (libmfx-gen for
// Tiger Lake and newer, the legacy Media SDK runtime libmfxhw64 for older
// GPUs) and forwards the MFXVideo* calls to it.
//
// The struct definitions mirror the VPL headers (vpl/mfx*.h, API 2.8);
// layout_test.go checks their sizes and field offsets against values
// measured with a C compiler for x86_64-linux-gnu. Several structures are
// declared with #pragma pack(4) in C; the mirrors only use field orders in
// which Go's natural alignment produces the same layout.
//
// Structures the runtime keeps a pointer to between calls (FrameSurface,
// Bitstream) hold their buffer addresses as uintptr, so the garbage collector
// never scans memory the runtime's threads write to; the Go side keeps the
// buffers alive separately.
package sys

import (
	"fmt"
	"unsafe"
)

// mfxStatus values. Negative values are errors, positive values warnings.
const (
	ErrNone                   int32 = 0
	ErrUnknown                int32 = -1
	ErrNullPtr                int32 = -2
	ErrUnsupported            int32 = -3
	ErrMemoryAlloc            int32 = -4
	ErrNotEnoughBuffer        int32 = -5
	ErrInvalidHandle          int32 = -6
	ErrLockMemory             int32 = -7
	ErrNotInitialized         int32 = -8
	ErrNotFound               int32 = -9
	ErrMoreData               int32 = -10
	ErrMoreSurface            int32 = -11
	ErrAborted                int32 = -12
	ErrDeviceLost             int32 = -13
	ErrIncompatibleVideoParam int32 = -14
	ErrInvalidVideoParam      int32 = -15
	ErrUndefinedBehavior      int32 = -16
	ErrDeviceFailed           int32 = -17
	ErrMoreBitstream          int32 = -18
	ErrGPUHang                int32 = -21
	ErrReallocSurface         int32 = -22
	ErrResourceMapped         int32 = -23
	ErrNotImplemented         int32 = -24

	WrnInExecution            int32 = 1
	WrnDeviceBusy             int32 = 2
	WrnVideoParamChanged      int32 = 3
	WrnPartialAcceleration    int32 = 4
	WrnIncompatibleVideoParam int32 = 5
	WrnValueNotChanged        int32 = 6
	WrnOutOfRange             int32 = 7
)

var statusNames = map[int32]string{
	ErrNone:                   "MFX_ERR_NONE",
	ErrUnknown:                "MFX_ERR_UNKNOWN",
	ErrNullPtr:                "MFX_ERR_NULL_PTR",
	ErrUnsupported:            "MFX_ERR_UNSUPPORTED",
	ErrMemoryAlloc:            "MFX_ERR_MEMORY_ALLOC",
	ErrNotEnoughBuffer:        "MFX_ERR_NOT_ENOUGH_BUFFER",
	ErrInvalidHandle:          "MFX_ERR_INVALID_HANDLE",
	ErrLockMemory:             "MFX_ERR_LOCK_MEMORY",
	ErrNotInitialized:         "MFX_ERR_NOT_INITIALIZED",
	ErrNotFound:               "MFX_ERR_NOT_FOUND",
	ErrMoreData:               "MFX_ERR_MORE_DATA",
	ErrMoreSurface:            "MFX_ERR_MORE_SURFACE",
	ErrAborted:                "MFX_ERR_ABORTED",
	ErrDeviceLost:             "MFX_ERR_DEVICE_LOST",
	ErrIncompatibleVideoParam: "MFX_ERR_INCOMPATIBLE_VIDEO_PARAM",
	ErrInvalidVideoParam:      "MFX_ERR_INVALID_VIDEO_PARAM",
	ErrUndefinedBehavior:      "MFX_ERR_UNDEFINED_BEHAVIOR",
	ErrDeviceFailed:           "MFX_ERR_DEVICE_FAILED",
	ErrMoreBitstream:          "MFX_ERR_MORE_BITSTREAM",
	ErrGPUHang:                "MFX_ERR_GPU_HANG",
	ErrReallocSurface:         "MFX_ERR_REALLOC_SURFACE",
	ErrResourceMapped:         "MFX_ERR_RESOURCE_MAPPED",
	ErrNotImplemented:         "MFX_ERR_NOT_IMPLEMENTED",
	WrnInExecution:            "MFX_WRN_IN_EXECUTION",
	WrnDeviceBusy:             "MFX_WRN_DEVICE_BUSY",
	WrnVideoParamChanged:      "MFX_WRN_VIDEO_PARAM_CHANGED",
	WrnPartialAcceleration:    "MFX_WRN_PARTIAL_ACCELERATION",
	WrnIncompatibleVideoParam: "MFX_WRN_INCOMPATIBLE_VIDEO_PARAM",
	WrnValueNotChanged:        "MFX_WRN_VALUE_NOT_CHANGED",
	WrnOutOfRange:             "MFX_WRN_OUT_OF_RANGE",
}

// StatusString names an mfxStatus.
func StatusString(st int32) string {
	if s, ok := statusNames[st]; ok {
		return s
	}
	return fmt.Sprintf("mfxStatus %d", st)
}

// VariantU32 returns the two machine words of an mfxVariant holding a 32-bit
// unsigned value. MFXSetConfigFilterProperty takes the 16-byte structure by
// value; both the System V x86-64 and the AAPCS64 calling conventions pass
// such a structure in two consecutive integer registers, which is exactly
// how two uint64 arguments are passed.
func VariantU32(v uint32) (lo, hi uint64) {
	return uint64(VariantVersion) | uint64(VariantTypeU32)<<32, uint64(v)
}

func fourCC(a, b, c, d byte) uint32 {
	return uint32(a) | uint32(b)<<8 | uint32(c)<<16 | uint32(d)<<24
}

// Codec identifiers (mfxInfoMFX.CodecId), colour formats (mfxFrameInfo.FourCC)
// and extension buffer identifiers (mfxExtBuffer.BufferId).
var (
	CodecAVC  = fourCC('A', 'V', 'C', ' ')
	CodecHEVC = fourCC('H', 'E', 'V', 'C')
	CodecAV1  = fourCC('A', 'V', '1', ' ')

	FourCCNV12 = fourCC('N', 'V', '1', '2')

	ExtBuffCodingOption       = fourCC('C', 'D', 'O', 'P')
	ExtBuffCodingOption2      = fourCC('C', 'D', 'O', '2')
	ExtBuffCodingOption3      = fourCC('C', 'D', 'O', '3')
	ExtBuffVideoSignalInfo    = fourCC('V', 'S', 'I', 'N')
	ExtBuffCodingOptionSPSPPS = fourCC('C', 'O', 'S', 'P')
	ExtBuffCodingOptionVPS    = fourCC('C', 'O', 'V', 'P')
)

const (
	// mfxImplType, used as the "mfxImplDescription.Impl" dispatcher filter.
	ImplTypeSoftware uint32 = 1
	ImplTypeHardware uint32 = 2

	// mfxVariantType / mfxVariant.Version.
	VariantTypeU32 uint32 = 5
	VariantVersion uint16 = 1 << 8 // MFX_STRUCT_VERSION(1, 0)

	// mfxHandleType.
	HandleVADisplay int32 = 4

	// mfxFrameInfo.ChromaFormat and PicStruct.
	ChromaFormatYUV420    uint16 = 1
	PicStructUnknown      uint16 = 0
	PicStructProgressive  uint16 = 1
	IOPatternInSystemMem  uint16 = 0x02
	IOPatternOutSystemMem uint16 = 0x20

	// TimestampUnknown is MFX_TIMESTAMP_UNKNOWN: "no time stamp".
	TimestampUnknown uint64 = 1<<64 - 1

	// mfxBitstream.DataFlag.
	BitstreamCompleteFrame uint16 = 0x0001

	// Profiles (mfxInfoMFX.CodecProfile).
	ProfileAVCBaseline            uint16 = 66
	ProfileAVCConstrainedBaseline uint16 = 66 + 0x200
	ProfileAVCMain                uint16 = 77
	ProfileAVCHigh                uint16 = 100
	ProfileHEVCMain               uint16 = 1
	ProfileAV1Main                uint16 = 1

	// mfxInfoMFX.GopOptFlag, TargetUsage and RateControlMethod.
	GopClosed           uint16 = 1
	GopStrict           uint16 = 2
	TargetUsageBalanced uint16 = 4
	RateControlCBR      uint16 = 1
	RateControlVBR      uint16 = 2
	RateControlCQP      uint16 = 3

	// Tri-state coding options.
	CodingOptionUnknown uint16 = 0
	CodingOptionOn      uint16 = 0x10
	CodingOptionOff     uint16 = 0x20

	// mfxExtCodingOption2.BRefType.
	BRefOff uint16 = 1

	// Frame types (mfxEncodeCtrl.FrameType, mfxBitstream.FrameType).
	FrameTypeI   uint16 = 0x0001
	FrameTypeP   uint16 = 0x0002
	FrameTypeB   uint16 = 0x0004
	FrameTypeRef uint16 = 0x0040
	FrameTypeIDR uint16 = 0x0080

	// Infinite is the MFXVideoCORE_SyncOperation wait that never times out.
	Infinite uint32 = 0xFFFFFFFF
)

// Version mirrors mfxVersion.
type Version struct {
	Minor uint16
	Major uint16
}

// ExtBuffer mirrors mfxExtBuffer, the header of every extension buffer.
type ExtBuffer struct {
	BufferID uint32
	BufferSz uint32
}

// FrameInfo mirrors mfxFrameInfo (the Width/Height/Crop variant of its
// union).
type FrameInfo struct {
	_              [4]uint32
	ChannelID      uint16
	BitDepthLuma   uint16
	BitDepthChroma uint16
	Shift          uint16
	FrameID        [4]uint16 // mfxFrameId
	FourCC         uint32
	Width          uint16
	Height         uint16
	CropX          uint16
	CropY          uint16
	CropW          uint16
	CropH          uint16
	FrameRateExtN  uint32
	FrameRateExtD  uint32
	_              uint16
	AspectRatioW   uint16
	AspectRatioH   uint16
	PicStruct      uint16
	ChromaFormat   uint16
	_              uint16
}

// InfoMFX mirrors mfxInfoMFX. The codec-specific union is laid out with the
// encoder's field names; a decoder leaves those thirteen words zero
// (DecodedOrder, ExtendedPicStruct, TimeStampCalc, SliceGroupsPresent,
// MaxDecFrameBuffering, EnableReallocRequest, ...).
type InfoMFX struct {
	_                  [7]uint32
	LowPower           uint16
	BRCParamMultiplier uint16
	FrameInfo          FrameInfo
	CodecID            uint32
	CodecProfile       uint16
	CodecLevel         uint16
	NumThread          uint16

	TargetUsage       uint16
	GopPicSize        uint16
	GopRefDist        uint16
	GopOptFlag        uint16
	IdrInterval       uint16
	RateControlMethod uint16
	InitialDelayInKB  uint16 // QPI for constant QP
	BufferSizeInKB    uint16
	TargetKbps        uint16 // QPP for constant QP
	MaxKbps           uint16 // QPB for constant QP
	NumSlice          uint16
	NumRefFrame       uint16
	EncodedOrder      uint16
}

// VideoParam mirrors mfxVideoParam. The mfx/vpp union is 168 bytes (the
// size of mfxInfoVPP); only the mfx member is used.
type VideoParam struct {
	AllocID     uint32
	_           [2]uint32
	_           uint16
	AsyncDepth  uint16
	MFX         InfoMFX
	_           [32]byte
	Protected   uint16
	IOPattern   uint16
	ExtParam    unsafe.Pointer // mfxExtBuffer **
	NumExtParam uint16
	_           uint16
}

// Bitstream mirrors mfxBitstream. Data is a uintptr because encoders write
// to the structure from their own threads until the sync point completes.
type Bitstream struct {
	EncryptedData   uintptr
	ExtParam        uintptr
	NumExtParam     uint16
	_               uint16
	CodecID         uint32
	DecodeTimeStamp int64
	TimeStamp       uint64
	Data            uintptr
	DataOffset      uint32
	DataLength      uint32
	MaxLength       uint32
	PicStruct       uint16
	FrameType       uint16
	DataFlag        uint16
	_               uint16
}

// FrameData mirrors mfxFrameData. For NV12, Y points at the luma plane and
// UV at the interleaved chroma plane; both use Pitch.
type FrameData struct {
	ExtParam    uintptr
	NumExtParam uint16
	_           [9]uint16
	MemType     uint16
	PitchHigh   uint16
	TimeStamp   uint64
	FrameOrder  uint32
	Locked      uint16
	Pitch       uint16 // PitchLow
	Y           uintptr
	UV          uintptr
	V           uintptr
	A           uintptr
	MemID       uintptr
	Corrupted   uint16
	DataFlag    uint16
}

// FrameSurface mirrors mfxFrameSurface1. Data.Locked is the runtime's
// reference count: a surface may be handed out again only when it is zero.
type FrameSurface struct {
	FrameInterface uintptr
	Version        uint16
	_              [3]uint16
	Info           FrameInfo
	Data           FrameData
}

// FrameAllocRequest mirrors mfxFrameAllocRequest, the result of the
// QueryIOSurf functions.
type FrameAllocRequest struct {
	AllocID           uint32
	_                 [3]uint32
	Info              FrameInfo
	Type              uint16
	NumFrameMin       uint16
	NumFrameSuggested uint16
	_                 uint16
}

// EncodeCtrl mirrors mfxEncodeCtrl, the per-frame encoder control.
type EncodeCtrl struct {
	Header         ExtBuffer
	_              [4]uint32
	_              uint16
	MfxNalUnitType uint16
	SkipFrame      uint16
	QP             uint16
	FrameType      uint16
	NumExtParam    uint16
	NumPayload     uint16
	_              uint16
	ExtParam       uintptr
	Payload        uintptr
}

// ExtCodingOption mirrors mfxExtCodingOption.
type ExtCodingOption struct {
	Header               ExtBuffer
	_                    uint16
	RateDistortionOpt    uint16
	MECostType           uint16
	MESearchType         uint16
	MVSearchWindow       [2]int16
	EndOfSequence        uint16
	FramePicture         uint16
	CAVLC                uint16
	_                    [2]uint16
	RecoveryPointSEI     uint16
	ViewOutput           uint16
	NalHrdConformance    uint16
	SingleSeiNalUnit     uint16
	VuiVclHrdParameters  uint16
	RefPicListReordering uint16
	ResetRefList         uint16
	RefPicMarkRep        uint16
	FieldOutput          uint16
	IntraPredBlockSize   uint16
	InterPredBlockSize   uint16
	MVPrecision          uint16
	MaxDecFrameBuffering uint16
	AUDelimiter          uint16
	EndOfStream          uint16
	PicTimingSEI         uint16
	VuiNalHrdParameters  uint16
}

// ExtCodingOption2 mirrors mfxExtCodingOption2.
type ExtCodingOption2 struct {
	Header               ExtBuffer
	IntRefType           uint16
	IntRefCycleSize      uint16
	IntRefQPDelta        int16
	MaxFrameSize         uint32
	MaxSliceSize         uint32
	BitrateLimit         uint16
	MBBRC                uint16
	ExtBRC               uint16
	LookAheadDepth       uint16
	Trellis              uint16
	RepeatPPS            uint16
	BRefType             uint16
	AdaptiveI            uint16
	AdaptiveB            uint16
	LookAheadDS          uint16
	NumMbPerSlice        uint16
	SkipFrame            uint16
	MinQPI               uint8
	MaxQPI               uint8
	MinQPP               uint8
	MaxQPP               uint8
	MinQPB               uint8
	MaxQPB               uint8
	FixedFrameRate       uint16
	DisableDeblockingIdc uint16
	DisableVUI           uint16
	BufferingPeriodSEI   uint16
	EnableMAD            uint16
	UseRawRef            uint16
}

// ExtCodingOption3 mirrors mfxExtCodingOption3 as far as the backend uses
// it: GPB selects generalised P/B pictures (B slices without backward
// references) in place of P pictures in HEVC.
type ExtCodingOption3 struct {
	Header ExtBuffer
	_      [29]uint16
	GPB    uint16
	_      [444]byte
}

// ExtVideoSignalInfo mirrors mfxExtVideoSignalInfo: the video signal type
// fields of the VUI.
type ExtVideoSignalInfo struct {
	Header                   ExtBuffer
	VideoFormat              uint16
	VideoFullRange           uint16
	ColourDescriptionPresent uint16
	ColourPrimaries          uint16
	TransferCharacteristics  uint16
	MatrixCoefficients       uint16
}

// ExtCodingOptionSPSPPS mirrors mfxExtCodingOptionSPSPPS; attached to
// MFXVideoENCODE_GetVideoParam it returns the encoder's SPS and PPS.
type ExtCodingOptionSPSPPS struct {
	Header     ExtBuffer
	SPSBuffer  unsafe.Pointer
	PPSBuffer  unsafe.Pointer
	SPSBufSize uint16
	PPSBufSize uint16
	SPSID      uint16
	PPSID      uint16
}

// ExtCodingOptionVPS mirrors mfxExtCodingOptionVPS (HEVC).
type ExtCodingOptionVPS struct {
	Header     ExtBuffer
	VPSBuffer  unsafe.Pointer
	VPSBufSize uint16
	VPSID      uint16
	_          [6]uint16
}
