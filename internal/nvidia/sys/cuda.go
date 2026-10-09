//go:build linux || (windows && amd64)

package sys

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Bound CUDA driver entry points (libcuda.so.1 on Linux, nvcuda.dll on
// Windows). They are valid after LoadCUDA returns nil.
var (
	CuInit                    func(flags uint32) int32
	CuDriverGetVersion        func(version *int32) int32
	CuDeviceGetCount          func(count *int32) int32
	CuDeviceGet               func(device *int32, ordinal int32) int32
	CuDeviceGetName           func(name *byte, n int32, device int32) int32
	CuDeviceGetAttribute      func(value *int32, attrib int32, device int32) int32
	CuDevicePrimaryCtxRetain  func(ctx *uintptr, device int32) int32
	CuDevicePrimaryCtxRelease func(device int32) int32
	CuCtxPushCurrent          func(ctx uintptr) int32
	CuCtxPopCurrent           func(ctx *uintptr) int32
	CuCtxSynchronize          func() int32
	CuMemcpy2D                func(copy *Memcpy2D) int32
	CuGetErrorString          func(result int32, str **byte) int32
	CuGetErrorName            func(result int32, str **byte) int32
)

// ErrNotAvailable is wrapped by the Load functions when a library is not
// installed, which on a machine without the NVIDIA driver is the normal
// case.
var ErrNotAvailable = errors.New("nvidia: driver library is not available")

type binder struct {
	handle uintptr
	lib    string
	errs   []string
}

func (b *binder) fn(fptr any, name string) {
	addr, err := lookup(b.handle, name)
	if err != nil {
		b.errs = append(b.errs, fmt.Sprintf("%s: %s", b.lib, name))
		return
	}
	purego.RegisterFunc(fptr, addr)
}

// fnAlt binds the first of the given symbol names that exists.
func (b *binder) fnAlt(fptr any, names ...string) {
	for _, n := range names {
		if addr, err := lookup(b.handle, n); err == nil {
			purego.RegisterFunc(fptr, addr)
			return
		}
	}
	b.errs = append(b.errs, fmt.Sprintf("%s: %s", b.lib, strings.Join(names, "/")))
}

func (b *binder) missing() error {
	if len(b.errs) > 0 {
		return errors.New("nvidia: missing symbols: " + strings.Join(b.errs, ", "))
	}
	return nil
}

// open loads the first of the given libraries that exists; openLibrary and
// lookup are the platform's loader (lib_linux.go, lib_windows.go).
func open(names []string) (*binder, error) {
	var errs []string
	for _, n := range names {
		h, err := openLibrary(n)
		if err == nil {
			return &binder{handle: h, lib: n}, nil
		}
		errs = append(errs, err.Error())
	}
	return nil, fmt.Errorf("%w: %s", ErrNotAvailable, strings.Join(errs, "; "))
}

var (
	cudaOnce sync.Once
	cudaErr  error
)

// LoadCUDA opens the CUDA driver library and binds the driver entry points.
// It is safe to call repeatedly; the result is cached.
func LoadCUDA() error {
	cudaOnce.Do(func() { cudaErr = loadCUDA() })
	return cudaErr
}

func loadCUDA() error {
	b, err := open(cudaLibraries)
	if err != nil {
		return err
	}
	b.fn(&CuInit, "cuInit")
	b.fn(&CuDriverGetVersion, "cuDriverGetVersion")
	b.fn(&CuDeviceGetCount, "cuDeviceGetCount")
	b.fn(&CuDeviceGet, "cuDeviceGet")
	b.fn(&CuDeviceGetName, "cuDeviceGetName")
	b.fn(&CuDeviceGetAttribute, "cuDeviceGetAttribute")
	b.fn(&CuDevicePrimaryCtxRetain, "cuDevicePrimaryCtxRetain")
	b.fnAlt(&CuDevicePrimaryCtxRelease, "cuDevicePrimaryCtxRelease_v2", "cuDevicePrimaryCtxRelease")
	b.fn(&CuCtxPushCurrent, "cuCtxPushCurrent_v2")
	b.fn(&CuCtxPopCurrent, "cuCtxPopCurrent_v2")
	b.fn(&CuCtxSynchronize, "cuCtxSynchronize")
	b.fn(&CuMemcpy2D, "cuMemcpy2D_v2")
	b.fn(&CuGetErrorString, "cuGetErrorString")
	b.fn(&CuGetErrorName, "cuGetErrorName")
	return b.missing()
}

// CUDAErrorString describes a CUresult.
func CUDAErrorString(result int32) string {
	if CuGetErrorString != nil {
		var p *byte
		if CuGetErrorString(result, &p) == CUDASuccess && p != nil {
			s := cString(p)
			if CuGetErrorName != nil {
				var n *byte
				if CuGetErrorName(result, &n) == CUDASuccess && n != nil {
					s = cString(n) + ": " + s
				}
			}
			return s
		}
	}
	return fmt.Sprintf("CUresult %d", result)
}

func cString(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
		if n > 4096 {
			break
		}
	}
	return string(unsafe.Slice(p, n))
}
