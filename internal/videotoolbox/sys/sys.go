//go:build darwin

// Package sys binds the CoreFoundation, CoreMedia, CoreVideo and VideoToolbox
// entry points used by the VideoToolbox backend through purego, so that no
// cgo is required.
package sys

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	rtldLazy   = 0x1
	rtldGlobal = 0x8
)

// Four-character codes.
const (
	CodecTypeH264 uint32 = 0x61766331 // 'avc1'
	CodecTypeHEVC uint32 = 0x68766331 // 'hvc1'
	CodecTypeAV1  uint32 = 0x61763031 // 'av01'
	CodecTypeVP9  uint32 = 0x76703039 // 'vp09'

	PixelFormat420YpCbCr8BiPlanarVideoRange uint32 = 0x34323076 // '420v'
	PixelFormat420YpCbCr8BiPlanarFullRange  uint32 = 0x34323066 // '420f'
	PixelFormat32BGRA                       uint32 = 0x42475241 // 'BGRA'
	PixelFormat32RGBA                       uint32 = 0x52474241 // 'RGBA'
)

const (
	cfStringEncodingUTF8 uint32 = 0x08000100
	cfNumberSInt32Type   int    = 3

	// CMTime flags.
	CMTimeFlagsValid uint32 = 1 << 0

	// VTDecodeFrameFlags.
	DecodeFrameEnableAsynchronousDecompression uint32 = 1 << 0
	DecodeFrameDoNotOutputFrame                uint32 = 1 << 1
	DecodeFrameEnableTemporalProcessing        uint32 = 1 << 3

	// CVPixelBufferLockFlags.
	PixelBufferLockReadOnly uint64 = 1
)

// CMTime mirrors the CoreMedia struct (24 bytes).
type CMTime struct {
	Value     int64
	Timescale int32
	Flags     uint32
	Epoch     int64
}

// CMSampleTimingInfo mirrors the CoreMedia struct.
type CMSampleTimingInfo struct {
	Duration              CMTime
	PresentationTimeStamp CMTime
	DecodeTimeStamp       CMTime
}

// VTDecompressionOutputCallbackRecord mirrors the VideoToolbox struct.
type VTDecompressionOutputCallbackRecord struct {
	Callback uintptr
	RefCon   uintptr
}

// Bound functions. They are valid after Load returns nil.
var (
	CFRelease                 func(cf uintptr)
	CFRetain                  func(cf uintptr) uintptr
	CFStringCreateWithCString func(alloc uintptr, cstr string, encoding uint32) uintptr
	CFDictionaryCreateMutable func(alloc uintptr, capacity int, keyCallBacks, valueCallBacks uintptr) uintptr
	CFDictionarySetValue      func(dict, key, value uintptr)
	CFNumberCreate            func(alloc uintptr, numberType int, valuePtr *int32) uintptr
	CFDataCreate              func(alloc uintptr, bytes *byte, length int) uintptr

	// CMVideoFormatDescriptionCreate builds a format description from a codec
	// type, picture size and an extensions dictionary; the AV1 decoder uses
	// it with the av1C record under SampleDescriptionExtensionAtoms.
	CMVideoFormatDescriptionCreate                      func(alloc uintptr, codecType uint32, width, height int32, extensions uintptr, out *uintptr) int32
	CMVideoFormatDescriptionCreateFromH264ParameterSets func(alloc uintptr, count uintptr, pointers *uintptr, sizes *uintptr, nalUnitHeaderLength int32, out *uintptr) int32
	CMVideoFormatDescriptionCreateFromHEVCParameterSets func(alloc uintptr, count uintptr, pointers *uintptr, sizes *uintptr, nalUnitHeaderLength int32, extensions uintptr, out *uintptr) int32
	CMBlockBufferCreateWithMemoryBlock                  func(alloc uintptr, memoryBlock uintptr, blockLength uintptr, blockAllocator uintptr, customBlockSource uintptr, offsetToData uintptr, dataLength uintptr, flags uint32, out *uintptr) int32
	CMBlockBufferReplaceDataBytes                       func(source *byte, dest uintptr, offset uintptr, length uintptr) int32
	CMSampleBufferCreateReady                           func(alloc uintptr, dataBuffer uintptr, formatDescription uintptr, numSamples int, numTiming int, timing *CMSampleTimingInfo, numSizes int, sizes *uintptr, out *uintptr) int32

	CVPixelBufferLockBaseAddress       func(pb uintptr, flags uint64) int32
	CVPixelBufferUnlockBaseAddress     func(pb uintptr, flags uint64) int32
	CVPixelBufferGetWidth              func(pb uintptr) uintptr
	CVPixelBufferGetHeight             func(pb uintptr) uintptr
	CVPixelBufferGetPixelFormatType    func(pb uintptr) uint32
	CVPixelBufferGetPlaneCount         func(pb uintptr) uintptr
	CVPixelBufferGetBaseAddress        func(pb uintptr) *byte
	CVPixelBufferGetBytesPerRow        func(pb uintptr) uintptr
	CVPixelBufferGetBaseAddressOfPlane func(pb uintptr, plane uintptr) *byte
	CVPixelBufferGetBytesPerRowOfPlane func(pb uintptr, plane uintptr) uintptr
	CVPixelBufferGetWidthOfPlane       func(pb uintptr, plane uintptr) uintptr
	CVPixelBufferGetHeightOfPlane      func(pb uintptr, plane uintptr) uintptr

	VTDecompressionSessionCreate                     func(alloc uintptr, formatDescription uintptr, decoderSpecification uintptr, destinationAttributes uintptr, callback *VTDecompressionOutputCallbackRecord, out *uintptr) int32
	VTDecompressionSessionDecodeFrame                func(session uintptr, sampleBuffer uintptr, flags uint32, sourceFrameRefCon uintptr, infoFlagsOut *uint32) int32
	VTDecompressionSessionFinishDelayedFrames        func(session uintptr) int32
	VTDecompressionSessionWaitForAsynchronousFrames  func(session uintptr) int32
	VTDecompressionSessionInvalidate                 func(session uintptr)
	VTDecompressionSessionCanAcceptFormatDescription func(session uintptr, formatDescription uintptr) uint8
	VTIsHardwareDecodeSupported                      func(codecType uint32) uint8
	// VTRegisterSupplementalVideoDecoderIfAvailable is nil on systems that do
	// not export it (it exists on macOS 11 and later).
	VTRegisterSupplementalVideoDecoderIfAvailable func(codecType uint32)
)

// Constants read from the frameworks (CFStringRef values and callback
// structure addresses).
var (
	KCFBooleanTrue                   uintptr
	KCFBooleanFalse                  uintptr
	KCFTypeDictionaryKeyCallBacks    uintptr
	KCFTypeDictionaryValueCallBacks  uintptr
	KCVPixelBufferPixelFormatTypeKey uintptr
	KVTRequireHardwareDecoder        uintptr
	KVTEnableHardwareDecoder         uintptr
	// KCMFormatDescriptionExtensionSampleDescriptionExtensionAtoms keys the
	// dictionary of ISOBMFF sample description atoms (such as "av1C") in a
	// format description's extensions.
	KCMFormatDescriptionExtensionSampleDescriptionExtensionAtoms uintptr
)

var (
	loadOnce sync.Once
	loadErr  error
)

// Load opens the frameworks and binds every symbol. It is safe to call
// repeatedly; the result is cached.
func Load() error {
	loadOnce.Do(func() { loadErr = load() })
	return loadErr
}

type binder struct {
	handle uintptr
	lib    string
	errs   []string
}

func (b *binder) fn(fptr any, name string) {
	addr, err := purego.Dlsym(b.handle, name)
	if err != nil {
		b.errs = append(b.errs, fmt.Sprintf("%s: %s", b.lib, name))
		return
	}
	purego.RegisterFunc(fptr, addr)
}

func (b *binder) ptrConst(dst *uintptr, name string) {
	addr, err := purego.Dlsym(b.handle, name)
	if err != nil {
		b.errs = append(b.errs, fmt.Sprintf("%s: %s", b.lib, name))
		return
	}
	*dst = *(*uintptr)(unsafe.Add(unsafe.Pointer(nil), addr))
}

// optPtrConst is ptrConst for symbols that older macOS releases lack; the
// destination stays 0 when the symbol is absent.
func (b *binder) optPtrConst(dst *uintptr, name string) {
	addr, err := purego.Dlsym(b.handle, name)
	if err != nil {
		return
	}
	*dst = *(*uintptr)(unsafe.Add(unsafe.Pointer(nil), addr))
}

func (b *binder) addrConst(dst *uintptr, name string) {
	addr, err := purego.Dlsym(b.handle, name)
	if err != nil {
		b.errs = append(b.errs, fmt.Sprintf("%s: %s", b.lib, name))
		return
	}
	*dst = addr
}

func open(path string) (*binder, error) {
	h, err := purego.Dlopen(path, rtldLazy|rtldGlobal)
	if err != nil {
		return nil, fmt.Errorf("videotoolbox: dlopen %s: %w", path, err)
	}
	return &binder{handle: h, lib: path}, nil
}

func load() error {
	cf, err := open("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
	if err != nil {
		return err
	}
	cm, err := open("/System/Library/Frameworks/CoreMedia.framework/CoreMedia")
	if err != nil {
		return err
	}
	cv, err := open("/System/Library/Frameworks/CoreVideo.framework/CoreVideo")
	if err != nil {
		return err
	}
	vt, err := open("/System/Library/Frameworks/VideoToolbox.framework/VideoToolbox")
	if err != nil {
		return err
	}

	cf.fn(&CFRelease, "CFRelease")
	cf.fn(&CFRetain, "CFRetain")
	cf.fn(&CFStringCreateWithCString, "CFStringCreateWithCString")
	cf.fn(&CFDictionaryCreateMutable, "CFDictionaryCreateMutable")
	cf.fn(&CFDictionarySetValue, "CFDictionarySetValue")
	cf.fn(&CFNumberCreate, "CFNumberCreate")
	cf.fn(&CFDataCreate, "CFDataCreate")
	cf.ptrConst(&KCFBooleanTrue, "kCFBooleanTrue")
	cf.ptrConst(&KCFBooleanFalse, "kCFBooleanFalse")
	cf.addrConst(&KCFTypeDictionaryKeyCallBacks, "kCFTypeDictionaryKeyCallBacks")
	cf.addrConst(&KCFTypeDictionaryValueCallBacks, "kCFTypeDictionaryValueCallBacks")

	cm.fn(&CMVideoFormatDescriptionCreate, "CMVideoFormatDescriptionCreate")
	cm.ptrConst(&KCMFormatDescriptionExtensionSampleDescriptionExtensionAtoms, "kCMFormatDescriptionExtension_SampleDescriptionExtensionAtoms")
	cm.fn(&CMVideoFormatDescriptionCreateFromH264ParameterSets, "CMVideoFormatDescriptionCreateFromH264ParameterSets")
	cm.fn(&CMVideoFormatDescriptionCreateFromHEVCParameterSets, "CMVideoFormatDescriptionCreateFromHEVCParameterSets")
	cm.fn(&CMBlockBufferCreateWithMemoryBlock, "CMBlockBufferCreateWithMemoryBlock")
	cm.fn(&CMBlockBufferReplaceDataBytes, "CMBlockBufferReplaceDataBytes")
	cm.fn(&CMSampleBufferCreateReady, "CMSampleBufferCreateReady")

	cv.fn(&CVPixelBufferLockBaseAddress, "CVPixelBufferLockBaseAddress")
	cv.fn(&CVPixelBufferUnlockBaseAddress, "CVPixelBufferUnlockBaseAddress")
	cv.fn(&CVPixelBufferGetWidth, "CVPixelBufferGetWidth")
	cv.fn(&CVPixelBufferGetHeight, "CVPixelBufferGetHeight")
	cv.fn(&CVPixelBufferGetPixelFormatType, "CVPixelBufferGetPixelFormatType")
	cv.fn(&CVPixelBufferGetPlaneCount, "CVPixelBufferGetPlaneCount")
	cv.fn(&CVPixelBufferGetBaseAddress, "CVPixelBufferGetBaseAddress")
	cv.fn(&CVPixelBufferGetBytesPerRow, "CVPixelBufferGetBytesPerRow")
	cv.fn(&CVPixelBufferGetBaseAddressOfPlane, "CVPixelBufferGetBaseAddressOfPlane")
	cv.fn(&CVPixelBufferGetBytesPerRowOfPlane, "CVPixelBufferGetBytesPerRowOfPlane")
	cv.fn(&CVPixelBufferGetWidthOfPlane, "CVPixelBufferGetWidthOfPlane")
	cv.fn(&CVPixelBufferGetHeightOfPlane, "CVPixelBufferGetHeightOfPlane")
	cv.ptrConst(&KCVPixelBufferPixelFormatTypeKey, "kCVPixelBufferPixelFormatTypeKey")

	vt.fn(&VTDecompressionSessionCreate, "VTDecompressionSessionCreate")
	vt.fn(&VTDecompressionSessionDecodeFrame, "VTDecompressionSessionDecodeFrame")
	vt.fn(&VTDecompressionSessionFinishDelayedFrames, "VTDecompressionSessionFinishDelayedFrames")
	vt.fn(&VTDecompressionSessionWaitForAsynchronousFrames, "VTDecompressionSessionWaitForAsynchronousFrames")
	vt.fn(&VTDecompressionSessionInvalidate, "VTDecompressionSessionInvalidate")
	vt.fn(&VTDecompressionSessionCanAcceptFormatDescription, "VTDecompressionSessionCanAcceptFormatDescription")
	vt.fn(&VTIsHardwareDecodeSupported, "VTIsHardwareDecodeSupported")
	vt.ptrConst(&KVTRequireHardwareDecoder, "kVTVideoDecoderSpecification_RequireHardwareAcceleratedVideoDecoder")
	vt.ptrConst(&KVTEnableHardwareDecoder, "kVTVideoDecoderSpecification_EnableHardwareAcceleratedVideoDecoder")
	if addr, err := purego.Dlsym(vt.handle, "VTRegisterSupplementalVideoDecoderIfAvailable"); err == nil {
		purego.RegisterFunc(&VTRegisterSupplementalVideoDecoderIfAvailable, addr)
	}

	bindEncode(cf, cm, cv, vt)

	var missing []string
	for _, b := range []*binder{cf, cm, cv, vt} {
		missing = append(missing, b.errs...)
	}
	if len(missing) > 0 {
		return errors.New("videotoolbox: missing symbols: " + strings.Join(missing, ", "))
	}
	return nil
}

// CFString creates a CFStringRef from s. The caller owns the result.
func CFString(s string) uintptr {
	return CFStringCreateWithCString(0, s, cfStringEncodingUTF8)
}

// CFNumberInt32 creates a CFNumberRef. The caller owns the result.
func CFNumberInt32(v int32) uintptr {
	return CFNumberCreate(0, cfNumberSInt32Type, &v)
}

// CFData creates a CFDataRef holding a copy of b. The caller owns the result.
func CFData(b []byte) uintptr {
	if len(b) == 0 {
		return CFDataCreate(0, nil, 0)
	}
	d := CFDataCreate(0, &b[0], len(b))
	runtime.KeepAlive(b)
	return d
}

// NewDictionary creates a mutable CFDictionary with CFType callbacks.
func NewDictionary() uintptr {
	return CFDictionaryCreateMutable(0, 0, KCFTypeDictionaryKeyCallBacks, KCFTypeDictionaryValueCallBacks)
}

// Release calls CFRelease when cf is non-zero.
func Release(cf uintptr) {
	if cf != 0 {
		CFRelease(cf)
	}
}

// OSStatus codes from VideoToolbox (VTErrors.h) and CoreMedia.
const (
	StatusVTPropertyNotSupported            = -12900
	StatusVTPropertyReadOnly                = -12901
	StatusVTParameter                       = -12902
	StatusVTInvalidSession                  = -12903
	StatusVTAllocationFailed                = -12904
	StatusVTPixelTransferNotSupported       = -12905
	StatusVTCouldNotFindVideoDecoder        = -12906
	StatusVTCouldNotCreateInstance          = -12907
	StatusVTCouldNotFindVideoEncoder        = -12908
	StatusVTVideoDecoderBadData             = -12909
	StatusVTVideoDecoderUnsupportedDataFmt  = -12910
	StatusVTVideoDecoderMalfunction         = -12911
	StatusVTVideoEncoderMalfunction         = -12912
	StatusVTVideoDecoderNotAvailableNow     = -12913
	StatusVTImageRotationNotSupported       = -12914
	StatusVTVideoEncoderNotAvailableNow     = -12915
	StatusVTFormatDescriptionChangeNotSupp  = -12916
	StatusVTInsufficientSourceColorData     = -12917
	StatusVTCouldNotCreateColorCorrection   = -12918
	StatusVTColorSyncTransformConvertFailed = -12919
	StatusVTVideoDecoderAuthorization       = -12210
	StatusVTVideoEncoderAuthorization       = -12211
	StatusVTColorCorrectionPixelTransfer    = -12212
	StatusVTMultiPassStorageIdentifierMism  = -12213
	StatusVTMultiPassStorageInvalid         = -12214
	StatusVTFrameSiloInvalidTimeStamp       = -12215
	StatusVTFrameSiloInvalidTimeRange       = -12216
	StatusVTCouldNotFindTemporalFilter      = -12217
	StatusVTPixelTransferNotPermitted       = -12218
	StatusVTColorCorrectionImageRotation    = -12219
	StatusVTVideoDecoderRemoved             = -17690
	StatusVTSessionMalfunction              = -17691
	StatusVTVideoDecoderNeedsRosetta        = -17692
	StatusVTVideoEncoderNeedsRosetta        = -17693
	StatusVTVideoDecoderReferenceMissing    = -17694
	StatusVTVideoDecoderCallbackMessaging   = -17695
	StatusVTVideoDecoderUnknown             = -17696
	StatusVTExtensionDisabled               = -17697
	StatusVTVideoEncoderMVHEVCNotSupported  = -17698
)

var statusNames = map[int32]string{
	StatusVTPropertyNotSupported:           "property not supported",
	StatusVTPropertyReadOnly:               "property is read-only",
	StatusVTParameter:                      "invalid parameter",
	StatusVTInvalidSession:                 "invalid session",
	StatusVTAllocationFailed:               "allocation failed",
	StatusVTPixelTransferNotSupported:      "pixel transfer not supported",
	StatusVTCouldNotFindVideoDecoder:       "could not find a video decoder",
	StatusVTCouldNotCreateInstance:         "could not create instance",
	StatusVTCouldNotFindVideoEncoder:       "could not find a video encoder",
	StatusVTVideoDecoderBadData:            "decoder rejected the data",
	StatusVTVideoDecoderUnsupportedDataFmt: "decoder does not support this data format",
	StatusVTVideoDecoderMalfunction:        "decoder malfunction",
	StatusVTVideoDecoderNotAvailableNow:    "decoder not available now",
	StatusVTFormatDescriptionChangeNotSupp: "format description change not supported",
	StatusVTVideoDecoderAuthorization:      "decoder authorization error",
	StatusVTVideoDecoderRemoved:            "decoder removed",
	StatusVTSessionMalfunction:             "session malfunction",
	StatusVTVideoDecoderReferenceMissing:   "reference frame missing",
	StatusVTVideoDecoderUnknown:            "unknown decoder error",
	StatusVTVideoEncoderMalfunction:        "encoder malfunction",
	StatusVTVideoEncoderNotAvailableNow:    "encoder not available now",
	StatusVTVideoEncoderAuthorization:      "encoder authorization error",
}

// StatusString describes an OSStatus value.
func StatusString(status int32) string {
	if s, ok := statusNames[status]; ok {
		return s
	}
	return fmt.Sprintf("OSStatus %d", status)
}
