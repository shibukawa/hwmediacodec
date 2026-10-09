//go:build windows && (amd64 || arm64)

package sys

import (
	"syscall"
	"unsafe"
)

// COM interfaces are called through their vtables. Each Go type below is a
// view of the native object pointer: the first word of the object points at
// the vtable, whose entries are listed in declaration order from the SDK
// headers. Methods convert Go pointers to uintptr inside the SyscallN call so
// the compiler keeps the pointed-to memory alive for the duration of the call.

// IUnknown is the root COM interface.
type IUnknown struct{ vtbl *iUnknownVtbl }

type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

// QueryInterface stores an interface pointer for iid in *out.
func (u *IUnknown) QueryInterface(iid *GUID, out unsafe.Pointer) HRESULT {
	r, _, _ := syscall.SyscallN(u.vtbl.QueryInterface, uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(iid)), uintptr(out))
	return hres(r)
}

// AddRef increments the reference count.
func (u *IUnknown) AddRef() uint32 {
	r, _, _ := syscall.SyscallN(u.vtbl.AddRef, uintptr(unsafe.Pointer(u)))
	return uint32(r)
}

// Release decrements the reference count. It is a no-op on a nil receiver.
func (u *IUnknown) Release() uint32 {
	if u == nil {
		return 0
	}
	r, _, _ := syscall.SyscallN(u.vtbl.Release, uintptr(unsafe.Pointer(u)))
	return uint32(r)
}

// IMFAttributes is the Media Foundation attribute store (mfobjects.h).
type IMFAttributes struct{ vtbl *iMFAttributesVtbl }

type iMFAttributesVtbl struct {
	iUnknownVtbl
	GetItem            uintptr
	GetItemType        uintptr
	CompareItem        uintptr
	Compare            uintptr
	GetUINT32          uintptr
	GetUINT64          uintptr
	GetDouble          uintptr
	GetGUID            uintptr
	GetStringLength    uintptr
	GetString          uintptr
	GetAllocatedString uintptr
	GetBlobSize        uintptr
	GetBlob            uintptr
	GetAllocatedBlob   uintptr
	GetUnknown         uintptr
	SetItem            uintptr
	DeleteItem         uintptr
	DeleteAllItems     uintptr
	SetUINT32          uintptr
	SetUINT64          uintptr
	SetDouble          uintptr
	SetGUID            uintptr
	SetString          uintptr
	SetBlob            uintptr
	SetUnknown         uintptr
	LockStore          uintptr
	UnlockStore        uintptr
	GetCount           uintptr
	GetItemByIndex     uintptr
	CopyAllItems       uintptr
}

// Unknown returns the IUnknown view.
func (a *IMFAttributes) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(a)) }

// Release releases the object.
func (a *IMFAttributes) Release() { a.Unknown().Release() }

// GetUINT32 reads a UINT32 attribute.
func (a *IMFAttributes) GetUINT32(key *GUID, out *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetUINT32, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetUINT64 reads a UINT64 attribute.
func (a *IMFAttributes) GetUINT64(key *GUID, out *uint64) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetUINT64, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetGUID reads a GUID attribute.
func (a *IMFAttributes) GetGUID(key *GUID, out *GUID) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetGUID, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetBlobSize reads the size of a blob attribute.
func (a *IMFAttributes) GetBlobSize(key *GUID, out *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetBlobSize, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetBlob copies a blob attribute into buf.
func (a *IMFAttributes) GetBlob(key *GUID, buf *byte, size uint32, written *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetBlob, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(buf)), uintptr(size), uintptr(unsafe.Pointer(written)))
	return hres(r)
}

// GetAllocatedString reads a string attribute into COM-allocated memory.
func (a *IMFAttributes) GetAllocatedString(key *GUID, out **uint16, length *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.GetAllocatedString, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(out)), uintptr(unsafe.Pointer(length)))
	return hres(r)
}

// SetUINT32 writes a UINT32 attribute.
func (a *IMFAttributes) SetUINT32(key *GUID, v uint32) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.SetUINT32, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(v))
	return hres(r)
}

// SetUINT64 writes a UINT64 attribute.
func (a *IMFAttributes) SetUINT64(key *GUID, v uint64) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.SetUINT64, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(v))
	return hres(r)
}

// SetGUID writes a GUID attribute.
func (a *IMFAttributes) SetGUID(key *GUID, v *GUID) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.SetGUID, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(v)))
	return hres(r)
}

// UINT32 returns a UINT32 attribute and whether it is present.
func (a *IMFAttributes) UINT32(key *GUID) (uint32, bool) {
	var v uint32
	if a.GetUINT32(key, &v).Failed() {
		return 0, false
	}
	return v, true
}

// UINT64 returns a UINT64 attribute and whether it is present.
func (a *IMFAttributes) UINT64(key *GUID) (uint64, bool) {
	var v uint64
	if a.GetUINT64(key, &v).Failed() {
		return 0, false
	}
	return v, true
}

// GUID returns a GUID attribute and whether it is present.
func (a *IMFAttributes) GUID(key *GUID) (GUID, bool) {
	var v GUID
	if a.GetGUID(key, &v).Failed() {
		return GUID{}, false
	}
	return v, true
}

// Blob returns a copy of a blob attribute and whether it is present.
func (a *IMFAttributes) Blob(key *GUID) ([]byte, bool) {
	var size uint32
	if a.GetBlobSize(key, &size).Failed() || size == 0 {
		return nil, false
	}
	buf := make([]byte, size)
	var written uint32
	if a.GetBlob(key, &buf[0], size, &written).Failed() {
		return nil, false
	}
	return buf[:written], true
}

// String returns a string attribute and whether it is present.
func (a *IMFAttributes) String(key *GUID) (string, bool) {
	var p *uint16
	var n uint32
	if a.GetAllocatedString(key, &p, &n).Failed() || p == nil {
		return "", false
	}
	s := UTF16PtrToString(p)
	CoTaskMemFree(unsafe.Pointer(p))
	return s, true
}

// IMFMediaType describes a media format (mfobjects.h).
type IMFMediaType struct{ vtbl *iMFMediaTypeVtbl }

type iMFMediaTypeVtbl struct {
	iMFAttributesVtbl
	GetMajorType       uintptr
	IsCompressedFormat uintptr
	IsEqual            uintptr
	GetRepresentation  uintptr
	FreeRepresentation uintptr
}

// Attributes returns the IMFAttributes view.
func (t *IMFMediaType) Attributes() *IMFAttributes { return (*IMFAttributes)(unsafe.Pointer(t)) }

// Release releases the object.
func (t *IMFMediaType) Release() { t.Attributes().Release() }

// IMFActivate creates an object on demand (mfobjects.h).
type IMFActivate struct{ vtbl *iMFActivateVtbl }

type iMFActivateVtbl struct {
	iMFAttributesVtbl
	ActivateObject uintptr
	ShutdownObject uintptr
	DetachObject   uintptr
}

// Attributes returns the IMFAttributes view.
func (a *IMFActivate) Attributes() *IMFAttributes { return (*IMFAttributes)(unsafe.Pointer(a)) }

// Release releases the activation object (not the object it created).
func (a *IMFActivate) Release() { a.Attributes().Release() }

// ActivateObject creates the object and stores an interface pointer in *out.
func (a *IMFActivate) ActivateObject(iid *GUID, out unsafe.Pointer) HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.ActivateObject, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(iid)), uintptr(out))
	return hres(r)
}

// ShutdownObject shuts down the created object.
func (a *IMFActivate) ShutdownObject() HRESULT {
	r, _, _ := syscall.SyscallN(a.vtbl.ShutdownObject, uintptr(unsafe.Pointer(a)))
	return hres(r)
}

// IMFSample carries media buffers plus timing (mfobjects.h).
type IMFSample struct{ vtbl *iMFSampleVtbl }

type iMFSampleVtbl struct {
	iMFAttributesVtbl
	GetSampleFlags            uintptr
	SetSampleFlags            uintptr
	GetSampleTime             uintptr
	SetSampleTime             uintptr
	GetSampleDuration         uintptr
	SetSampleDuration         uintptr
	GetBufferCount            uintptr
	GetBufferByIndex          uintptr
	ConvertToContiguousBuffer uintptr
	AddBuffer                 uintptr
	RemoveBufferByIndex       uintptr
	RemoveAllBuffers          uintptr
	GetTotalLength            uintptr
	CopyToBuffer              uintptr
}

// Attributes returns the IMFAttributes view.
func (s *IMFSample) Attributes() *IMFAttributes { return (*IMFAttributes)(unsafe.Pointer(s)) }

// Release releases the object.
func (s *IMFSample) Release() { s.Attributes().Release() }

// GetSampleTime reads the presentation time in 100-nanosecond units.
func (s *IMFSample) GetSampleTime(out *int64) HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.GetSampleTime, uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// SetSampleTime sets the presentation time in 100-nanosecond units.
func (s *IMFSample) SetSampleTime(t int64) HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.SetSampleTime, uintptr(unsafe.Pointer(s)), uintptr(t))
	return hres(r)
}

// SetSampleDuration sets the duration in 100-nanosecond units.
func (s *IMFSample) SetSampleDuration(d int64) HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.SetSampleDuration, uintptr(unsafe.Pointer(s)), uintptr(d))
	return hres(r)
}

// GetBufferCount returns the number of buffers in the sample.
func (s *IMFSample) GetBufferCount(out *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.GetBufferCount, uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetBufferByIndex returns a buffer of the sample.
func (s *IMFSample) GetBufferByIndex(index uint32, out **IMFMediaBuffer) HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.GetBufferByIndex, uintptr(unsafe.Pointer(s)), uintptr(index), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// AddBuffer appends a buffer to the sample.
func (s *IMFSample) AddBuffer(b *IMFMediaBuffer) HRESULT {
	r, _, _ := syscall.SyscallN(s.vtbl.AddBuffer, uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(b)))
	return hres(r)
}

// IMFMediaBuffer is a block of memory (mfobjects.h).
type IMFMediaBuffer struct{ vtbl *iMFMediaBufferVtbl }

type iMFMediaBufferVtbl struct {
	iUnknownVtbl
	Lock             uintptr
	Unlock           uintptr
	GetCurrentLength uintptr
	SetCurrentLength uintptr
	GetMaxLength     uintptr
}

// Unknown returns the IUnknown view.
func (b *IMFMediaBuffer) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(b)) }

// Release releases the object.
func (b *IMFMediaBuffer) Release() { b.Unknown().Release() }

// Lock maps the buffer memory.
func (b *IMFMediaBuffer) Lock(data **byte, maxLength, currentLength *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.Lock, uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(data)), uintptr(unsafe.Pointer(maxLength)), uintptr(unsafe.Pointer(currentLength)))
	return hres(r)
}

// Unlock unmaps the buffer memory.
func (b *IMFMediaBuffer) Unlock() HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.Unlock, uintptr(unsafe.Pointer(b)))
	return hres(r)
}

// SetCurrentLength sets the number of valid bytes.
func (b *IMFMediaBuffer) SetCurrentLength(n uint32) HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.SetCurrentLength, uintptr(unsafe.Pointer(b)), uintptr(n))
	return hres(r)
}

// IMF2DBuffer2 exposes a buffer as a 2D image (mfobjects.h).
type IMF2DBuffer2 struct{ vtbl *iMF2DBuffer2Vtbl }

type iMF2DBuffer2Vtbl struct {
	iUnknownVtbl
	Lock2D               uintptr
	Unlock2D             uintptr
	GetScanline0AndPitch uintptr
	IsContiguousFormat   uintptr
	GetContiguousLength  uintptr
	ContiguousCopyTo     uintptr
	ContiguousCopyFrom   uintptr
	Lock2DSize           uintptr
	Copy2DTo             uintptr
}

// Unknown returns the IUnknown view.
func (b *IMF2DBuffer2) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(b)) }

// Release releases the object.
func (b *IMF2DBuffer2) Release() { b.Unknown().Release() }

// Lock2DSize maps the image and reports its pitch and total extent.
func (b *IMF2DBuffer2) Lock2DSize(flags uint32, scanline0 **byte, pitch *int32, bufferStart **byte, bufferLength *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.Lock2DSize, uintptr(unsafe.Pointer(b)), uintptr(flags), uintptr(unsafe.Pointer(scanline0)), uintptr(unsafe.Pointer(pitch)), uintptr(unsafe.Pointer(bufferStart)), uintptr(unsafe.Pointer(bufferLength)))
	return hres(r)
}

// Unlock2D unmaps the image.
func (b *IMF2DBuffer2) Unlock2D() HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.Unlock2D, uintptr(unsafe.Pointer(b)))
	return hres(r)
}

// IMFDXGIBuffer wraps a Direct3D 11 texture (mfobjects.h).
type IMFDXGIBuffer struct{ vtbl *iMFDXGIBufferVtbl }

type iMFDXGIBufferVtbl struct {
	iUnknownVtbl
	GetResource         uintptr
	GetSubresourceIndex uintptr
	GetUnknown          uintptr
	SetUnknown          uintptr
}

// Unknown returns the IUnknown view.
func (b *IMFDXGIBuffer) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(b)) }

// Release releases the object.
func (b *IMFDXGIBuffer) Release() { b.Unknown().Release() }

// GetResource queries the underlying texture for iid.
func (b *IMFDXGIBuffer) GetResource(iid *GUID, out unsafe.Pointer) HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.GetResource, uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(iid)), uintptr(out))
	return hres(r)
}

// GetSubresourceIndex returns the texture array slice this buffer refers to.
func (b *IMFDXGIBuffer) GetSubresourceIndex(out *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(b.vtbl.GetSubresourceIndex, uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// IMFDXGIDeviceManager shares a Direct3D 11 device with transforms
// (mfobjects.h).
type IMFDXGIDeviceManager struct{ vtbl *iMFDXGIDeviceManagerVtbl }

type iMFDXGIDeviceManagerVtbl struct {
	iUnknownVtbl
	CloseDeviceHandle uintptr
	GetVideoService   uintptr
	LockDevice        uintptr
	OpenDeviceHandle  uintptr
	ResetDevice       uintptr
	TestDevice        uintptr
	UnlockDevice      uintptr
}

// Unknown returns the IUnknown view.
func (m *IMFDXGIDeviceManager) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(m)) }

// Release releases the object.
func (m *IMFDXGIDeviceManager) Release() { m.Unknown().Release() }

// ResetDevice installs the device in the manager.
func (m *IMFDXGIDeviceManager) ResetDevice(device *ID3D11Device, resetToken uint32) HRESULT {
	r, _, _ := syscall.SyscallN(m.vtbl.ResetDevice, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(device)), uintptr(resetToken))
	return hres(r)
}

// IMFTransform is a Media Foundation transform (mftransform.h).
type IMFTransform struct{ vtbl *iMFTransformVtbl }

type iMFTransformVtbl struct {
	iUnknownVtbl
	GetStreamLimits           uintptr
	GetStreamCount            uintptr
	GetStreamIDs              uintptr
	GetInputStreamInfo        uintptr
	GetOutputStreamInfo       uintptr
	GetAttributes             uintptr
	GetInputStreamAttributes  uintptr
	GetOutputStreamAttributes uintptr
	DeleteInputStream         uintptr
	AddInputStreams           uintptr
	GetInputAvailableType     uintptr
	GetOutputAvailableType    uintptr
	SetInputType              uintptr
	SetOutputType             uintptr
	GetInputCurrentType       uintptr
	GetOutputCurrentType      uintptr
	GetInputStatus            uintptr
	GetOutputStatus           uintptr
	SetOutputBounds           uintptr
	ProcessEvent              uintptr
	ProcessMessage            uintptr
	ProcessInput              uintptr
	ProcessOutput             uintptr
}

// Unknown returns the IUnknown view.
func (t *IMFTransform) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(t)) }

// Release releases the object.
func (t *IMFTransform) Release() { t.Unknown().Release() }

// GetStreamIDs returns the stream identifiers; E_NOTIMPL means they are 0..n-1.
func (t *IMFTransform) GetStreamIDs(inputSize uint32, inputIDs *uint32, outputSize uint32, outputIDs *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.GetStreamIDs, uintptr(unsafe.Pointer(t)), uintptr(inputSize), uintptr(unsafe.Pointer(inputIDs)), uintptr(outputSize), uintptr(unsafe.Pointer(outputIDs)))
	return hres(r)
}

// GetInputStreamInfo describes an input stream.
func (t *IMFTransform) GetInputStreamInfo(id uint32, info *MFT_INPUT_STREAM_INFO) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.GetInputStreamInfo, uintptr(unsafe.Pointer(t)), uintptr(id), uintptr(unsafe.Pointer(info)))
	return hres(r)
}

// GetOutputStreamInfo describes an output stream.
func (t *IMFTransform) GetOutputStreamInfo(id uint32, info *MFT_OUTPUT_STREAM_INFO) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.GetOutputStreamInfo, uintptr(unsafe.Pointer(t)), uintptr(id), uintptr(unsafe.Pointer(info)))
	return hres(r)
}

// GetAttributes returns the transform's attribute store.
func (t *IMFTransform) GetAttributes(out **IMFAttributes) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.GetAttributes, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetOutputStreamAttributes returns an output stream's attribute store.
func (t *IMFTransform) GetOutputStreamAttributes(id uint32, out **IMFAttributes) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.GetOutputStreamAttributes, uintptr(unsafe.Pointer(t)), uintptr(id), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetOutputAvailableType enumerates the output types the transform offers.
func (t *IMFTransform) GetOutputAvailableType(id, index uint32, out **IMFMediaType) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.GetOutputAvailableType, uintptr(unsafe.Pointer(t)), uintptr(id), uintptr(index), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// SetInputType sets or tests an input type.
func (t *IMFTransform) SetInputType(id uint32, mt *IMFMediaType, flags uint32) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.SetInputType, uintptr(unsafe.Pointer(t)), uintptr(id), uintptr(unsafe.Pointer(mt)), uintptr(flags))
	return hres(r)
}

// SetOutputType sets or tests an output type.
func (t *IMFTransform) SetOutputType(id uint32, mt *IMFMediaType, flags uint32) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.SetOutputType, uintptr(unsafe.Pointer(t)), uintptr(id), uintptr(unsafe.Pointer(mt)), uintptr(flags))
	return hres(r)
}

// GetOutputCurrentType returns the current output type.
func (t *IMFTransform) GetOutputCurrentType(id uint32, out **IMFMediaType) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.GetOutputCurrentType, uintptr(unsafe.Pointer(t)), uintptr(id), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// ProcessMessage sends a control message without an object parameter.
func (t *IMFTransform) ProcessMessage(message uint32, param uintptr) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.ProcessMessage, uintptr(unsafe.Pointer(t)), uintptr(message), param)
	return hres(r)
}

// SetD3DManager sends MFT_MESSAGE_SET_D3D_MANAGER with the device manager
// (nil to detach).
func (t *IMFTransform) SetD3DManager(m *IMFDXGIDeviceManager) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.ProcessMessage, uintptr(unsafe.Pointer(t)), uintptr(MFT_MESSAGE_SET_D3D_MANAGER), uintptr(unsafe.Pointer(m)))
	return hres(r)
}

// ProcessInput delivers a sample to an input stream.
func (t *IMFTransform) ProcessInput(id uint32, sample *IMFSample, flags uint32) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.ProcessInput, uintptr(unsafe.Pointer(t)), uintptr(id), uintptr(unsafe.Pointer(sample)), uintptr(flags))
	return hres(r)
}

// ProcessOutput generates output into the buffers.
func (t *IMFTransform) ProcessOutput(flags, count uint32, buffers *MFT_OUTPUT_DATA_BUFFER, status *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(t.vtbl.ProcessOutput, uintptr(unsafe.Pointer(t)), uintptr(flags), uintptr(count), uintptr(unsafe.Pointer(buffers)), uintptr(unsafe.Pointer(status)))
	return hres(r)
}
