//go:build windows && (amd64 || arm64)

package sys

import (
	"syscall"
	"unsafe"
)

// Bindings used by the encoder: the asynchronous MFT event model
// (IMFMediaEventGenerator / IMFMediaEvent), ICodecAPI with its VARIANT
// values, and the Codec API property GUIDs (codecapi.h).

// Media event types and flags (mfobjects.h).
const (
	MF_EVENT_FLAG_NO_WAIT uint32 = 0x1

	MEError                  uint32 = 1
	METransformUnknown       uint32 = 600
	METransformNeedInput     uint32 = 601
	METransformHaveOutput    uint32 = 602
	METransformDrainComplete uint32 = 603
	METransformMarker        uint32 = 604

	E_UNEXPECTED             HRESULT = 0x8000FFFF
	MF_E_NO_EVENTS_AVAILABLE HRESULT = 0xC00D3E80
)

// VARIANT type tags and values (wtypes.h).
const (
	VT_EMPTY uint16 = 0
	VT_I4    uint16 = 3
	VT_BOOL  uint16 = 11
	VT_UI4   uint16 = 19
	VT_UI8   uint16 = 21

	VARIANT_TRUE  uint64 = 0xFFFF
	VARIANT_FALSE uint64 = 0
)

// Codec API enumerations (codecapi.h).
const (
	EAVEncCommonRateControlMode_CBR                uint32 = 0
	EAVEncCommonRateControlMode_PeakConstrainedVBR uint32 = 1
	EAVEncCommonRateControlMode_UnconstrainedVBR   uint32 = 2
	EAVEncCommonRateControlMode_Quality            uint32 = 3
	EAVEncCommonRateControlMode_LowDelayVBR        uint32 = 4

	EAVEncH264VProfile_Base       uint32 = 66
	EAVEncH264VProfile_Main       uint32 = 77
	EAVEncH264VProfile_High       uint32 = 100
	EAVEncH265VProfile_Main_420_8 uint32 = 1
)

// Attribute and property GUIDs used by the encoder.
var (
	MF_MT_AVG_BITRATE                 = guid(0x20332624, 0xfb0d, 0x4d9e, 0xbd, 0x0d, 0xcb, 0xf6, 0x78, 0x6c, 0x10, 0x2e)
	MF_MT_FRAME_RATE                  = guid(0xc459a2e8, 0x3d2c, 0x4e44, 0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0)
	MF_MT_PIXEL_ASPECT_RATIO          = guid(0xc6376a1e, 0x8d0a, 0x4027, 0xbe, 0x45, 0x6d, 0x9a, 0x0a, 0xd3, 0x9b, 0xb6)
	MF_MT_MPEG2_PROFILE               = guid(0xad76a80b, 0x2d5c, 0x4e0b, 0xb3, 0x75, 0x64, 0xe5, 0x20, 0x13, 0x70, 0x36)
	MF_MT_MPEG_SEQUENCE_HEADER        = guid(0x3c036de7, 0x3ad0, 0x4c9e, 0x92, 0x16, 0xee, 0x6d, 0x6a, 0xc2, 0x1c, 0xb3)
	MF_MT_ALL_SAMPLES_INDEPENDENT     = guid(0xc9173739, 0x5e56, 0x461c, 0xb7, 0x13, 0x46, 0xfb, 0x99, 0x5c, 0xb9, 0x5f)
	MFSampleExtension_DecodeTimestamp = guid(0x73a954d4, 0x09e2, 0x4861, 0xbe, 0xfc, 0x94, 0xbd, 0x97, 0xc0, 0x8e, 0x6e)

	CODECAPI_AVEncCommonRateControlMode   = guid(0x1c0608e9, 0x370c, 0x4710, 0x8a, 0x58, 0xcb, 0x61, 0x81, 0xc4, 0x24, 0x23)
	CODECAPI_AVEncCommonMeanBitRate       = guid(0xf7222374, 0x2144, 0x4815, 0xb5, 0x50, 0xa3, 0x7f, 0x8e, 0x12, 0xee, 0x52)
	CODECAPI_AVEncCommonMaxBitRate        = guid(0x9651eae4, 0x39b9, 0x4ebf, 0x85, 0xef, 0xd7, 0xf4, 0x44, 0xec, 0x74, 0x65)
	CODECAPI_AVEncCommonQuality           = guid(0xfcbf57a3, 0x7ea5, 0x4b0c, 0x96, 0x44, 0x69, 0xb4, 0x0c, 0x39, 0xc3, 0x91)
	CODECAPI_AVEncCommonLowLatency        = guid(0x9d3ecd55, 0x89e8, 0x490a, 0x97, 0x0a, 0x0c, 0x95, 0x48, 0xd5, 0xa5, 0x6e)
	CODECAPI_AVEncCommonRealTime          = guid(0x143a0ff6, 0xa131, 0x43da, 0xb8, 0x1e, 0x98, 0xfb, 0xb8, 0xec, 0x37, 0x8e)
	CODECAPI_AVEncMPVGOPSize              = guid(0x95f31b26, 0x95a4, 0x41aa, 0x93, 0x03, 0x24, 0x6a, 0x7f, 0xc6, 0xee, 0xf1)
	CODECAPI_AVEncMPVDefaultBPictureCount = guid(0x8d390aac, 0xdc5c, 0x4200, 0xb5, 0x7f, 0x81, 0x4d, 0x04, 0xba, 0xba, 0xb2)
	CODECAPI_AVEncVideoForceKeyFrame      = guid(0x398c1b98, 0x8353, 0x475a, 0x9e, 0xf2, 0x8f, 0x26, 0x5d, 0x26, 0x03, 0x45)
	// CODECAPI_AVLowLatencyMode shares its GUID with MF_LOW_LATENCY.
	CODECAPI_AVLowLatencyMode = MF_LOW_LATENCY
)

// VARIANT mirrors the 64-bit Automation VARIANT: a 16-bit type tag, three
// reserved words and a 16-byte union. Only scalar members are used here.
type VARIANT struct {
	VT  uint16
	_   [3]uint16
	Val uint64
	_   uint64
}

var (
	_ [24 - unsafe.Sizeof(VARIANT{})]byte
	_ [unsafe.Sizeof(VARIANT{}) - 24]byte
)

// VariantUI4 builds a VT_UI4 value.
func VariantUI4(v uint32) VARIANT { return VARIANT{VT: VT_UI4, Val: uint64(v)} }

// VariantBool builds a VT_BOOL value.
func VariantBool(b bool) VARIANT {
	v := VARIANT{VT: VT_BOOL}
	if b {
		v.Val = VARIANT_TRUE
	}
	return v
}

// IMFMediaEventGenerator delivers the out-of-band events of an asynchronous
// MFT (mfobjects.h).
type IMFMediaEventGenerator struct{ vtbl *iMFMediaEventGeneratorVtbl }

type iMFMediaEventGeneratorVtbl struct {
	iUnknownVtbl
	GetEvent      uintptr
	BeginGetEvent uintptr
	EndGetEvent   uintptr
	QueueEvent    uintptr
}

// Unknown returns the IUnknown view.
func (g *IMFMediaEventGenerator) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(g)) }

// Release releases the object.
func (g *IMFMediaEventGenerator) Release() { g.Unknown().Release() }

// GetEvent returns the next event; with MF_EVENT_FLAG_NO_WAIT it returns
// MF_E_NO_EVENTS_AVAILABLE instead of blocking.
func (g *IMFMediaEventGenerator) GetEvent(flags uint32, out **IMFMediaEvent) HRESULT {
	r, _, _ := syscall.SyscallN(g.vtbl.GetEvent, uintptr(unsafe.Pointer(g)), uintptr(flags), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// IMFMediaEvent is one event (mfobjects.h). It derives from IMFAttributes.
type IMFMediaEvent struct{ vtbl *iMFMediaEventVtbl }

type iMFMediaEventVtbl struct {
	iMFAttributesVtbl
	GetType         uintptr
	GetExtendedType uintptr
	GetStatus       uintptr
	GetValue        uintptr
}

// Attributes returns the IMFAttributes view.
func (e *IMFMediaEvent) Attributes() *IMFAttributes { return (*IMFAttributes)(unsafe.Pointer(e)) }

// Release releases the object.
func (e *IMFMediaEvent) Release() { e.Attributes().Release() }

// GetType returns the MediaEventType.
func (e *IMFMediaEvent) GetType(out *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(e.vtbl.GetType, uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetStatus returns the HRESULT carried by the event.
func (e *IMFMediaEvent) GetStatus(out *HRESULT) HRESULT {
	r, _, _ := syscall.SyscallN(e.vtbl.GetStatus, uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// ICodecAPI exposes encoder settings (strmif.h).
type ICodecAPI struct{ vtbl *iCodecAPIVtbl }

type iCodecAPIVtbl struct {
	iUnknownVtbl
	IsSupported              uintptr
	IsModifiable             uintptr
	GetParameterRange        uintptr
	GetParameterValues       uintptr
	GetDefaultValue          uintptr
	GetValue                 uintptr
	SetValue                 uintptr
	RegisterForEvent         uintptr
	UnregisterForEvent       uintptr
	SetAllDefaults           uintptr
	SetValueWithNotify       uintptr
	SetAllDefaultsWithNotify uintptr
	GetAllSettings           uintptr
	SetAllSettings           uintptr
	SetAllSettingsWithNotify uintptr
}

// Unknown returns the IUnknown view.
func (c *ICodecAPI) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(c)) }

// Release releases the object.
func (c *ICodecAPI) Release() { c.Unknown().Release() }

// IsSupported returns S_OK when the property is supported and S_FALSE or an
// error otherwise.
func (c *ICodecAPI) IsSupported(api *GUID) HRESULT {
	r, _, _ := syscall.SyscallN(c.vtbl.IsSupported, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(api)))
	return hres(r)
}

// SetValue sets a property.
func (c *ICodecAPI) SetValue(api *GUID, v *VARIANT) HRESULT {
	r, _, _ := syscall.SyscallN(c.vtbl.SetValue, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(api)), uintptr(unsafe.Pointer(v)))
	return hres(r)
}

// GetValue reads a property.
func (c *ICodecAPI) GetValue(api *GUID, v *VARIANT) HRESULT {
	r, _, _ := syscall.SyscallN(c.vtbl.GetValue, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(api)), uintptr(unsafe.Pointer(v)))
	return hres(r)
}
