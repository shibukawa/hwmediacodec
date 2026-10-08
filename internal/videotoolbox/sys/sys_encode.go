//go:build darwin

package sys

// Bindings used by the encoder. They are bound by Load together with the
// decoder's.

const (
	cfNumberFloat32Type int = 5
	cfNumberFloat64Type int = 6

	// VTEncodeInfoFlags.
	EncodeInfoAsynchronous uint32 = 1 << 0
	EncodeInfoFrameDropped uint32 = 1 << 1
)

// Bound functions. They are valid after Load returns nil.
var (
	CFNumberCreateFloat32 func(alloc uintptr, numberType int, valuePtr *float32) uintptr
	CFNumberCreateFloat64 func(alloc uintptr, numberType int, valuePtr *float64) uintptr

	CMSampleBufferGetDataBuffer                        func(sample uintptr) uintptr
	CMSampleBufferGetFormatDescription                 func(sample uintptr) uintptr
	CMSampleBufferGetSampleTimingInfo                  func(sample uintptr, index int, out *CMSampleTimingInfo) int32
	CMBlockBufferGetDataLength                         func(block uintptr) uintptr
	CMBlockBufferCopyDataBytes                         func(block uintptr, offset, length uintptr, dst *byte) int32
	CMVideoFormatDescriptionGetH264ParameterSetAtIndex func(fd uintptr, index uintptr, ptr **byte, size *uintptr, count *uintptr, nalUnitHeaderLength *int32) int32
	CMVideoFormatDescriptionGetHEVCParameterSetAtIndex func(fd uintptr, index uintptr, ptr **byte, size *uintptr, count *uintptr, nalUnitHeaderLength *int32) int32

	CVPixelBufferCreate                func(alloc uintptr, width, height uintptr, pixelFormat uint32, attrs uintptr, out *uintptr) int32
	CVPixelBufferPoolCreatePixelBuffer func(alloc uintptr, pool uintptr, out *uintptr) int32

	VTCompressionSessionCreate                  func(alloc uintptr, width, height int32, codecType uint32, encoderSpecification, sourceImageBufferAttributes, compressedDataAllocator uintptr, callback uintptr, refCon uintptr, out *uintptr) int32
	VTCompressionSessionPrepareToEncodeFrames   func(session uintptr) int32
	VTCompressionSessionEncodeFrame             func(session, imageBuffer uintptr, presentationTimeStamp CMTime, duration CMTime, frameProperties uintptr, sourceFrameRefCon uintptr, infoFlagsOut *uint32) int32
	VTCompressionSessionCompleteFrames          func(session uintptr, completeUntilPresentationTimeStamp CMTime) int32
	VTCompressionSessionInvalidate              func(session uintptr)
	VTCompressionSessionGetPixelBufferPool      func(session uintptr) uintptr
	VTSessionSetProperty                        func(session, key, value uintptr) int32
	VTCopySupportedPropertyDictionaryForEncoder func(width, height int32, codecType uint32, encoderSpecification uintptr, encoderIDOut *uintptr, supportedPropertiesOut *uintptr) int32
)

// CFStringRef constants read from the frameworks. The ones marked optional
// are 0 on macOS releases that predate them.
var (
	KCVPixelBufferWidthKey  uintptr
	KCVPixelBufferHeightKey uintptr

	KVTEnableHardwareEncoder       uintptr
	KVTRequireHardwareEncoder      uintptr
	KVTEnableLowLatencyRateControl uintptr // optional, macOS 11.3

	KVTAverageBitRate              uintptr
	KVTConstantBitRate             uintptr // optional, macOS 13
	KVTQuality                     uintptr
	KVTRealTime                    uintptr
	KVTAllowFrameReordering        uintptr
	KVTMaxKeyFrameInterval         uintptr
	KVTMaxKeyFrameIntervalDuration uintptr
	KVTExpectedFrameRate           uintptr
	KVTProfileLevel                uintptr
	KVTForceKeyFrame               uintptr

	KVTProfileH264Baseline uintptr
	KVTProfileH264Main     uintptr
	KVTProfileH264High     uintptr
	KVTProfileHEVCMain     uintptr
)

func bindEncode(cf, cm, cv, vt *binder) {
	cf.fn(&CFNumberCreateFloat32, "CFNumberCreate")
	cf.fn(&CFNumberCreateFloat64, "CFNumberCreate")

	cm.fn(&CMSampleBufferGetDataBuffer, "CMSampleBufferGetDataBuffer")
	cm.fn(&CMSampleBufferGetFormatDescription, "CMSampleBufferGetFormatDescription")
	cm.fn(&CMSampleBufferGetSampleTimingInfo, "CMSampleBufferGetSampleTimingInfo")
	cm.fn(&CMBlockBufferGetDataLength, "CMBlockBufferGetDataLength")
	cm.fn(&CMBlockBufferCopyDataBytes, "CMBlockBufferCopyDataBytes")
	cm.fn(&CMVideoFormatDescriptionGetH264ParameterSetAtIndex, "CMVideoFormatDescriptionGetH264ParameterSetAtIndex")
	cm.fn(&CMVideoFormatDescriptionGetHEVCParameterSetAtIndex, "CMVideoFormatDescriptionGetHEVCParameterSetAtIndex")

	cv.fn(&CVPixelBufferCreate, "CVPixelBufferCreate")
	cv.fn(&CVPixelBufferPoolCreatePixelBuffer, "CVPixelBufferPoolCreatePixelBuffer")
	cv.ptrConst(&KCVPixelBufferWidthKey, "kCVPixelBufferWidthKey")
	cv.ptrConst(&KCVPixelBufferHeightKey, "kCVPixelBufferHeightKey")

	vt.fn(&VTCompressionSessionCreate, "VTCompressionSessionCreate")
	vt.fn(&VTCompressionSessionPrepareToEncodeFrames, "VTCompressionSessionPrepareToEncodeFrames")
	vt.fn(&VTCompressionSessionEncodeFrame, "VTCompressionSessionEncodeFrame")
	vt.fn(&VTCompressionSessionCompleteFrames, "VTCompressionSessionCompleteFrames")
	vt.fn(&VTCompressionSessionInvalidate, "VTCompressionSessionInvalidate")
	vt.fn(&VTCompressionSessionGetPixelBufferPool, "VTCompressionSessionGetPixelBufferPool")
	vt.fn(&VTSessionSetProperty, "VTSessionSetProperty")
	vt.fn(&VTCopySupportedPropertyDictionaryForEncoder, "VTCopySupportedPropertyDictionaryForEncoder")

	vt.ptrConst(&KVTEnableHardwareEncoder, "kVTVideoEncoderSpecification_EnableHardwareAcceleratedVideoEncoder")
	vt.ptrConst(&KVTRequireHardwareEncoder, "kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder")
	vt.optPtrConst(&KVTEnableLowLatencyRateControl, "kVTVideoEncoderSpecification_EnableLowLatencyRateControl")
	vt.ptrConst(&KVTAverageBitRate, "kVTCompressionPropertyKey_AverageBitRate")
	vt.optPtrConst(&KVTConstantBitRate, "kVTCompressionPropertyKey_ConstantBitRate")
	vt.ptrConst(&KVTQuality, "kVTCompressionPropertyKey_Quality")
	vt.ptrConst(&KVTRealTime, "kVTCompressionPropertyKey_RealTime")
	vt.ptrConst(&KVTAllowFrameReordering, "kVTCompressionPropertyKey_AllowFrameReordering")
	vt.ptrConst(&KVTMaxKeyFrameInterval, "kVTCompressionPropertyKey_MaxKeyFrameInterval")
	vt.ptrConst(&KVTMaxKeyFrameIntervalDuration, "kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration")
	vt.ptrConst(&KVTExpectedFrameRate, "kVTCompressionPropertyKey_ExpectedFrameRate")
	vt.ptrConst(&KVTProfileLevel, "kVTCompressionPropertyKey_ProfileLevel")
	vt.ptrConst(&KVTForceKeyFrame, "kVTEncodeFrameOptionKey_ForceKeyFrame")
	vt.ptrConst(&KVTProfileH264Baseline, "kVTProfileLevel_H264_Baseline_AutoLevel")
	vt.ptrConst(&KVTProfileH264Main, "kVTProfileLevel_H264_Main_AutoLevel")
	vt.ptrConst(&KVTProfileH264High, "kVTProfileLevel_H264_High_AutoLevel")
	vt.ptrConst(&KVTProfileHEVCMain, "kVTProfileLevel_HEVC_Main_AutoLevel")
}

// CFNumberFloat32 creates a CFNumberRef holding a float. The caller owns the
// result.
func CFNumberFloat32(v float32) uintptr {
	return CFNumberCreateFloat32(0, cfNumberFloat32Type, &v)
}

// CFNumberFloat64 creates a CFNumberRef holding a double. The caller owns the
// result.
func CFNumberFloat64(v float64) uintptr {
	return CFNumberCreateFloat64(0, cfNumberFloat64Type, &v)
}

// CFBoolean returns the shared kCFBooleanTrue or kCFBooleanFalse. It must
// not be released.
func CFBoolean(v bool) uintptr {
	if v {
		return KCFBooleanTrue
	}
	return KCFBooleanFalse
}
