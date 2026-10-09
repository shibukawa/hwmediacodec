//go:build darwin

package videotoolbox

import (
	"context"
	"encoding/binary"
	"io"
	"sync"

	"github.com/ebitengine/purego"

	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/videotoolbox/sys"
)

// Decoders are reached from the VideoToolbox output callback through a
// small integer handle, never through a Go pointer.
var (
	registryMu   sync.Mutex
	registry             = map[uintptr]*decoder{}
	nextHandle   uintptr = 1
	callbackOnce sync.Once
	callbackPtr  uintptr

	// VideoToolbox keeps global decoder registry state; serialize session
	// creation and teardown to stay clear of races in that state.
	sessionMu sync.Mutex
)

func outputCallback() uintptr {
	callbackOnce.Do(func() {
		callbackPtr = purego.NewCallback(onDecodedFrame)
	})
	return callbackPtr
}

// onDecodedFrame is the VTDecompressionOutputCallback. The two trailing CMTime
// parameters (presentation time and duration) are not declared: on arm64 they
// arrive as pointers and on amd64 on the stack, and in both cases ignoring
// them is safe. Timestamps are tracked by sourceFrameRefCon instead.
func onDecodedFrame(refCon, sourceRefCon uintptr, status int32, infoFlags uint32, imageBuffer uintptr) uintptr {
	registryMu.Lock()
	d := registry[refCon]
	registryMu.Unlock()
	if d == nil {
		return 0
	}
	d.outMu.Lock()
	defer d.outMu.Unlock()
	in := d.inflight[sourceRefCon]
	delete(d.inflight, sourceRefCon)
	if status != 0 {
		d.cbErr = &codec.BackendError{Backend: Name, Op: "decode callback", Status: int64(status), Message: sys.StatusString(status)}
		return 0
	}
	if imageBuffer == 0 {
		// Frame intentionally dropped (for example DoNotOutputFrame).
		return 0
	}
	sys.CFRetain(imageBuffer)
	d.pending = append(d.pending, pendingFrame{buf: imageBuffer, inflightFrame: in})
	return 0
}

// inflightFrame is what a decode call records about its input packet.
type inflightFrame struct {
	pts   int64
	order codec.Order
}

type pendingFrame struct {
	buf uintptr
	inflightFrame
}

type decoder struct {
	mu      sync.Mutex
	cfg     codec.DecoderConfig
	vtCodec uint32
	handle  uintptr

	session    uintptr
	formatDesc uintptr
	params     *paramSetStore // H.264 and HEVC parameter sets
	av1        av1State       // AV1 sequence header

	waitKeyframe bool
	flushed      bool
	closed       bool
	seq          uintptr
	// swizzle is set when the session emits BGRA although RGBA was
	// requested; the copy then exchanges the R and B bytes.
	swizzle bool

	// outMu guards state shared with the output callback.
	outMu    sync.Mutex
	inflight map[uintptr]inflightFrame
	pending  []pendingFrame
	cbErr    error

	pool sync.Pool
}

func newDecoder(cfg codec.DecoderConfig, vtCodec uint32) *decoder {
	d := &decoder{
		cfg:          cfg,
		vtCodec:      vtCodec,
		params:       newParamSetStore(cfg.Codec),
		waitKeyframe: true,
		inflight:     map[uintptr]inflightFrame{},
	}
	registryMu.Lock()
	d.handle = nextHandle
	nextHandle++
	registry[d.handle] = d
	registryMu.Unlock()
	return d
}

// OutputsDisplayOrder implements codec.DisplayOrderer. An AV1 temporal unit
// carries exactly one shown frame and the units come in presentation
// order, so VideoToolbox's output is already in display order. H.264 and
// HEVC frames come out in decode order and are reordered by the public
// API.
func (d *decoder) OutputsDisplayOrder() bool { return d.cfg.Codec == codec.AV1 }

func (d *decoder) Send(ctx context.Context, p codec.Packet) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.flushed = false
	if d.cfg.Codec == codec.AV1 {
		return d.sendAV1(p)
	}
	return d.sendAnnexB(p)
}

// sendAnnexB is Send for H.264 and HEVC: p.Data is one access unit in
// Annex-B form.
func (d *decoder) sendAnnexB(p codec.Packet) error {
	nals := annexb.Split(p.Data)
	if len(nals) == 0 {
		return codec.ErrInvalidData
	}

	var payload [][]byte
	hasVCL, hasKeyframe := false, false
	for _, nal := range nals {
		t := annexb.NALUnitType(d.cfg.Codec, nal)
		switch {
		case t < 0:
			continue
		case annexb.IsParameterSet(d.cfg.Codec, t):
			d.params.add(t, nal)
		case annexb.IsVCL(d.cfg.Codec, t):
			hasVCL = true
			if annexb.IsKeyframe(d.cfg.Codec, t) {
				hasKeyframe = true
			}
			payload = append(payload, nal)
		case annexb.IsSEI(d.cfg.Codec, t):
			payload = append(payload, nal)
		default:
			// AUD, end of sequence/bitstream, filler and reserved types are
			// not needed by VideoToolbox.
		}
	}

	if d.params.dirty && d.params.complete() {
		fd, st := d.params.formatDescription()
		if st != 0 || fd == 0 {
			return &codec.BackendError{Backend: Name, Op: "CMVideoFormatDescriptionCreateFromParameterSets", Status: int64(st), Message: sys.StatusString(st)}
		}
		if err := d.recreateSession(fd); err != nil {
			return err
		}
	}
	if !hasVCL {
		return nil
	}
	if d.session == 0 {
		// No parameter sets yet: skip until the stream provides them.
		return nil
	}
	if d.waitKeyframe {
		if !hasKeyframe {
			return nil
		}
		d.waitKeyframe = false
	}
	return d.decodePacket(lengthPrefixed(payload), inflightFrame{pts: p.PTS, order: codec.PacketOrder(p)})
}

// lengthPrefixed joins NAL units in the 4-byte length-prefixed layout
// (AVCC/HVCC) that a VideoToolbox sample buffer holds.
func lengthPrefixed(nals [][]byte) []byte {
	size := 0
	for _, n := range nals {
		size += 4 + len(n)
	}
	data := make([]byte, 0, size)
	for _, n := range nals {
		data = binary.BigEndian.AppendUint32(data, uint32(len(n)))
		data = append(data, n...)
	}
	return data
}

// decodePacket hands one coded picture (length-prefixed NAL units, or an
// AV1 temporal unit) to the session and collects its output.
func (d *decoder) decodePacket(data []byte, in inflightFrame) error {
	size := len(data)
	if size == 0 {
		return codec.ErrInvalidData
	}
	var block uintptr
	if st := sys.CMBlockBufferCreateWithMemoryBlock(0, 0, uintptr(size), 0, 0, 0, uintptr(size), 0, &block); st != 0 {
		return &codec.BackendError{Backend: Name, Op: "CMBlockBufferCreateWithMemoryBlock", Status: int64(st), Message: sys.StatusString(st)}
	}
	defer sys.Release(block)
	if st := sys.CMBlockBufferReplaceDataBytes(&data[0], block, 0, uintptr(size)); st != 0 {
		return &codec.BackendError{Backend: Name, Op: "CMBlockBufferReplaceDataBytes", Status: int64(st), Message: sys.StatusString(st)}
	}

	timing := sys.CMSampleTimingInfo{
		PresentationTimeStamp: sys.CMTime{Value: in.pts, Timescale: d.cfg.TimeScale, Flags: sys.CMTimeFlagsValid},
	}
	sampleSize := uintptr(size)
	var sample uintptr
	if st := sys.CMSampleBufferCreateReady(0, block, d.formatDesc, 1, 1, &timing, 1, &sampleSize, &sample); st != 0 {
		return &codec.BackendError{Backend: Name, Op: "CMSampleBufferCreateReady", Status: int64(st), Message: sys.StatusString(st)}
	}
	defer sys.Release(sample)

	d.seq++
	seq := d.seq
	d.outMu.Lock()
	d.inflight[seq] = in
	d.outMu.Unlock()

	var info uint32
	// Synchronous decode without temporal processing: the output callback
	// runs before this call returns and frames are emitted in decode order.
	// (Experiment 2026-10-09: kVTDecodeFrame_EnableTemporalProcessing, with
	// or without asynchronous decompression and with real presentation
	// timestamps, still produced decode order on an M3, so display-order
	// output has to be done in Go.)
	st := sys.VTDecompressionSessionDecodeFrame(d.session, sample, 0, seq, &info)
	if st != 0 {
		d.outMu.Lock()
		delete(d.inflight, seq)
		d.outMu.Unlock()
		return &codec.BackendError{Backend: Name, Op: "VTDecompressionSessionDecodeFrame", Status: int64(st), Message: sys.StatusString(st)}
	}
	d.outMu.Lock()
	err := d.cbErr
	d.cbErr = nil
	d.outMu.Unlock()
	return err
}

// recreateSession replaces the decompression session with one for the
// format description fd, whose reference it takes over.
func (d *decoder) recreateSession(fd uintptr) error {
	if d.session != 0 {
		d.drainSession()
		d.destroySession()
	}
	sys.Release(d.formatDesc)
	d.formatDesc = fd

	bufFormat := d.cfg.OutputFormat
	if bufFormat == codec.RGBA {
		// VideoToolbox accepts 'RGBA' as a destination format but the
		// decode then fails with kCVReturnInvalidPixelFormat (verified on
		// an M3, 2026-10-09); BGRA is always available and is swapped into
		// RGBA during the copy.
		bufFormat = codec.BGRA
		d.swizzle = true
	}
	pixfmt, _ := vtPixelFormat(bufFormat)
	session, st := d.createSession(fd, pixfmt)
	if st != 0 {
		switch st {
		case sys.StatusVTCouldNotFindVideoDecoder, sys.StatusVTVideoDecoderUnsupportedDataFmt, sys.StatusVTVideoDecoderNotAvailableNow:
			return &codec.UnsupportedError{Backend: Name, Codec: d.cfg.Codec, Direction: codec.Decode, Reason: sys.StatusString(st)}
		}
		return &codec.BackendError{Backend: Name, Op: "VTDecompressionSessionCreate", Status: int64(st), Message: sys.StatusString(st)}
	}
	d.session = session
	d.waitKeyframe = true
	return nil
}

// createSession creates a decompression session for the format description
// that emits pixel buffers of the given Core Video pixel format.
func (d *decoder) createSession(fd uintptr, pixfmt uint32) (uintptr, int32) {
	spec := sys.NewDictionary()
	if d.cfg.AllowSoftware {
		sys.CFDictionarySetValue(spec, sys.KVTEnableHardwareDecoder, sys.KCFBooleanTrue)
	} else {
		sys.CFDictionarySetValue(spec, sys.KVTRequireHardwareDecoder, sys.KCFBooleanTrue)
	}
	attrs := sys.NewDictionary()
	n := sys.CFNumberInt32(int32(pixfmt))
	sys.CFDictionarySetValue(attrs, sys.KCVPixelBufferPixelFormatTypeKey, n)

	record := sys.VTDecompressionOutputCallbackRecord{Callback: outputCallback(), RefCon: d.handle}
	var session uintptr
	sessionMu.Lock()
	st := sys.VTDecompressionSessionCreate(0, fd, spec, attrs, &record, &session)
	sessionMu.Unlock()
	sys.Release(n)
	sys.Release(attrs)
	sys.Release(spec)
	if st != 0 {
		return 0, st
	}
	return session, 0
}

func (d *decoder) drainSession() {
	if d.session == 0 {
		return
	}
	sys.VTDecompressionSessionFinishDelayedFrames(d.session)
	sys.VTDecompressionSessionWaitForAsynchronousFrames(d.session)
}

func (d *decoder) destroySession() {
	if d.session == 0 {
		return
	}
	sessionMu.Lock()
	sys.VTDecompressionSessionInvalidate(d.session)
	sessionMu.Unlock()
	sys.Release(d.session)
	d.session = 0
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
	d.outMu.Lock()
	if len(d.pending) == 0 {
		d.outMu.Unlock()
		if d.flushed {
			return nil, io.EOF
		}
		return nil, codec.ErrAgain
	}
	pf := d.pending[0]
	copy(d.pending, d.pending[1:])
	d.pending = d.pending[:len(d.pending)-1]
	d.outMu.Unlock()

	frame, err := d.copyFrame(pf.buf, pf.inflightFrame)
	sys.Release(pf.buf)
	return frame, err
}

func (d *decoder) copyFrame(pb uintptr, in inflightFrame) (*codec.Frame, error) {
	if st := sys.CVPixelBufferLockBaseAddress(pb, sys.PixelBufferLockReadOnly); st != 0 {
		return nil, &codec.BackendError{Backend: Name, Op: "CVPixelBufferLockBaseAddress", Status: int64(st)}
	}
	defer sys.CVPixelBufferUnlockBaseAddress(pb, sys.PixelBufferLockReadOnly)

	format := d.cfg.OutputFormat
	bufFormat := format
	if d.swizzle {
		bufFormat = codec.BGRA
	}
	planes, ok := bufferPlanes(pb, bufFormat)
	if !ok {
		return nil, &codec.BackendError{Backend: Name, Op: "copy frame", Status: int64(sys.CVPixelBufferGetPixelFormatType(pb)), Message: "unexpected pixel buffer layout from the decoder"}
	}
	width := int(sys.CVPixelBufferGetWidth(pb))
	height := int(sys.CVPixelBufferGetHeight(pb))

	total := 0
	for _, pl := range planes {
		total += pl.width * pl.rows
	}
	buf := d.getBuffer(total)
	off := 0
	out := make([][]byte, len(planes))
	strides := make([]int, len(planes))
	for i, pl := range planes {
		dst := buf[off : off+pl.width*pl.rows]
		copyRows(dst, pl.width, pl.bytes(), pl.stride, pl.rows, pl.width, d.swizzle)
		out[i] = dst
		strides[i] = pl.width
		off += pl.width * pl.rows
	}

	f := &codec.Frame{Width: width, Height: height, Format: format, Planes: out, Strides: strides, PTS: in.pts}
	codec.SetFrameOrder(f, in.order)
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
	d.drainSession()
	d.flushed = true
	d.waitKeyframe = true
	d.outMu.Lock()
	err := d.cbErr
	d.cbErr = nil
	d.outMu.Unlock()
	return err
}

func (d *decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.drainSession()
	d.destroySession()
	sys.Release(d.formatDesc)
	d.formatDesc = 0
	registryMu.Lock()
	delete(registry, d.handle)
	registryMu.Unlock()
	d.outMu.Lock()
	for _, pf := range d.pending {
		sys.Release(pf.buf)
	}
	d.pending = nil
	d.outMu.Unlock()
	return nil
}
