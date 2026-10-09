//go:build windows && (amd64 || arm64)

package mediafoundation

import (
	"context"
	"errors"
	"io"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediafoundation/sys"
)

// maxPrefix bounds the non-picture data held back for the next picture.
const maxPrefix = 1 << 20

type decoder struct {
	mu   sync.Mutex
	cfg  codec.DecoderConfig
	info codecInfo
	name string // friendly name of the MFT, for diagnostics

	dev       *d3dDevice
	transform *sys.IMFTransform
	inputID   uint32
	outputID  uint32

	// Output allocation as reported by the transform for the current type.
	providesSamples bool
	outputSize      uint32
	outputAlign     uint32

	// Geometry of the current output type: the coded frame size and the
	// visible picture inside it.
	codedW, codedH int
	crop           rect

	// CPU-readable copy target for decoder textures, recreated on size change.
	staging     *sys.ID3D11Texture2D
	stagingDesc sys.D3D11_TEXTURE2D_DESC

	params   *paramSets
	prefix   []byte          // non-picture NAL units waiting for the next picture
	inflight map[int64]int64 // sample time (100 ns) -> caller PTS
	pending  []*codec.Frame  // frames drained by Flush

	streaming     bool // MFT_MESSAGE_NOTIFY_BEGIN_STREAMING was sent
	needStart     bool // MFT_MESSAGE_NOTIFY_START_OF_STREAM precedes the next input
	needParams    bool // prefix stored parameter sets to the next keyframe
	discontinuity bool // flag the next input sample as a discontinuity
	waitKeyframe  bool
	flushed       bool
	closed        bool

	pool sync.Pool
}

func newDecoder(cfg codec.DecoderConfig, info codecInfo, dev *d3dDevice, t *sys.IMFTransform, name string) *decoder {
	return &decoder{
		cfg:          cfg,
		info:         info,
		name:         name,
		dev:          dev,
		transform:    t,
		params:       newParamSets(cfg.Codec),
		inflight:     map[int64]int64{},
		waitKeyframe: true,
	}
}

// configure sets the input type (Annex-B elementary stream), negotiates an
// NV12 output type and starts streaming.
func (d *decoder) configure() error {
	var in, out uint32
	switch hr := d.transform.GetStreamIDs(1, &in, 1, &out); {
	case hr == sys.E_NOTIMPL:
		// Stream identifiers are 0..n-1.
	case hr.Failed():
		return backendErr("IMFTransform::GetStreamIDs", hr)
	default:
		d.inputID, d.outputID = in, out
	}

	var mt *sys.IMFMediaType
	if hr := sys.MFCreateMediaType(&mt); hr.Failed() {
		return backendErr("MFCreateMediaType", hr)
	}
	a := mt.Attributes()
	a.SetGUID(&sys.MF_MT_MAJOR_TYPE, &sys.MFMediaType_Video)
	a.SetGUID(&sys.MF_MT_SUBTYPE, d.info.subtype)
	if d.cfg.Codec == codec.H264 {
		// Recommended by the H.264 decoder documentation: interlacing can
		// change per picture and the stream takes precedence anyway.
		a.SetUINT32(&sys.MF_MT_INTERLACE_MODE, sys.MFVideoInterlace_MixedInterlaceOrProgressive)
	}
	hr := d.transform.SetInputType(d.inputID, mt, 0)
	mt.Release()
	switch {
	case hr == sys.MF_E_UNSUPPORTED_D3D_TYPE:
		return unsupported(d.cfg.Codec, d.name+" cannot decode "+d.cfg.Codec.String()+" on this Direct3D 11 device")
	case hr.Failed():
		return backendErr("IMFTransform::SetInputType", hr)
	}
	if err := d.negotiateOutputType(); err != nil {
		return err
	}
	if err := d.notify(sys.MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, "BEGIN_STREAMING"); err != nil {
		return err
	}
	d.streaming = true
	d.needStart = true
	return nil
}

// notify sends a notification message. Transforms may answer E_NOTIMPL to
// notifications they do not care about; that is not a failure.
func (d *decoder) notify(message uint32, name string) error {
	if hr := d.transform.ProcessMessage(message, 0); hr.Failed() && hr != sys.E_NOTIMPL {
		return backendErr("IMFTransform::ProcessMessage("+name+")", hr)
	}
	return nil
}

// negotiateOutputType selects the NV12 output type the transform offers and
// records the picture geometry. It runs at start-up and on every
// MF_E_TRANSFORM_STREAM_CHANGE.
func (d *decoder) negotiateOutputType() error {
	for i := uint32(0); ; i++ {
		var mt *sys.IMFMediaType
		hr := d.transform.GetOutputAvailableType(d.outputID, i, &mt)
		if hr == sys.MF_E_NO_MORE_TYPES {
			return unsupported(d.cfg.Codec, d.name+" offers no NV12 output for this stream (10-bit content is not supported yet)")
		}
		if hr.Failed() {
			return backendErr("IMFTransform::GetOutputAvailableType", hr)
		}
		sub, ok := mt.Attributes().GUID(&sys.MF_MT_SUBTYPE)
		if !ok || sub != sys.MFVideoFormat_NV12 {
			mt.Release()
			continue
		}
		hr = d.transform.SetOutputType(d.outputID, mt, 0)
		if hr.Failed() {
			mt.Release()
			return backendErr("IMFTransform::SetOutputType", hr)
		}
		d.readGeometry(mt.Attributes())
		mt.Release()
		break
	}
	var info sys.MFT_OUTPUT_STREAM_INFO
	if hr := d.transform.GetOutputStreamInfo(d.outputID, &info); hr.Failed() {
		return backendErr("IMFTransform::GetOutputStreamInfo", hr)
	}
	d.providesSamples = info.Flags&(sys.MFT_OUTPUT_STREAM_PROVIDES_SAMPLES|sys.MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES) != 0
	d.outputSize = info.Size
	d.outputAlign = sys.MF_16_BYTE_ALIGNMENT
	if info.Alignment > 1 {
		d.outputAlign = info.Alignment - 1
	}
	return nil
}

// readGeometry records the coded size and the visible picture area of an
// output type. Decoders pad the coded height (1088 rows for 1080p) and
// describe the visible part in MF_MT_MINIMUM_DISPLAY_APERTURE.
func (d *decoder) readGeometry(a *sys.IMFAttributes) {
	if v, ok := a.UINT64(&sys.MF_MT_FRAME_SIZE); ok {
		d.codedW, d.codedH = int(v>>32), int(uint32(v))
	}
	d.crop = rect{0, 0, d.codedW, d.codedH}
	for _, key := range []*sys.GUID{&sys.MF_MT_MINIMUM_DISPLAY_APERTURE, &sys.MF_MT_GEOMETRIC_APERTURE} {
		if b, ok := a.Blob(key); ok {
			if area, ok := decodeVideoArea(b); ok && !area.empty() {
				d.crop = area
				break
			}
		}
	}
	if d.staging != nil {
		d.staging.Release()
		d.staging = nil
	}
}

func (d *decoder) Send(ctx context.Context, p codec.Packet) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	nals := annexb.Split(p.Data)
	if len(nals) == 0 {
		return codec.ErrInvalidData
	}
	d.flushed = false
	hasVCL, hasKeyframe := false, false
	var kinds [3]bool
	for _, nal := range nals {
		t := annexb.NALUnitType(d.cfg.Codec, nal)
		switch {
		case t < 0:
		case annexb.IsParameterSet(d.cfg.Codec, t):
			if kind, ok := d.params.add(t, nal); ok {
				kinds[kind] = true
			}
		case annexb.IsVCL(d.cfg.Codec, t):
			hasVCL = true
			if annexb.IsKeyframe(d.cfg.Codec, t) {
				hasKeyframe = true
			}
		}
	}
	if !hasVCL {
		// Parameter sets or SEI without a picture: deliver them with the
		// next picture so every sample holds one access unit.
		if len(d.prefix)+len(p.Data) <= maxPrefix {
			d.prefix = append(d.prefix, p.Data...)
		}
		return nil
	}
	if d.waitKeyframe {
		if !hasKeyframe {
			return nil
		}
		d.waitKeyframe = false
	}

	var head []byte
	if d.needParams && !d.params.covers(kinds) {
		head = d.params.annexB()
	}
	head = append(head, d.prefix...)
	data := p.Data
	if len(head) > 0 {
		data = append(head, p.Data...)
	}
	err := d.decode(data, p.PTS, hasKeyframe)
	if errors.Is(err, codec.ErrAgain) {
		// The caller resends the packet after draining output; keep what was
		// prepended for it.
		d.prefix = head
		d.needParams = false
		return err
	}
	d.prefix = nil
	d.needParams = false
	return err
}

func (d *decoder) decode(data []byte, pts int64, keyframe bool) error {
	if d.needStart {
		if err := d.notify(sys.MFT_MESSAGE_NOTIFY_START_OF_STREAM, "START_OF_STREAM"); err != nil {
			return err
		}
		d.needStart = false
		d.discontinuity = true
	}
	hns := toHNS(pts, d.cfg.TimeScale)
	sample, err := newInputSample(data, hns)
	if err != nil {
		return err
	}
	defer sample.Release()
	if d.discontinuity {
		sample.Attributes().SetUINT32(&sys.MFSampleExtension_Discontinuity, 1)
	}
	if keyframe {
		sample.Attributes().SetUINT32(&sys.MFSampleExtension_CleanPoint, 1)
	}
	hr := d.transform.ProcessInput(d.inputID, sample, 0)
	if hr == sys.MF_E_NOTACCEPTING {
		return codec.ErrAgain
	}
	if hr.Failed() {
		return backendErr("IMFTransform::ProcessInput", hr)
	}
	d.discontinuity = false
	d.inflight[hns] = pts
	return nil
}

// newInputSample wraps one access unit in a sample with the given time.
func newInputSample(data []byte, hns int64) (*sys.IMFSample, error) {
	var buf *sys.IMFMediaBuffer
	if hr := sys.MFCreateMemoryBuffer(uint32(len(data)), &buf); hr.Failed() {
		return nil, backendErr("MFCreateMemoryBuffer", hr)
	}
	defer buf.Release()
	var p *byte
	var maxLen, curLen uint32
	if hr := buf.Lock(&p, &maxLen, &curLen); hr.Failed() {
		return nil, backendErr("IMFMediaBuffer::Lock", hr)
	}
	copy(unsafe.Slice(p, len(data)), data)
	buf.Unlock()
	buf.SetCurrentLength(uint32(len(data)))

	var sample *sys.IMFSample
	if hr := sys.MFCreateSample(&sample); hr.Failed() {
		return nil, backendErr("MFCreateSample", hr)
	}
	if hr := sample.AddBuffer(buf); hr.Failed() {
		sample.Release()
		return nil, backendErr("IMFSample::AddBuffer", hr)
	}
	sample.SetSampleTime(hns)
	return sample, nil
}

func (d *decoder) Receive(ctx context.Context) (*codec.Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n := len(d.pending); n > 0 {
		f := d.pending[0]
		copy(d.pending, d.pending[1:])
		d.pending[n-1] = nil
		d.pending = d.pending[:n-1]
		return f, nil
	}
	if d.flushed {
		return nil, io.EOF
	}
	if !d.streaming {
		return nil, codec.ErrAgain
	}
	return d.processOutput()
}

// processOutput asks the transform for one frame. It returns ErrAgain when
// the transform needs more input and renegotiates the output type on a
// stream change.
func (d *decoder) processOutput() (*codec.Frame, error) {
	for changes := 0; changes < 4; {
		out := sys.MFT_OUTPUT_DATA_BUFFER{StreamID: d.outputID}
		var own *sys.IMFSample
		if !d.providesSamples {
			s, err := d.newOutputSample()
			if err != nil {
				return nil, err
			}
			own = s
			out.Sample = s
		}
		var status uint32
		hr := d.transform.ProcessOutput(0, 1, &out, &status)
		if out.Events != nil {
			out.Events.Release()
		}
		sample := out.Sample
		release := func() {
			if sample != nil {
				sample.Release()
			}
			if own != nil && own != sample {
				own.Release()
			}
		}
		switch {
		case hr == sys.MF_E_TRANSFORM_STREAM_CHANGE,
			hr == sys.S_OK && sample == nil && out.Status&sys.MFT_OUTPUT_DATA_BUFFER_FORMAT_CHANGE != 0:
			release()
			changes++
			if err := d.negotiateOutputType(); err != nil {
				return nil, err
			}
			continue
		case hr == sys.MF_E_TRANSFORM_NEED_MORE_INPUT:
			release()
			return nil, codec.ErrAgain
		case hr.Failed():
			release()
			return nil, backendErr("IMFTransform::ProcessOutput", hr)
		case sample == nil:
			release()
			return nil, &codec.BackendError{Backend: Name, Op: "IMFTransform::ProcessOutput", Message: "no sample returned"}
		}
		f, err := d.readSample(sample)
		release()
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	return nil, &codec.BackendError{Backend: Name, Op: "IMFTransform::ProcessOutput", Message: "repeated output format changes"}
}

// newOutputSample allocates a system-memory sample for transforms that do
// not provide their own output samples.
func (d *decoder) newOutputSample() (*sys.IMFSample, error) {
	size := d.outputSize
	if n := uint32(nv12Size(d.codedW, d.codedH)); n > size {
		size = n
	}
	if size == 0 {
		return nil, &codec.BackendError{Backend: Name, Op: "allocate output", Message: "output size unknown"}
	}
	var buf *sys.IMFMediaBuffer
	if hr := sys.MFCreateAlignedMemoryBuffer(size, d.outputAlign, &buf); hr.Failed() {
		return nil, backendErr("MFCreateAlignedMemoryBuffer", hr)
	}
	defer buf.Release()
	var sample *sys.IMFSample
	if hr := sys.MFCreateSample(&sample); hr.Failed() {
		return nil, backendErr("MFCreateSample", hr)
	}
	if hr := sample.AddBuffer(buf); hr.Failed() {
		sample.Release()
		return nil, backendErr("IMFSample::AddBuffer", hr)
	}
	return sample, nil
}

// readSample copies the picture in sample into a Go-owned NV12 frame.
func (d *decoder) readSample(sample *sys.IMFSample) (*codec.Frame, error) {
	pts := d.timestampOf(sample)
	var buf *sys.IMFMediaBuffer
	if hr := sample.GetBufferByIndex(0, &buf); hr.Failed() || buf == nil {
		return nil, backendErr("IMFSample::GetBufferByIndex", hr)
	}
	defer buf.Release()

	var dxgi *sys.IMFDXGIBuffer
	if buf.Unknown().QueryInterface(&sys.IID_IMFDXGIBuffer, unsafe.Pointer(&dxgi)) == sys.S_OK && dxgi != nil {
		defer dxgi.Release()
		return d.readTexture(dxgi, pts)
	}
	var b2 *sys.IMF2DBuffer2
	if buf.Unknown().QueryInterface(&sys.IID_IMF2DBuffer2, unsafe.Pointer(&b2)) == sys.S_OK && b2 != nil {
		defer b2.Release()
		return d.read2D(b2, pts)
	}
	return nil, &codec.BackendError{Backend: Name, Op: "read frame", Message: "output buffer is neither a DXGI surface nor a 2D buffer"}
}

// timestampOf maps the sample time back to the caller's PTS. Exact matches
// come from the inflight table; anything else is converted arithmetically.
func (d *decoder) timestampOf(sample *sys.IMFSample) int64 {
	var hns int64
	if sample.GetSampleTime(&hns).Failed() {
		return 0
	}
	if pts, ok := d.inflight[hns]; ok {
		delete(d.inflight, hns)
		return pts
	}
	return fromHNS(hns, d.cfg.TimeScale)
}

// readTexture copies a decoder texture through a staging texture, which is
// the only way to reach DXVA output from the CPU.
func (d *decoder) readTexture(dxgi *sys.IMFDXGIBuffer, pts int64) (*codec.Frame, error) {
	var tex *sys.ID3D11Texture2D
	if hr := dxgi.GetResource(&sys.IID_ID3D11Texture2D, unsafe.Pointer(&tex)); hr.Failed() || tex == nil {
		return nil, backendErr("IMFDXGIBuffer::GetResource", hr)
	}
	defer tex.Release()
	var sub uint32
	dxgi.GetSubresourceIndex(&sub)
	var desc sys.D3D11_TEXTURE2D_DESC
	tex.GetDesc(&desc)
	if desc.Format != sys.DXGI_FORMAT_NV12 {
		return nil, &codec.BackendError{Backend: Name, Op: "read frame", Status: int64(desc.Format), Message: "decoder produced a texture that is not NV12 (10-bit content is not supported yet)"}
	}
	if err := d.ensureStaging(desc); err != nil {
		return nil, err
	}
	dc := d.dev.context
	dc.CopySubresource(d.staging, 0, tex, sub)
	var mapped sys.D3D11_MAPPED_SUBRESOURCE
	if hr := dc.Map(d.staging, 0, sys.D3D11_MAP_READ, 0, &mapped); hr.Failed() || mapped.Data == nil {
		return nil, backendErr("ID3D11DeviceContext::Map", hr)
	}
	defer dc.Unmap(d.staging, 0)

	pitch := int(mapped.RowPitch)
	allocH := int(desc.Height)
	n := int(mapped.DepthPitch)
	if n == 0 {
		n = pitch * (allocH + (allocH+1)/2)
	}
	img := nv12Image{data: unsafe.Slice(mapped.Data, n), pitch: pitch, allocHeight: allocH}
	return d.copyFrame(img, int(desc.Width), allocH, pts)
}

// ensureStaging keeps a CPU-readable texture matching the decoder's output.
func (d *decoder) ensureStaging(desc sys.D3D11_TEXTURE2D_DESC) error {
	if d.staging != nil && d.stagingDesc.Width == desc.Width && d.stagingDesc.Height == desc.Height && d.stagingDesc.Format == desc.Format {
		return nil
	}
	if d.staging != nil {
		d.staging.Release()
		d.staging = nil
	}
	sd := sys.D3D11_TEXTURE2D_DESC{
		Width:          desc.Width,
		Height:         desc.Height,
		MipLevels:      1,
		ArraySize:      1,
		Format:         desc.Format,
		SampleCount:    1,
		Usage:          sys.D3D11_USAGE_STAGING,
		CPUAccessFlags: sys.D3D11_CPU_ACCESS_READ,
	}
	if hr := d.dev.device.CreateTexture2D(&sd, &d.staging); hr.Failed() || d.staging == nil {
		return backendErr("ID3D11Device::CreateTexture2D", hr)
	}
	d.stagingDesc = sd
	return nil
}

// read2D copies a system-memory 2D buffer (used when a transform does not
// hand out textures). The allocated height is derived from the buffer size.
func (d *decoder) read2D(b2 *sys.IMF2DBuffer2, pts int64) (*codec.Frame, error) {
	var scan0, start *byte
	var pitch int32
	var length uint32
	if hr := b2.Lock2DSize(sys.MF2DBuffer_LockFlags_Read, &scan0, &pitch, &start, &length); hr.Failed() || scan0 == nil {
		return nil, backendErr("IMF2DBuffer2::Lock2DSize", hr)
	}
	defer b2.Unlock2D()
	if pitch <= 0 {
		return nil, &codec.BackendError{Backend: Name, Op: "read frame", Status: int64(pitch), Message: "bottom-up 2D buffers are not supported"}
	}
	avail := int(length) - int(uintptr(unsafe.Pointer(scan0))-uintptr(unsafe.Pointer(start)))
	if avail <= 0 {
		return nil, &codec.BackendError{Backend: Name, Op: "read frame", Message: "empty 2D buffer"}
	}
	allocH := avail * 2 / (3 * int(pitch))
	width := d.codedW
	if width <= 0 || width > int(pitch) {
		width = int(pitch)
	}
	img := nv12Image{data: unsafe.Slice(scan0, avail), pitch: int(pitch), allocHeight: allocH}
	return d.copyFrame(img, width, allocH, pts)
}

// copyFrame crops the visible picture out of img into a pooled buffer.
func (d *decoder) copyFrame(img nv12Image, surfaceW, surfaceH int, pts int64) (*codec.Frame, error) {
	crop := d.crop
	if crop.empty() {
		crop = rect{0, 0, surfaceW, surfaceH}
	}
	crop = crop.clampTo(surfaceW, surfaceH)
	if crop.empty() {
		return nil, &codec.BackendError{Backend: Name, Op: "read frame", Message: "empty picture area"}
	}
	buf := d.getBuffer(nv12Size(crop.w, crop.h))
	planes, strides, err := copyNV12(buf, img, crop)
	if err != nil {
		d.putBuffer(buf)
		return nil, err
	}
	f := &codec.Frame{Width: crop.w, Height: crop.h, Format: codec.NV12, Planes: planes, Strides: strides, PTS: pts}
	codec.SetRelease(f, func() { d.putBuffer(buf) })
	return f, nil
}

func (d *decoder) getBuffer(n int) []byte {
	if b, ok := d.pool.Get().(*[]byte); ok && cap(*b) >= n {
		return (*b)[:n]
	}
	return make([]byte, n)
}

func (d *decoder) putBuffer(b []byte) {
	d.pool.Put(&b)
}

func (d *decoder) Flush(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.streaming && !d.flushed {
		if hr := d.transform.ProcessMessage(sys.MFT_MESSAGE_COMMAND_DRAIN, 0); hr.Failed() {
			return backendErr("IMFTransform::ProcessMessage(DRAIN)", hr)
		}
		for {
			f, err := d.processOutput()
			if err != nil {
				if errors.Is(err, codec.ErrAgain) {
					break
				}
				return err
			}
			d.pending = append(d.pending, f)
		}
		d.needStart = true
		d.needParams = true
	}
	d.flushed = true
	d.waitKeyframe = true
	d.prefix = nil
	clear(d.inflight)
	return nil
}

func (d *decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if d.transform != nil {
		if d.streaming {
			d.notify(sys.MFT_MESSAGE_NOTIFY_END_STREAMING, "END_STREAMING")
		}
		d.transform.Release()
		d.transform = nil
	}
	if d.staging != nil {
		d.staging.Release()
		d.staging = nil
	}
	d.dev.release()
	d.dev = nil
	// Frames already returned are Go memory and stay valid.
	d.pending = nil
	return nil
}
