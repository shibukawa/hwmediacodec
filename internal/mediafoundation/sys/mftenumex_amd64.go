//go:build windows && amd64

package sys

import "unsafe"

// MFTEnumEx enumerates Media Foundation transforms. The category GUID is
// passed by value; on x64 a 16-byte struct travels as a pointer to a copy.
func MFTEnumEx(category GUID, flags uint32, in, out *MFT_REGISTER_TYPE_INFO, activates ***IMFActivate, count *uint32) HRESULT {
	r, _, _ := procMFTEnumEx.Call(uintptr(unsafe.Pointer(&category)), uintptr(flags),
		uintptr(unsafe.Pointer(in)), uintptr(unsafe.Pointer(out)),
		uintptr(unsafe.Pointer(activates)), uintptr(unsafe.Pointer(count)))
	return hres(r)
}
