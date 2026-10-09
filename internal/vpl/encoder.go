//go:build linux

package vpl

import (
	"context"
	"fmt"
	"io"
	"math"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vpl/sys"
)

const (
	// maxPendingPackets bounds the packets held for Receive; Send reports
	// ErrAgain beyond it.
	maxPendingPackets = 64
	// maxBitstreamBuffer bounds the growth of the output buffer after
	// MFX_ERR_NOT_ENOUGH_BUFFER.
	maxBitstreamBuffer = 256 << 20
	// paramSetBufferSize holds one parameter set returned by
	// MFXVideoENCODE_GetVideoParam.
	paramSetBufferSize = 1024
	// keptControls is the number of per-frame controls kept alive for the
	// frames the encoder may still be holding.
	keptControls = 64
)

// encoder drives MFXVideoENCODE with system-memory input surfaces. Every
// submitted frame is synchronised at once, so packets are produced inside
// Send (or Flush); with B-frames the runtime holds frames back and Send
// produces nothing until a reference picture arrives.
type encoder struct {
	mu      sync.Mutex
	cfg     codec.EncoderConfig
	ses     *session
	params  encodeParams
	running bool // MFXVideoENCODE is initialised

	surfaces []*surface
	bs       *sys.Bitstream
	bsBuf    []byte
	controls []*sys.EncodeCtrl

	inputPTS []int64 // PTS of submitted frames in input order (for DTS)
	pending  []codec.Packet

	paramSets     []byte // Annex-B VPS/SPS/PPS of the encoder
	reorder       int    // frames a picture may be held back (GopRefDist - 1)
	frameDuration int64  // in TimeScale units
	frameOrder    uint32
	forceNext     bool
	needReset     bool
	flushed       bool
	closed        bool
}

func newEncoder(cfg codec.EncoderConfig, ses *session) (*encoder, error) {
	e := &encoder{cfg: cfg, ses: ses, forceNext: true}
	if err := e.params.fill(cfg); err != nil {
		return nil, err
	}
	if cfg.FrameRate > 0 {
		e.frameDuration = int64(math.Round(float64(cfg.TimeScale) / cfg.FrameRate))
	} else {
		e.frameDuration = int64(cfg.TimeScale) / 30
	}
	if err := e.start(); err != nil {
		return nil, err
	}
	return e, nil
}

// start initialises MFXVideoENCODE and reads back what the runtime made of
// the configuration.
func (e *encoder) start() error {
	c := e.cfg.Codec
	par := &e.params.par
	st := sys.EncodeInit(e.ses.ses, par)
	for (st == sys.ErrUnsupported || st == sys.ErrInvalidVideoParam) && e.params.reduce() {
		// A runtime that does not accept a coding-option buffer for this
		// codec or GPU: retry with fewer of them, down to the plain
		// parameters.
		sys.EncodeClose(e.ses.ses)
		st = sys.EncodeInit(e.ses.ses, par)
	}
	switch {
	case st == sys.WrnPartialAcceleration && !e.cfg.AllowSoftware:
		sys.EncodeClose(e.ses.ses)
		return unsupportedEncode(c, "the GPU has no hardware encoder for this configuration")
	case st == sys.ErrUnsupported || st == sys.ErrInvalidVideoParam || st == sys.ErrIncompatibleVideoParam:
		return unsupportedEncode(c, fmt.Sprintf("MFXVideoENCODE_Init rejected the configuration (%s): %s", e.ses.describe(), sys.StatusString(st)))
	case st < 0:
		return mfxError("MFXVideoENCODE_Init", st)
	}
	e.running = true

	got, err := e.queryState()
	if err != nil {
		e.stop()
		return err
	}
	if want := par.MFX.RateControlMethod; got.RateControlMethod != want {
		e.stop()
		return unsupportedEncode(c, fmt.Sprintf("the GPU runtime replaced rate control method %d with %d", want, got.RateControlMethod))
	}
	e.reorder = 0
	if got.GopRefDist > 1 {
		e.reorder = int(got.GopRefDist) - 1
	}
	if n := bitstreamCapacity(got); n > len(e.bsBuf) {
		e.allocBitstream(n)
	}
	return nil
}

func (e *encoder) stop() {
	if e.running {
		sys.EncodeClose(e.ses.ses)
		e.running = false
	}
	e.surfaces = nil
}

func (e *encoder) allocBitstream(n int) {
	e.bsBuf = make([]byte, n)
	e.bs = &sys.Bitstream{Data: uintptr(unsafe.Pointer(&e.bsBuf[0])), MaxLength: uint32(n)}
}

// queryState reads the parameters the encoder actually runs with and its
// parameter sets, which are prepended to keyframes should the stream lack
// them.
func (e *encoder) queryState() (*sys.InfoMFX, error) {
	sps := make([]byte, paramSetBufferSize)
	pps := make([]byte, paramSetBufferSize)
	vps := make([]byte, paramSetBufferSize)
	sp := sys.ExtCodingOptionSPSPPS{
		Header:    sys.ExtBuffer{BufferID: sys.ExtBuffCodingOptionSPSPPS, BufferSz: uint32(unsafe.Sizeof(sys.ExtCodingOptionSPSPPS{}))},
		SPSBuffer: unsafe.Pointer(&sps[0]), SPSBufSize: paramSetBufferSize,
		PPSBuffer: unsafe.Pointer(&pps[0]), PPSBufSize: paramSetBufferSize,
	}
	vp := sys.ExtCodingOptionVPS{
		Header:    sys.ExtBuffer{BufferID: sys.ExtBuffCodingOptionVPS, BufferSz: uint32(unsafe.Sizeof(sys.ExtCodingOptionVPS{}))},
		VPSBuffer: unsafe.Pointer(&vps[0]), VPSBufSize: paramSetBufferSize,
	}
	ext := []unsafe.Pointer{unsafe.Pointer(&sp)}
	if e.cfg.Codec == codec.HEVC {
		ext = append(ext, unsafe.Pointer(&vp))
	}
	var got sys.VideoParam
	got.MFX.CodecID = e.params.par.MFX.CodecID
	got.ExtParam = unsafe.Pointer(&ext[0])
	got.NumExtParam = uint16(len(ext))
	if st := sys.EncodeGetVideoParam(e.ses.ses, &got); st >= 0 {
		var ps []byte
		if e.cfg.Codec == codec.HEVC {
			ps = append(ps, withStartCode(vps[:min(int(vp.VPSBufSize), len(vps))])...)
		}
		ps = append(ps, withStartCode(sps[:min(int(sp.SPSBufSize), len(sps))])...)
		ps = append(ps, withStartCode(pps[:min(int(sp.PPSBufSize), len(pps))])...)
		e.paramSets = ps
		return &got.MFX, nil
	}
	// The parameter-set buffers are optional; the plain query is not.
	got = sys.VideoParam{}
	got.MFX.CodecID = e.params.par.MFX.CodecID
	if st := sys.EncodeGetVideoParam(e.ses.ses, &got); st < 0 {
		return nil, mfxError("MFXVideoENCODE_GetVideoParam", st)
	}
	e.paramSets = nil
	return &got.MFX, nil
}

// inputSurface returns a surface the encoder does not hold, creating one
// when all are in use (the encoder keeps B-frames and references).
func (e *encoder) inputSurface() (*surface, error) {
	for _, s := range e.surfaces {
		if !s.locked() {
			return s, nil
		}
	}
	if len(e.surfaces) >= maxSurfaces {
		return nil, &codec.BackendError{Backend: Name, Op: "encode", Message: "every input surface is still held by the encoder"}
	}
	s := newSurface(e.params.par.MFX.FrameInfo)
	e.surfaces = append(e.surfaces, s)
	return s, nil
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

// restart prepares the encoder for a new sequence after Flush. A drained
// encoder is closed and initialised again: MFXVideoENCODE_Reset leaves it to
// the runtime whether a new coded sequence starts.
func (e *encoder) restart() error {
	e.frameOrder = 0
	e.stop()
	if err := e.start(); err != nil {
		return err
	}
	e.needReset = false
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
	if e.needReset || !e.running {
		if err := e.restart(); err != nil {
			return err
		}
	}
	s, err := e.inputSurface()
	if err != nil {
		return err
	}
	s.upload(f.Planes[0], f.Strides[0], f.Planes[1], f.Strides[1], f.Width, f.Height)
	s.s.Data.TimeStamp = toStamp(f.PTS)
	s.s.Data.FrameOrder = e.frameOrder

	var ctrl *sys.EncodeCtrl
	if e.forceNext || f.ForceKeyframe {
		ctrl = &sys.EncodeCtrl{FrameType: sys.FrameTypeI | sys.FrameTypeRef | sys.FrameTypeIDR}
		// The encoder may consult the control until the frame is coded.
		if len(e.controls) >= keptControls {
			e.controls = e.controls[1:]
		}
		e.controls = append(e.controls, ctrl)
	}
	// The access unit this call completes is taken from the queue inside
	// submit, so the frame has to be in it first.
	e.inputPTS = append(e.inputPTS, f.PTS)
	accepted, err := e.submit(ctrl, s)
	if !accepted {
		e.inputPTS = e.inputPTS[:len(e.inputPTS)-1]
		return err
	}
	e.frameOrder++
	e.forceNext = false
	e.flushed = false
	return err
}

// submit calls MFXVideoENCODE_EncodeFrameAsync for one input surface, or
// for none to drain, and collects the access unit it may complete.
// accepted reports whether the runtime took the frame or, when draining,
// whether it may hold more access units.
func (e *encoder) submit(ctrl *sys.EncodeCtrl, s *surface) (accepted bool, err error) {
	const op = "MFXVideoENCODE_EncodeFrameAsync"
	var in *sys.FrameSurface
	if s != nil {
		in = s.s
	}
	busy := 0
	for {
		var syncPoint uintptr
		st := sys.EncodeFrameAsync(e.ses.ses, ctrl, in, e.bs, &syncPoint)
		switch {
		case st == sys.WrnDeviceBusy || st == sys.WrnInExecution:
			if !busyWait(&busy) {
				return false, mfxError(op, st)
			}
			continue
		case st == sys.ErrNotEnoughBuffer:
			if 2*len(e.bsBuf) > maxBitstreamBuffer {
				return false, mfxError(op, st)
			}
			e.allocBitstream(2 * len(e.bsBuf))
			continue
		case st == sys.ErrMoreData:
			// With a frame: taken and held back. Without: drained.
			return s != nil, nil
		case st < 0:
			return false, mfxError(op, st)
		}
		if syncPoint == 0 {
			// Any other warning: the frame was taken, nothing came out.
			return true, nil
		}
		return true, e.collect(syncPoint)
	}
}

// collect waits for the access unit of a sync point and queues it.
func (e *encoder) collect(syncPoint uintptr) error {
	if err := e.ses.sync("MFXVideoCORE_SyncOperation(encode)", syncPoint); err != nil {
		return err
	}
	bs := e.bs
	off, n := int(bs.DataOffset), int(bs.DataLength)
	if n == 0 || off+n > len(e.bsBuf) {
		bs.DataOffset, bs.DataLength = 0, 0
		return &codec.BackendError{Backend: Name, Op: "MFXVideoENCODE_EncodeFrameAsync", Message: "the encoder produced no data for the picture"}
	}
	data := append([]byte(nil), e.bsBuf[off:off+n]...)
	bs.DataOffset, bs.DataLength = 0, 0
	pts, ok := fromStamp(bs.TimeStamp)
	if !ok && len(e.inputPTS) > 0 {
		pts = e.inputPTS[0]
	}
	dts := pts
	if len(e.inputPTS) > 0 {
		// Packets come in decode order; the k-th one may be decoded no
		// earlier than the k-th input frame minus the reorder depth,
		// which is what a muxer needs for monotonic DTS.
		dts = e.inputPTS[0] - int64(e.reorder)*e.frameDuration
		e.inputPTS = e.inputPTS[1:]
	}
	e.pending = append(e.pending, e.packet(data, pts, dts))
	return nil
}

// packet wraps coded data as an Annex-B access unit, adding the parameter
// sets in front of keyframes when the encoder did not.
func (e *encoder) packet(data []byte, pts, dts int64) codec.Packet {
	c := e.cfg.Codec
	sps := annexb.H264NALSPS
	if c == codec.HEVC {
		sps = annexb.HEVCNALSPS
	}
	keyframe, hasPS := false, false
	for _, nal := range annexb.Split(data) {
		t := annexb.NALUnitType(c, nal)
		if annexb.IsVCL(c, t) && annexb.IsKeyframe(c, t) {
			keyframe = true
		}
		if t == sps {
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

// Flush drains the frames the encoder holds back and readies it for reuse
// at a keyframe.
func (e *encoder) Flush(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// One access unit comes out per call; the bound only guards against a
	// runtime that keeps answering with warnings.
	var err error
	for calls := 2*len(e.inputPTS) + 2; e.running && len(e.inputPTS) > 0 && calls > 0; calls-- {
		var more bool
		if more, err = e.submit(nil, nil); err != nil || !more {
			break
		}
		if err = ctx.Err(); err != nil {
			break
		}
	}
	e.flushed = true
	e.forceNext = true
	e.needReset = true
	e.inputPTS = e.inputPTS[:0]
	e.controls = nil
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
	e.stop()
	e.ses.close()
	return nil
}
