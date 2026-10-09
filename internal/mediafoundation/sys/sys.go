//go:build windows && (amd64 || arm64)

// Package sys binds the Media Foundation, Direct3D 11 and COM entry points
// used by the Media Foundation backend. Flat functions go through
// golang.org/x/sys/windows lazy DLL loading and COM interfaces are called
// through their vtables with syscall.SyscallN, so no cgo is required.
//
// Names of constants, GUIDs and structures follow the Windows SDK headers so
// that they can be verified against mfapi.h, mftransform.h, mfobjects.h,
// mferror.h and d3d11.h.
package sys

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// HRESULT is a COM result code. Failed reports the severity bit.
type HRESULT uint32

// Failed reports whether hr is an error code.
func (hr HRESULT) Failed() bool { return hr&0x80000000 != 0 }

func (hr HRESULT) String() string {
	if s, ok := hresultNames[hr]; ok {
		return fmt.Sprintf("%s (0x%08X)", s, uint32(hr))
	}
	return fmt.Sprintf("HRESULT 0x%08X", uint32(hr))
}

// hres truncates a raw return register to the 32-bit HRESULT; the upper bits
// of a 32-bit return value are unspecified on 64-bit targets.
func hres(r uintptr) HRESULT { return HRESULT(uint32(r)) }

// Result codes (winerror.h, mferror.h, dxgi.h).
const (
	S_OK    HRESULT = 0
	S_FALSE HRESULT = 1

	E_NOTIMPL          HRESULT = 0x80004001
	E_NOINTERFACE      HRESULT = 0x80004002
	E_POINTER          HRESULT = 0x80004003
	E_FAIL             HRESULT = 0x80004005
	E_OUTOFMEMORY      HRESULT = 0x8007000E
	E_INVALIDARG       HRESULT = 0x80070057
	RPC_E_CHANGED_MODE HRESULT = 0x80010106

	MF_E_PLATFORM_NOT_INITIALIZED                           HRESULT = 0xC00D36B0
	MF_E_INVALIDMEDIATYPE                                   HRESULT = 0xC00D36B4
	MF_E_NOTACCEPTING                                       HRESULT = 0xC00D36B5
	MF_E_NOT_INITIALIZED                                    HRESULT = 0xC00D36B6
	MF_E_NO_MORE_TYPES                                      HRESULT = 0xC00D36B9
	MF_E_INVALIDTYPE                                        HRESULT = 0xC00D36BD
	MF_E_INVALID_TIMESTAMP                                  HRESULT = 0xC00D36C0
	MF_E_NO_SAMPLE_TIMESTAMP                                HRESULT = 0xC00D36C8
	MF_E_INVALID_STREAM_DATA                                HRESULT = 0xC00D36CB
	MF_E_NOT_FOUND                                          HRESULT = 0xC00D36D5
	MF_E_NOT_AVAILABLE                                      HRESULT = 0xC00D36D6
	MF_E_ATTRIBUTENOTFOUND                                  HRESULT = 0xC00D36E6
	MF_E_UNAUTHORIZED                                       HRESULT = 0xC00D3701
	MF_E_HW_MFT_FAILED_START_STREAMING                      HRESULT = 0xC00D3704
	MF_E_SHUTDOWN                                           HRESULT = 0xC00D3E85
	MF_E_UNSUPPORTED_FORMAT                                 HRESULT = 0xC00D3E98
	MF_E_VIDEO_DEVICE_LOCKED                                HRESULT = 0xC00D4E24
	MF_E_TOPO_CODEC_NOT_FOUND                               HRESULT = 0xC00D5212
	MF_E_TRANSFORM_TYPE_NOT_SET                             HRESULT = 0xC00D6D60
	MF_E_TRANSFORM_STREAM_CHANGE                            HRESULT = 0xC00D6D61
	MF_E_TRANSFORM_INPUT_REMAINING                          HRESULT = 0xC00D6D62
	MF_E_TRANSFORM_NEED_MORE_INPUT                          HRESULT = 0xC00D6D72
	MF_E_TRANSFORM_CANNOT_CHANGE_MEDIATYPE_WHILE_PROCESSING HRESULT = 0xC00D6D74
	MF_E_UNSUPPORTED_D3D_TYPE                               HRESULT = 0xC00D6D76
	MF_E_TRANSFORM_ASYNC_LOCKED                             HRESULT = 0xC00D6D77
	MF_E_TRANSFORM_STREAM_INVALID_RESOLUTION                HRESULT = 0xC00D6D79
	MF_E_INSUFFICIENT_BUFFER                                HRESULT = 0xC00D7170

	DXGI_ERROR_INVALID_CALL   HRESULT = 0x887A0001
	DXGI_ERROR_UNSUPPORTED    HRESULT = 0x887A0004
	DXGI_ERROR_DEVICE_REMOVED HRESULT = 0x887A0005
	DXGI_ERROR_DEVICE_HUNG    HRESULT = 0x887A0006
	DXGI_ERROR_DEVICE_RESET   HRESULT = 0x887A0007
)

var hresultNames = map[HRESULT]string{
	E_NOTIMPL:                          "not implemented",
	E_NOINTERFACE:                      "interface not supported",
	E_POINTER:                          "invalid pointer",
	E_FAIL:                             "unspecified failure",
	E_OUTOFMEMORY:                      "out of memory",
	E_INVALIDARG:                       "invalid argument",
	RPC_E_CHANGED_MODE:                 "COM apartment mode already set",
	MF_E_PLATFORM_NOT_INITIALIZED:      "Media Foundation is not initialized",
	MF_E_INVALIDMEDIATYPE:              "invalid media type",
	MF_E_NOTACCEPTING:                  "transform is not accepting input",
	MF_E_NOT_INITIALIZED:               "object is not initialized",
	MF_E_NO_MORE_TYPES:                 "no more media types",
	MF_E_INVALIDTYPE:                   "invalid type",
	MF_E_INVALID_TIMESTAMP:             "invalid timestamp",
	MF_E_NO_SAMPLE_TIMESTAMP:           "sample has no timestamp",
	MF_E_INVALID_STREAM_DATA:           "invalid stream data",
	MF_E_NOT_FOUND:                     "not found",
	MF_E_NOT_AVAILABLE:                 "not available",
	MF_E_ATTRIBUTENOTFOUND:             "attribute not found",
	MF_E_UNAUTHORIZED:                  "unauthorized",
	MF_E_HW_MFT_FAILED_START_STREAMING: "hardware MFT failed to start streaming",
	MF_E_SHUTDOWN:                      "object was shut down",
	MF_E_UNSUPPORTED_FORMAT:            "unsupported format",
	MF_E_VIDEO_DEVICE_LOCKED:           "video device is locked",
	MF_E_TOPO_CODEC_NOT_FOUND:          "codec not found",
	MF_E_TRANSFORM_TYPE_NOT_SET:        "transform media type not set",
	MF_E_TRANSFORM_STREAM_CHANGE:       "transform stream change",
	MF_E_TRANSFORM_INPUT_REMAINING:     "transform has unprocessed input",
	MF_E_TRANSFORM_NEED_MORE_INPUT:     "transform needs more input",
	MF_E_TRANSFORM_CANNOT_CHANGE_MEDIATYPE_WHILE_PROCESSING: "media type cannot change while processing",
	MF_E_UNSUPPORTED_D3D_TYPE:                               "unsupported Direct3D type",
	MF_E_TRANSFORM_ASYNC_LOCKED:                             "asynchronous transform is locked",
	MF_E_TRANSFORM_STREAM_INVALID_RESOLUTION:                "stream resolution not supported",
	MF_E_INSUFFICIENT_BUFFER:                                "insufficient buffer",
	DXGI_ERROR_INVALID_CALL:                                 "DXGI invalid call",
	DXGI_ERROR_UNSUPPORTED:                                  "DXGI unsupported",
	DXGI_ERROR_DEVICE_REMOVED:                               "graphics device removed",
	DXGI_ERROR_DEVICE_HUNG:                                  "graphics device hung",
	DXGI_ERROR_DEVICE_RESET:                                 "graphics device reset",
}

// Media Foundation constants (mfapi.h, mftransform.h, mfobjects.h).
const (
	MF_SDK_VERSION uint32 = 0x0002
	MF_API_VERSION uint32 = 0x0070
	MF_VERSION     uint32 = MF_SDK_VERSION<<16 | MF_API_VERSION
	MFSTARTUP_FULL uint32 = 0

	MFT_ENUM_FLAG_SYNCMFT            uint32 = 0x00000001
	MFT_ENUM_FLAG_ASYNCMFT           uint32 = 0x00000002
	MFT_ENUM_FLAG_HARDWARE           uint32 = 0x00000004
	MFT_ENUM_FLAG_FIELDOFUSE         uint32 = 0x00000008
	MFT_ENUM_FLAG_LOCALMFT           uint32 = 0x00000010
	MFT_ENUM_FLAG_TRANSCODE_ONLY     uint32 = 0x00000020
	MFT_ENUM_FLAG_SORTANDFILTER      uint32 = 0x00000040
	MFT_ENUM_FLAG_UNTRUSTED_STOREMFT uint32 = 0x00000400

	MFT_MESSAGE_COMMAND_FLUSH          uint32 = 0x00000000
	MFT_MESSAGE_COMMAND_DRAIN          uint32 = 0x00000001
	MFT_MESSAGE_SET_D3D_MANAGER        uint32 = 0x00000002
	MFT_MESSAGE_NOTIFY_BEGIN_STREAMING uint32 = 0x10000000
	MFT_MESSAGE_NOTIFY_END_STREAMING   uint32 = 0x10000001
	MFT_MESSAGE_NOTIFY_END_OF_STREAM   uint32 = 0x10000002
	MFT_MESSAGE_NOTIFY_START_OF_STREAM uint32 = 0x10000003

	MFT_OUTPUT_STREAM_PROVIDES_SAMPLES    uint32 = 0x00000100
	MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES uint32 = 0x00000200

	MFT_OUTPUT_DATA_BUFFER_INCOMPLETE    uint32 = 0x01000000
	MFT_OUTPUT_DATA_BUFFER_FORMAT_CHANGE uint32 = 0x00000100
	MFT_OUTPUT_DATA_BUFFER_STREAM_END    uint32 = 0x00000200
	MFT_OUTPUT_DATA_BUFFER_NO_SAMPLE     uint32 = 0x00000300

	MFVideoInterlace_Progressive                 uint32 = 2
	MFVideoInterlace_MixedInterlaceOrProgressive uint32 = 7

	MF2DBuffer_LockFlags_Read uint32 = 0x1

	// MFCreateAlignedMemoryBuffer alignment values are "alignment - 1".
	MF_16_BYTE_ALIGNMENT uint32 = 0x0000000F
)

// Direct3D 11 / DXGI constants (d3d11.h, d3dcommon.h, dxgiformat.h).
const (
	D3D_DRIVER_TYPE_HARDWARE uint32 = 1
	D3D11_SDK_VERSION        uint32 = 7

	D3D11_CREATE_DEVICE_BGRA_SUPPORT  uint32 = 0x20
	D3D11_CREATE_DEVICE_VIDEO_SUPPORT uint32 = 0x800

	D3D11_USAGE_DEFAULT uint32 = 0
	D3D11_USAGE_STAGING uint32 = 3

	D3D11_BIND_SHADER_RESOURCE uint32 = 0x8
	D3D11_BIND_DECODER         uint32 = 0x200

	D3D11_CPU_ACCESS_WRITE uint32 = 0x10000
	D3D11_CPU_ACCESS_READ  uint32 = 0x20000

	D3D11_MAP_READ uint32 = 1

	DXGI_FORMAT_UNKNOWN    uint32 = 0
	DXGI_FORMAT_NV12       uint32 = 103
	DXGI_FORMAT_P010       uint32 = 104
	DXGI_FORMAT_420_OPAQUE uint32 = 106
)

// MFT_REGISTER_TYPE_INFO mirrors the Media Foundation struct.
type MFT_REGISTER_TYPE_INFO struct {
	MajorType GUID
	Subtype   GUID
}

// MFT_INPUT_STREAM_INFO mirrors the Media Foundation struct.
type MFT_INPUT_STREAM_INFO struct {
	MaxLatency   int64
	Flags        uint32
	Size         uint32
	MaxLookahead uint32
	Alignment    uint32
}

// MFT_OUTPUT_STREAM_INFO mirrors the Media Foundation struct.
type MFT_OUTPUT_STREAM_INFO struct {
	Flags     uint32
	Size      uint32
	Alignment uint32
}

// MFT_OUTPUT_DATA_BUFFER mirrors the Media Foundation struct.
type MFT_OUTPUT_DATA_BUFFER struct {
	StreamID uint32
	Sample   *IMFSample
	Status   uint32
	Events   *IUnknown // IMFCollection
}

// MFOffset mirrors the Media Foundation struct (a 16.16 fixed-point value).
type MFOffset struct {
	Fract uint16
	Value int16
}

// MFVideoArea mirrors the Media Foundation struct stored in the aperture
// attributes (MF_MT_MINIMUM_DISPLAY_APERTURE and friends).
type MFVideoArea struct {
	OffsetX MFOffset
	OffsetY MFOffset
	Width   int32
	Height  int32
}

// D3D11_TEXTURE2D_DESC mirrors the Direct3D 11 struct.
type D3D11_TEXTURE2D_DESC struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleCount    uint32
	SampleQuality  uint32
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

// D3D11_MAPPED_SUBRESOURCE mirrors the Direct3D 11 struct.
type D3D11_MAPPED_SUBRESOURCE struct {
	Data       *byte
	RowPitch   uint32
	DepthPitch uint32
}

// D3D11_VIDEO_DECODER_DESC mirrors the Direct3D 11 struct.
type D3D11_VIDEO_DECODER_DESC struct {
	Guid         GUID
	SampleWidth  uint32
	SampleHeight uint32
	OutputFormat uint32
}

// Compile-time layout checks against the C definitions.
var (
	_ [32 - unsafe.Sizeof(MFT_REGISTER_TYPE_INFO{})]byte
	_ [unsafe.Sizeof(MFT_REGISTER_TYPE_INFO{}) - 32]byte
	_ [24 - unsafe.Sizeof(MFT_INPUT_STREAM_INFO{})]byte
	_ [unsafe.Sizeof(MFT_INPUT_STREAM_INFO{}) - 24]byte
	_ [12 - unsafe.Sizeof(MFT_OUTPUT_STREAM_INFO{})]byte
	_ [unsafe.Sizeof(MFT_OUTPUT_STREAM_INFO{}) - 12]byte
	_ [4*unsafe.Sizeof(uintptr(0)) - unsafe.Sizeof(MFT_OUTPUT_DATA_BUFFER{})]byte
	_ [unsafe.Sizeof(MFT_OUTPUT_DATA_BUFFER{}) - 4*unsafe.Sizeof(uintptr(0))]byte
	_ [16 - unsafe.Sizeof(MFVideoArea{})]byte
	_ [unsafe.Sizeof(MFVideoArea{}) - 16]byte
	_ [44 - unsafe.Sizeof(D3D11_TEXTURE2D_DESC{})]byte
	_ [unsafe.Sizeof(D3D11_TEXTURE2D_DESC{}) - 44]byte
	_ [unsafe.Sizeof(uintptr(0)) + 8 - unsafe.Sizeof(D3D11_MAPPED_SUBRESOURCE{})]byte
	_ [unsafe.Sizeof(D3D11_MAPPED_SUBRESOURCE{}) - unsafe.Sizeof(uintptr(0)) - 8]byte
	_ [28 - unsafe.Sizeof(D3D11_VIDEO_DECODER_DESC{})]byte
	_ [unsafe.Sizeof(D3D11_VIDEO_DECODER_DESC{}) - 28]byte
)

var (
	modMfplat = windows.NewLazySystemDLL("mfplat.dll")
	modD3D11  = windows.NewLazySystemDLL("d3d11.dll")

	procMFStartup                   = modMfplat.NewProc("MFStartup")
	procMFCreateMediaType           = modMfplat.NewProc("MFCreateMediaType")
	procMFCreateSample              = modMfplat.NewProc("MFCreateSample")
	procMFCreateMemoryBuffer        = modMfplat.NewProc("MFCreateMemoryBuffer")
	procMFCreateAlignedMemoryBuffer = modMfplat.NewProc("MFCreateAlignedMemoryBuffer")
	procMFCreateDXGIDeviceManager   = modMfplat.NewProc("MFCreateDXGIDeviceManager")
	procMFTEnumEx                   = modMfplat.NewProc("MFTEnumEx")
	procD3D11CreateDevice           = modD3D11.NewProc("D3D11CreateDevice")
)

var (
	loadOnce sync.Once
	loadErr  error
)

// Load loads mfplat.dll and d3d11.dll, resolves every entry point and starts
// the Media Foundation platform. It is safe to call repeatedly; the result is
// cached. Media Foundation stays started for the life of the process.
func Load() error {
	loadOnce.Do(func() { loadErr = load() })
	return loadErr
}

func load() error {
	for _, m := range []*windows.LazyDLL{modMfplat, modD3D11} {
		if err := m.Load(); err != nil {
			return fmt.Errorf("mediafoundation: %w", err)
		}
	}
	var missing []string
	for _, p := range []*windows.LazyProc{
		procMFStartup, procMFCreateMediaType, procMFCreateSample, procMFCreateMemoryBuffer,
		procMFCreateAlignedMemoryBuffer, procMFCreateDXGIDeviceManager, procMFTEnumEx, procD3D11CreateDevice,
	} {
		if err := p.Find(); err != nil {
			missing = append(missing, p.Name)
		}
	}
	if len(missing) > 0 {
		return errors.New("mediafoundation: missing symbols: " + strings.Join(missing, ", "))
	}
	if hr := MFStartup(MF_VERSION, MFSTARTUP_FULL); hr.Failed() {
		return fmt.Errorf("mediafoundation: MFStartup: %s", hr)
	}
	return nil
}

// CoInitializeMTA joins the calling thread to the multithreaded COM
// apartment. A thread that already belongs to an apartment (S_FALSE or
// RPC_E_CHANGED_MODE) is accepted; the objects used here are free-threaded.
func CoInitializeMTA() error {
	err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)
	if err == nil {
		return nil
	}
	if e, ok := err.(windows.Errno); ok {
		switch HRESULT(uint32(e)) {
		case S_FALSE, RPC_E_CHANGED_MODE:
			return nil
		}
	}
	return fmt.Errorf("mediafoundation: CoInitializeEx: %w", err)
}

// MFStartup initializes Media Foundation.
func MFStartup(version, flags uint32) HRESULT {
	r, _, _ := procMFStartup.Call(uintptr(version), uintptr(flags))
	return hres(r)
}

// MFCreateMediaType creates an empty media type.
func MFCreateMediaType(out **IMFMediaType) HRESULT {
	r, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// MFCreateSample creates an empty sample.
func MFCreateSample(out **IMFSample) HRESULT {
	r, _, _ := procMFCreateSample.Call(uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// MFCreateMemoryBuffer creates a system-memory buffer.
func MFCreateMemoryBuffer(maxLength uint32, out **IMFMediaBuffer) HRESULT {
	r, _, _ := procMFCreateMemoryBuffer.Call(uintptr(maxLength), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// MFCreateAlignedMemoryBuffer creates an aligned system-memory buffer;
// alignment is one of the MF_*_BYTE_ALIGNMENT values (alignment minus one).
func MFCreateAlignedMemoryBuffer(maxLength, alignment uint32, out **IMFMediaBuffer) HRESULT {
	r, _, _ := procMFCreateAlignedMemoryBuffer.Call(uintptr(maxLength), uintptr(alignment), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// MFCreateDXGIDeviceManager creates a DXGI device manager and its reset token.
func MFCreateDXGIDeviceManager(resetToken *uint32, out **IMFDXGIDeviceManager) HRESULT {
	r, _, _ := procMFCreateDXGIDeviceManager.Call(uintptr(unsafe.Pointer(resetToken)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// D3D11CreateDevice creates a Direct3D 11 device and its immediate context.
// featureLevels may be nil to use the runtime defaults.
func D3D11CreateDevice(driverType, flags uint32, featureLevels []uint32, device **ID3D11Device, featureLevel *uint32, context **ID3D11DeviceContext) HRESULT {
	var levels *uint32
	if len(featureLevels) > 0 {
		levels = &featureLevels[0]
	}
	r, _, _ := procD3D11CreateDevice.Call(0, uintptr(driverType), 0, uintptr(flags),
		uintptr(unsafe.Pointer(levels)), uintptr(len(featureLevels)), uintptr(D3D11_SDK_VERSION),
		uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(featureLevel)), uintptr(unsafe.Pointer(context)))
	return hres(r)
}

// CoTaskMemFree frees memory allocated by COM (MFTEnumEx arrays,
// IMFAttributes::GetAllocatedString results).
func CoTaskMemFree(p unsafe.Pointer) { windows.CoTaskMemFree(p) }

// UTF16PtrToString converts a NUL-terminated UTF-16 string.
func UTF16PtrToString(p *uint16) string { return windows.UTF16PtrToString(p) }
