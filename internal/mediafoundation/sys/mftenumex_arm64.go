//go:build windows && arm64

package sys

import "unsafe"

// MFTEnumEx enumerates Media Foundation transforms. The category GUID is
// passed by value; on ARM64 a 16-byte struct occupies two argument registers.
func MFTEnumEx(category GUID, flags uint32, in, out *MFT_REGISTER_TYPE_INFO, activates ***IMFActivate, count *uint32) HRESULT {
	words := *(*[2]uintptr)(unsafe.Pointer(&category))
	r, _, _ := procMFTEnumEx.Call(words[0], words[1], uintptr(flags),
		uintptr(unsafe.Pointer(in)), uintptr(unsafe.Pointer(out)),
		uintptr(unsafe.Pointer(activates)), uintptr(unsafe.Pointer(count)))
	return hres(r)
}
