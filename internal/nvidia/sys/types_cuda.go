// Package sys binds the NVIDIA driver libraries (libcuda, libnvcuvid and
// libnvidia-encode) through purego so that no cgo is required. The struct
// definitions mirror the Video Codec SDK 11.1 headers as redistributed in
// FFmpeg's nv-codec-headers; layout_test.go checks their sizes and field
// offsets against values measured with a C compiler for linux/amd64 and
// linux/arm64.
package sys

import "unsafe"

// CUresult values the backend interprets. Other codes are described through
// cuGetErrorString.
const (
	CUDASuccess             int32 = 0
	CUDAErrorInvalidValue   int32 = 1
	CUDAErrorOutOfMemory    int32 = 2
	CUDAErrorNotInitialized int32 = 3
	CUDAErrorDeinitialized  int32 = 4
	CUDAErrorNoDevice       int32 = 100
	CUDAErrorInvalidDevice  int32 = 101
	CUDAErrorInvalidContext int32 = 201
	CUDAErrorNotSupported   int32 = 801
	CUDAErrorUnknown        int32 = 999
)

// CUmemorytype values.
const (
	MemoryTypeHost   uint32 = 1
	MemoryTypeDevice uint32 = 2
	MemoryTypeArray  uint32 = 3
)

// CUdevice_attribute values.
const (
	DeviceAttributeComputeCapabilityMajor int32 = 75
	DeviceAttributeComputeCapabilityMinor int32 = 76
)

// Memcpy2D mirrors CUDA_MEMCPY2D (the _v2 variant with size_t fields).
type Memcpy2D struct {
	SrcXInBytes   uintptr
	SrcY          uintptr
	SrcMemoryType uint32
	SrcHost       unsafe.Pointer
	SrcDevice     uint64
	SrcArray      uintptr
	SrcPitch      uintptr
	DstXInBytes   uintptr
	DstY          uintptr
	DstMemoryType uint32
	DstHost       unsafe.Pointer
	DstDevice     uint64
	DstArray      uintptr
	DstPitch      uintptr
	WidthInBytes  uintptr
	Height        uintptr
}
