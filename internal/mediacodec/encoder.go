//go:build android || mediacodec_typecheck

package mediacodec

import (
	"context"
	"fmt"
	"io"
	"math"
	"sync"
	"time"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediacodec/sys"
)

type encoder struct {
	mu   sync.Mutex
	cfg  codec.EncoderConfig
	mime string
	name string

	codec       uintptr // AMediaCodec, owned
	colorFormat int32   // the input colour format the encoder accepted
	in          layout  // layout of the input buffers

	ps       *paramSets      // from the codec-config buffer, prepended to keyframes
	inflight map[int64]int64 // presentationTimeUs -> caller PTS
	sent     []int64         // PTS of every frame given to the encoder, in order
	taken    int             // packets produced so far (decode order)
	frameDur int64           // one frame in TimeScale units, for DTS with B-frames
	pending  []codec.Packet

	frameIndex int
	forceNext  bool
	eosQueued  bool
	eosSeen    bool
	flushed    bool
	closed     bool
}

func unsupportedEnc(c codec.Codec, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: codec.Encode, Reason: reason}
}

// newEncoder configures and starts the encoder. MediaCodec is asked for
// semi-planar (NV12) input first, then planar (I420), which the caller's
// NV12 frames are converted to while they are copied into the input buffer.
func newEncoder(cfg codec.EncoderConfig, mime, name string, c uintptr) (*encoder, error) {
	e := &encoder{cfg: cfg, mime: mime, name: name, codec: c, ps: newParamSets(cfg.Codec), inflight: map[int64]int64{}, forceNext: true}
	if cfg.FrameRate > 0 {
		e.frameDur = int64(math.Round(float64(cfg.TimeScale) / cfg.FrameRate))
	}
	var last int32
	for _, cf := range []int32{sys.ColorFormatYUV420SemiPlanar, sys.ColorFormatYUV420Planar} {
		f, err := e.format(cf)
		if err != nil {
			e.Close()
			return nil, err
		}
		st := sys.AMediaCodec_configure(c, f, 0, 0, sys.ConfigureFlagEncode)
		sys.AMediaFormat_delete(f)
		if st == sys.OK {
			e.colorFormat = cf
			break
		}
		last = st
	}
	if e.colorFormat == 0 {
		e.Close()
		return nil, unsupportedEnc(cfg.Codec, fmt.Sprintf("%s rejected the configuration (%s): %s", name, sys.StatusString(last), e.describe()))
	}
	if err := e.readInputFormat(); err != nil {
		e.Close()
		return nil, err
	}
	if st := sys.AMediaCodec_start(c); st != sys.OK {
		e.Close()
		return nil, backendErr("AMediaCodec_start", st)
	}
	return e, nil
}

// describe summarises the requested controls for error messages.
func (e *encoder) describe() string {
	cfg := &e.cfg
	s := fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
	switch {
	case cfg.Quality > 0:
		s += fmt.Sprintf(" quality %.2f", cfg.Quality)
	case cfg.Bitrate > 0:
		s += fmt.Sprintf(" %d bit/s %s", cfg.Bitrate, cfg.RateControl)
	}
	if cfg.Profile != codec.ProfileDefault {
		s += " profile " + cfg.Profile.String()
	}
	if cfg.BFrames {
		s += " b-frames"
	}
	if cfg.LowLatency {
		s += " low-latency"
	}
	return s
}

// format builds the MediaFormat for configure.
func (e *encoder) format(colorFormat int32) (uintptr, error) {
	cfg := &e.cfg
	f := sys.AMediaFormat_new()
	if f == 0 {
		return 0, backendErr("AMediaFormat_new", sys.ErrorUnknown)
	}
	sys.AMediaFormat_setString(f, sys.KeyMime, e.mime)
	sys.AMediaFormat_setInt32(f, sys.KeyWidth, int32(cfg.Width))
	sys.AMediaFormat_setInt32(f, sys.KeyHeight, int32(cfg.Height))
	sys.AMediaFormat_setInt32(f, sys.KeyColorFormat, colorFormat)

	fps := cfg.FrameRate
	if fps <= 0 {
		fps = 30
	}
	sys.AMediaFormat_setFloat(f, sys.KeyFrameRate, float32(fps))

	// MediaCodec wants a bitrate even in constant-quality mode; without a
	// target, 0.1 bit per pixel per frame (at least 200 kbit/s) as on
	// Windows.
	bitrate := cfg.Bitrate
	if bitrate <= 0 {
		bitrate = int(float64(cfg.Width*cfg.Height) * fps * 0.1)
		if bitrate < 200_000 {
			bitrate = 200_000
		}
	}
	if bitrate > math.MaxInt32 {
		sys.AMediaFormat_delete(f)
		return 0, unsupportedEnc(cfg.Codec, fmt.Sprintf("bitrate %d exceeds the MediaCodec maximum", bitrate))
	}
	sys.AMediaFormat_setInt32(f, sys.KeyBitrate, int32(bitrate))
	switch {
	case cfg.Quality > 0:
		sys.AMediaFormat_setInt32(f, sys.KeyBitrateMode, sys.BitrateModeCQ)
		sys.AMediaFormat_setInt32(f, sys.KeyQuality, int32(math.Round(cfg.Quality*100)))
	case cfg.RateControl == codec.CBR:
		sys.AMediaFormat_setInt32(f, sys.KeyBitrateMode, sys.BitrateModeCBR)
	default:
		sys.AMediaFormat_setInt32(f, sys.KeyBitrateMode, sys.BitrateModeVBR)
	}

	// The keyframe interval is a hint: keyframes are forced from Go at
	// the exact frame count. Without one, one keyframe per second.
	seconds := 1
	if cfg.KeyframeInterval > 0 {
		seconds = int(math.Ceil(float64(cfg.KeyframeInterval) / fps))
		if seconds < 1 {
			seconds = 1
		}
	}
	sys.AMediaFormat_setInt32(f, sys.KeyIFrameInterval, int32(seconds))

	if cfg.Profile != codec.ProfileDefault {
		profile, level, ok := profileLevel(cfg.Codec, cfg.Profile, cfg.Width, cfg.Height)
		if !ok {
			sys.AMediaFormat_delete(f)
			return 0, unsupportedEnc(cfg.Codec, "profile "+cfg.Profile.String()+" is not defined for "+cfg.Codec.String())
		}
		sys.AMediaFormat_setInt32(f, sys.KeyProfile, profile)
		sys.AMediaFormat_setInt32(f, sys.KeyLevel, level)
	}
	if cfg.LowLatency {
		sys.AMediaFormat_setInt32(f, sys.KeyLatency, 1)
		sys.AMediaFormat_setInt32(f, sys.KeyLowLatency, 1)
	}
	if cfg.BFrames && !cfg.LowLatency {
		sys.AMediaFormat_setInt32(f, sys.KeyMaxBFrames, 1)
	} else {
		sys.AMediaFormat_setInt32(f, sys.KeyMaxBFrames, 0)
	}
	if cfg.BT709 {
		sys.AMediaFormat_setInt32(f, sys.KeyColorStandard, sys.ColorStandardBT709)
		sys.AMediaFormat_setInt32(f, sys.KeyColorRange, sys.ColorRangeLimited)
		sys.AMediaFormat_setInt32(f, sys.KeyColorTransfer, sys.ColorTransferSDRVideo)
	}
	// prepend-sps-pps-to-idr-frames is not asked for: an encoder without
	// that control fails configure with BAD_VALUE (the Android 14
	// emulator's c2.android.avc.encoder does), and the parameter sets are
	// prepended from Go anyway.
	return f, nil
}

// readInputFormat learns the layout of the input buffers from the codec's
// input format: a MediaImage2 when the framework provides one, else the
// accepted colour format with its stride and slice height.
func (e *encoder) readInputFormat() error {
	f := sys.AMediaCodec_getInputFormat(e.codec)
	if f == 0 {
		return backendErr("AMediaCodec_getInputFormat", sys.ErrorUnknown)
	}
	defer sys.AMediaFormat_delete(f)
	if img := sys.FormatBuffer(f, sys.KeyImageData); img != nil {
		if l, err := parseMediaImage2(img); err == nil && l.width >= e.cfg.Width && l.height >= e.cfg.Height {
			e.in = l
			return nil
		}
	}
	cf, ok := sys.FormatInt32(f, sys.KeyColorFormat)
	if !ok || (cf != sys.ColorFormatYUV420SemiPlanar && cf != sys.ColorFormatYUV420Planar) {
		cf = e.colorFormat
	}
	stride, _ := sys.FormatInt32(f, sys.KeyStride)
	sliceH, _ := sys.FormatInt32(f, sys.KeySliceHeight)
	l, err := layoutFromColorFormat(cf, e.cfg.Width, e.cfg.Height, int(stride), int(sliceH))
	if err != nil {
		return unsupportedEnc(e.cfg.Codec, fmt.Sprintf("%s input layout not understood (%v): %s", e.name, err, sys.FormatString(f)))
	}
	e.in = l
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
	if e.flushed {
		if err := e.restart(); err != nil {
			return err
		}
	}
	if len(e.pending) >= maxPending {
		return codec.ErrAgain
	}

	idx, err := dequeueInput(ctx, e.codec, e.drainReady)
	if err != nil {
		return err
	}
	var size uintptr
	buf := sys.AMediaCodec_getInputBuffer(e.codec, uintptr(idx), &size)
	need := e.in.size()
	if buf == nil || int(size) < need {
		return &codec.BackendError{Backend: Name, Op: "AMediaCodec_getInputBuffer", Message: fmt.Sprintf("input buffer of %d bytes, the %dx%d layout needs %d", size, e.in.width, e.in.height, need)}
	}
	e.in.unpackNV12(unsafe.Slice(buf, size), f.Planes[0], f.Strides[0], f.Planes[1], f.Strides[1], e.cfg.Width, e.cfg.Height)

	force := f.ForceKeyframe || e.forceNext
	if e.cfg.KeyframeInterval > 0 && e.frameIndex%e.cfg.KeyframeInterval == 0 {
		force = true
	}
	if force {
		p := sys.AMediaFormat_new()
		if p != 0 {
			sys.AMediaFormat_setInt32(p, sys.KeyRequestSyncFrame, 0)
			sys.AMediaCodec_setParameters(e.codec, p)
			sys.AMediaFormat_delete(p)
		}
	}
	us := toMicros(f.PTS, e.cfg.TimeScale)
	for {
		if _, taken := e.inflight[us]; !taken {
			break
		}
		us++
	}
	if st := sys.AMediaCodec_queueInputBuffer(e.codec, uintptr(idx), 0, uintptr(need), uint64(us), 0); st != sys.OK {
		return backendErr("AMediaCodec_queueInputBuffer", st)
	}
	e.inflight[us] = f.PTS
	if e.frameDur == 0 && len(e.sent) == 1 && f.PTS > e.sent[0] {
		e.frameDur = f.PTS - e.sent[0]
	}
	e.sent = append(e.sent, f.PTS)
	e.frameIndex++
	e.forceNext = false
	return e.drainReady()
}

func (e *encoder) checkFrame(f *codec.Frame) error {
	n := e.cfg.InputFormat.PlaneCount()
	switch {
	case f == nil:
		return fmt.Errorf("%w: nil frame", codec.ErrInvalidData)
	case f.Format != e.cfg.InputFormat:
		return fmt.Errorf("%w: frame format %s, encoder expects %s", codec.ErrInvalidData, f.Format, e.cfg.InputFormat)
	case f.Width != e.cfg.Width || f.Height != e.cfg.Height:
		return fmt.Errorf("%w: frame size %dx%d, encoder expects %dx%d", codec.ErrInvalidData, f.Width, f.Height, e.cfg.Width, e.cfg.Height)
	case len(f.Planes) != n || len(f.Strides) != n:
		return fmt.Errorf("%w: %s frame needs %d planes and %d strides, got %d and %d", codec.ErrInvalidData, f.Format, n, n, len(f.Planes), len(f.Strides))
	}
	for i := range f.Planes {
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

// restart makes an encoder that has delivered its end of stream take new
// frames; the first one is a keyframe.
func (e *encoder) restart() error {
	if st := sys.AMediaCodec_flush(e.codec); st != sys.OK {
		return backendErr("AMediaCodec_flush", st)
	}
	e.flushed = false
	e.eosQueued, e.eosSeen = false, false
	e.forceNext = true
	e.frameIndex = 0
	clear(e.inflight)
	e.sent = e.sent[:0]
	e.taken = 0
	return nil
}

func (e *encoder) drainReady() error { return e.drain(0) }

// drain dequeues output until none is ready, waiting up to timeoutUs for
// the first one.
func (e *encoder) drain(timeoutUs int64) error {
	for {
		var info sys.BufferInfo
		idx := sys.AMediaCodec_dequeueOutputBuffer(e.codec, &info, timeoutUs)
		switch {
		case idx >= 0:
			if err := e.takeOutput(idx, &info); err != nil {
				return err
			}
			if info.Flags&sys.BufferFlagEndOfStream != 0 {
				e.eosSeen = true
				return nil
			}
			timeoutUs = 0
		case idx == sys.InfoOutputFormatChanged:
			// The output format carries csd-0/csd-1 too, but the
			// codec-config buffer delivers the same bytes.
		case idx == sys.InfoOutputBuffersChanged:
		case idx == sys.InfoTryAgainLater:
			return nil
		default:
			return backendErr("AMediaCodec_dequeueOutputBuffer", int32(idx))
		}
	}
}

// takeOutput turns one output buffer into a packet (or stores the
// parameter sets it carries) and releases it.
func (e *encoder) takeOutput(idx int, info *sys.BufferInfo) error {
	defer sys.AMediaCodec_releaseOutputBuffer(e.codec, uintptr(idx), false)
	if info.Size <= 0 {
		return nil
	}
	var size uintptr
	p := sys.AMediaCodec_getOutputBuffer(e.codec, uintptr(idx), &size)
	if p == nil || int(size) < int(info.Offset)+int(info.Size) {
		return &codec.BackendError{Backend: Name, Op: "AMediaCodec_getOutputBuffer", Message: fmt.Sprintf("buffer of %d bytes, sample at %d+%d", size, info.Offset, info.Size)}
	}
	data := append([]byte(nil), unsafe.Slice(p, size)[info.Offset:info.Offset+info.Size]...)
	pinfo := e.ps.inspect(data)
	if info.Flags&sys.BufferFlagCodecConfig != 0 {
		// The parameter sets, delivered once ahead of the first keyframe
		// (and again after a change); inspect stored them.
		return nil
	}
	if pinfo.nals == 0 {
		return &codec.BackendError{Backend: Name, Op: "output buffer", Message: "an output buffer holds no NAL unit"}
	}
	keyframe := info.Flags&sys.BufferFlagKeyFrame != 0 || pinfo.keyframe
	if keyframe && !e.ps.covers(pinfo.kinds) && !e.ps.empty() {
		data = append(e.ps.annexB(), data...)
	}
	pts, ok := e.inflight[info.PresentationTimeUs]
	if ok {
		delete(e.inflight, info.PresentationTimeUs)
	} else {
		pts = fromMicros(info.PresentationTimeUs, e.cfg.TimeScale)
	}
	dts := pts
	if e.cfg.BFrames && !e.cfg.LowLatency && e.taken < len(e.sent) {
		// Packets come in decode order; with one B-frame of reordering
		// the decode time of the k-th packet is one frame before the
		// presentation time of the k-th frame given to the encoder.
		dts = e.sent[e.taken] - e.frameDur
		if dts > pts {
			dts = pts
		}
	}
	e.taken++
	e.pending = append(e.pending, codec.Packet{Data: data, PTS: pts, DTS: dts, Keyframe: keyframe})
	return nil
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
	if p, ok := e.popPending(); ok {
		return p, nil
	}
	if e.flushed {
		return codec.Packet{}, io.EOF
	}
	if err := e.drainReady(); err != nil {
		return codec.Packet{}, err
	}
	if p, ok := e.popPending(); ok {
		return p, nil
	}
	return codec.Packet{}, codec.ErrAgain
}

func (e *encoder) popPending() (codec.Packet, bool) {
	n := len(e.pending)
	if n == 0 {
		return codec.Packet{}, false
	}
	p := e.pending[0]
	copy(e.pending, e.pending[1:])
	e.pending[n-1] = codec.Packet{}
	e.pending = e.pending[:n-1]
	return p, true
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
	if !e.flushed {
		if !e.eosQueued {
			if err := queueEOS(ctx, e.codec, e.drainReady); err != nil {
				return err
			}
			e.eosQueued = true
		}
		deadline := time.Now().Add(eosTimeout)
		for !e.eosSeen {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := e.drain(int64(inputPoll / time.Microsecond)); err != nil {
				return err
			}
			if time.Now().After(deadline) {
				return &codec.BackendError{Backend: Name, Op: "AMediaCodec_dequeueOutputBuffer", Message: e.name + " did not deliver its end of stream"}
			}
		}
	}
	e.flushed = true
	e.forceNext = true
	e.frameIndex = 0
	return nil
}

func (e *encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.codec != 0 {
		sys.AMediaCodec_stop(e.codec)
		sys.AMediaCodec_delete(e.codec)
		e.codec = 0
	}
	e.pending = nil
	return nil
}
