//go:build windows && (amd64 || arm64)

package mediafoundation

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediafoundation/sys"
)

// Name is the backend identifier reported in Capability.Backend.
const Name = "mediafoundation"

// Backend is the Media Foundation backend. It drives the synchronous decoder
// MFTs that ship with Windows (and the HEVC and AV1 Video Extensions) with
// a Direct3D 11 device attached, so the GPU's DXVA engine does the decoding
// regardless of vendor.
type Backend struct{}

// Name implements codec.Backend.
func (Backend) Name() string { return Name }

// codecInfo maps a codec to its Media Foundation subtype and DXVA profile.
type codecInfo struct {
	subtype *sys.GUID
	profile *sys.GUID
}

func mfCodec(c codec.Codec) (codecInfo, bool) {
	switch c {
	case codec.H264:
		return codecInfo{&sys.MFVideoFormat_H264, &sys.D3D11_DECODER_PROFILE_H264_VLD_NOFGT}, true
	case codec.HEVC:
		return codecInfo{&sys.MFVideoFormat_HEVC, &sys.D3D11_DECODER_PROFILE_HEVC_VLD_MAIN}, true
	case codec.AV1:
		return codecInfo{&sys.MFVideoFormat_AV1, &sys.D3D11_DECODER_PROFILE_AV1_VLD_PROFILE0}, true
	}
	return codecInfo{}, false
}

// extensionHint names the Microsoft Store package that provides the decoder
// MFT of a codec Windows does not decode out of the box.
func extensionHint(c codec.Codec) string {
	switch c {
	case codec.HEVC:
		return " (HEVC needs the \"HEVC Video Extensions\" package)"
	case codec.AV1:
		return " (AV1 needs the \"AV1 Video Extension\" package)"
	}
	return ""
}

func unsupported(c codec.Codec, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: codec.Decode, Reason: reason}
}

func backendErr(op string, hr sys.HRESULT) error {
	return &codec.BackendError{Backend: Name, Op: op, Status: int64(hr), Message: hr.String()}
}

// d3dDevice bundles the Direct3D 11 objects shared by Probe and the decoders.
type d3dDevice struct {
	device  *sys.ID3D11Device
	context *sys.ID3D11DeviceContext
	video   *sys.ID3D11VideoDevice
	manager *sys.IMFDXGIDeviceManager
}

// newD3DDevice creates a hardware Direct3D 11 device with video support and
// turns on multithread protection, which Media Foundation requires when the
// device is shared through a DXGI device manager.
func newD3DDevice() (*d3dDevice, error) {
	d := &d3dDevice{}
	var level uint32
	flags := sys.D3D11_CREATE_DEVICE_VIDEO_SUPPORT | sys.D3D11_CREATE_DEVICE_BGRA_SUPPORT
	if hr := sys.D3D11CreateDevice(sys.D3D_DRIVER_TYPE_HARDWARE, flags, nil, &d.device, &level, &d.context); hr.Failed() {
		return nil, backendErr("D3D11CreateDevice", hr)
	}
	var mt *sys.ID3D10Multithread
	if d.device.Unknown().QueryInterface(&sys.IID_ID3D10Multithread, unsafe.Pointer(&mt)) == sys.S_OK ||
		d.context.Unknown().QueryInterface(&sys.IID_ID3D10Multithread, unsafe.Pointer(&mt)) == sys.S_OK {
		mt.SetMultithreadProtected(true)
		mt.Release()
	}
	if hr := d.device.Unknown().QueryInterface(&sys.IID_ID3D11VideoDevice, unsafe.Pointer(&d.video)); hr.Failed() {
		d.release()
		return nil, backendErr("ID3D11Device::QueryInterface(ID3D11VideoDevice)", hr)
	}
	return d, nil
}

// supportsProfile reports whether the GPU exposes the DXVA decoder profile
// with NV12 output, which is what the Microsoft decoder MFTs need to decode
// in hardware.
func (d *d3dDevice) supportsProfile(profile *sys.GUID) bool {
	n := d.video.GetVideoDecoderProfileCount()
	found := false
	for i := uint32(0); i < n && !found; i++ {
		var g sys.GUID
		if d.video.GetVideoDecoderProfile(i, &g) == sys.S_OK && g == *profile {
			found = true
		}
	}
	if !found {
		return false
	}
	var supported int32
	if d.video.CheckVideoDecoderFormat(profile, sys.DXGI_FORMAT_NV12, &supported).Failed() {
		return false
	}
	return supported != 0
}

// createManager creates the DXGI device manager through which the transform
// receives the device.
func (d *d3dDevice) createManager() error {
	var token uint32
	if hr := sys.MFCreateDXGIDeviceManager(&token, &d.manager); hr.Failed() {
		return backendErr("MFCreateDXGIDeviceManager", hr)
	}
	if hr := d.manager.ResetDevice(d.device, token); hr.Failed() {
		return backendErr("IMFDXGIDeviceManager::ResetDevice", hr)
	}
	return nil
}

func (d *d3dDevice) release() {
	if d == nil {
		return
	}
	d.manager.Release()
	d.video.Release()
	d.context.Release()
	d.device.Release()
	*d = d3dDevice{}
}

// candidate is one decoder MFT returned by MFTEnumEx.
type candidate struct {
	activate *sys.IMFActivate
	name     string
	flags    uint32
}

// usable reports whether the MFT uses the synchronous processing model this
// backend drives. Vendor hardware MFTs are asynchronous and not supported
// yet; the Microsoft decoders are synchronous and use DXVA through the
// Direct3D 11 device instead.
func (c candidate) usable() bool {
	if c.flags == 0 {
		return true // attribute absent: assume a plain synchronous MFT
	}
	return c.flags&sys.MFT_ENUM_FLAG_SYNCMFT != 0 && c.flags&sys.MFT_ENUM_FLAG_HARDWARE == 0
}

// enumerateDecoders lists the decoder MFTs registered for a subtype, best
// first. The caller must releaseCandidates the result.
func enumerateDecoders(subtype *sys.GUID) ([]candidate, error) {
	in := sys.MFT_REGISTER_TYPE_INFO{MajorType: sys.MFMediaType_Video, Subtype: *subtype}
	flags := sys.MFT_ENUM_FLAG_SYNCMFT | sys.MFT_ENUM_FLAG_ASYNCMFT | sys.MFT_ENUM_FLAG_HARDWARE |
		sys.MFT_ENUM_FLAG_LOCALMFT | sys.MFT_ENUM_FLAG_SORTANDFILTER
	cands, err := enumerate(sys.MFT_CATEGORY_VIDEO_DECODER, flags, &in, nil)
	if err != nil || len(cands) > 0 {
		return cands, err
	}
	// Codec packages installed from the Store may only be listed with this
	// flag on some Windows versions; it is unknown to older releases, so a
	// failure here is not an error.
	if more, err := enumerate(sys.MFT_CATEGORY_VIDEO_DECODER, flags|sys.MFT_ENUM_FLAG_UNTRUSTED_STOREMFT, &in, nil); err == nil {
		return more, nil
	}
	return cands, nil
}

// enumerateEncoders lists the encoder MFTs that take NV12 and produce the
// subtype. With hardware set only vendor hardware MFTs are returned,
// otherwise only software ones (the Microsoft encoder). The caller must
// releaseCandidates the result.
func enumerateEncoders(subtype *sys.GUID, hardware bool) ([]candidate, error) {
	in := sys.MFT_REGISTER_TYPE_INFO{MajorType: sys.MFMediaType_Video, Subtype: sys.MFVideoFormat_NV12}
	out := sys.MFT_REGISTER_TYPE_INFO{MajorType: sys.MFMediaType_Video, Subtype: *subtype}
	flags := sys.MFT_ENUM_FLAG_SYNCMFT | sys.MFT_ENUM_FLAG_ASYNCMFT | sys.MFT_ENUM_FLAG_SORTANDFILTER
	if hardware {
		flags |= sys.MFT_ENUM_FLAG_HARDWARE
	}
	cands, err := enumerate(sys.MFT_CATEGORY_VIDEO_ENCODER, flags, &in, &out)
	if err != nil {
		return nil, err
	}
	keep := cands[:0]
	for _, c := range cands {
		if (c.flags&sys.MFT_ENUM_FLAG_HARDWARE != 0) == hardware {
			keep = append(keep, c)
		} else {
			c.activate.Release()
		}
	}
	return keep, nil
}

func enumerate(category sys.GUID, flags uint32, in, out *sys.MFT_REGISTER_TYPE_INFO) ([]candidate, error) {
	var arr **sys.IMFActivate
	var n uint32
	if hr := sys.MFTEnumEx(category, flags, in, out, &arr, &n); hr.Failed() {
		return nil, backendErr("MFTEnumEx", hr)
	}
	if arr == nil || n == 0 {
		return nil, nil
	}
	defer sys.CoTaskMemFree(unsafe.Pointer(arr))
	cands := make([]candidate, 0, n)
	for _, a := range unsafe.Slice(arr, n) {
		c := candidate{activate: a}
		c.name, _ = a.Attributes().String(&sys.MFT_FRIENDLY_NAME_Attribute)
		if c.name == "" {
			c.name = "unnamed MFT"
		}
		c.flags, _ = a.Attributes().UINT32(&sys.MF_TRANSFORM_FLAGS_Attribute)
		cands = append(cands, c)
	}
	return cands, nil
}

func releaseCandidates(cands []candidate) {
	for _, c := range cands {
		c.activate.Release()
	}
}

// Probe implements codec.Backend. A codec is reported as hardware-decodable
// when the GPU exposes its DXVA profile and a synchronous decoder MFT exists,
// and as hardware-encodable when the driver registers a hardware encoder MFT
// for it. AV1 is decoded only.
func (Backend) Probe(ctx context.Context) ([]codec.Capability, error) {
	if err := sys.Load(); err != nil {
		// Media Foundation or Direct3D 11 is absent (Windows N without the
		// Media Feature Pack, Server Core): the backend does not exist here.
		return nil, nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := sys.CoInitializeMTA(); err != nil {
		return nil, err
	}
	dev, err := newD3DDevice()
	if err != nil {
		// No hardware Direct3D 11 device with video support: nothing to report.
		return nil, nil
	}
	defer dev.release()
	var caps []codec.Capability
	for _, c := range []codec.Codec{codec.H264, codec.HEVC, codec.AV1} {
		if err := ctx.Err(); err != nil {
			return caps, err
		}
		info, _ := mfCodec(c)
		if !dev.supportsProfile(info.profile) {
			continue
		}
		cands, err := enumerateDecoders(info.subtype)
		if err != nil {
			return caps, err
		}
		usable := false
		for _, cand := range cands {
			usable = usable || cand.usable()
		}
		releaseCandidates(cands)
		if usable {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Decode, Hardware: true})
		}
	}
	for _, c := range []codec.Codec{codec.H264, codec.HEVC} {
		if err := ctx.Err(); err != nil {
			return caps, err
		}
		info, _ := mfCodec(c)
		encoders, err := enumerateEncoders(info.subtype, true)
		if err != nil {
			return caps, err
		}
		releaseCandidates(encoders)
		if len(encoders) > 0 {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Encode, Hardware: true})
		}
	}
	return caps, nil
}

// NewDecoder implements codec.Backend.
func (Backend) NewDecoder(ctx context.Context, cfg codec.DecoderConfig) (codec.Decoder, error) {
	info, ok := mfCodec(cfg.Codec)
	if !ok {
		return nil, unsupported(cfg.Codec, "only h264, hevc and av1 decoding are implemented")
	}
	if cfg.OutputFormat != codec.NV12 {
		return nil, unsupported(cfg.Codec, "output format "+cfg.OutputFormat.String()+" is not supported; use NV12")
	}
	if err := sys.Load(); err != nil {
		return nil, unsupported(cfg.Codec, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := sys.CoInitializeMTA(); err != nil {
		return nil, &codec.BackendError{Backend: Name, Op: "CoInitializeEx", Message: err.Error()}
	}
	dev, err := newD3DDevice()
	if err != nil {
		if cfg.AllowSoftware {
			return nil, unsupported(cfg.Codec, "no Direct3D 11 hardware device, and software decoding is not implemented by this backend")
		}
		return nil, unsupported(cfg.Codec, "no Direct3D 11 hardware device with video support: "+err.Error())
	}
	if !dev.supportsProfile(info.profile) {
		dev.release()
		return nil, unsupported(cfg.Codec, "the graphics device reports no DXVA decoder profile for "+cfg.Codec.String()+" with NV12 output")
	}
	if err := dev.createManager(); err != nil {
		dev.release()
		return nil, err
	}
	t, name, err := openTransform(dev, cfg.Codec, info)
	if err != nil {
		dev.release()
		return nil, err
	}
	d := newDecoder(cfg, info, dev, t, name)
	if cfg.Codec == codec.AV1 {
		// The AV1 decoder wants the frame size in its input type, which
		// is known once the first sequence header arrives.
		return d, nil
	}
	if err := d.configure(0, 0); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// openTransform activates the first usable decoder MFT for the codec and
// attaches the Direct3D 11 device manager to it.
func openTransform(dev *d3dDevice, c codec.Codec, info codecInfo) (*sys.IMFTransform, string, error) {
	cands, err := enumerateDecoders(info.subtype)
	if err != nil {
		return nil, "", err
	}
	defer releaseCandidates(cands)
	if len(cands) == 0 {
		return nil, "", unsupported(c, "no Media Foundation decoder is registered for "+c.String()+extensionHint(c))
	}
	var reasons []string
	for _, cand := range cands {
		if !cand.usable() {
			reasons = append(reasons, cand.name+": asynchronous hardware MFTs are not supported yet")
			continue
		}
		var t *sys.IMFTransform
		if hr := cand.activate.ActivateObject(&sys.IID_IMFTransform, unsafe.Pointer(&t)); hr.Failed() || t == nil {
			reasons = append(reasons, fmt.Sprintf("%s: activation failed: %s", cand.name, hr))
			continue
		}
		if why := attachD3D(t, dev.manager); why != "" {
			reasons = append(reasons, cand.name+": "+why)
			t.Release()
			cand.activate.ShutdownObject()
			continue
		}
		return t, cand.name, nil
	}
	return nil, "", unsupported(c, "no usable decoder MFT: "+strings.Join(reasons, "; "))
}

// attachD3D checks that the transform is synchronous and Direct3D 11 aware,
// then hands it the device manager. It returns a reason when the transform
// cannot be used.
func attachD3D(t *sys.IMFTransform, mgr *sys.IMFDXGIDeviceManager) string {
	var attrs *sys.IMFAttributes
	if hr := t.GetAttributes(&attrs); hr.Failed() || attrs == nil {
		return "exposes no attributes, so Direct3D 11 support cannot be confirmed"
	}
	async, _ := attrs.UINT32(&sys.MF_TRANSFORM_ASYNC)
	aware, _ := attrs.UINT32(&sys.MF_SA_D3D11_AWARE)
	attrs.Release()
	if async != 0 {
		return "asynchronous MFTs are not supported yet"
	}
	if aware == 0 {
		return "not Direct3D 11 aware (would decode in software)"
	}
	if hr := t.SetD3DManager(mgr); hr.Failed() {
		return "rejected the Direct3D 11 device: " + hr.String()
	}
	return ""
}

func unsupportedEnc(c codec.Codec, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: codec.Encode, Reason: reason}
}

// NewEncoder implements codec.Backend. It uses the vendor's hardware encoder
// MFT (Intel Quick Sync, AMD, NVIDIA) and only with AllowSoftware falls back
// to the Microsoft software encoder.
func (Backend) NewEncoder(ctx context.Context, cfg codec.EncoderConfig) (codec.Encoder, error) {
	info, ok := mfCodec(cfg.Codec)
	if !ok || cfg.Codec == codec.AV1 {
		return nil, unsupportedEnc(cfg.Codec, "only h264 and hevc encoding are implemented (AV1 is decoded only)")
	}
	if cfg.InputFormat != codec.NV12 {
		return nil, unsupportedEnc(cfg.Codec, "input format "+cfg.InputFormat.String()+" is not supported; use NV12")
	}
	if err := sys.Load(); err != nil {
		return nil, unsupportedEnc(cfg.Codec, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := sys.CoInitializeMTA(); err != nil {
		return nil, &codec.BackendError{Backend: Name, Op: "CoInitializeEx", Message: err.Error()}
	}
	cands, err := enumerateEncoders(info.subtype, true)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 && cfg.AllowSoftware {
		if cands, err = enumerateEncoders(info.subtype, false); err != nil {
			return nil, err
		}
	}
	if len(cands) == 0 {
		if cfg.AllowSoftware {
			return nil, unsupportedEnc(cfg.Codec, "no encoder MFT is registered for "+cfg.Codec.String())
		}
		return nil, unsupportedEnc(cfg.Codec, "the graphics driver registers no hardware encoder MFT for "+cfg.Codec.String()+" (use WithSoftwareFallback to allow the Microsoft software encoder)")
	}
	defer releaseCandidates(cands)
	var reasons []string
	for _, cand := range cands {
		var t *sys.IMFTransform
		if hr := cand.activate.ActivateObject(&sys.IID_IMFTransform, unsafe.Pointer(&t)); hr.Failed() || t == nil {
			reasons = append(reasons, fmt.Sprintf("%s: activation failed: %s", cand.name, hr))
			continue
		}
		e := newEncoder(cfg, info, t, cand.name)
		err := e.configure()
		if err == nil {
			return e, nil
		}
		e.Close()
		cand.activate.ShutdownObject()
		if len(cands) == 1 {
			return nil, err
		}
		reasons = append(reasons, cand.name+": "+err.Error())
	}
	return nil, unsupportedEnc(cfg.Codec, "no usable encoder MFT: "+strings.Join(reasons, "; "))
}
