//go:build linux

package vpl

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vpl/sys"
)

// maxStalledCalls bounds consecutive MFXVideoDECODE_DecodeFrameAsync calls
// that neither consume input nor produce output.
const maxStalledCalls = 16

// decoder drives MFXVideoDECODE with system-memory output surfaces. The
// runtime decodes on the GPU and copies each finished picture into one of
// the surfaces; the picture is copied once more into a pooled frame buffer
// when its sync point completes, so no surface outlives a Send or Flush
// call on the caller's side.
type decoder struct {
	mu      sync.Mutex
	cfg     codec.DecoderConfig
	ses     *session
	codecID uint32
	ps      *paramSets

	inited      bool
	progressive bool           // the sequence has no field pictures
	par         sys.VideoParam // as passed to MFXVideoDECODE_Init
	surfaces    []*surface
	byAddr      map[uintptr]*surface
	bs          sys.Bitstream

	pending []*codec.Frame
	pool    sync.Pool

	// AV1: the current sequence header, parsed and as an OBU. A key frame
	// that restarts decoding without carrying one is prefixed with it.
	av1Seq    *av1.SequenceHeader
	av1SeqOBU []byte

	waitKeyframe bool
	flushed      bool
	closed       bool
}

func newDecoder(cfg codec.DecoderConfig, ses *session, id uint32) *decoder {
	return &decoder{cfg: cfg, ses: ses, codecID: id, ps: newParamSets(cfg.Codec), byAddr: map[uintptr]*surface{}, waitKeyframe: true}
}

// OutputsDisplayOrder implements codec.DisplayOrderer: the runtime reorders
// pictures itself, so the public API does not wrap the decoder in its own
// reorder layer.
func (d *decoder) OutputsDisplayOrder() bool { return true }

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

	data := p.Data
	if d.cfg.Codec == codec.AV1 {
		// p.Data is one temporal unit in the low-overhead OBU format,
		// which is what the runtime's AV1 decoder takes.
		tu, err := av1.ParseTemporalUnit(p.Data, d.av1Seq)
		if err != nil {
			return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
		}
		if tu.Sequence != nil {
			d.av1Seq = tu.Sequence
			d.av1SeqOBU = append(d.av1SeqOBU[:0], tu.SequenceHeader.Raw...)
		}
		if !tu.HasFrame {
			return nil // a sequence header alone was stored above
		}
		if d.waitKeyframe {
			// Decoding (re)starts at a shown key frame.
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
		if d.waitKeyframe {
			// Decoding (re)starts at a random access point. Parameter
			// sets of skipped packets were stored by inspect.
			if !info.keyframe {
				return nil
			}
			if !d.ps.covers(info.kinds) {
				data = append(d.ps.annexB(), p.Data...)
			}
		}
	}
	if err := d.decode(ctx, data, p.PTS); err != nil {
		if !d.inited {
			// Nothing can be decoded until the next sequence starts.
			d.waitKeyframe = true
		}
		return err
	}
	d.waitKeyframe = false
	return nil
}

// decode feeds one access unit, initialising the decoder from it first when
// necessary.
func (d *decoder) decode(ctx context.Context, data []byte, pts int64) error {
	defer runtime.KeepAlive(data)
	d.bs = sys.Bitstream{
		Data:       uintptr(unsafe.Pointer(&data[0])),
		DataLength: uint32(len(data)),
		MaxLength:  uint32(len(data)),
		TimeStamp:  toStamp(pts),
	}
	if !d.inited {
		if err := d.init(); err != nil {
			return err
		}
	}
	d.bs.DataFlag = d.dataFlag()
	return d.run(ctx, &d.bs, true)
}

// dataFlag tells the decoder that a Packet is one complete picture, so that
// it need not wait for the next access unit to know this one has ended. A
// field-coded stream delivers a frame in two access units, where the flag
// would be wrong.
func (d *decoder) dataFlag() uint16 {
	if d.progressive {
		return sys.BitstreamCompleteFrame
	}
	return 0
}

// init reads the sequence header at the start of d.bs and initialises the
// decoder for it.
func (d *decoder) init() error {
	c := d.cfg.Codec
	var par sys.VideoParam
	par.MFX.CodecID = d.codecID
	switch st := sys.DecodeHeader(d.ses.ses, &d.bs, &par); {
	case st == sys.ErrMoreData:
		return fmt.Errorf("%w: keyframe without usable parameter sets", codec.ErrInvalidData)
	case st == sys.ErrUnsupported:
		return unsupported(c, "the GPU runtime cannot decode this stream ("+d.ses.describe()+")")
	case st < 0:
		return mfxError("MFXVideoDECODE_DecodeHeader", st)
	}
	fi := &par.MFX.FrameInfo
	if fi.FourCC != sys.FourCCNV12 || fi.ChromaFormat != sys.ChromaFormatYUV420 || fi.BitDepthLuma > 8 || fi.BitDepthChroma > 8 {
		return unsupported(c, fmt.Sprintf("only 8-bit 4:2:0 streams are supported (chroma format %d, %d-bit)", fi.ChromaFormat, max(fi.BitDepthLuma, 8)))
	}
	if fi.Width == 0 || fi.Height == 0 {
		return fmt.Errorf("%w: sequence header without a picture size", codec.ErrInvalidData)
	}
	par.IOPattern = sys.IOPatternOutSystemMem
	par.AsyncDepth = 1 // every picture is synchronised as soon as it is output

	var req sys.FrameAllocRequest
	if st := sys.DecodeQueryIOSurf(d.ses.ses, &par, &req); st < 0 {
		if st == sys.ErrUnsupported || st == sys.ErrInvalidVideoParam {
			return unsupported(c, fmt.Sprintf("the GPU runtime rejects the stream (%dx%d, profile %d): %s", fi.CropW, fi.CropH, par.MFX.CodecProfile, sys.StatusString(st)))
		}
		return mfxError("MFXVideoDECODE_QueryIOSurf", st)
	}
	switch st := sys.DecodeInit(d.ses.ses, &par); {
	case st == sys.WrnPartialAcceleration && !d.cfg.AllowSoftware:
		sys.DecodeClose(d.ses.ses)
		return unsupported(c, "the GPU has no hardware decoder for this stream")
	case st == sys.ErrUnsupported || st == sys.ErrInvalidVideoParam:
		return unsupported(c, fmt.Sprintf("the GPU runtime rejects the stream (%dx%d, profile %d): %s", fi.CropW, fi.CropH, par.MFX.CodecProfile, sys.StatusString(st)))
	case st < 0:
		return mfxError("MFXVideoDECODE_Init", st)
	}
	d.par = par
	d.inited = true
	d.progressive = fi.PicStruct == sys.PicStructProgressive
	for i := 0; i < int(req.NumFrameSuggested); i++ {
		d.addSurface()
	}
	return nil
}

func (d *decoder) addSurface() *surface {
	s := newSurface(d.par.MFX.FrameInfo)
	d.surfaces = append(d.surfaces, s)
	d.byAddr[s.addr()] = s
	return s
}

// workSurface returns a surface the runtime does not hold, growing the pool
// when the runtime keeps more pictures than it announced.
func (d *decoder) workSurface() (*surface, error) {
	for _, s := range d.surfaces {
		if !s.locked() {
			return s, nil
		}
	}
	if len(d.surfaces) >= maxSurfaces {
		return nil, &codec.BackendError{Backend: Name, Op: "decode", Message: "every surface is still held by the decoder"}
	}
	return d.addSurface(), nil
}

// shutdown closes the MFX decoder and drops its surfaces; the next keyframe
// initialises a new one.
func (d *decoder) shutdown() {
	if d.inited {
		sys.DecodeClose(d.ses.ses)
		d.inited = false
	}
	d.surfaces = nil
	d.byAddr = map[uintptr]*surface{}
}

// run calls MFXVideoDECODE_DecodeFrameAsync until the access unit in bs is
// consumed, or, with bs == nil, until the decoder has returned every
// picture it holds. Finished pictures are appended to d.pending.
func (d *decoder) run(ctx context.Context, bs *sys.Bitstream, mayReinit bool) error {
	busy, stalled := 0, 0
	for {
		work, err := d.workSurface()
		if err != nil {
			return err
		}
		var out, syncPoint uintptr
		var before uint32
		if bs != nil {
			before = bs.DataLength
		}
		st := sys.DecodeFrameAsync(d.ses.ses, bs, work.s, &out, &syncPoint)
		if st == sys.WrnDeviceBusy {
			if !busyWait(&busy) {
				return mfxError("MFXVideoDECODE_DecodeFrameAsync", st)
			}
			continue
		}
		if syncPoint != 0 && st >= 0 {
			if err := d.output(out, syncPoint); err != nil {
				return err
			}
		}
		switch {
		case st == sys.ErrMoreData:
			if d.cfg.Codec == codec.AV1 && bs != nil && bs.DataLength > 0 && bs.DataLength != before {
				// An AV1 temporal unit can hold a frame that is decoded
				// but not shown in front of the one that is; the decoder
				// stops after the first and wants the rest of the unit.
				continue
			}
			return nil
		case st == sys.ErrMoreSurface:
			// The work surface was taken; the same input continues with
			// another one. A surface left unlocked would be offered again
			// for ever.
			if work.locked() {
				continue
			}
		case st == sys.ErrIncompatibleVideoParam && bs != nil && mayReinit:
			// A new sequence that does not fit the current decoder:
			// return what it still holds and start over from this
			// access unit, as it begins with the new parameter sets.
			if err := d.run(ctx, nil, false); err != nil {
				return err
			}
			d.shutdown()
			bs.DataOffset, bs.DataLength = 0, bs.MaxLength
			if err := d.init(); err != nil {
				return err
			}
			bs.DataFlag = d.dataFlag()
			mayReinit = false
			continue
		case st < 0:
			return mfxError("MFXVideoDECODE_DecodeFrameAsync", st)
		}
		// Success or a warning such as MFX_WRN_VIDEO_PARAM_CHANGED: go on
		// until the decoder asks for more data.
		if syncPoint != 0 || (bs != nil && bs.DataLength != before) {
			stalled = 0
		} else if stalled++; stalled > maxStalledCalls {
			return &codec.BackendError{Backend: Name, Op: "MFXVideoDECODE_DecodeFrameAsync", Status: int64(st), Message: "the decoder makes no progress"}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// output waits for a decoded picture and copies its visible area into a
// frame for Receive.
func (d *decoder) output(out, syncPoint uintptr) error {
	if err := d.ses.sync("MFXVideoCORE_SyncOperation(decode)", syncPoint); err != nil {
		return err
	}
	s := d.byAddr[out]
	if s == nil {
		return &codec.BackendError{Backend: Name, Op: "MFXVideoDECODE_DecodeFrameAsync", Message: "the decoder returned a surface it was not given"}
	}
	x, y, width, height := s.cropRect()
	rows0, rowBytes0 := codec.NV12.PlaneLayout(0, width, height)
	rows1, rowBytes1 := codec.NV12.PlaneLayout(1, width, height)
	if x+rowBytes1 > s.pitch || y/2+rows1 > s.height/2 {
		return &codec.BackendError{Backend: Name, Op: "decode", Message: fmt.Sprintf("visible area %dx%d+%d+%d exceeds the %dx%d surface", width, height, x, y, s.width, s.height)}
	}
	buf := d.getBuffer(rows0*rowBytes0 + rows1*rowBytes1)
	luma, chroma := buf[:rows0*rowBytes0], buf[rows0*rowBytes0:]
	s.copyOut(luma, chroma, x, y, rowBytes0, rows0, rowBytes1, rows1)
	pts, _ := fromStamp(s.s.Data.TimeStamp)
	f := &codec.Frame{Width: width, Height: height, Format: codec.NV12,
		Planes: [][]byte{luma, chroma}, Strides: []int{rowBytes0, rowBytes1}, PTS: pts}
	codec.SetRelease(f, func() { d.putBuffer(buf) })
	d.pending = append(d.pending, f)
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
	if len(d.pending) == 0 {
		if d.flushed {
			return nil, io.EOF
		}
		return nil, codec.ErrAgain
	}
	f := d.pending[0]
	copy(d.pending, d.pending[1:])
	d.pending[len(d.pending)-1] = nil
	d.pending = d.pending[:len(d.pending)-1]
	return f, nil
}

// Flush makes the decoder return every picture it holds, then resets it;
// the next Send waits for a random access point.
func (d *decoder) Flush(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	if d.inited {
		err = d.run(ctx, nil, false)
		if st := sys.DecodeReset(d.ses.ses, &d.par); st < 0 || err != nil {
			d.shutdown()
		}
	}
	d.flushed = true
	d.waitKeyframe = true
	return err
}

func (d *decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.pending = nil
	d.shutdown()
	d.ses.close()
	return nil
}
