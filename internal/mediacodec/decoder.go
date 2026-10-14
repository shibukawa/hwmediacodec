//go:build android || mediacodec_typecheck

package mediacodec

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/mediacodec/sys"
)

// inflightFrame is what a decode call records about its input packet.
type inflightFrame struct {
	pts   int64
	order codec.Order
}

type decoder struct {
	mu   sync.Mutex
	cfg  codec.DecoderConfig
	mime string
	name string

	codec      uintptr // AMediaCodec, owned
	configured bool

	ps *paramSets
	// Picture size from the parameter sets: the display size that
	// MediaCodec is configured with and that frames are cropped to.
	dispW, dispH int
	// AV1: the current sequence header, parsed and as an OBU. A key frame
	// that restarts decoding without carrying one is prefixed with it.
	av1Seq    *av1.SequenceHeader
	av1SeqOBU []byte

	// Output buffer geometry from the output format.
	out      layout
	outKnown bool
	cropL    int
	cropT    int
	cropW    int
	cropH    int

	inflight map[int64]inflightFrame // presentationTimeUs -> packet
	pending  []*codec.Frame

	waitKeyframe bool
	eosQueued    bool
	eosSeen      bool
	flushed      bool
	closed       bool

	pool sync.Pool
}

// OutputsDisplayOrder implements codec.DisplayOrderer. An AV1 temporal
// unit yields exactly one shown frame and the units come in presentation
// order, so MediaCodec's AV1 output is display order. For H.264 and HEVC
// the answer is false: MediaCodec decoders order their output by the
// timestamps they are given (the Android 14 emulator's decoders returned
// B-frame streams in decode order when the packets carried decode-order
// timestamps), so the public API's reorder layer sorts the frames by
// picture order count, as on macOS, and WithDecodeOrder returns MediaCodec's
// own order.
func (d *decoder) OutputsDisplayOrder() bool { return d.cfg.Codec == codec.AV1 }

func newDecoder(cfg codec.DecoderConfig, mime, name string, c uintptr) *decoder {
	return &decoder{
		cfg:          cfg,
		mime:         mime,
		name:         name,
		codec:        c,
		ps:           newParamSets(cfg.Codec),
		inflight:     map[int64]inflightFrame{},
		waitKeyframe: true,
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
	if d.flushed {
		// A new stream after end of stream: MediaCodec must be flushed
		// before it takes input again.
		if err := d.restart(); err != nil {
			return err
		}
	}
	if len(d.pending)+len(d.inflight) >= maxPending {
		return codec.ErrAgain
	}

	data := p.Data
	if d.cfg.Codec == codec.AV1 {
		tu, err := av1.ParseTemporalUnit(p.Data, d.av1Seq)
		if err != nil {
			return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
		}
		if tu.Sequence != nil {
			if d.av1Seq == nil || tu.Sequence.MaxFrameWidth != d.av1Seq.MaxFrameWidth || tu.Sequence.MaxFrameHeight != d.av1Seq.MaxFrameHeight {
				if err := d.resize(ctx, tu.Sequence.MaxFrameWidth, tu.Sequence.MaxFrameHeight); err != nil {
					return err
				}
			}
			d.av1Seq = tu.Sequence
			d.av1SeqOBU = append(d.av1SeqOBU[:0], tu.SequenceHeader.Raw...)
		}
		if !tu.HasFrame {
			return nil
		}
		if d.waitKeyframe {
			if !tu.Keyframe || d.av1Seq == nil {
				return nil
			}
			if tu.SequenceHeader == nil {
				data = append(append([]byte{}, d.av1SeqOBU...), p.Data...)
			}
		}
	} else {
		info := d.ps.inspect(p.Data)
		if info.nals == 0 {
			return codec.ErrInvalidData
		}
		if info.kinds[annexb.ParamSPS] {
			w, h, err := d.pictureSize()
			if err != nil {
				return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
			}
			if err := d.resize(ctx, w, h); err != nil {
				return err
			}
		}
		if !info.vcl {
			return nil
		}
		if d.waitKeyframe {
			if !info.keyframe {
				return nil
			}
			if !d.ps.covers(info.kinds) {
				data = append(d.ps.annexB(), p.Data...)
			}
		}
	}
	if !d.configured {
		if d.dispW == 0 {
			// A keyframe without any parameter sets cannot be decoded;
			// wait for them.
			return nil
		}
		if err := d.configure(); err != nil {
			return err
		}
	}
	if err := d.queue(ctx, data, p.PTS, codec.PacketOrder(p)); err != nil {
		return err
	}
	d.waitKeyframe = false
	return nil
}

// pictureSize reads the display size from the newest SPS.
func (d *decoder) pictureSize() (int, int, error) {
	if d.ps.lastSPS == nil {
		return 0, 0, fmt.Errorf("no sequence parameter set")
	}
	switch d.cfg.Codec {
	case codec.H264:
		s, err := h264.ParseSPS(d.ps.lastSPS)
		if err != nil {
			return 0, 0, err
		}
		w := int(s.PicWidthInMbs) * 16
		h := int(s.PicHeightInMapUnits) * 16
		if !s.FrameMbsOnly {
			h *= 2
		}
		if s.FrameCropping {
			// 4:2:0 pictures: crop units are 2 luma samples horizontally
			// and 2 (frame) or 4 (field) vertically.
			cy := 2
			if !s.FrameMbsOnly {
				cy = 4
			}
			w -= int(s.CropLeft+s.CropRight) * 2
			h -= int(s.CropTop+s.CropBottom) * cy
		}
		return w, h, nil
	case codec.HEVC:
		s, err := hevc.ParseSPS(d.ps.lastSPS)
		if err != nil {
			return 0, 0, err
		}
		w, h := s.Width, s.Height
		if s.ConformanceWindow {
			// 4:2:0: the window offsets are in units of 2 luma samples.
			w -= int(s.ConfWinLeft+s.ConfWinRight) * 2
			h -= int(s.ConfWinTop+s.ConfWinBottom) * 2
		}
		return w, h, nil
	}
	return 0, 0, fmt.Errorf("no parameter sets for %s", d.cfg.Codec)
}

// resize records a new picture size. A configured decoder is run to the
// end of its stream and reconfigured with the new size, since MediaCodec
// was told the old one; frames still inside it are delivered first.
func (d *decoder) resize(ctx context.Context, w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("%w: picture size %dx%d", codec.ErrInvalidData, w, h)
	}
	if w == d.dispW && h == d.dispH {
		return nil
	}
	if d.configured {
		if !d.flushed {
			if err := d.finish(ctx); err != nil {
				return err
			}
		}
		if st := sys.AMediaCodec_stop(d.codec); st != sys.OK {
			return backendErr("AMediaCodec_stop", st)
		}
		d.configured = false
		d.outKnown = false
		d.flushed = false
		d.eosQueued, d.eosSeen = false, false
		d.waitKeyframe = true
	}
	d.dispW, d.dispH = w, h
	return nil
}

// configure configures and starts MediaCodec for the current picture size,
// with flexible YUV 4:2:0 output into byte buffers and the parameter sets
// as codec-specific data (they are also sent in-band).
func (d *decoder) configure() error {
	f := sys.AMediaFormat_new()
	if f == 0 {
		return backendErr("AMediaFormat_new", sys.ErrorUnknown)
	}
	defer sys.AMediaFormat_delete(f)
	sys.AMediaFormat_setString(f, sys.KeyMime, d.mime)
	sys.AMediaFormat_setInt32(f, sys.KeyWidth, int32(d.dispW))
	sys.AMediaFormat_setInt32(f, sys.KeyHeight, int32(d.dispH))
	sys.AMediaFormat_setInt32(f, sys.KeyColorFormat, sys.ColorFormatYUV420Flexible)
	switch d.cfg.Codec {
	case codec.H264:
		sys.SetBuffer(f, sys.KeyCSD0, d.ps.annexBOf(annexb.ParamSPS))
		sys.SetBuffer(f, sys.KeyCSD1, d.ps.annexBOf(annexb.ParamPPS))
	case codec.HEVC:
		sys.SetBuffer(f, sys.KeyCSD0, d.ps.annexB())
	}
	if st := sys.AMediaCodec_configure(d.codec, f, 0, 0, 0); st != sys.OK {
		if st == sys.ErrorUnsupported || st == sys.ErrorInvalidParameter {
			return unsupported(d.cfg.Codec, codec.Decode, fmt.Sprintf("%s rejected %dx%d %s (%s)", d.name, d.dispW, d.dispH, d.mime, sys.StatusString(st)))
		}
		return backendErr("AMediaCodec_configure", st)
	}
	if st := sys.AMediaCodec_start(d.codec); st != sys.OK {
		return backendErr("AMediaCodec_start", st)
	}
	d.configured = true
	d.outKnown = false
	return nil
}

// restart makes a codec that has delivered its end of stream take a new
// stream.
func (d *decoder) restart() error {
	if d.configured {
		if st := sys.AMediaCodec_flush(d.codec); st != sys.OK {
			return backendErr("AMediaCodec_flush", st)
		}
	}
	d.flushed = false
	d.eosQueued, d.eosSeen = false, false
	d.waitKeyframe = true
	clear(d.inflight)
	return nil
}

// queue hands one access unit to MediaCodec and collects any output that is
// ready.
func (d *decoder) queue(ctx context.Context, data []byte, pts int64, order codec.Order) error {
	if len(data) == 0 {
		return codec.ErrInvalidData
	}
	idx, err := dequeueInput(ctx, d.codec, d.drainReady)
	if err != nil {
		return err
	}
	var size uintptr
	buf := sys.AMediaCodec_getInputBuffer(d.codec, uintptr(idx), &size)
	if buf == nil || int(size) < len(data) {
		return &codec.BackendError{Backend: Name, Op: "AMediaCodec_getInputBuffer", Message: fmt.Sprintf("input buffer of %d bytes cannot take a %d byte access unit", size, len(data))}
	}
	copy(unsafe.Slice(buf, size), data)
	// Timestamps identify frames on the way back; keep them unique.
	us := toMicros(pts, d.cfg.TimeScale)
	for {
		if _, taken := d.inflight[us]; !taken {
			break
		}
		us++
	}
	if st := sys.AMediaCodec_queueInputBuffer(d.codec, uintptr(idx), 0, uintptr(len(data)), uint64(us), 0); st != sys.OK {
		return backendErr("AMediaCodec_queueInputBuffer", st)
	}
	d.inflight[us] = inflightFrame{pts: pts, order: order}
	return d.drainReady()
}

// drainReady moves every frame MediaCodec has finished into pending without
// waiting.
func (d *decoder) drainReady() error { return d.drain(0) }

// drain dequeues output until none is ready, waiting up to timeoutUs for
// the first one.
func (d *decoder) drain(timeoutUs int64) error {
	for {
		var info sys.BufferInfo
		idx := sys.AMediaCodec_dequeueOutputBuffer(d.codec, &info, timeoutUs)
		switch {
		case idx >= 0:
			if err := d.takeOutput(idx, &info); err != nil {
				return err
			}
			if info.Flags&sys.BufferFlagEndOfStream != 0 {
				d.eosSeen = true
				return nil
			}
			timeoutUs = 0
		case idx == sys.InfoOutputFormatChanged:
			if err := d.readOutputFormat(); err != nil {
				return err
			}
		case idx == sys.InfoOutputBuffersChanged:
			// Buffers are fetched by index on every use.
		case idx == sys.InfoTryAgainLater:
			return nil
		default:
			return backendErr("AMediaCodec_dequeueOutputBuffer", int32(idx))
		}
	}
}

// takeOutput copies one output buffer into a frame and releases it.
func (d *decoder) takeOutput(idx int, info *sys.BufferInfo) error {
	defer sys.AMediaCodec_releaseOutputBuffer(d.codec, uintptr(idx), false)
	if info.Size <= 0 {
		return nil // the end-of-stream marker, or nothing
	}
	if !d.outKnown {
		if err := d.readOutputFormat(); err != nil {
			return err
		}
	}
	var size uintptr
	p := sys.AMediaCodec_getOutputBuffer(d.codec, uintptr(idx), &size)
	if p == nil {
		return &codec.BackendError{Backend: Name, Op: "AMediaCodec_getOutputBuffer", Message: "no buffer"}
	}
	start, end, ok := outputRange(size, info.Offset, info.Size)
	if !ok {
		return &codec.BackendError{Backend: Name, Op: "AMediaCodec_getOutputBuffer", Message: fmt.Sprintf("buffer of %d bytes, sample at %d+%d", size, info.Offset, info.Size)}
	}
	buf := unsafe.Slice(p, end)[start:end]
	if need := d.out.size(); len(buf) < need {
		return &codec.BackendError{Backend: Name, Op: "output buffer", Message: fmt.Sprintf("%d bytes, the %dx%d layout needs %d", len(buf), d.out.width, d.out.height, need)}
	}
	w, h := d.cropW, d.cropH
	uvStride := (w + 1) / 2 * 2
	ySize, uvSize := w*h, uvStride*((h+1)/2)
	mem := d.getBuffer(ySize + uvSize)
	y, uv := mem[:ySize], mem[ySize:]
	d.out.packNV12(y, uv, buf, d.cropL, d.cropT, w, h)

	in, ok := d.inflight[info.PresentationTimeUs]
	if ok {
		delete(d.inflight, info.PresentationTimeUs)
	} else {
		in.pts = fromMicros(info.PresentationTimeUs, d.cfg.TimeScale)
	}
	f := &codec.Frame{Width: w, Height: h, Format: codec.NV12, Planes: [][]byte{y, uv}, Strides: []int{w, uvStride}, PTS: in.pts}
	codec.SetFrameOrder(f, in.order)
	codec.SetRelease(f, func() { d.putBuffer(mem) })
	d.pending = append(d.pending, f)
	return nil
}

// readOutputFormat reads the plane layout and the crop rectangle of the
// output buffers from the codec's output format.
func (d *decoder) readOutputFormat() error {
	f := sys.AMediaCodec_getOutputFormat(d.codec)
	if f == 0 {
		return backendErr("AMediaCodec_getOutputFormat", sys.ErrorUnknown)
	}
	defer sys.AMediaFormat_delete(f)
	bw, _ := sys.FormatInt32(f, sys.KeyWidth)
	bh, _ := sys.FormatInt32(f, sys.KeyHeight)
	var l layout
	var err error
	if img := sys.FormatBuffer(f, sys.KeyImageData); img != nil {
		l, err = parseMediaImage2(img)
	} else {
		cf, _ := sys.FormatInt32(f, sys.KeyColorFormat)
		stride, _ := sys.FormatInt32(f, sys.KeyStride)
		sliceH, _ := sys.FormatInt32(f, sys.KeySliceHeight)
		l, err = layoutFromColorFormat(cf, int(bw), int(bh), int(stride), int(sliceH))
	}
	if err != nil {
		return unsupported(d.cfg.Codec, codec.Decode, fmt.Sprintf("%s output layout not understood (%v): %s", d.name, err, sys.FormatString(f)))
	}
	d.out = l

	// The visible picture inside the buffer: the crop rectangle when the
	// format carries one that fits the buffer, else the whole buffer; and
	// never larger than the stream's own picture size.
	cl, ct, cw, ch := 0, 0, l.width, l.height
	var left, top, right, bottom int32
	if sys.AMediaFormat_getRect(f, sys.KeyCrop, &left, &top, &right, &bottom) {
		cl, ct, cw, ch = int(left), int(top), int(right-left+1), int(bottom-top+1)
	} else if l2, ok := sys.FormatInt32(f, sys.KeyCropLeft); ok {
		t2, _ := sys.FormatInt32(f, sys.KeyCropTop)
		r2, _ := sys.FormatInt32(f, sys.KeyCropRight)
		b2, _ := sys.FormatInt32(f, sys.KeyCropBottom)
		cl, ct, cw, ch = int(l2), int(t2), int(r2-l2+1), int(b2-t2+1)
	}
	if cl < 0 || ct < 0 || cw <= 0 || ch <= 0 || cl+cw > l.width || ct+ch > l.height {
		cl, ct, cw, ch = 0, 0, l.width, l.height
	}
	if d.dispW > 0 && cw > d.dispW {
		cw = d.dispW
	}
	if d.dispH > 0 && ch > d.dispH {
		ch = d.dispH
	}
	d.cropL, d.cropT, d.cropW, d.cropH = cl, ct, cw, ch
	d.outKnown = true
	return nil
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

func (d *decoder) Receive(ctx context.Context) (*codec.Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f := d.popPending(); f != nil {
		return f, nil
	}
	if d.flushed {
		return nil, io.EOF
	}
	if !d.configured {
		return nil, codec.ErrAgain
	}
	if err := d.drainReady(); err != nil {
		return nil, err
	}
	if f := d.popPending(); f != nil {
		return f, nil
	}
	return nil, codec.ErrAgain
}

func (d *decoder) popPending() *codec.Frame {
	n := len(d.pending)
	if n == 0 {
		return nil
	}
	f := d.pending[0]
	copy(d.pending, d.pending[1:])
	d.pending[n-1] = nil
	d.pending = d.pending[:n-1]
	return f
}

// finish sends the end-of-stream marker and collects every remaining frame
// into pending.
func (d *decoder) finish(ctx context.Context) error {
	if !d.eosQueued {
		if err := queueEOS(ctx, d.codec, d.drainReady); err != nil {
			return err
		}
		d.eosQueued = true
	}
	deadline := time.Now().Add(eosTimeout)
	for !d.eosSeen {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.drain(int64(inputPoll / time.Microsecond)); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return &codec.BackendError{Backend: Name, Op: "AMediaCodec_dequeueOutputBuffer", Message: d.name + " did not deliver its end of stream"}
		}
	}
	clear(d.inflight)
	return nil
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
	if d.configured && !d.flushed {
		if err := d.finish(ctx); err != nil {
			return err
		}
	}
	d.flushed = true
	d.waitKeyframe = true
	return nil
}

func (d *decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if d.codec != 0 {
		if d.configured {
			sys.AMediaCodec_stop(d.codec)
		}
		sys.AMediaCodec_delete(d.codec)
		d.codec = 0
	}
	d.pending = nil
	return nil
}
