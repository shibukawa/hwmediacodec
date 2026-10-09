//go:build windows && (amd64 || arm64)

package sys

import (
	"syscall"
	"unsafe"
)

// ID3D11Device is the Direct3D 11 device (d3d11.h). Only the entries used by
// the backend have methods; the vtable lists every slot so the indices match
// the header.
type ID3D11Device struct{ vtbl *iD3D11DeviceVtbl }

type iD3D11DeviceVtbl struct {
	iUnknownVtbl
	CreateBuffer                         uintptr
	CreateTexture1D                      uintptr
	CreateTexture2D                      uintptr
	CreateTexture3D                      uintptr
	CreateShaderResourceView             uintptr
	CreateUnorderedAccessView            uintptr
	CreateRenderTargetView               uintptr
	CreateDepthStencilView               uintptr
	CreateInputLayout                    uintptr
	CreateVertexShader                   uintptr
	CreateGeometryShader                 uintptr
	CreateGeometryShaderWithStreamOutput uintptr
	CreatePixelShader                    uintptr
	CreateHullShader                     uintptr
	CreateDomainShader                   uintptr
	CreateComputeShader                  uintptr
	CreateClassLinkage                   uintptr
	CreateBlendState                     uintptr
	CreateDepthStencilState              uintptr
	CreateRasterizerState                uintptr
	CreateSamplerState                   uintptr
	CreateQuery                          uintptr
	CreatePredicate                      uintptr
	CreateCounter                        uintptr
	CreateDeferredContext                uintptr
	OpenSharedResource                   uintptr
	CheckFormatSupport                   uintptr
	CheckMultisampleQualityLevels        uintptr
	CheckCounterInfo                     uintptr
	CheckCounter                         uintptr
	CheckFeatureSupport                  uintptr
	GetPrivateData                       uintptr
	SetPrivateData                       uintptr
	SetPrivateDataInterface              uintptr
	GetFeatureLevel                      uintptr
	GetCreationFlags                     uintptr
	GetDeviceRemovedReason               uintptr
	GetImmediateContext                  uintptr
	SetExceptionMode                     uintptr
	GetExceptionMode                     uintptr
}

// Unknown returns the IUnknown view.
func (d *ID3D11Device) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(d)) }

// Release releases the object.
func (d *ID3D11Device) Release() { d.Unknown().Release() }

// CreateTexture2D creates a texture without initial data.
func (d *ID3D11Device) CreateTexture2D(desc *D3D11_TEXTURE2D_DESC, out **ID3D11Texture2D) HRESULT {
	r, _, _ := syscall.SyscallN(d.vtbl.CreateTexture2D, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(desc)), 0, uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// GetImmediateContext returns the immediate context (AddRef'd).
func (d *ID3D11Device) GetImmediateContext(out **ID3D11DeviceContext) {
	syscall.SyscallN(d.vtbl.GetImmediateContext, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(out)))
}

// ID3D11DeviceContext issues commands to the device (d3d11.h).
type ID3D11DeviceContext struct{ vtbl *iD3D11DeviceContextVtbl }

type iD3D11DeviceContextVtbl struct {
	iUnknownVtbl
	GetDevice                                 uintptr
	GetPrivateData                            uintptr
	SetPrivateData                            uintptr
	SetPrivateDataInterface                   uintptr
	VSSetConstantBuffers                      uintptr
	PSSetShaderResources                      uintptr
	PSSetShader                               uintptr
	PSSetSamplers                             uintptr
	VSSetShader                               uintptr
	DrawIndexed                               uintptr
	Draw                                      uintptr
	Map                                       uintptr
	Unmap                                     uintptr
	PSSetConstantBuffers                      uintptr
	IASetInputLayout                          uintptr
	IASetVertexBuffers                        uintptr
	IASetIndexBuffer                          uintptr
	DrawIndexedInstanced                      uintptr
	DrawInstanced                             uintptr
	GSSetConstantBuffers                      uintptr
	GSSetShader                               uintptr
	IASetPrimitiveTopology                    uintptr
	VSSetShaderResources                      uintptr
	VSSetSamplers                             uintptr
	Begin                                     uintptr
	End                                       uintptr
	GetData                                   uintptr
	SetPredication                            uintptr
	GSSetShaderResources                      uintptr
	GSSetSamplers                             uintptr
	OMSetRenderTargets                        uintptr
	OMSetRenderTargetsAndUnorderedAccessViews uintptr
	OMSetBlendState                           uintptr
	OMSetDepthStencilState                    uintptr
	SOSetTargets                              uintptr
	DrawAuto                                  uintptr
	DrawIndexedInstancedIndirect              uintptr
	DrawInstancedIndirect                     uintptr
	Dispatch                                  uintptr
	DispatchIndirect                          uintptr
	RSSetState                                uintptr
	RSSetViewports                            uintptr
	RSSetScissorRects                         uintptr
	CopySubresourceRegion                     uintptr
	CopyResource                              uintptr
	UpdateSubresource                         uintptr
	// Later entries (CopyStructureCount ... FinishCommandList) are not used.
}

// Unknown returns the IUnknown view.
func (c *ID3D11DeviceContext) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(c)) }

// Release releases the object.
func (c *ID3D11DeviceContext) Release() { c.Unknown().Release() }

// Map maps a subresource for CPU access.
func (c *ID3D11DeviceContext) Map(resource *ID3D11Texture2D, subresource, mapType, flags uint32, mapped *D3D11_MAPPED_SUBRESOURCE) HRESULT {
	r, _, _ := syscall.SyscallN(c.vtbl.Map, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(resource)), uintptr(subresource), uintptr(mapType), uintptr(flags), uintptr(unsafe.Pointer(mapped)))
	return hres(r)
}

// Unmap unmaps a subresource.
func (c *ID3D11DeviceContext) Unmap(resource *ID3D11Texture2D, subresource uint32) {
	syscall.SyscallN(c.vtbl.Unmap, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(resource)), uintptr(subresource))
}

// CopySubresource copies one whole subresource of src into dst at (0,0,0).
func (c *ID3D11DeviceContext) CopySubresource(dst *ID3D11Texture2D, dstSubresource uint32, src *ID3D11Texture2D, srcSubresource uint32) {
	syscall.SyscallN(c.vtbl.CopySubresourceRegion, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(dst)), uintptr(dstSubresource), 0, 0, 0, uintptr(unsafe.Pointer(src)), uintptr(srcSubresource), 0)
}

// ID3D11Texture2D is a 2D texture (d3d11.h). ID3D11Resource and
// ID3D11DeviceChild methods precede GetDesc.
type ID3D11Texture2D struct{ vtbl *iD3D11Texture2DVtbl }

type iD3D11Texture2DVtbl struct {
	iUnknownVtbl
	GetDevice               uintptr
	GetPrivateData          uintptr
	SetPrivateData          uintptr
	SetPrivateDataInterface uintptr
	GetType                 uintptr
	SetEvictionPriority     uintptr
	GetEvictionPriority     uintptr
	GetDesc                 uintptr
}

// Unknown returns the IUnknown view.
func (t *ID3D11Texture2D) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(t)) }

// Release releases the object.
func (t *ID3D11Texture2D) Release() { t.Unknown().Release() }

// GetDesc fills in the texture description.
func (t *ID3D11Texture2D) GetDesc(desc *D3D11_TEXTURE2D_DESC) {
	syscall.SyscallN(t.vtbl.GetDesc, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(desc)))
}

// ID3D10Multithread toggles the device's internal locking (d3d10.h).
type ID3D10Multithread struct{ vtbl *iD3D10MultithreadVtbl }

type iD3D10MultithreadVtbl struct {
	iUnknownVtbl
	Enter                   uintptr
	Leave                   uintptr
	SetMultithreadProtected uintptr
	GetMultithreadProtected uintptr
}

// Unknown returns the IUnknown view.
func (m *ID3D10Multithread) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(m)) }

// Release releases the object.
func (m *ID3D10Multithread) Release() { m.Unknown().Release() }

// SetMultithreadProtected enables or disables locking and returns the
// previous setting.
func (m *ID3D10Multithread) SetMultithreadProtected(protect bool) bool {
	var v uintptr
	if protect {
		v = 1
	}
	r, _, _ := syscall.SyscallN(m.vtbl.SetMultithreadProtected, uintptr(unsafe.Pointer(m)), v)
	return uint32(r) != 0
}

// ID3D11VideoDevice exposes the video decoding capabilities of a device
// (d3d11.h).
type ID3D11VideoDevice struct{ vtbl *iD3D11VideoDeviceVtbl }

type iD3D11VideoDeviceVtbl struct {
	iUnknownVtbl
	CreateVideoDecoder             uintptr
	CreateVideoProcessor           uintptr
	CreateAuthenticatedChannel     uintptr
	CreateCryptoSession            uintptr
	CreateVideoDecoderOutputView   uintptr
	CreateVideoProcessorInputView  uintptr
	CreateVideoProcessorOutputView uintptr
	CreateVideoProcessorEnumerator uintptr
	GetVideoDecoderProfileCount    uintptr
	GetVideoDecoderProfile         uintptr
	CheckVideoDecoderFormat        uintptr
	GetVideoDecoderConfigCount     uintptr
	GetVideoDecoderConfig          uintptr
	GetContentProtectionCaps       uintptr
	CheckCryptoKeyExchange         uintptr
	SetPrivateData                 uintptr
	SetPrivateDataInterface        uintptr
}

// Unknown returns the IUnknown view.
func (v *ID3D11VideoDevice) Unknown() *IUnknown { return (*IUnknown)(unsafe.Pointer(v)) }

// Release releases the object.
func (v *ID3D11VideoDevice) Release() { v.Unknown().Release() }

// GetVideoDecoderProfileCount returns the number of DXVA decoder profiles.
func (v *ID3D11VideoDevice) GetVideoDecoderProfileCount() uint32 {
	r, _, _ := syscall.SyscallN(v.vtbl.GetVideoDecoderProfileCount, uintptr(unsafe.Pointer(v)))
	return uint32(r)
}

// GetVideoDecoderProfile returns the profile GUID at index.
func (v *ID3D11VideoDevice) GetVideoDecoderProfile(index uint32, out *GUID) HRESULT {
	r, _, _ := syscall.SyscallN(v.vtbl.GetVideoDecoderProfile, uintptr(unsafe.Pointer(v)), uintptr(index), uintptr(unsafe.Pointer(out)))
	return hres(r)
}

// CheckVideoDecoderFormat reports whether a profile can output format.
func (v *ID3D11VideoDevice) CheckVideoDecoderFormat(profile *GUID, format uint32, supported *int32) HRESULT {
	r, _, _ := syscall.SyscallN(v.vtbl.CheckVideoDecoderFormat, uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(profile)), uintptr(format), uintptr(unsafe.Pointer(supported)))
	return hres(r)
}

// GetVideoDecoderConfigCount returns how many decoder configurations exist
// for desc; zero means the resolution/format combination is unsupported.
func (v *ID3D11VideoDevice) GetVideoDecoderConfigCount(desc *D3D11_VIDEO_DECODER_DESC, count *uint32) HRESULT {
	r, _, _ := syscall.SyscallN(v.vtbl.GetVideoDecoderConfigCount, uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(desc)), uintptr(unsafe.Pointer(count)))
	return hres(r)
}
