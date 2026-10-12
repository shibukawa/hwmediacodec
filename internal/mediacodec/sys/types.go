// Package sys binds the Android NDK media library (libmediandk.so: the
// AMediaCodec and AMediaFormat C API) through purego, so that no cgo source
// is needed. The Android build itself runs with CGO_ENABLED=1 because purego
// needs the cgo runtime there.
//
// This file holds the constants and structures, which compile everywhere so
// that the pure-Go parts of the backend can be tested on the desktop; the
// bindings are in sys.go (android, or the mediacodec_typecheck tag).
package sys

import "unsafe"

// media_status_t values (media/NdkMediaError.h).
const (
	OK                    int32 = 0
	ErrorUnknown          int32 = -10000
	ErrorMalformed        int32 = -10001
	ErrorUnsupported      int32 = -10002
	ErrorInvalidObject    int32 = -10003
	ErrorInvalidParameter int32 = -10004
	ErrorInvalidOperation int32 = -10005
	ErrorEndOfStream      int32 = -10006
	ErrorIO               int32 = -10007
	ErrorWouldBlock       int32 = -10008
)

// AMediaCodec buffer flags.
const (
	BufferFlagKeyFrame     uint32 = 1
	BufferFlagCodecConfig  uint32 = 2
	BufferFlagEndOfStream  uint32 = 4
	BufferFlagPartialFrame uint32 = 8
)

// Negative results of AMediaCodec_dequeueOutputBuffer.
const (
	InfoOutputBuffersChanged = -3
	InfoOutputFormatChanged  = -2
	InfoTryAgainLater        = -1
)

// AMediaCodec_configure flags.
const ConfigureFlagEncode uint32 = 1

// MediaCodecInfo.CodecCapabilities colour formats.
const (
	ColorFormatYUV420Planar     int32 = 19
	ColorFormatYUV420SemiPlanar int32 = 21
	ColorFormatYUV420Flexible   int32 = 0x7F420888
)

// MediaCodecInfo.EncoderCapabilities bitrate modes.
const (
	BitrateModeCQ  int32 = 0
	BitrateModeVBR int32 = 1
	BitrateModeCBR int32 = 2
)

// MediaCodecInfo.CodecProfileLevel values.
const (
	AVCProfileBaseline int32 = 0x01
	AVCProfileMain     int32 = 0x02
	AVCProfileHigh     int32 = 0x08
	AVCLevel41         int32 = 0x1000
	AVCLevel51         int32 = 0x8000

	HEVCProfileMain     int32 = 0x01
	HEVCMainTierLevel41 int32 = 0x1000
	HEVCMainTierLevel51 int32 = 0x10000
	HEVCMainTierLevel61 int32 = 0x100000
)

// MediaFormat colour description values.
const (
	ColorStandardBT709    int32 = 1
	ColorStandardBT601PAL int32 = 2
	ColorRangeFull        int32 = 1
	ColorRangeLimited     int32 = 2
	ColorTransferSDRVideo int32 = 3
)

// MediaFormat keys (media/NdkMediaFormat.h and MediaFormat.java).
const (
	KeyMime             = "mime"
	KeyWidth            = "width"
	KeyHeight           = "height"
	KeyColorFormat      = "color-format"
	KeyStride           = "stride"
	KeySliceHeight      = "slice-height"
	KeyCrop             = "crop"
	KeyCropLeft         = "crop-left"
	KeyCropTop          = "crop-top"
	KeyCropRight        = "crop-right"
	KeyCropBottom       = "crop-bottom"
	KeyImageData        = "image-data"
	KeyCSD0             = "csd-0"
	KeyCSD1             = "csd-1"
	KeyCSD2             = "csd-2"
	KeyBitrate          = "bitrate"
	KeyBitrateMode      = "bitrate-mode"
	KeyQuality          = "quality"
	KeyFrameRate        = "frame-rate"
	KeyIFrameInterval   = "i-frame-interval"
	KeyProfile          = "profile"
	KeyLevel            = "level"
	KeyLatency          = "latency"
	KeyLowLatency       = "low-latency"
	KeyMaxBFrames       = "max-bframes"
	KeyRequestSyncFrame = "request-sync"
	KeyColorStandard    = "color-standard"
	KeyColorRange       = "color-range"
	KeyColorTransfer    = "color-transfer"
	KeyPrependHeaders   = "prepend-sps-pps-to-idr-frames"
)

// MIME types.
const (
	MimeH264 = "video/avc"
	MimeHEVC = "video/hevc"
	MimeAV1  = "video/av01"
)

// BufferInfo mirrors AMediaCodecBufferInfo.
type BufferInfo struct {
	Offset             int32
	Size               int32
	PresentationTimeUs int64
	Flags              uint32
	_                  uint32
}

// Compile-time layout check: the structure is 24 bytes on LP64 Android.
var _ [24]byte = [unsafe.Sizeof(BufferInfo{})]byte{}

// MediaImage2 mirrors android.media.MediaImage2 (media/hardware/VideoAPI.h),
// the plane layout that a flexible YUV buffer carries in the "image-data"
// entry of its format. All fields are little-endian 32-bit values.
type MediaImage2 struct {
	Type              uint32
	NumPlanes         uint32
	Width             uint32
	Height            uint32
	BitDepth          uint32
	BitDepthAllocated uint32
	Plane             [4]MediaImagePlane
}

// MediaImagePlane is one plane of a MediaImage2.
type MediaImagePlane struct {
	Offset           uint32
	ColInc           int32
	RowInc           int32
	HorizSubsampling uint32
	VertSubsampling  uint32
}

// MediaImage2 type values (sequential in VideoAPI.h: UNKNOWN, YUV, YUVA,
// RGB, RGBA, Y; the Android 14 emulator reports 1 for its YUV buffers).
const (
	MediaImageTypeUnknown uint32 = 0
	MediaImageTypeYUV     uint32 = 1
	MediaImageTypeYUVA    uint32 = 2
	MediaImageTypeRGB     uint32 = 3
	MediaImageTypeRGBA    uint32 = 4
	MediaImageTypeY       uint32 = 5
)

// MediaImage2Size is the size of the structure in bytes.
const MediaImage2Size = 104

var _ [MediaImage2Size]byte = [unsafe.Sizeof(MediaImage2{})]byte{}
