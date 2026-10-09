//go:build linux || (windows && amd64)

package nvidia

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/nvidia/sys"
)

// extraSurfaces is added to the parser's minimum decode surface count. The
// decoded picture is copied to host memory inside the decode callback, so
// the parser can recycle any index afterwards; the margin only keeps the
// decoder from stalling when the driver's minimum is tight.
const extraSurfaces = 2

// decoder drives one cuvid parser and one NVDEC decoder. The parser calls
// back into Go for every sequence header and every complete picture; the
// picture callback submits the picture and copies the result to a pooled
// NV12 buffer at once, so nothing device-side outlives a Send call.
type decoder struct {
	mu        sync.Mutex
	cfg       codec.DecoderConfig
	dev       *device
	codecType uint32
	caps      sys.DecodeCaps

	key         uintptr // callback registry key handed to the parser as user data
	parser      uintptr
	dec         uintptr
	haveDecoder bool
	format      sys.VideoFormat // the format the decoder was created for
	numSurfaces uint32

	// The packet being parsed, for the decode callback.
	curPTS   int64
	curOrder codec.Order

	pending []*codec.Frame
	pool    sync.Pool
	cbErr   error // first error raised inside a callback

	waitKeyframe bool
	flushed      bool
	closed       bool
}

// Parser callbacks are C function pointers created once per process with
// purego and dispatched on the user-data key, since the number of
// callbacks purego can create is limited.
var (
	registryMu sync.Mutex
	registry   = map[uintptr]*decoder{}
	nextKey    uintptr

	callbacksOnce sync.Once
	cbSequence    uintptr
	cbDecode      uintptr
	cbDisplay     uintptr
)

func registerDecoder(d *decoder) {
	callbacksOnce.Do(func() {
		cbSequence = purego.NewCallback(onSequence)
		cbDecode = purego.NewCallback(onDecode)
		cbDisplay = purego.NewCallback(onDisplay)
	})
	registryMu.Lock()
	nextKey++
	d.key = nextKey
	registry[d.key] = d
	registryMu.Unlock()
}

func unregisterDecoder(d *decoder) {
	registryMu.Lock()
	delete(registry, d.key)
	registryMu.Unlock()
}

func lookupDecoder(key uintptr) *decoder {
	registryMu.Lock()
	defer registryMu.Unlock()
	return registry[key]
}

// onSequence is PFNVIDSEQUENCECALLBACK; it returns the number of decode
// surfaces, or 0 on failure.
func onSequence(user uintptr, f *sys.VideoFormat) uintptr {
	d := lookupDecoder(user)
	if d == nil || f == nil {
		return 0
	}
	return d.sequence(f)
}

// onDecode is PFNVIDDECODECALLBACK; it returns 1 on success.
func onDecode(user uintptr, p *sys.DecodePicParams) uintptr {
	d := lookupDecoder(user)
	if d == nil || p == nil {
		return 0
	}
	return d.decode(p)
}

// onDisplay is PFNVIDDISPLAYCALLBACK. Frames are taken from the decode
// callback in decode order, so display notifications are ignored.
func onDisplay(user uintptr, info *sys.ParserDispInfo) uintptr { return 1 }

func newDecoder(cfg codec.DecoderConfig, dev *device, codecType uint32, caps sys.DecodeCaps) *decoder {
	d := &decoder{cfg: cfg, dev: dev, codecType: codecType, caps: caps, waitKeyframe: true}
	registerDecoder(d)
	return d
}

func (d *decoder) createParser() error {
	params := sys.ParserParams{
		CodecType:            d.codecType,
		MaxNumDecodeSurfaces: 1, // raised by the sequence callback's return value
		ErrorThreshold:       100,
		MaxDisplayDelay:      0,
		UserData:             d.key,
		SequenceCallback:     cbSequence,
		DecodePicture:        cbDecode,
		DisplayPicture:       cbDisplay,
	}
	if st := sys.CuvidCreateVideoParser(&d.parser, &params); st != sys.CUDASuccess {
		d.parser = 0
		return cuError("cuvidCreateVideoParser", st)
	}
	return nil
}

func (d *decoder) destroyParser() {
	if d.parser != 0 {
		sys.CuvidDestroyVideoParser(d.parser)
		d.parser = 0
	}
}

func (d *decoder) destroyDecoder() {
	if d.haveDecoder {
		sys.CuvidDestroyDecoder(d.dec)
		d.dec = 0
		d.haveDecoder = false
	}
}

// parameterSetsOnly returns an Annex-B buffer with just the parameter-set
// NAL units of an access unit, so that a skipped picture still updates the
// parser's SPS/PPS state.
func parameterSetsOnly(c codec.Codec, nals [][]byte) []byte {
	var out []byte
	for _, nal := range nals {
		if annexb.IsParameterSet(c, annexb.NALUnitType(c, nal)) {
			out = append(append(out, 0, 0, 0, 1), nal...)
		}
	}
	return out
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
	d.flushed = false

	nals := annexb.Split(p.Data)
	if len(nals) == 0 {
		return codec.ErrInvalidData
	}
	c := d.cfg.Codec
	hasVCL, hasKeyframe := false, false
	for _, nal := range nals {
		if t := annexb.NALUnitType(c, nal); annexb.IsVCL(c, t) {
			hasVCL = true
			if annexb.IsKeyframe(c, t) {
				hasKeyframe = true
			}
		}
	}
	data := p.Data
	if d.waitKeyframe && hasVCL && !hasKeyframe {
		// Decoding (re)starts at a random access point; keep the
		// parameter sets and drop the picture.
		data = parameterSetsOnly(c, nals)
		hasVCL = false
		if len(data) == 0 {
			return nil
		}
	}
	if hasKeyframe {
		d.waitKeyframe = false
	}
	return d.dev.run(func() error {
		if d.parser == 0 {
			if err := d.createParser(); err != nil {
				return err
			}
		}
		d.curPTS, d.curOrder = p.PTS, codec.PacketOrder(p)
		pkt := sys.SourceDataPacket{PayloadSize: sys.ULong(len(data)), Payload: &data[0]}
		if hasVCL {
			// The packet holds exactly one picture, so the parser can
			// emit it now instead of waiting for the next access unit.
			pkt.Flags |= sys.PktEndOfPicture
		}
		st := sys.CuvidParseVideoData(d.parser, &pkt)
		if d.cbErr != nil {
			err := d.cbErr
			d.cbErr = nil
			return err
		}
		if st != sys.CUDASuccess {
			return cuError("cuvidParseVideoData", st)
		}
		return nil
	})
}

// sequence handles a sequence header: it creates the NVDEC decoder for the
// coded size, or recreates it after a change, and returns the number of
// decode surfaces the parser may cycle through.
func (d *decoder) sequence(f *sys.VideoFormat) uintptr {
	if f.Codec != d.codecType {
		d.cbErr = unsupported(d.cfg.Codec, fmt.Sprintf("the stream is cuvid codec %d, not %s", f.Codec, d.cfg.Codec))
		return 0
	}
	if f.ChromaFormat != sys.Chroma420 || f.BitDepthLumaMinus8 != 0 || f.BitDepthChromaMinus8 != 0 {
		d.cbErr = unsupported(d.cfg.Codec, fmt.Sprintf("only 8-bit 4:2:0 streams are supported (chroma format %d, %d-bit)", f.ChromaFormat, 8+f.BitDepthLumaMinus8))
		return 0
	}
	n := uint32(f.MinNumDecodeSurfaces)
	if n < 1 {
		n = 1
	}
	n += extraSurfaces
	if d.haveDecoder && d.format.CodedWidth == f.CodedWidth && d.format.CodedHeight == f.CodedHeight && n <= d.numSurfaces {
		d.format = *f // the display area may differ
		return uintptr(d.numSurfaces)
	}
	w, h := f.CodedWidth, f.CodedHeight
	switch {
	case w > d.caps.MaxWidth || h > d.caps.MaxHeight || (w/16)*(h/16) > d.caps.MaxMBCount:
		d.cbErr = unsupported(d.cfg.Codec, fmt.Sprintf("%dx%d exceeds the NVDEC limit of %dx%d", w, h, d.caps.MaxWidth, d.caps.MaxHeight))
		return 0
	case w < uint32(d.caps.MinWidth) || h < uint32(d.caps.MinHeight):
		d.cbErr = unsupported(d.cfg.Codec, fmt.Sprintf("%dx%d is below the NVDEC minimum of %dx%d", w, h, d.caps.MinWidth, d.caps.MinHeight))
		return 0
	case w > 32767 || h > 32767:
		d.cbErr = unsupported(d.cfg.Codec, fmt.Sprintf("%dx%d does not fit the decoder's display rectangle", w, h))
		return 0
	}
	// Every decoded picture is already in host memory, so the old decoder
	// can go.
	d.destroyDecoder()
	info := sys.DecodeCreateInfo{
		Width:             sys.ULong(w),
		Height:            sys.ULong(h),
		NumDecodeSurfaces: sys.ULong(n),
		CodecType:         d.codecType,
		ChromaFormat:      sys.Chroma420,
		CreationFlags:     sys.CreatePreferCUVID,
		MaxWidth:          sys.ULong(w),
		MaxHeight:         sys.ULong(h),
		DisplayArea:       sys.ShortRect{Right: int16(w), Bottom: int16(h)},
		OutputFormat:      sys.SurfaceNV12,
		DeinterlaceMode:   sys.DeinterlaceWeave,
		TargetWidth:       sys.ULong(w),
		TargetHeight:      sys.ULong(h),
		NumOutputSurfaces: 1,
	}
	if st := sys.CuvidCreateDecoder(&d.dec, &info); st != sys.CUDASuccess {
		d.dec = 0
		d.cbErr = cuError("cuvidCreateDecoder", st)
		return 0
	}
	d.haveDecoder = true
	d.format = *f
	d.numSurfaces = n
	return uintptr(n)
}

// decode handles one complete picture: it submits it to NVDEC and copies
// the result into a frame for Receive.
func (d *decoder) decode(p *sys.DecodePicParams) uintptr {
	if !d.haveDecoder {
		d.cbErr = &codec.BackendError{Backend: Name, Op: "cuvidParseVideoData", Message: "picture before the sequence header"}
		return 0
	}
	if p.FieldPicFlag != 0 {
		d.cbErr = unsupported(d.cfg.Codec, "interlaced field pictures are not supported")
		return 0
	}
	if st := sys.CuvidDecodePicture(d.dec, p); st != sys.CUDASuccess {
		d.cbErr = cuError("cuvidDecodePicture", st)
		return 0
	}
	f, err := d.copyPicture(p.CurrPicIdx)
	if err != nil {
		d.cbErr = err
		return 0
	}
	d.pending = append(d.pending, f)
	return 1
}

// copyPicture waits for the picture, maps it and copies the display area
// into an NV12 frame in CPU memory.
func (d *decoder) copyPicture(idx int32) (*codec.Frame, error) {
	var devPtr uint64
	var pitch uint32
	proc := sys.ProcParams{ProgressiveFrame: 1}
	if st := sys.CuvidMapVideoFrame(d.dec, idx, &devPtr, &pitch, &proc); st != sys.CUDASuccess {
		return nil, cuError("cuvidMapVideoFrame", st)
	}
	defer sys.CuvidUnmapVideoFrame(d.dec, devPtr)

	codedW, codedH := int(d.format.CodedWidth), int(d.format.CodedHeight)
	area := d.format.DisplayArea
	left, top := int(area.Left), int(area.Top)
	width, height := int(area.Right)-left, int(area.Bottom)-top
	if left < 0 || top < 0 || width <= 0 || height <= 0 || left+width > codedW || top+height > codedH {
		left, top, width, height = 0, 0, codedW, codedH
	}
	left &^= 1
	top &^= 1

	rows0, rowBytes0 := codec.NV12.PlaneLayout(0, width, height)
	rows1, rowBytes1 := codec.NV12.PlaneLayout(1, width, height)
	buf := d.getBuffer(rows0*rowBytes0 + rows1*rowBytes1)
	luma, chroma := buf[:rows0*rowBytes0], buf[rows0*rowBytes0:]
	if err := copy2D(devPtr, int(pitch), left, top, luma, rowBytes0, rowBytes0, rows0); err != nil {
		d.putBuffer(buf)
		return nil, err
	}
	// NV12 output: the interleaved chroma plane follows the luma plane at
	// pitch * target height.
	chromaBase := devPtr + uint64(pitch)*uint64(codedH)
	if err := copy2D(chromaBase, int(pitch), left, top/2, chroma, rowBytes1, rowBytes1, rows1); err != nil {
		d.putBuffer(buf)
		return nil, err
	}
	f := &codec.Frame{Width: width, Height: height, Format: codec.NV12,
		Planes: [][]byte{luma, chroma}, Strides: []int{rowBytes0, rowBytes1}, PTS: d.curPTS}
	codec.SetFrameOrder(f, d.curOrder)
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

// Flush ends the parser's stream so that it emits any picture it still
// holds, then drops the parser; the next Send creates a fresh one and
// waits for a random access point. The NVDEC decoder is kept for reuse.
func (d *decoder) Flush(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := d.dev.run(func() error {
		if d.parser == 0 {
			return nil
		}
		pkt := sys.SourceDataPacket{Flags: sys.PktEndOfStream}
		st := sys.CuvidParseVideoData(d.parser, &pkt)
		d.destroyParser()
		if d.cbErr != nil {
			err := d.cbErr
			d.cbErr = nil
			return err
		}
		if st != sys.CUDASuccess {
			return cuError("cuvidParseVideoData(end of stream)", st)
		}
		return nil
	})
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
	d.dev.run(func() error { //nolint:errcheck // best effort teardown
		d.destroyParser()
		d.destroyDecoder()
		return nil
	})
	unregisterDecoder(d)
	d.dev.close()
	return nil
}
