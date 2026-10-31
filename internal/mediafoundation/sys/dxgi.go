//go:build windows && (amd64 || arm64)

package sys

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modDXGI                = windows.NewLazySystemDLL("dxgi.dll")
	procCreateDXGIFactory1 = modDXGI.NewProc("CreateDXGIFactory1")

	iidIDXGIFactory1 = guid(0x770aae78, 0xf26f, 0x4dba, 0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87)
)

// Adapter is one graphics adapter as DXGI lists it.
type Adapter struct {
	Name     string // "AMD Radeon(TM) Graphics"
	VendorID uint32 // PCI vendor: 0x1002 AMD, 0x10DE NVIDIA, 0x8086 Intel
}

// dxgiAdapterDesc is DXGI_ADAPTER_DESC (dxgi.h).
type dxgiAdapterDesc struct {
	Description           [128]uint16
	VendorID              uint32
	DeviceID              uint32
	SubSysID              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	AdapterLuid           [8]byte
}

// vtable slots of IDXGIFactory1::EnumAdapters1 and IDXGIAdapter::GetDesc
// (IUnknown, IDXGIObject's four entries, then the interface's own).
const (
	slotEnumAdapters1 = 12
	slotGetDesc       = 8
)

// slot returns entry i of the object's vtable.
func slot(obj *IUnknown, i int) uintptr {
	return (*[16]uintptr)(unsafe.Pointer(obj.vtbl))[i]
}

// Adapters lists the adapters in DXGI order; the first is the one
// D3D11CreateDevice uses without an adapter argument. It returns nil when
// DXGI is unavailable.
func Adapters() []Adapter {
	if procCreateDXGIFactory1.Find() != nil {
		return nil
	}
	var factory *IUnknown
	if r, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); hres(r).Failed() || factory == nil {
		return nil
	}
	defer factory.Release()
	var out []Adapter
	for i := 0; ; i++ {
		var adapter *IUnknown
		if r, _, _ := syscall.SyscallN(slot(factory, slotEnumAdapters1), uintptr(unsafe.Pointer(factory)), uintptr(i), uintptr(unsafe.Pointer(&adapter))); hres(r).Failed() || adapter == nil {
			break
		}
		var desc dxgiAdapterDesc
		if r, _, _ := syscall.SyscallN(slot(adapter, slotGetDesc), uintptr(unsafe.Pointer(adapter)), uintptr(unsafe.Pointer(&desc))); !hres(r).Failed() {
			out = append(out, Adapter{Name: windows.UTF16ToString(desc.Description[:]), VendorID: desc.VendorID})
		}
		adapter.Release()
	}
	return out
}
