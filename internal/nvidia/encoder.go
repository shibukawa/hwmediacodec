//go:build linux || (windows && amd64)

package nvidia

import (
	"context"
	"fmt"
	"io"
	"math"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/nvidia/sys"
)

const (
	// maxPendingPackets bounds the packets held for Receive; Send reports
	// ErrAgain beyond it.
	maxPendingPackets = 64
	// maxSurfaces bounds the input/output buffer pairs created on demand.
	maxSurfaces = 32
	// defaultQP is the constant quantiser used when neither a bitrate nor
	// a quality was requested.
	defaultQP = 26
	// bframesWhenEnabled is the number of consecutive B-frames used for
	// WithBFrames when the GPU supports them.
	bframesWhenEnabled = 2
	// paramSetBufferSize holds the VPS/SPS/PPS nvEncGetSequenceParams
	// returns.
	paramSetBufferSize = 4096
)

// surface is one NVENC input buffer and bitstream buffer pair.
type surface struct {
	in    uintptr // NV_ENC_INPUT_PTR
	out   uintptr // NV_ENC_OUTPUT_PTR
	pitch uint32
	busy  bool
}

// encoder is an NVENC session on the CUDA device. Encoding is synchronous:
// nvEncEncodePicture returns NV_ENC_ERR_NEED_MORE_INPUT while the encoder
// buffers frames for B-frame decisions and NV_ENC_SUCCESS once every
// submitted picture can be read back, so packets are produced in bursts.
type encoder struct {
	mu  sync.Mutex
	cfg codec.EncoderConfig
	dev *device
	api *sys.API
	enc uintptr

	codecGUID sys.GUID
	bufferFmt uint32
	tuning    uint32
	config    sys.Config
	initParam sys.InitializeParams

	surfaces []*surface
	inFlight []*surface // submitted pictures in submission order
	inputPTS []int64    // PTS of submitted pictures in input order (for DTS)
	pending  []codec.Packet

	paramSets     []byte // Annex-B VPS/SPS/PPS from nvEncGetSequenceParams
	bframes       int    // frameIntervalP - 1
	frameDuration int64  // in TimeScale units
	frameIdx      uint32
	forceNext     bool
	flushed       bool
	closed        bool
}

func (e *encoder) nvencErr(op string, st uint32) error {
	msg := sys.StatusString(st)
	if e.enc != 0 {
		if s := e.api.GetLastErrorString(e.enc); s != "" {
			msg += " (" + s + ")"
		}
	}
	return &codec.BackendError{Backend: Name, Op: op, Status: int64(st), Message: msg}
}

// hasCodec reports whether the session offers the encode GUID.
func (e *encoder) hasCodec(guid sys.GUID) (bool, error) {
	var n uint32
	if st := e.api.GetEncodeGUIDCount(e.enc, &n); st != sys.StatusSuccess {
		return false, e.nvencErr("nvEncGetEncodeGUIDCount", st)
	}
	if n == 0 {
		return false, nil
	}
	guids := make([]sys.GUID, n)
	if st := e.api.GetEncodeGUIDs(e.enc, &guids[0], n, &n); st != sys.StatusSuccess {
		return false, e.nvencErr("nvEncGetEncodeGUIDs", st)
	}
	for _, g := range guids[:n] {
		if g == guid {
			return true, nil
		}
	}
	return false, nil
}

// capability queries one NV_ENC_CAPS value for the session's codec; 0 when
// the query fails.
func (e *encoder) capability(id uint32) int {
	param := sys.CapsParam{Version: sys.CapsParamVer, CapsToQuery: id}
	var v int32
	if st := e.api.GetEncodeCaps(e.enc, e.codecGUID, &param, &v); st != sys.StatusSuccess {
		return 0
	}
	return int(v)
}

func (e *encoder) openSession() error {
	params := sys.OpenEncodeSessionExParams{Version: sys.OpenEncodeSessionExParamsVer, DeviceType: sys.DeviceTypeCUDA, Device: e.dev.ctx, APIVersion: sys.APIVersion}
	if st := e.api.OpenEncodeSessionEx(&params, &e.enc); st != sys.StatusSuccess {
		e.enc = 0
		return e.nvencErr("nvEncOpenEncodeSessionEx", st)
	}
	return nil
}

// probeEncoder opens a session to list the codecs NVENC offers.
func probeEncoder(dev *device, api *sys.API) ([]codec.Capability, error) {
	var caps []codec.Capability
	err := dev.run(func() error {
		e := &encoder{dev: dev, api: api}
		if err := e.openSession(); err != nil {
			return err
		}
		defer api.DestroyEncoder(e.enc)
		for _, c := range probeCodecs {
			guid, _ := encodeGUID(c)
			ok, err := e.hasCodec(guid)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			e.codecGUID = guid
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Encode, Hardware: true,
				MaxWidth: e.capability(sys.CapsWidthMax), MaxHeight: e.capability(sys.CapsHeightMax)})
		}
		return nil
	})
	return caps, err
}

func newEncoder(cfg codec.EncoderConfig, dev *device, api *sys.API) (*encoder, error) {
	guid, _ := encodeGUID(cfg.Codec)
	e := &encoder{cfg: cfg, dev: dev, api: api, codecGUID: guid, forceNext: true}
	fail := func(reason string) (*encoder, error) { return nil, unsupportedEncode(cfg.Codec, reason) }

	if cfg.Width%2 != 0 || cfg.Height%2 != 0 {
		return fail(fmt.Sprintf("picture size %dx%d: 4:2:0 encoding needs even dimensions", cfg.Width, cfg.Height))
	}
	switch cfg.InputFormat {
	case codec.NV12:
		e.bufferFmt = sys.BufferFormatNV12
	case codec.RGBA:
		e.bufferFmt = sys.BufferFormatABGR
	case codec.BGRA:
		e.bufferFmt = sys.BufferFormatARGB
	}
	var profile sys.GUID
	switch cfg.Codec {
	case codec.H264:
		switch cfg.Profile {
		case codec.ProfileBaseline:
			profile = sys.H264ProfileBaselineGUID
		case codec.ProfileMain:
			profile = sys.H264ProfileMainGUID
		default:
			profile = sys.H264ProfileHighGUID
		}
	case codec.HEVC:
		switch cfg.Profile {
		case codec.ProfileDefault, codec.ProfileMain:
			profile = sys.HEVCProfileMainGUID
		default:
			return fail("profile " + cfg.Profile.String() + " is not defined for hevc")
		}
	}
	if cfg.Bitrate > math.MaxUint32 {
		return fail(fmt.Sprintf("bitrate %d is out of range", cfg.Bitrate))
	}
	e.tuning = sys.TuningInfoHighQuality
	if cfg.LowLatency {
		e.tuning = sys.TuningInfoLowLatency
	}
	if cfg.FrameRate > 0 {
		e.frameDuration = int64(math.Round(float64(cfg.TimeScale) / cfg.FrameRate))
	} else {
		e.frameDuration = int64(cfg.TimeScale) / 30
	}

	err := dev.run(func() error {
		if err := e.openSession(); err != nil {
			return err
		}
		ok, err := e.hasCodec(guid)
		if err != nil {
			return err
		}
		if !ok {
			return unsupportedEncode(cfg.Codec, "the GPU ("+dev.name+") has no NVENC engine for "+cfg.Codec.String())
		}
		if maxW, maxH := e.capability(sys.CapsWidthMax), e.capability(sys.CapsHeightMax); (maxW > 0 && cfg.Width > maxW) || (maxH > 0 && cfg.Height > maxH) {
			return unsupportedEncode(cfg.Codec, fmt.Sprintf("%dx%d exceeds the NVENC maximum of %dx%d", cfg.Width, cfg.Height, maxW, maxH))
		}
		if minW, minH := e.capability(sys.CapsWidthMin), e.capability(sys.CapsHeightMin); cfg.Width < minW || cfg.Height < minH {
			return unsupportedEncode(cfg.Codec, fmt.Sprintf("%dx%d is below the NVENC minimum of %dx%d", cfg.Width, cfg.Height, minW, minH))
		}
		if cfg.Profile == codec.ProfileBaseline && cfg.Codec == codec.H264 && e.capability(sys.CapsSupportCABAC) == 0 {
			// Not a problem: Baseline uses CAVLC anyway.
			_ = 0
		}

		// Start from the driver's P4 preset for the tuning and adjust.
		var preset sys.PresetConfig
		preset.Version = sys.PresetConfigVer
		preset.PresetCfg.Version = sys.ConfigVer
		if st := e.api.GetEncodePresetConfigEx(e.enc, guid, sys.PresetP4GUID, e.tuning, &preset); st != sys.StatusSuccess {
			return e.nvencErr("nvEncGetEncodePresetConfigEx", st)
		}
		e.config = preset.PresetCfg
		e.config.Version = sys.ConfigVer
		e.config.ProfileGUID = profile
		e.config.FrameFieldMode = sys.FrameFieldModeFrame
		e.configureGOP(e.capability(sys.CapsNumMaxBFrames))
		e.configureRateControl()
		e.configureCodec()

		num, den := frameRateFraction(cfg.FrameRate)
		if num == 0 {
			num, den = 30, 1
		}
		e.initParam = sys.InitializeParams{
			Version:         sys.InitializeParamsVer,
			EncodeGUID:      guid,
			PresetGUID:      sys.PresetP4GUID,
			EncodeWidth:     uint32(cfg.Width),
			EncodeHeight:    uint32(cfg.Height),
			DARWidth:        uint32(cfg.Width),
			DARHeight:       uint32(cfg.Height),
			FrameRateNum:    num,
			FrameRateDen:    den,
			EnablePTD:       1,
			EncodeConfig:    &e.config,
			MaxEncodeWidth:  uint32(cfg.Width),
			MaxEncodeHeight: uint32(cfg.Height),
			TuningInfo:      e.tuning,
		}
		if st := e.api.InitializeEncoder(e.enc, &e.initParam); st != sys.StatusSuccess {
			if st == sys.StatusErrUnsupportedParam || st == sys.StatusErrInvalidParam {
				return unsupportedEncode(cfg.Codec, "nvEncInitializeEncoder rejected the configuration: "+e.nvencErr("nvEncInitializeEncoder", st).Error())
			}
			return e.nvencErr("nvEncInitializeEncoder", st)
		}
		return e.fetchParameterSets()
	})
	if err != nil {
		e.teardown()
		return nil, err
	}
	return e, nil
}

// configureGOP sets the keyframe interval and B-frame structure.
func (e *encoder) configureGOP(maxBFrames int) {
	cfg := &e.cfg
	gop := cfg.KeyframeInterval
	switch {
	case gop > 0:
	case cfg.FrameRate > 0:
		gop = int(cfg.FrameRate*2 + 0.5)
	default:
		gop = 60
	}
	e.config.GOPLength = uint32(gop)
	e.config.FrameIntervalP = 1
	e.bframes = 0
	if cfg.BFrames && !cfg.LowLatency && !(cfg.Codec == codec.H264 && cfg.Profile == codec.ProfileBaseline) && maxBFrames > 0 {
		e.bframes = min(bframesWhenEnabled, maxBFrames)
		e.config.FrameIntervalP = int32(e.bframes + 1)
	}
}

// configureRateControl maps the public bitrate and quality controls to
// NV_ENC_RC_PARAMS.
func (e *encoder) configureRateControl() {
	cfg := &e.cfg
	rc := &e.config.RC
	rc.Version = sys.RCParamsVer
	// Lookahead would delay output by its depth and is not needed for the
	// controls this API exposes.
	rc.Flags &^= sys.RCFlagEnableLookahead
	rc.LookaheadDepth = 0
	constQP := func(qp int) {
		rc.RateControlMode = sys.RCConstQP
		rc.ConstQP = sys.QP{InterP: uint32(qp), InterB: uint32(qp), Intra: uint32(qp)}
		rc.AverageBitRate, rc.MaxBitRate = 0, 0
		rc.VBVBufferSize, rc.VBVInitialDelay = 0, 0
		rc.MultiPass = 0
	}
	switch {
	case cfg.Quality > 0:
		constQP(min(max(int(math.Round(51-45*cfg.Quality)), 1), 51))
	case cfg.Bitrate > 0:
		rc.AverageBitRate = uint32(cfg.Bitrate)
		if cfg.RateControl == codec.CBR {
			rc.RateControlMode = sys.RCCBR
			rc.MaxBitRate = uint32(cfg.Bitrate)
		} else {
			rc.RateControlMode = sys.RCVBR
			rc.MaxBitRate = uint32(min(2*cfg.Bitrate, math.MaxUint32))
		}
		rc.VBVBufferSize, rc.VBVInitialDelay = 0, 0 // driver defaults
	default:
		constQP(defaultQP)
	}
	if cfg.LowLatency {
		rc.Flags |= sys.RCFlagZeroReorderDelay
	}
}

// configureCodec sets the H.264 or HEVC specific configuration: in-band
// parameter sets at every IDR, 4:2:0, and the colour description for RGB
// input, which NVENC converts with the BT.601 matrix.
func (e *encoder) configureCodec() {
	cfg := &e.cfg
	var vui *sys.VUIParameters
	switch cfg.Codec {
	case codec.H264:
		h := (*sys.ConfigH264)(unsafe.Pointer(&e.config.CodecConfig))
		h.IDRPeriod = e.config.GOPLength
		h.Flags |= sys.H264FlagRepeatSPSPPS
		h.Flags &^= sys.H264FlagDisableSPSPPS | sys.H264FlagOutputAUD
		h.ChromaFormatIDC = 1
		h.Level = 0 // autoselect
		if cfg.Profile == codec.ProfileBaseline {
			h.EntropyCodingMode = sys.EntropyCodingCAVLC
		}
		vui = &h.VUI
	case codec.HEVC:
		h := (*sys.ConfigHEVC)(unsafe.Pointer(&e.config.CodecConfig))
		h.IDRPeriod = e.config.GOPLength
		h.Flags |= sys.HEVCFlagRepeatSPSPPS
		h.Flags &^= sys.HEVCFlagDisableSPSPPS | sys.HEVCFlagOutputAUD
		h.Flags = h.Flags&^(3<<sys.HEVCFlagChromaFormatIDCShift) | 1<<sys.HEVCFlagChromaFormatIDCShift
		h.Flags &^= 7 << sys.HEVCFlagPixelBitDepthMinus8Shift
		h.Level = 0
		vui = &h.VUI
	}
	if cfg.InputFormat == codec.RGBA || cfg.InputFormat == codec.BGRA {
		vui.VideoSignalTypePresentFlag = 1
		vui.VideoFormat = 5 // unspecified
		vui.VideoFullRangeFlag = 0
		vui.ColourDescriptionPresentFlag = 1
		vui.ColourPrimaries = 2
		vui.TransferCharacteristics = 2
		vui.ColourMatrix = 5 // BT.470BG (BT.601), the matrix NVENC's RGB conversion uses
	}
}

// frameRateFraction turns fps into a numerator/denominator pair; (0, 0)
// when the rate is unknown.
func frameRateFraction(fps float64) (num, den uint32) {
	if fps <= 0 {
		return 0, 0
	}
	if fps == math.Trunc(fps) && fps < 1<<24 {
		return uint32(fps), 1
	}
	if n := math.Round(fps * 1001); n < 1<<30 {
		return uint32(n), 1001
	}
	return uint32(math.Round(fps)), 1
}

// fetchParameterSets asks the encoder for its VPS/SPS/PPS so that they can
// be prepended to keyframes should the stream lack them.
func (e *encoder) fetchParameterSets() error {
	buf := make([]byte, paramSetBufferSize)
	var n uint32
	payload := sys.SequenceParamPayload{Version: sys.SequenceParamPayloadVer, InBufferSize: uint32(len(buf)),
		SPSPPSBuffer: unsafe.Pointer(&buf[0]), OutSPSPPSPayloadSize: &n}
	if st := e.api.GetSequenceParams(e.enc, &payload); st != sys.StatusSuccess {
		return e.nvencErr("nvEncGetSequenceParams", st)
	}
	if int(n) > len(buf) {
		n = uint32(len(buf))
	}
	e.paramSets = append([]byte(nil), buf[:n]...)
	return nil
}

// acquireSurface returns a free buffer pair, creating one when all are in
// flight.
func (e *encoder) acquireSurface() (*surface, error) {
	for _, s := range e.surfaces {
		if !s.busy {
			s.busy = true
			return s, nil
		}
	}
	if len(e.surfaces) >= maxSurfaces {
		return nil, &codec.BackendError{Backend: Name, Op: "encode", Message: "every input buffer is still held by the encoder"}
	}
	in := sys.CreateInputBuffer{Version: sys.CreateInputBufferVer, Width: uint32(e.cfg.Width), Height: uint32(e.cfg.Height),
		MemoryHeap: sys.MemoryHeapAutoselect, BufferFmt: e.bufferFmt}
	if st := e.api.CreateInputBuffer(e.enc, &in); st != sys.StatusSuccess {
		return nil, e.nvencErr("nvEncCreateInputBuffer", st)
	}
	out := sys.CreateBitstreamBuffer{Version: sys.CreateBitstreamBufferVer}
	if st := e.api.CreateBitstreamBuffer(e.enc, &out); st != sys.StatusSuccess {
		e.api.DestroyInputBuffer(e.enc, in.InputBuffer)
		return nil, e.nvencErr("nvEncCreateBitstreamBuffer", st)
	}
	s := &surface{in: in.InputBuffer, out: out.BitstreamBuffer, busy: true}
	e.surfaces = append(e.surfaces, s)
	return s, nil
}

func (e *encoder) teardown() {
	e.dev.run(func() error { //nolint:errcheck // best effort teardown
		if e.enc == 0 {
			return nil
		}
		for _, s := range e.surfaces {
			e.api.DestroyInputBuffer(e.enc, s.in)
			e.api.DestroyBitstreamBuffer(e.enc, s.out)
		}
		e.surfaces = nil
		e.inFlight = nil
		e.api.DestroyEncoder(e.enc)
		e.enc = 0
		return nil
	})
}

func (e *encoder) checkFrame(f *codec.Frame) error {
	switch {
	case f == nil:
		return fmt.Errorf("%w: nil frame", codec.ErrInvalidData)
	case f.Format != e.cfg.InputFormat:
		return fmt.Errorf("%w: frame format %s, encoder expects %s", codec.ErrInvalidData, f.Format, e.cfg.InputFormat)
	case f.Width != e.cfg.Width || f.Height != e.cfg.Height:
		return fmt.Errorf("%w: frame size %dx%d, encoder expects %dx%d", codec.ErrInvalidData, f.Width, f.Height, e.cfg.Width, e.cfg.Height)
	}
	n := f.Format.PlaneCount()
	if len(f.Planes) != n || len(f.Strides) != n {
		return fmt.Errorf("%w: %s frame needs %d planes and strides, got %d and %d", codec.ErrInvalidData, f.Format, n, len(f.Planes), len(f.Strides))
	}
	for i := 0; i < n; i++ {
		rows, rowBytes := f.Format.PlaneLayout(i, f.Width, f.Height)
		if f.Strides[i] < rowBytes {
			return fmt.Errorf("%w: plane %d stride %d is smaller than its row of %d bytes", codec.ErrInvalidData, i, f.Strides[i], rowBytes)
		}
		if need := (rows-1)*f.Strides[i] + rowBytes; len(f.Planes[i]) < need {
			return fmt.Errorf("%w: plane %d holds %d bytes, need %d", codec.ErrInvalidData, i, len(f.Planes[i]), need)
		}
	}
	return nil
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
	if err := e.checkFrame(f); err != nil {
		return err
	}
	if len(e.pending) >= maxPendingPackets {
		return codec.ErrAgain
	}
	return e.dev.run(func() error {
		s, err := e.acquireSurface()
		if err != nil {
			return err
		}
		if err := e.upload(s, f); err != nil {
			s.busy = false
			return err
		}
		var flags uint32
		if e.forceNext || f.ForceKeyframe {
			flags = sys.PicFlagForceIDR | sys.PicFlagOutputSPSPPS
		}
		pic := sys.PicParams{
			Version:         sys.PicParamsVer,
			InputWidth:      uint32(e.cfg.Width),
			InputHeight:     uint32(e.cfg.Height),
			InputPitch:      s.pitch,
			EncodePicFlags:  flags,
			FrameIdx:        e.frameIdx,
			InputTimeStamp:  uint64(f.PTS),
			InputBuffer:     s.in,
			OutputBitstream: s.out,
			BufferFmt:       e.bufferFmt,
			PictureStruct:   sys.PicStructFrame,
		}
		st := e.api.EncodePicture(e.enc, &pic)
		switch st {
		case sys.StatusSuccess, sys.StatusErrNeedMoreInput:
		default:
			s.busy = false
			return e.nvencErr("nvEncEncodePicture", st)
		}
		e.frameIdx++
		e.forceNext = false
		e.flushed = false
		e.inFlight = append(e.inFlight, s)
		e.inputPTS = append(e.inputPTS, f.PTS)
		if st == sys.StatusSuccess {
			return e.drain()
		}
		return nil
	})
}

// upload copies the frame into the surface's input buffer.
func (e *encoder) upload(s *surface, f *codec.Frame) error {
	lock := sys.LockInputBuffer{Version: sys.LockInputBufferVer, InputBuffer: s.in}
	if st := e.api.LockInputBuffer(e.enc, &lock); st != sys.StatusSuccess {
		return e.nvencErr("nvEncLockInputBuffer", st)
	}
	defer e.api.UnlockInputBuffer(e.enc, s.in)
	if lock.BufferDataPtr == nil || lock.Pitch == 0 {
		return &codec.BackendError{Backend: Name, Op: "nvEncLockInputBuffer", Message: "locked buffer has no data pointer"}
	}
	s.pitch = lock.Pitch
	pitch := int(lock.Pitch)
	w, h := f.Width, f.Height
	switch f.Format {
	case codec.NV12:
		if pitch < w {
			return &codec.BackendError{Backend: Name, Op: "nvEncLockInputBuffer", Message: fmt.Sprintf("pitch %d is smaller than the width %d", pitch, w)}
		}
		dst := unsafe.Slice(lock.BufferDataPtr, pitch*(h+(h+1)/2))
		luma, chroma := dst[:pitch*h], dst[pitch*h:]
		copyRows(luma, pitch, f.Planes[0], f.Strides[0], w, h)
		copyRows(chroma, pitch, f.Planes[1], f.Strides[1], (w+1)/2*2, (h+1)/2)
	default: // RGBA, BGRA: one packed plane
		if pitch < 4*w {
			return &codec.BackendError{Backend: Name, Op: "nvEncLockInputBuffer", Message: fmt.Sprintf("pitch %d is smaller than the row of %d bytes", pitch, 4*w)}
		}
		dst := unsafe.Slice(lock.BufferDataPtr, pitch*h)
		copyRows(dst, pitch, f.Planes[0], f.Strides[0], 4*w, h)
	}
	return nil
}

func copyRows(dst []byte, dstPitch int, src []byte, srcStride, rowBytes, rows int) {
	for r := 0; r < rows; r++ {
		copy(dst[r*dstPitch:r*dstPitch+rowBytes], src[r*srcStride:r*srcStride+rowBytes])
	}
}

// drain reads back every submitted picture; it is called once
// nvEncEncodePicture reports NV_ENC_SUCCESS, which means all of them are
// ready.
func (e *encoder) drain() error {
	for len(e.inFlight) > 0 {
		s := e.inFlight[0]
		lock := sys.LockBitstream{Version: sys.LockBitstreamVer, OutputBitstream: s.out}
		if st := e.api.LockBitstream(e.enc, &lock); st != sys.StatusSuccess {
			return e.nvencErr("nvEncLockBitstream", st)
		}
		var data []byte
		if lock.BitstreamBufferPtr != nil && lock.BitstreamSizeInBytes > 0 {
			data = append([]byte(nil), unsafe.Slice(lock.BitstreamBufferPtr, int(lock.BitstreamSizeInBytes))...)
		}
		e.api.UnlockBitstream(e.enc, s.out)
		e.inFlight[0] = nil
		e.inFlight = e.inFlight[1:]
		s.busy = false
		if len(data) == 0 {
			return &codec.BackendError{Backend: Name, Op: "nvEncLockBitstream", Message: "the encoder produced no data for the picture"}
		}
		pts := int64(lock.OutputTimeStamp)
		dts := pts
		if len(e.inputPTS) > 0 {
			// Packets come in decode order; the k-th one may be decoded
			// no earlier than the k-th input frame minus the reorder
			// depth, which is what a muxer needs for monotonic DTS.
			dts = e.inputPTS[0] - int64(e.bframes)*e.frameDuration
			e.inputPTS = e.inputPTS[1:]
		}
		e.pending = append(e.pending, e.packet(data, pts, dts))
	}
	return nil
}

// packet wraps coded data as an Annex-B access unit, adding the parameter
// sets in front of keyframes when the encoder did not.
func (e *encoder) packet(data []byte, pts, dts int64) codec.Packet {
	c := e.cfg.Codec
	keyframe, hasPS := false, false
	for _, nal := range annexb.Split(data) {
		t := annexb.NALUnitType(c, nal)
		if annexb.IsVCL(c, t) && annexb.IsKeyframe(c, t) {
			keyframe = true
		}
		if t == map[codec.Codec]int{codec.H264: annexb.H264NALSPS, codec.HEVC: annexb.HEVCNALSPS}[c] {
			hasPS = true
		}
	}
	if keyframe && !hasPS && len(e.paramSets) > 0 {
		data = append(append(make([]byte, 0, len(e.paramSets)+len(data)), e.paramSets...), data...)
	}
	return codec.Packet{Data: data, PTS: pts, DTS: dts, Keyframe: keyframe}
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
	if len(e.pending) == 0 {
		if e.flushed {
			return codec.Packet{}, io.EOF
		}
		return codec.Packet{}, codec.ErrAgain
	}
	p := e.pending[0]
	copy(e.pending, e.pending[1:])
	e.pending[len(e.pending)-1] = codec.Packet{}
	e.pending = e.pending[:len(e.pending)-1]
	return p, nil
}

// Flush sends an end-of-stream picture so that NVENC emits every frame it
// is holding, then readies the encoder for reuse at a keyframe.
func (e *encoder) Flush(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := e.dev.run(func() error {
		if len(e.inFlight) == 0 {
			return nil
		}
		pic := sys.PicParams{Version: sys.PicParamsVer, EncodePicFlags: sys.PicFlagEOS}
		if st := e.api.EncodePicture(e.enc, &pic); st != sys.StatusSuccess {
			return e.nvencErr("nvEncEncodePicture(end of stream)", st)
		}
		return e.drain()
	})
	e.flushed = true
	e.forceNext = true
	e.inputPTS = e.inputPTS[:0]
	return err
}

func (e *encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	e.pending = nil
	e.teardown()
	e.dev.close()
	return nil
}
