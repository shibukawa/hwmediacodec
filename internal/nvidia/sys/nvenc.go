//go:build linux || (windows && amd64)

package sys

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

// Exported entry points of libnvidia-encode.so.1 (Linux) and
// nvEncodeAPI64.dll (Windows).
var (
	NvEncodeAPIGetMaxSupportedVersion func(version *uint32) uint32
	NvEncodeAPICreateInstance         func(list *FunctionList) uint32
)

// API is the NVENC function table (NV_ENCODE_API_FUNCTION_LIST) bound
// through purego. Encoder handles are the opaque void* the session calls
// return.
type API struct {
	OpenEncodeSessionEx     func(params *OpenEncodeSessionExParams, encoder *uintptr) uint32
	GetEncodeGUIDCount      func(encoder uintptr, count *uint32) uint32
	GetEncodeGUIDs          func(encoder uintptr, guids *GUID, arraySize uint32, count *uint32) uint32
	GetEncodeCaps           func(encoder uintptr, encodeGUID GUID, param *CapsParam, value *int32) uint32
	GetEncodePresetConfigEx func(encoder uintptr, encodeGUID, presetGUID GUID, tuning uint32, config *PresetConfig) uint32
	InitializeEncoder       func(encoder uintptr, params *InitializeParams) uint32
	CreateInputBuffer       func(encoder uintptr, params *CreateInputBuffer) uint32
	DestroyInputBuffer      func(encoder uintptr, buffer uintptr) uint32
	CreateBitstreamBuffer   func(encoder uintptr, params *CreateBitstreamBuffer) uint32
	DestroyBitstreamBuffer  func(encoder uintptr, buffer uintptr) uint32
	EncodePicture           func(encoder uintptr, params *PicParams) uint32
	LockBitstream           func(encoder uintptr, params *LockBitstream) uint32
	UnlockBitstream         func(encoder uintptr, buffer uintptr) uint32
	LockInputBuffer         func(encoder uintptr, params *LockInputBuffer) uint32
	UnlockInputBuffer       func(encoder uintptr, buffer uintptr) uint32
	GetSequenceParams       func(encoder uintptr, payload *SequenceParamPayload) uint32
	DestroyEncoder          func(encoder uintptr) uint32
	GetLastErrorString      func(encoder uintptr) string
}

var (
	nvencOnce sync.Once
	nvencErr  error
	nvencAPI  *API
	// NVENCMaxVersion is the value NvEncodeAPIGetMaxSupportedVersion
	// reported, (major << 4) | minor, valid after LoadNVENC.
	NVENCMaxVersion uint32
)

// ErrNVENCVersion is wrapped by LoadNVENC when the driver is older than the
// API version this package was written against.
var ErrNVENCVersion = errors.New("nvidia: the driver's NVENC API is older than 11.1")

// LoadNVENC opens the NVENC library, checks the driver's NVENC API version
// and binds the function table. The result is cached.
func LoadNVENC() (*API, error) {
	nvencOnce.Do(func() { nvencAPI, nvencErr = loadNVENC() })
	return nvencAPI, nvencErr
}

func loadNVENC() (*API, error) {
	if err := LoadCUDA(); err != nil {
		return nil, err
	}
	b, err := open(nvencLibraries)
	if err != nil {
		return nil, err
	}
	b.fn(&NvEncodeAPIGetMaxSupportedVersion, "NvEncodeAPIGetMaxSupportedVersion")
	b.fn(&NvEncodeAPICreateInstance, "NvEncodeAPICreateInstance")
	if err := b.missing(); err != nil {
		return nil, err
	}
	var max uint32
	if st := NvEncodeAPIGetMaxSupportedVersion(&max); st != StatusSuccess {
		return nil, fmt.Errorf("nvidia: NvEncodeAPIGetMaxSupportedVersion: %s", StatusString(st))
	}
	NVENCMaxVersion = max
	if max < MaxSupportedVersionFloor {
		return nil, fmt.Errorf("%w (driver reports %d.%d)", ErrNVENCVersion, max>>4, max&0xf)
	}
	list := &FunctionList{Version: FunctionListVer}
	if st := NvEncodeAPICreateInstance(list); st != StatusSuccess {
		return nil, fmt.Errorf("nvidia: NvEncodeAPICreateInstance: %s", StatusString(st))
	}
	api := &API{}
	var missing []string
	reg := func(fptr any, addr uintptr, name string) {
		if addr == 0 {
			missing = append(missing, name)
			return
		}
		purego.RegisterFunc(fptr, addr)
	}
	reg(&api.OpenEncodeSessionEx, list.OpenEncodeSessionEx, "nvEncOpenEncodeSessionEx")
	reg(&api.GetEncodeGUIDCount, list.GetEncodeGUIDCount, "nvEncGetEncodeGUIDCount")
	reg(&api.GetEncodeGUIDs, list.GetEncodeGUIDs, "nvEncGetEncodeGUIDs")
	reg(&api.GetEncodeCaps, list.GetEncodeCaps, "nvEncGetEncodeCaps")
	reg(&api.GetEncodePresetConfigEx, list.GetEncodePresetConfigEx, "nvEncGetEncodePresetConfigEx")
	reg(&api.InitializeEncoder, list.InitializeEncoder, "nvEncInitializeEncoder")
	reg(&api.CreateInputBuffer, list.CreateInputBuffer, "nvEncCreateInputBuffer")
	reg(&api.DestroyInputBuffer, list.DestroyInputBuffer, "nvEncDestroyInputBuffer")
	reg(&api.CreateBitstreamBuffer, list.CreateBitstreamBuffer, "nvEncCreateBitstreamBuffer")
	reg(&api.DestroyBitstreamBuffer, list.DestroyBitstreamBuffer, "nvEncDestroyBitstreamBuffer")
	reg(&api.EncodePicture, list.EncodePicture, "nvEncEncodePicture")
	reg(&api.LockBitstream, list.LockBitstream, "nvEncLockBitstream")
	reg(&api.UnlockBitstream, list.UnlockBitstream, "nvEncUnlockBitstream")
	reg(&api.LockInputBuffer, list.LockInputBuffer, "nvEncLockInputBuffer")
	reg(&api.UnlockInputBuffer, list.UnlockInputBuffer, "nvEncUnlockInputBuffer")
	reg(&api.GetSequenceParams, list.GetSequenceParams, "nvEncGetSequenceParams")
	reg(&api.DestroyEncoder, list.DestroyEncoder, "nvEncDestroyEncoder")
	reg(&api.GetLastErrorString, list.GetLastErrorString, "nvEncGetLastErrorString")
	if len(missing) > 0 {
		return nil, fmt.Errorf("nvidia: the NVENC function table lacks %v", missing)
	}
	return api, nil
}
