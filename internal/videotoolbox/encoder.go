//go:build darwin

package videotoolbox

import (
	"context"
	"fmt"
	"io"
	"math"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/videotoolbox/sys"
)

// Encoders are reached from the VideoToolbox output callback through a small
// integer handle, never through a Go pointer. The callback may run on a
// VideoToolbox thread.
var (
	encRegistryMu   sync.Mutex
	encRegistry             = map[uintptr]*encoder{}
	encNextHandle   uintptr = 1
	encCallbackOnce sync.Once
	encCallbackPtr  uintptr
)

// maxPendingPackets bounds the packets held for Receive; Send reports
// ErrAgain beyond it.
const maxPendingPackets = 64

func encodeCallback() uintptr {
	encCallbackOnce.Do(func() {
		encCallbackPtr = purego.NewCallback(onEncodedFrame)
	})
	return encCallbackPtr
}

// onEncodedFrame is the VTCompressionOutputCallback. sourceRefCon carries the
// input frame's PTS.
func onEncodedFrame(refCon, sourceRefCon uintptr, status int32, infoFlags uint32, sample uintptr) uintptr {
	encRegistryMu.Lock()
	e := encRegistry[refCon]
	encRegistryMu.Unlock()
	if e == nil {
		return 0
	}
	e.outMu.Lock()
	defer e.outMu.Unlock()
	if status != 0 {
		e.cbErr = &codec.BackendError{Backend: Name, Op: "encode callback", Status: int64(status), Message: sys.StatusString(status)}
		return 0
	}
	if sample == 0 || infoFlags&sys.EncodeInfoFrameDropped != 0 {
		e.dropped++
		return 0
	}
	sys.CFRetain(sample)
	e.pending = append(e.pending, pendingSample{sample: sample, pts: int64(sourceRefCon)})
	return 0
}

type pendingSample struct {
	sample uintptr
	pts    int64
}

type encoder struct {
	mu      sync.Mutex
	cfg     codec.EncoderConfig
	vtCodec uint32
	handle  uintptr

	session  uintptr
	pool     uintptr // owned by the session
	keyProps uintptr // frame properties forcing a keyframe
	duration sys.CMTime

	// frameIndex counts frames since the start or the last Flush; it drives
	// the keyframe interval.
	frameIndex int
	forceNext  bool
	flushed    bool
	closed     bool

	// Parameter sets of the format description seen last, prepended to
	// keyframes. cachedFD is retained while cached.
	cachedFD  uintptr
	paramSets [][]byte
	nalLength int

	// outMu guards state shared with the output callback.
	outMu   sync.Mutex
	pending []pendingSample
	cbErr   error
	dropped int
}

func newEncoder(cfg codec.EncoderConfig, vtCodec uint32) (*encoder, error) {
	e := &encoder{cfg: cfg, vtCodec: vtCodec, forceNext: true}
	encRegistryMu.Lock()
	e.handle = encNextHandle
	encNextHandle++
	encRegistry[e.handle] = e
	encRegistryMu.Unlock()
	if err := e.createSession(); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *encoder) unsupported(reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: e.cfg.Codec, Direction: codec.Encode, Reason: reason}
}

func (e *encoder) createSession() error {
	cfg := &e.cfg
	spec := sys.NewDictionary()
	defer sys.Release(spec)
	if cfg.AllowSoftware {
		sys.CFDictionarySetValue(spec, sys.KVTEnableHardwareEncoder, sys.KCFBooleanTrue)
	} else {
		sys.CFDictionarySetValue(spec, sys.KVTRequireHardwareEncoder, sys.KCFBooleanTrue)
	}
	if cfg.LowLatency {
		if sys.KVTEnableLowLatencyRateControl == 0 {
			return e.unsupported("low-latency mode needs macOS 11.3 or later")
		}
		sys.CFDictionarySetValue(spec, sys.KVTEnableLowLatencyRateControl, sys.KCFBooleanTrue)
	}

	attrs := sys.NewDictionary()
	defer sys.Release(attrs)
	for _, kv := range []struct {
		key uintptr
		val int32
	}{
		{sys.KCVPixelBufferPixelFormatTypeKey, int32(sys.PixelFormat420YpCbCr8BiPlanarVideoRange)},
		{sys.KCVPixelBufferWidthKey, int32(cfg.Width)},
		{sys.KCVPixelBufferHeightKey, int32(cfg.Height)},
	} {
		n := sys.CFNumberInt32(kv.val)
		sys.CFDictionarySetValue(attrs, kv.key, n)
		sys.Release(n)
	}

	var session uintptr
	sessionMu.Lock()
	st := sys.VTCompressionSessionCreate(0, int32(cfg.Width), int32(cfg.Height), e.vtCodec, spec, attrs, 0, encodeCallback(), e.handle, &session)
	sessionMu.Unlock()
	if st != 0 {
		switch st {
		case sys.StatusVTCouldNotFindVideoEncoder, sys.StatusVTVideoEncoderNotAvailableNow:
			return e.unsupported(sys.StatusString(st))
		}
		return &codec.BackendError{Backend: Name, Op: "VTCompressionSessionCreate", Status: int64(st), Message: sys.StatusString(st)}
	}
	e.session = session

	// Offline (false) lets the encoder run faster than real time; low
	// latency ties it to the wall clock.
	if err := e.setProperty("RealTime", sys.KVTRealTime, sys.CFBoolean(cfg.LowLatency), false); err != nil {
		return err
	}
	if err := e.setProperty("AllowFrameReordering", sys.KVTAllowFrameReordering, sys.CFBoolean(cfg.BFrames && !cfg.LowLatency), false); err != nil {
		return err
	}
	if cfg.Profile != codec.ProfileDefault {
		level, ok := vtProfile(cfg.Codec, cfg.Profile)
		if !ok {
			return e.unsupported("profile " + cfg.Profile.String() + " is not defined for " + cfg.Codec.String())
		}
		if err := e.setProperty("ProfileLevel", sys.KVTProfileLevel, level, false); err != nil {
			return err
		}
	}
	if cfg.FrameRate > 0 {
		if err := e.setProperty("ExpectedFrameRate", sys.KVTExpectedFrameRate, sys.CFNumberFloat64(cfg.FrameRate), true); err != nil {
			return err
		}
		e.duration = sys.CMTime{
			Value:     int64(math.Round(float64(cfg.TimeScale) / cfg.FrameRate)),
			Timescale: cfg.TimeScale,
			Flags:     sys.CMTimeFlagsValid,
		}
	}
	if cfg.Bitrate > 0 {
		if cfg.Bitrate > math.MaxInt32 {
			return e.unsupported(fmt.Sprintf("bitrate %d exceeds the VideoToolbox maximum", cfg.Bitrate))
		}
		switch cfg.RateControl {
		case codec.CBR:
			if sys.KVTConstantBitRate == 0 {
				return e.unsupported("constant bitrate needs macOS 13 or later")
			}
			if err := e.setProperty("ConstantBitRate", sys.KVTConstantBitRate, sys.CFNumberInt32(int32(cfg.Bitrate)), true); err != nil {
				return err
			}
		default:
			if err := e.setProperty("AverageBitRate", sys.KVTAverageBitRate, sys.CFNumberInt32(int32(cfg.Bitrate)), true); err != nil {
				return err
			}
		}
	}
	if cfg.Quality > 0 {
		if err := e.setProperty("Quality", sys.KVTQuality, sys.CFNumberFloat32(float32(cfg.Quality)), true); err != nil {
			return err
		}
	}
	if cfg.KeyframeInterval > 0 {
		if err := e.setProperty("MaxKeyFrameInterval", sys.KVTMaxKeyFrameInterval, sys.CFNumberInt32(int32(cfg.KeyframeInterval)), true); err != nil {
			return err
		}
	}

	if st := sys.VTCompressionSessionPrepareToEncodeFrames(session); st != 0 {
		return &codec.BackendError{Backend: Name, Op: "VTCompressionSessionPrepareToEncodeFrames", Status: int64(st), Message: sys.StatusString(st)}
	}
	e.pool = sys.VTCompressionSessionGetPixelBufferPool(session)

	e.keyProps = sys.NewDictionary()
	sys.CFDictionarySetValue(e.keyProps, sys.KVTForceKeyFrame, sys.KCFBooleanTrue)
	return nil
}

// setProperty sets one session property. A value the encoder rejects as
// unsupported is reported as ErrUnsupported. When release is true the value
// is released afterwards.
func (e *encoder) setProperty(name string, key, value uintptr, release bool) error {
	st := sys.VTSessionSetProperty(e.session, key, value)
	if release {
		sys.Release(value)
	}
	switch st {
	case 0:
		return nil
	case sys.StatusVTPropertyNotSupported, sys.StatusVTPropertyReadOnly, sys.StatusVTParameter:
		return e.unsupported(name + " is not supported by this encoder (" + sys.StatusString(st) + ")")
	}
	return &codec.BackendError{Backend: Name, Op: "VTSessionSetProperty " + name, Status: int64(st), Message: sys.StatusString(st)}
}

func vtProfile(c codec.Codec, p codec.Profile) (uintptr, bool) {
	switch c {
	case codec.H264:
		switch p {
		case codec.ProfileBaseline:
			return sys.KVTProfileH264Baseline, true
		case codec.ProfileMain:
			return sys.KVTProfileH264Main, true
		case codec.ProfileHigh:
			return sys.KVTProfileH264High, true
		}
	case codec.HEVC:
		if p == codec.ProfileMain {
			return sys.KVTProfileHEVCMain, true
		}
	}
	return 0, false
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
	e.outMu.Lock()
	n := len(e.pending)
	e.outMu.Unlock()
	if n >= maxPendingPackets {
		return codec.ErrAgain
	}

	pb, err := e.newPixelBuffer()
	if err != nil {
		return err
	}
	defer sys.Release(pb)
	if err := copyFrameIn(pb, f); err != nil {
		return err
	}

	force := f.ForceKeyframe || e.forceNext
	if e.cfg.KeyframeInterval > 0 && e.frameIndex%e.cfg.KeyframeInterval == 0 {
		force = true
	}
	var props uintptr
	if force {
		props = e.keyProps
	}
	pts := sys.CMTime{Value: f.PTS, Timescale: e.cfg.TimeScale, Flags: sys.CMTimeFlagsValid}
	var info uint32
	st := sys.VTCompressionSessionEncodeFrame(e.session, pb, pts, e.duration, props, uintptr(f.PTS), &info)
	if st != 0 {
		return &codec.BackendError{Backend: Name, Op: "VTCompressionSessionEncodeFrame", Status: int64(st), Message: sys.StatusString(st)}
	}
	e.flushed = false
	e.forceNext = false
	e.frameIndex++
	return e.takeCallbackError()
}

func (e *encoder) checkFrame(f *codec.Frame) error {
	switch {
	case f == nil:
		return fmt.Errorf("%w: nil frame", codec.ErrInvalidData)
	case f.Format != e.cfg.InputFormat:
		return fmt.Errorf("%w: frame format %s, encoder expects %s", codec.ErrInvalidData, f.Format, e.cfg.InputFormat)
	case f.Width != e.cfg.Width || f.Height != e.cfg.Height:
		return fmt.Errorf("%w: frame size %dx%d, encoder expects %dx%d", codec.ErrInvalidData, f.Width, f.Height, e.cfg.Width, e.cfg.Height)
	case len(f.Planes) != 2 || len(f.Strides) != 2:
		return fmt.Errorf("%w: NV12 frame needs 2 planes and 2 strides, got %d and %d", codec.ErrInvalidData, len(f.Planes), len(f.Strides))
	}
	rows := [2]int{f.Height, (f.Height + 1) / 2}
	rowBytes := [2]int{f.Width, (f.Width + 1) / 2 * 2}
	for i := range f.Planes {
		if f.Strides[i] < rowBytes[i] {
			return fmt.Errorf("%w: plane %d stride %d is smaller than its row of %d bytes", codec.ErrInvalidData, i, f.Strides[i], rowBytes[i])
		}
		if need := (rows[i]-1)*f.Strides[i] + rowBytes[i]; len(f.Planes[i]) < need {
			return fmt.Errorf("%w: plane %d holds %d bytes, need %d", codec.ErrInvalidData, i, len(f.Planes[i]), need)
		}
	}
	return nil
}

func (e *encoder) newPixelBuffer() (uintptr, error) {
	var pb uintptr
	if e.pool != 0 {
		if st := sys.CVPixelBufferPoolCreatePixelBuffer(0, e.pool, &pb); st == 0 && pb != 0 {
			if sys.CVPixelBufferGetPixelFormatType(pb) == sys.PixelFormat420YpCbCr8BiPlanarVideoRange &&
				int(sys.CVPixelBufferGetWidth(pb)) == e.cfg.Width && int(sys.CVPixelBufferGetHeight(pb)) == e.cfg.Height {
				return pb, nil
			}
			sys.Release(pb)
			pb = 0
		}
		// The pool did not deliver a usable buffer; allocate directly from
		// now on.
		e.pool = 0
	}
	if st := sys.CVPixelBufferCreate(0, uintptr(e.cfg.Width), uintptr(e.cfg.Height), sys.PixelFormat420YpCbCr8BiPlanarVideoRange, 0, &pb); st != 0 || pb == 0 {
		return 0, &codec.BackendError{Backend: Name, Op: "CVPixelBufferCreate", Status: int64(st)}
	}
	return pb, nil
}

// copyFrameIn copies the NV12 planes of f into the pixel buffer.
func copyFrameIn(pb uintptr, f *codec.Frame) error {
	if st := sys.CVPixelBufferLockBaseAddress(pb, 0); st != 0 {
		return &codec.BackendError{Backend: Name, Op: "CVPixelBufferLockBaseAddress", Status: int64(st)}
	}
	defer sys.CVPixelBufferUnlockBaseAddress(pb, 0)
	if n := sys.CVPixelBufferGetPlaneCount(pb); n != 2 {
		return &codec.BackendError{Backend: Name, Op: "copy frame", Status: int64(n), Message: "unexpected plane count"}
	}
	for i := 0; i < 2; i++ {
		base := sys.CVPixelBufferGetBaseAddressOfPlane(pb, uintptr(i))
		stride := int(sys.CVPixelBufferGetBytesPerRowOfPlane(pb, uintptr(i)))
		rows := int(sys.CVPixelBufferGetHeightOfPlane(pb, uintptr(i)))
		rowBytes := int(sys.CVPixelBufferGetWidthOfPlane(pb, uintptr(i)))
		if i == 1 {
			rowBytes *= 2
		}
		if base == nil || stride < rowBytes {
			return &codec.BackendError{Backend: Name, Op: "copy frame", Message: "plane base address unavailable"}
		}
		// The frame was validated against the encoder size; clamp to what
		// the buffer actually has in case the two disagree by rounding.
		srcRows := (f.Height + 1) / 2
		srcRowBytes := (f.Width + 1) / 2 * 2
		if i == 0 {
			srcRows, srcRowBytes = f.Height, f.Width
		}
		rows = min(rows, srcRows)
		rowBytes = min(rowBytes, srcRowBytes)
		dst := unsafe.Slice(base, stride*rows)
		src := f.Planes[i]
		sstride := f.Strides[i]
		for r := 0; r < rows; r++ {
			copy(dst[r*stride:r*stride+rowBytes], src[r*sstride:r*sstride+rowBytes])
		}
	}
	return nil
}

func (e *encoder) takeCallbackError() error {
	e.outMu.Lock()
	err := e.cbErr
	e.cbErr = nil
	e.outMu.Unlock()
	return err
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
	e.outMu.Lock()
	if len(e.pending) == 0 {
		err := e.cbErr
		e.cbErr = nil
		e.outMu.Unlock()
		if err != nil {
			return codec.Packet{}, err
		}
		if e.flushed {
			return codec.Packet{}, io.EOF
		}
		return codec.Packet{}, codec.ErrAgain
	}
	ps := e.pending[0]
	copy(e.pending, e.pending[1:])
	e.pending = e.pending[:len(e.pending)-1]
	e.outMu.Unlock()

	pkt, err := e.packetFromSample(ps.sample, ps.pts)
	sys.Release(ps.sample)
	return pkt, err
}

// packetFromSample converts an AVCC/HVCC sample buffer into an Annex-B access
// unit, prepending the parameter sets to keyframes.
func (e *encoder) packetFromSample(sample uintptr, refPTS int64) (codec.Packet, error) {
	pts, dts := refPTS, refPTS
	var ti sys.CMSampleTimingInfo
	if st := sys.CMSampleBufferGetSampleTimingInfo(sample, 0, &ti); st == 0 {
		if ti.PresentationTimeStamp.Flags&sys.CMTimeFlagsValid != 0 {
			pts = e.rescale(ti.PresentationTimeStamp)
		}
		dts = pts
		if ti.DecodeTimeStamp.Flags&sys.CMTimeFlagsValid != 0 {
			dts = e.rescale(ti.DecodeTimeStamp)
		}
	}

	block := sys.CMSampleBufferGetDataBuffer(sample)
	if block == 0 {
		return codec.Packet{}, &codec.BackendError{Backend: Name, Op: "CMSampleBufferGetDataBuffer", Message: "sample has no data"}
	}
	n := int(sys.CMBlockBufferGetDataLength(block))
	if n == 0 {
		return codec.Packet{}, &codec.BackendError{Backend: Name, Op: "CMBlockBufferGetDataLength", Message: "sample is empty"}
	}
	raw := make([]byte, n)
	if st := sys.CMBlockBufferCopyDataBytes(block, 0, uintptr(n), &raw[0]); st != 0 {
		return codec.Packet{}, &codec.BackendError{Backend: Name, Op: "CMBlockBufferCopyDataBytes", Status: int64(st)}
	}
	if err := e.updateParameterSets(sys.CMSampleBufferGetFormatDescription(sample)); err != nil {
		return codec.Packet{}, err
	}

	var nals [][]byte
	keyframe := false
	total := 0
	for off := 0; off < n; {
		if off+e.nalLength > n {
			return codec.Packet{}, &codec.BackendError{Backend: Name, Op: "parse sample", Message: "truncated NAL unit length"}
		}
		size := 0
		for i := 0; i < e.nalLength; i++ {
			size = size<<8 | int(raw[off+i])
		}
		off += e.nalLength
		if size <= 0 || size > n-off {
			return codec.Packet{}, &codec.BackendError{Backend: Name, Op: "parse sample", Message: "NAL unit length out of range"}
		}
		nal := raw[off : off+size]
		off += size
		if t := annexb.NALUnitType(e.cfg.Codec, nal); annexb.IsVCL(e.cfg.Codec, t) && annexb.IsKeyframe(e.cfg.Codec, t) {
			keyframe = true
		}
		nals = append(nals, nal)
		total += 4 + size
	}
	if keyframe {
		for _, ps := range e.paramSets {
			total += 4 + len(ps)
		}
	}
	out := make([]byte, 0, total)
	if keyframe {
		for _, ps := range e.paramSets {
			out = append(out, 0, 0, 0, 1)
			out = append(out, ps...)
		}
	}
	for _, nal := range nals {
		out = append(out, 0, 0, 0, 1)
		out = append(out, nal...)
	}
	return codec.Packet{Data: out, PTS: pts, DTS: dts, Keyframe: keyframe}, nil
}

func (e *encoder) rescale(t sys.CMTime) int64 {
	if t.Timescale == e.cfg.TimeScale || t.Timescale == 0 {
		return t.Value
	}
	return int64(math.Round(float64(t.Value) * float64(e.cfg.TimeScale) / float64(t.Timescale)))
}

// updateParameterSets refreshes the cached VPS/SPS/PPS when the sample's
// format description differs from the cached one.
func (e *encoder) updateParameterSets(fd uintptr) error {
	if fd == 0 {
		return &codec.BackendError{Backend: Name, Op: "CMSampleBufferGetFormatDescription", Message: "sample has no format description"}
	}
	if fd == e.cachedFD {
		return nil
	}
	get := sys.CMVideoFormatDescriptionGetH264ParameterSetAtIndex
	if e.cfg.Codec == codec.HEVC {
		get = sys.CMVideoFormatDescriptionGetHEVCParameterSetAtIndex
	}
	var sets [][]byte
	var count uintptr
	var nalLen int32
	for i := uintptr(0); ; i++ {
		var ptr *byte
		var size uintptr
		if st := get(fd, i, &ptr, &size, &count, &nalLen); st != 0 {
			return &codec.BackendError{Backend: Name, Op: "CMVideoFormatDescriptionGetParameterSetAtIndex", Status: int64(st), Message: sys.StatusString(st)}
		}
		if ptr == nil || size == 0 {
			return &codec.BackendError{Backend: Name, Op: "CMVideoFormatDescriptionGetParameterSetAtIndex", Message: "empty parameter set"}
		}
		sets = append(sets, append([]byte(nil), unsafe.Slice(ptr, size)...))
		if i+1 >= count {
			break
		}
	}
	if nalLen != 1 && nalLen != 2 && nalLen != 4 {
		return &codec.BackendError{Backend: Name, Op: "CMVideoFormatDescriptionGetParameterSetAtIndex", Status: int64(nalLen), Message: "unexpected NAL unit length size"}
	}
	sys.CFRetain(fd)
	sys.Release(e.cachedFD)
	e.cachedFD = fd
	e.paramSets = sets
	e.nalLength = int(nalLen)
	return nil
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
	// An invalid time completes every frame. This blocks until the encoder
	// has emitted them all.
	st := sys.VTCompressionSessionCompleteFrames(e.session, sys.CMTime{})
	e.flushed = true
	e.forceNext = true
	e.frameIndex = 0
	if st != 0 {
		return &codec.BackendError{Backend: Name, Op: "VTCompressionSessionCompleteFrames", Status: int64(st), Message: sys.StatusString(st)}
	}
	return e.takeCallbackError()
}

func (e *encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	// Drop the registry entry first so that a late callback is a no-op.
	encRegistryMu.Lock()
	delete(encRegistry, e.handle)
	encRegistryMu.Unlock()
	if e.session != 0 {
		sessionMu.Lock()
		sys.VTCompressionSessionInvalidate(e.session)
		sessionMu.Unlock()
		sys.Release(e.session)
		e.session = 0
		e.pool = 0
	}
	sys.Release(e.keyProps)
	e.keyProps = 0
	sys.Release(e.cachedFD)
	e.cachedFD = 0
	e.outMu.Lock()
	for _, ps := range e.pending {
		sys.Release(ps.sample)
	}
	e.pending = nil
	e.outMu.Unlock()
	return nil
}
