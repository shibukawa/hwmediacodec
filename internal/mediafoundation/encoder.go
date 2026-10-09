//go:build windows && (amd64 || arm64)

package mediafoundation

import (
	"context"
	"io"
	"math"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/bitstream/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediafoundation/sys"
)

// maxPendingPackets bounds the packets held for Receive; Send reports
// ErrAgain beyond it.
const maxPendingPackets = 64

// encoder drives an encoder MFT. Hardware encoder MFTs (Intel, AMD, NVIDIA)
// are asynchronous: they ask for input and announce output through
// IMFMediaEventGenerator events. The Microsoft software encoder is
// synchronous. Both models are handled; events is nil for the latter.
type encoder struct {
	mu   sync.Mutex
	cfg  codec.EncoderConfig
	info codecInfo
	name string

	transform *sys.IMFTransform
	events    *sys.IMFMediaEventGenerator
	codecAPI  *sys.ICodecAPI
	inputID   uint32
	outputID  uint32

	providesSamples bool
	outputSize      uint32
	outputAlign     uint32
	inputAlign      uint32

	rateNum, rateDen uint32
	frameDuration    int64 // 100 ns

	// Asynchronous MFT bookkeeping.
	needInput  int // outstanding METransformNeedInput events
	haveOutput int // outstanding METransformHaveOutput events
	draining   bool
	drained    bool

	streaming     bool
	needStart     bool
	discontinuity bool
	sent          bool // input was delivered since the last START_OF_STREAM
	forceNext     bool
	frameIndex    int
	flushed       bool
	closed        bool

	params   *paramSets
	inflight map[int64]int64
	pending  []codec.Packet
}

func newEncoder(cfg codec.EncoderConfig, info codecInfo, t *sys.IMFTransform, name string) *encoder {
	num, den := frameRateRatio(cfg.FrameRate)
	return &encoder{
		cfg:           cfg,
		info:          info,
		name:          name,
		transform:     t,
		rateNum:       num,
		rateDen:       den,
		frameDuration: frameDurationHNS(num, den),
		forceNext:     true,
		params:        newParamSets(cfg.Codec),
		inflight:      map[int64]int64{},
	}
}

func (e *encoder) unsupported(reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: e.cfg.Codec, Direction: codec.Encode, Reason: reason}
}

// notify sends a notification message, tolerating E_NOTIMPL.
func (e *encoder) notify(message uint32, name string) error {
	if hr := e.transform.ProcessMessage(message, 0); hr.Failed() && hr != sys.E_NOTIMPL {
		return backendErr("IMFTransform::ProcessMessage("+name+")", hr)
	}
	return nil
}

// configure unlocks asynchronous MFTs, applies the Codec API settings, sets
// the output type (which encoders require first) and the input type, and
// starts streaming.
func (e *encoder) configure() error {
	var in, out uint32
	switch hr := e.transform.GetStreamIDs(1, &in, 1, &out); {
	case hr == sys.E_NOTIMPL:
	case hr.Failed():
		return backendErr("IMFTransform::GetStreamIDs", hr)
	default:
		e.inputID, e.outputID = in, out
	}
	if err := e.unlockAsync(); err != nil {
		return err
	}
	var api *sys.ICodecAPI
	if e.transform.Unknown().QueryInterface(&sys.IID_ICodecAPI, unsafe.Pointer(&api)) == sys.S_OK && api != nil {
		e.codecAPI = api
	}
	if err := e.applyCodecProperties(); err != nil {
		return err
	}
	if err := e.setOutputType(); err != nil {
		return err
	}
	if err := e.setInputType(); err != nil {
		return err
	}
	if err := e.readStreamInfo(); err != nil {
		return err
	}
	e.readSequenceHeader()
	if err := e.notify(sys.MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, "BEGIN_STREAMING"); err != nil {
		return err
	}
	e.streaming = true
	e.needStart = true
	return nil
}

// unlockAsync detects an asynchronous MFT, unlocks it and fetches its event
// generator. Synchronous MFTs are left alone.
func (e *encoder) unlockAsync() error {
	var attrs *sys.IMFAttributes
	if hr := e.transform.GetAttributes(&attrs); hr.Failed() || attrs == nil {
		return nil
	}
	defer attrs.Release()
	if async, _ := attrs.UINT32(&sys.MF_TRANSFORM_ASYNC); async == 0 {
		return nil
	}
	if hr := attrs.SetUINT32(&sys.MF_TRANSFORM_ASYNC_UNLOCK, 1); hr.Failed() {
		return backendErr("IMFAttributes::SetUINT32(MF_TRANSFORM_ASYNC_UNLOCK)", hr)
	}
	var gen *sys.IMFMediaEventGenerator
	if hr := e.transform.Unknown().QueryInterface(&sys.IID_IMFMediaEventGenerator, unsafe.Pointer(&gen)); hr.Failed() || gen == nil {
		return backendErr("IMFTransform::QueryInterface(IMFMediaEventGenerator)", hr)
	}
	e.events = gen
	return nil
}

// applyCodecProperties maps the encoder configuration onto ICodecAPI. The
// properties that select a mode (rate control, quality, B-frames) must be
// set before the output type. Controls the encoder rejects are reported as
// ErrUnsupported; hints it ignores are not errors.
func (e *encoder) applyCodecProperties() error {
	cfg := &e.cfg
	if cfg.Bitrate > math.MaxUint32 {
		return e.unsupported("bitrate exceeds the Media Foundation maximum")
	}
	if e.codecAPI == nil {
		switch {
		case cfg.Quality > 0:
			return e.unsupported(e.name + " exposes no Codec API, so constant quality cannot be requested")
		case cfg.Bitrate > 0 && cfg.RateControl == codec.CBR:
			return e.unsupported(e.name + " exposes no Codec API, so constant bitrate cannot be requested")
		case cfg.BFrames && !cfg.LowLatency:
			return e.unsupported(e.name + " exposes no Codec API, so B-frames cannot be requested")
		case cfg.LowLatency:
			return e.unsupported(e.name + " exposes no Codec API, so low-latency mode cannot be requested")
		}
		return nil
	}
	set := func(control string, api *sys.GUID, v sys.VARIANT, required bool) error {
		if hr := e.codecAPI.SetValue(api, &v); hr.Failed() && required {
			return e.unsupported(control + " is not supported by " + e.name + " (" + hr.String() + ")")
		}
		return nil
	}
	switch {
	case cfg.Quality > 0:
		if err := set("constant quality", &sys.CODECAPI_AVEncCommonRateControlMode, sys.VariantUI4(sys.EAVEncCommonRateControlMode_Quality), true); err != nil {
			return err
		}
		q := uint32(math.Round(cfg.Quality * 100))
		q = min(max(q, 1), 100)
		if err := set("constant quality", &sys.CODECAPI_AVEncCommonQuality, sys.VariantUI4(q), true); err != nil {
			return err
		}
	case cfg.Bitrate > 0 && cfg.RateControl == codec.CBR:
		if err := set("constant bitrate", &sys.CODECAPI_AVEncCommonRateControlMode, sys.VariantUI4(sys.EAVEncCommonRateControlMode_CBR), true); err != nil {
			return err
		}
		if err := set("bitrate", &sys.CODECAPI_AVEncCommonMeanBitRate, sys.VariantUI4(uint32(cfg.Bitrate)), true); err != nil {
			return err
		}
	case cfg.Bitrate > 0:
		// The output type carries MF_MT_AVG_BITRATE as well; these are hints.
		set("", &sys.CODECAPI_AVEncCommonRateControlMode, sys.VariantUI4(sys.EAVEncCommonRateControlMode_UnconstrainedVBR), false)
		set("", &sys.CODECAPI_AVEncCommonMeanBitRate, sys.VariantUI4(uint32(cfg.Bitrate)), false)
	}
	if cfg.KeyframeInterval > 0 && cfg.KeyframeInterval <= math.MaxUint32 {
		// Keyframes are also forced from Go at the interval, so this is a hint.
		set("", &sys.CODECAPI_AVEncMPVGOPSize, sys.VariantUI4(uint32(cfg.KeyframeInterval)), false)
	}
	if cfg.BFrames && !cfg.LowLatency {
		if err := set("B-frames", &sys.CODECAPI_AVEncMPVDefaultBPictureCount, sys.VariantUI4(2), true); err != nil {
			return err
		}
	} else {
		set("", &sys.CODECAPI_AVEncMPVDefaultBPictureCount, sys.VariantUI4(0), false)
	}
	if cfg.LowLatency {
		if err := set("low-latency mode", &sys.CODECAPI_AVLowLatencyMode, sys.VariantUI4(1), true); err != nil {
			return err
		}
		set("", &sys.CODECAPI_AVEncCommonRealTime, sys.VariantBool(true), false)
	}
	return nil
}

// pickType returns the first type the enumerator offers with the wanted
// subtype, or a fresh empty type when it offers none (hardware encoders
// often enumerate nothing before a type is set).
func pickType(enum func(id, index uint32, out **sys.IMFMediaType) sys.HRESULT, id uint32, subtype *sys.GUID) (*sys.IMFMediaType, error) {
	for i := uint32(0); ; i++ {
		var mt *sys.IMFMediaType
		if hr := enum(id, i, &mt); hr.Failed() || mt == nil {
			break
		}
		if sub, ok := mt.Attributes().GUID(&sys.MF_MT_SUBTYPE); ok && sub == *subtype {
			return mt, nil
		}
		mt.Release()
	}
	var mt *sys.IMFMediaType
	if hr := sys.MFCreateMediaType(&mt); hr.Failed() || mt == nil {
		return nil, backendErr("MFCreateMediaType", hr)
	}
	return mt, nil
}

func packRatio(num, den uint32) uint64 { return uint64(num)<<32 | uint64(den) }

func (e *encoder) setOutputType() error {
	mt, err := pickType(e.transform.GetOutputAvailableType, e.outputID, e.info.subtype)
	if err != nil {
		return err
	}
	defer mt.Release()
	a := mt.Attributes()
	a.SetGUID(&sys.MF_MT_MAJOR_TYPE, &sys.MFMediaType_Video)
	a.SetGUID(&sys.MF_MT_SUBTYPE, e.info.subtype)
	a.SetUINT64(&sys.MF_MT_FRAME_SIZE, packRatio(uint32(e.cfg.Width), uint32(e.cfg.Height)))
	a.SetUINT64(&sys.MF_MT_FRAME_RATE, packRatio(e.rateNum, e.rateDen))
	a.SetUINT64(&sys.MF_MT_PIXEL_ASPECT_RATIO, packRatio(1, 1))
	a.SetUINT32(&sys.MF_MT_INTERLACE_MODE, sys.MFVideoInterlace_Progressive)
	bitrate := e.cfg.Bitrate
	if bitrate <= 0 {
		bitrate = defaultBitrate(e.cfg.Width, e.cfg.Height, e.cfg.FrameRate)
	}
	a.SetUINT32(&sys.MF_MT_AVG_BITRATE, uint32(bitrate))
	if profile, ok, err := e.mfProfile(); err != nil {
		return err
	} else if ok {
		a.SetUINT32(&sys.MF_MT_MPEG2_PROFILE, profile)
	}
	hr := e.transform.SetOutputType(e.outputID, mt, 0)
	switch {
	case hr == sys.MF_E_INVALIDMEDIATYPE, hr == sys.MF_E_INVALIDTYPE, hr == sys.MF_E_UNSUPPORTED_D3D_TYPE:
		return e.unsupported(e.name + " rejected the output format (" + hr.String() + ")")
	case hr.Failed():
		return backendErr("IMFTransform::SetOutputType", hr)
	}
	return nil
}

// mfProfile maps the requested profile to MF_MT_MPEG2_PROFILE.
func (e *encoder) mfProfile() (uint32, bool, error) {
	switch e.cfg.Codec {
	case codec.H264:
		switch e.cfg.Profile {
		case codec.ProfileDefault:
			return 0, false, nil
		case codec.ProfileBaseline:
			return sys.EAVEncH264VProfile_Base, true, nil
		case codec.ProfileMain:
			return sys.EAVEncH264VProfile_Main, true, nil
		case codec.ProfileHigh:
			return sys.EAVEncH264VProfile_High, true, nil
		}
	case codec.HEVC:
		switch e.cfg.Profile {
		case codec.ProfileDefault:
			return 0, false, nil
		case codec.ProfileMain:
			return sys.EAVEncH265VProfile_Main_420_8, true, nil
		}
	}
	return 0, false, e.unsupported("profile " + e.cfg.Profile.String() + " is not defined for " + e.cfg.Codec.String())
}

func (e *encoder) setInputType() error {
	mt, err := pickType(e.transform.GetInputAvailableType, e.inputID, &sys.MFVideoFormat_NV12)
	if err != nil {
		return err
	}
	defer mt.Release()
	a := mt.Attributes()
	a.SetGUID(&sys.MF_MT_MAJOR_TYPE, &sys.MFMediaType_Video)
	a.SetGUID(&sys.MF_MT_SUBTYPE, &sys.MFVideoFormat_NV12)
	a.SetUINT64(&sys.MF_MT_FRAME_SIZE, packRatio(uint32(e.cfg.Width), uint32(e.cfg.Height)))
	a.SetUINT64(&sys.MF_MT_FRAME_RATE, packRatio(e.rateNum, e.rateDen))
	a.SetUINT64(&sys.MF_MT_PIXEL_ASPECT_RATIO, packRatio(1, 1))
	a.SetUINT32(&sys.MF_MT_INTERLACE_MODE, sys.MFVideoInterlace_Progressive)
	a.SetUINT32(&sys.MF_MT_DEFAULT_STRIDE, uint32(e.cfg.Width))
	a.SetUINT32(&sys.MF_MT_ALL_SAMPLES_INDEPENDENT, 1)
	hr := e.transform.SetInputType(e.inputID, mt, 0)
	switch {
	case hr == sys.MF_E_INVALIDMEDIATYPE, hr == sys.MF_E_INVALIDTYPE:
		return e.unsupported(e.name + " does not accept NV12 input of this size or rate (" + hr.String() + ")")
	case hr.Failed():
		return backendErr("IMFTransform::SetInputType", hr)
	}
	return nil
}

func (e *encoder) readStreamInfo() error {
	var oi sys.MFT_OUTPUT_STREAM_INFO
	if hr := e.transform.GetOutputStreamInfo(e.outputID, &oi); hr.Failed() {
		return backendErr("IMFTransform::GetOutputStreamInfo", hr)
	}
	e.providesSamples = oi.Flags&(sys.MFT_OUTPUT_STREAM_PROVIDES_SAMPLES|sys.MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES) != 0
	e.outputSize = oi.Size
	e.outputAlign = sys.MF_16_BYTE_ALIGNMENT
	if oi.Alignment > 1 {
		e.outputAlign = oi.Alignment - 1
	}
	var ii sys.MFT_INPUT_STREAM_INFO
	e.inputAlign = sys.MF_16_BYTE_ALIGNMENT
	if hr := e.transform.GetInputStreamInfo(e.inputID, &ii); hr == sys.S_OK && ii.Alignment > 1 {
		e.inputAlign = ii.Alignment - 1
	}
	return nil
}

// readSequenceHeader stores the parameter sets the encoder publishes in
// MF_MT_MPEG_SEQUENCE_HEADER (Annex-B VPS/SPS/PPS). Hardware encoders fill
// it in late, so this is called again when a keyframe arrives without them.
func (e *encoder) readSequenceHeader() {
	var mt *sys.IMFMediaType
	if hr := e.transform.GetOutputCurrentType(e.outputID, &mt); hr.Failed() || mt == nil {
		return
	}
	defer mt.Release()
	hdr, ok := mt.Attributes().Blob(&sys.MF_MT_MPEG_SEQUENCE_HEADER)
	if !ok {
		return
	}
	for _, nal := range annexb.Split(hdr) {
		if t := annexb.NALUnitType(e.cfg.Codec, nal); t >= 0 && annexb.IsParameterSet(e.cfg.Codec, t) {
			e.params.add(t, nal)
		}
	}
}

// pump handles queued events. With block set it waits for one event and
// returns after handling it; otherwise it drains the queue without waiting.
func (e *encoder) pump(block bool) error {
	flags := sys.MF_EVENT_FLAG_NO_WAIT
	if block {
		flags = 0
	}
	for {
		var ev *sys.IMFMediaEvent
		hr := e.events.GetEvent(flags, &ev)
		if hr == sys.MF_E_NO_EVENTS_AVAILABLE {
			return nil
		}
		if hr.Failed() || ev == nil {
			return backendErr("IMFMediaEventGenerator::GetEvent", hr)
		}
		var typ uint32
		var status sys.HRESULT
		ev.GetType(&typ)
		ev.GetStatus(&status)
		ev.Release()
		if status.Failed() {
			return backendErr("encoder event", status)
		}
		switch typ {
		case sys.METransformNeedInput:
			if !e.draining {
				e.needInput++
			}
		case sys.METransformHaveOutput:
			e.haveOutput++
		case sys.METransformDrainComplete:
			e.drained = true
		}
		if block {
			return nil
		}
	}
}

func (e *encoder) Send(ctx context.Context, f *codec.Frame) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkFrame(&e.cfg, f); err != nil {
		return err
	}
	if len(e.pending) >= maxPendingPackets {
		return codec.ErrAgain
	}
	if e.needStart {
		if err := e.notify(sys.MFT_MESSAGE_NOTIFY_START_OF_STREAM, "START_OF_STREAM"); err != nil {
			return err
		}
		e.needStart = false
		e.discontinuity = true
		e.needInput, e.haveOutput = 0, 0
		e.draining, e.drained = false, false
	}
	if e.events != nil {
		// Wait for an input request, serving output announced meanwhile.
		for e.needInput == 0 {
			if e.haveOutput > 0 {
				if err := e.pullOutput(); err != nil {
					return err
				}
				continue
			}
			if len(e.pending) >= maxPendingPackets {
				return codec.ErrAgain
			}
			if err := e.pump(true); err != nil {
				return err
			}
		}
	}

	force := f.ForceKeyframe || e.forceNext
	if e.cfg.KeyframeInterval > 0 && e.frameIndex%e.cfg.KeyframeInterval == 0 {
		force = true
	}
	if force && e.codecAPI != nil {
		v := sys.VariantUI4(1)
		e.codecAPI.SetValue(&sys.CODECAPI_AVEncVideoForceKeyFrame, &v)
	}
	hns := toHNS(f.PTS, e.cfg.TimeScale)
	sample, err := e.newInputSample(f, hns)
	if err != nil {
		return err
	}
	defer sample.Release()
	if e.discontinuity {
		sample.Attributes().SetUINT32(&sys.MFSampleExtension_Discontinuity, 1)
	}
	hr := e.transform.ProcessInput(e.inputID, sample, 0)
	if hr == sys.MF_E_NOTACCEPTING && e.events == nil {
		// Synchronous MFT holding output: collect it and retry once.
		if err := e.collectSync(); err != nil {
			return err
		}
		hr = e.transform.ProcessInput(e.inputID, sample, 0)
	}
	if hr == sys.MF_E_NOTACCEPTING {
		return codec.ErrAgain
	}
	if hr.Failed() {
		return backendErr("IMFTransform::ProcessInput", hr)
	}
	if e.events != nil {
		e.needInput--
	}
	e.inflight[hns] = f.PTS
	e.sent = true
	e.discontinuity = false
	e.forceNext = false
	e.frameIndex++
	e.flushed = false
	return nil
}

// newInputSample packs the frame into a system-memory sample.
func (e *encoder) newInputSample(f *codec.Frame, hns int64) (*sys.IMFSample, error) {
	size := nv12Size(e.cfg.Width, e.cfg.Height)
	var buf *sys.IMFMediaBuffer
	if hr := sys.MFCreateAlignedMemoryBuffer(uint32(size), e.inputAlign, &buf); hr.Failed() || buf == nil {
		return nil, backendErr("MFCreateAlignedMemoryBuffer", hr)
	}
	defer buf.Release()
	var p *byte
	var maxLen, curLen uint32
	if hr := buf.Lock(&p, &maxLen, &curLen); hr.Failed() || p == nil {
		return nil, backendErr("IMFMediaBuffer::Lock", hr)
	}
	packNV12(unsafe.Slice(p, size), f)
	buf.Unlock()
	buf.SetCurrentLength(uint32(size))

	var sample *sys.IMFSample
	if hr := sys.MFCreateSample(&sample); hr.Failed() || sample == nil {
		return nil, backendErr("MFCreateSample", hr)
	}
	if hr := sample.AddBuffer(buf); hr.Failed() {
		sample.Release()
		return nil, backendErr("IMFSample::AddBuffer", hr)
	}
	sample.SetSampleTime(hns)
	if e.frameDuration > 0 {
		sample.SetSampleDuration(e.frameDuration)
	}
	return sample, nil
}

func (e *encoder) Receive(ctx context.Context) (codec.Packet, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.Packet{}, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return codec.Packet{}, err
	}
	if pkt, ok := e.popPending(); ok {
		return pkt, nil
	}
	if e.events != nil {
		if err := e.pump(false); err != nil {
			return codec.Packet{}, err
		}
		for e.haveOutput > 0 && len(e.pending) == 0 {
			if err := e.pullOutput(); err != nil {
				return codec.Packet{}, err
			}
		}
		if pkt, ok := e.popPending(); ok {
			return pkt, nil
		}
		if e.flushed {
			return codec.Packet{}, io.EOF
		}
		return codec.Packet{}, codec.ErrAgain
	}
	if e.flushed {
		return codec.Packet{}, io.EOF
	}
	if !e.streaming {
		return codec.Packet{}, codec.ErrAgain
	}
	pkt, ok, err := e.processOutput()
	if err != nil {
		return codec.Packet{}, err
	}
	if !ok {
		return codec.Packet{}, codec.ErrAgain
	}
	return pkt, nil
}

func (e *encoder) popPending() (codec.Packet, bool) {
	n := len(e.pending)
	if n == 0 {
		return codec.Packet{}, false
	}
	pkt := e.pending[0]
	copy(e.pending, e.pending[1:])
	e.pending[n-1] = codec.Packet{}
	e.pending = e.pending[:n-1]
	return pkt, true
}

// pullOutput serves one METransformHaveOutput event.
func (e *encoder) pullOutput() error {
	e.haveOutput--
	pkt, ok, err := e.processOutput()
	if err != nil {
		return err
	}
	if ok {
		e.pending = append(e.pending, pkt)
	}
	return nil
}

// collectSync drains everything a synchronous MFT can produce right now.
func (e *encoder) collectSync() error {
	for {
		pkt, ok, err := e.processOutput()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		e.pending = append(e.pending, pkt)
	}
}

// processOutput asks the transform for one packet. ok is false when the
// transform has nothing to give.
func (e *encoder) processOutput() (codec.Packet, bool, error) {
	for changes := 0; changes < 4; {
		out := sys.MFT_OUTPUT_DATA_BUFFER{StreamID: e.outputID}
		var own *sys.IMFSample
		if !e.providesSamples {
			s, err := e.newOutputSample()
			if err != nil {
				return codec.Packet{}, false, err
			}
			own = s
			out.Sample = s
		}
		var status uint32
		hr := e.transform.ProcessOutput(0, 1, &out, &status)
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
			if err := e.renegotiateOutput(); err != nil {
				return codec.Packet{}, false, err
			}
			continue
		case hr == sys.MF_E_TRANSFORM_NEED_MORE_INPUT,
			hr == sys.E_UNEXPECTED && e.events != nil:
			release()
			return codec.Packet{}, false, nil
		case hr.Failed():
			release()
			return codec.Packet{}, false, backendErr("IMFTransform::ProcessOutput", hr)
		case sample == nil:
			release()
			return codec.Packet{}, false, &codec.BackendError{Backend: Name, Op: "IMFTransform::ProcessOutput", Message: "no sample returned"}
		}
		pkt, err := e.packetFromSample(sample)
		release()
		if err != nil {
			return codec.Packet{}, false, err
		}
		return pkt, true, nil
	}
	return codec.Packet{}, false, &codec.BackendError{Backend: Name, Op: "IMFTransform::ProcessOutput", Message: "repeated output format changes"}
}

// renegotiateOutput accepts the output type the encoder now proposes (it
// usually only adds the sequence header).
func (e *encoder) renegotiateOutput() error {
	mt, err := pickType(e.transform.GetOutputAvailableType, e.outputID, e.info.subtype)
	if err != nil {
		return err
	}
	defer mt.Release()
	if hr := e.transform.SetOutputType(e.outputID, mt, 0); hr.Failed() {
		return backendErr("IMFTransform::SetOutputType", hr)
	}
	if err := e.readStreamInfo(); err != nil {
		return err
	}
	e.readSequenceHeader()
	return nil
}

func (e *encoder) newOutputSample() (*sys.IMFSample, error) {
	size := e.outputSize
	if size == 0 {
		size = uint32(nv12Size(e.cfg.Width, e.cfg.Height))
	}
	var buf *sys.IMFMediaBuffer
	if hr := sys.MFCreateAlignedMemoryBuffer(size, e.outputAlign, &buf); hr.Failed() || buf == nil {
		return nil, backendErr("MFCreateAlignedMemoryBuffer", hr)
	}
	defer buf.Release()
	var sample *sys.IMFSample
	if hr := sys.MFCreateSample(&sample); hr.Failed() || sample == nil {
		return nil, backendErr("MFCreateSample", hr)
	}
	if hr := sample.AddBuffer(buf); hr.Failed() {
		sample.Release()
		return nil, backendErr("IMFSample::AddBuffer", hr)
	}
	return sample, nil
}

// packetFromSample copies the encoded access unit out of the sample and
// resolves its timestamps.
func (e *encoder) packetFromSample(sample *sys.IMFSample) (codec.Packet, error) {
	pts := int64(0)
	var hns int64
	if sample.GetSampleTime(&hns) == sys.S_OK {
		if p, ok := e.inflight[hns]; ok {
			delete(e.inflight, hns)
			pts = p
		} else {
			pts = fromHNS(hns, e.cfg.TimeScale)
		}
	}
	dts := pts
	if v, ok := sample.Attributes().UINT64(&sys.MFSampleExtension_DecodeTimestamp); ok {
		dts = fromHNS(int64(v), e.cfg.TimeScale)
	}
	var buf *sys.IMFMediaBuffer
	if hr := sample.ConvertToContiguousBuffer(&buf); hr.Failed() || buf == nil {
		return codec.Packet{}, backendErr("IMFSample::ConvertToContiguousBuffer", hr)
	}
	defer buf.Release()
	var p *byte
	var maxLen, curLen uint32
	if hr := buf.Lock(&p, &maxLen, &curLen); hr.Failed() || p == nil {
		return codec.Packet{}, backendErr("IMFMediaBuffer::Lock", hr)
	}
	data := append([]byte(nil), unsafe.Slice(p, curLen)...)
	buf.Unlock()
	if len(data) == 0 {
		return codec.Packet{}, &codec.BackendError{Backend: Name, Op: "read packet", Message: "encoder returned an empty sample"}
	}
	pkt := finishPacket(e.cfg.Codec, e.params, data, pts, dts)
	if pkt.Keyframe && e.params.empty() {
		// The encoder did not put parameter sets in the stream; fetch them
		// from the output type now that it has produced a frame.
		e.readSequenceHeader()
		pkt = finishPacket(e.cfg.Codec, e.params, data, pts, dts)
	}
	return pkt, nil
}

func (e *encoder) Flush(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.streaming && e.sent && !e.flushed {
		if hr := e.transform.ProcessMessage(sys.MFT_MESSAGE_COMMAND_DRAIN, 0); hr.Failed() {
			return backendErr("IMFTransform::ProcessMessage(DRAIN)", hr)
		}
		if e.events != nil {
			e.draining = true
			for !e.drained {
				if e.haveOutput > 0 {
					if err := e.pullOutput(); err != nil {
						return err
					}
					continue
				}
				if err := e.pump(true); err != nil {
					return err
				}
			}
			for e.haveOutput > 0 {
				if err := e.pullOutput(); err != nil {
					return err
				}
			}
			e.draining, e.drained = false, false
			e.needInput = 0
		} else if err := e.collectSync(); err != nil {
			return err
		}
		e.needStart = true
	}
	e.flushed = true
	e.forceNext = true
	e.frameIndex = 0
	e.sent = false
	clear(e.inflight)
	return nil
}

func (e *encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.transform != nil {
		if e.streaming {
			e.notify(sys.MFT_MESSAGE_NOTIFY_END_STREAMING, "END_STREAMING")
		}
		e.codecAPI.Release()
		e.events.Release()
		e.transform.Release()
		e.codecAPI, e.events, e.transform = nil, nil, nil
	}
	e.pending = nil
	return nil
}
