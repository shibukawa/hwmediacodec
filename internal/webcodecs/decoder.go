//go:build js && wasm

package webcodecs

import (
	"context"
	"fmt"
	"io"
	"syscall/js"
	"time"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/pixconv"
)

// inflightFrame is what Send records about a packet until its frame
// comes back.
type inflightFrame struct {
	pts   int64
	order codec.Order
}

// slot is one decoded picture in output order. The browser's VideoFrame
// is copied out asynchronously; the slot is filled when the copy is done.
type slot struct {
	frame *codec.Frame
	err   error
	done  bool
}

type decoder struct {
	cfg   codec.DecoderConfig
	class js.Value // the VideoDecoder constructor

	// dec is the VideoDecoder. The browser closes one for good when it
	// reports an error, so a new one is created on the next Send (opened
	// is false in between).
	dec       js.Value
	opened    bool
	onOutput  js.Func
	onError   js.Func
	onDequeue js.Func
	wake      waiter

	// gen is bumped when everything in flight is abandoned (a reset, a
	// failure, Close); a copy that completes for an older generation is
	// dropped.
	gen int
	// configKey identifies the active configuration: the codec string
	// and the coded size. Empty while the VideoDecoder is unconfigured.
	configKey string

	ps        *paramSets          // H.264 and HEVC
	av1Seq    *av1.SequenceHeader // AV1: the current sequence header ...
	av1SeqOBU []byte              // ... and its OBU, to prefix a restart with

	nextTS   int64 // timestamp of the next chunk
	inflight map[int64]inflightFrame
	slots    []*slot
	copying  int   // slots whose copy is still running
	failure  error // from the error callback, until a method reports it

	// fullRange is the sample range of the configured stream; a frame the
	// browser widened to full range is brought back to video range.
	fullRange bool

	waitKeyframe bool
	flushed      bool
	closed       bool

	jsBufs  jsBuffers
	goBufs  goBuffers
	scratch []byte // planar pictures on their way to NV12

	// canvas and context2D read the pixels of frames copyTo cannot: see
	// drawFrame.
	canvas    js.Value
	context2D js.Value
	canvasW   int
	canvasH   int
}

// OutputsDisplayOrder implements codec.DisplayOrderer. AV1 temporal units
// arrive in presentation order and yield one frame each. For H.264 and
// HEVC the answer is false although WebCodecs specifies presentation
// order (see the package documentation): the public API's reorder layer
// sorts the frames by picture order count, and WithDecodeOrder returns the
// browser's own order.
func (d *decoder) OutputsDisplayOrder() bool { return d.cfg.Codec == codec.AV1 }

func newDecoder(cfg codec.DecoderConfig, cls js.Value) *decoder {
	d := &decoder{
		cfg:          cfg,
		class:        cls,
		wake:         newWaiter(),
		inflight:     map[int64]inflightFrame{},
		waitKeyframe: true,
	}
	if cfg.Codec != codec.AV1 {
		d.ps = newParamSets(cfg.Codec)
	}
	// Callbacks never block and never let a JavaScript exception escape:
	// either would take the whole program down.
	d.onOutput = js.FuncOf(func(_ js.Value, args []js.Value) any {
		d.output(first(args))
		return nil
	})
	d.onError = js.FuncOf(func(_ js.Value, args []js.Value) any {
		d.fail(first(args))
		return nil
	})
	d.onDequeue = js.FuncOf(func(js.Value, []js.Value) any {
		d.wake.signal()
		return nil
	})
	return d
}

// open creates the VideoDecoder if there is none.
func (d *decoder) open() error {
	if d.opened {
		return nil
	}
	err := try("new VideoDecoder", func() {
		d.dec = d.class.New(map[string]any{"output": d.onOutput, "error": d.onError})
		d.dec.Call("addEventListener", "dequeue", d.onDequeue)
	})
	if err != nil {
		return err
	}
	d.opened = true
	d.configKey = ""
	return nil
}

// abandon drops everything in flight and the VideoDecoder itself.
func (d *decoder) abandon() {
	d.gen++
	for _, s := range d.slots {
		if s.done && s.frame != nil {
			s.frame.Release()
		}
	}
	d.slots = nil
	d.copying = 0
	clear(d.inflight)
	if d.opened {
		try("VideoDecoder.close", func() {
			d.dec.Call("removeEventListener", "dequeue", d.onDequeue)
			if d.dec.Get("state").String() != "closed" {
				d.dec.Call("close")
			}
		})
	}
	d.opened = false
	d.configKey = ""
	d.waitKeyframe = true
}

// fail is the VideoDecoder's error callback. The browser has closed the
// decoder; the error waits for the next method call.
func (d *decoder) fail(e js.Value) {
	switch errorName(e) {
	case "EncodingError", "DataError":
		d.failure = fmt.Errorf("%w: %s: %s", codec.ErrInvalidData, Name, errorText(e))
	case "NotSupportedError":
		d.failure = unsupported(d.cfg.Codec, codec.Decode, errorText(e))
	default:
		d.failure = jsError("VideoDecoder", e)
	}
	d.wake.signal()
}

// takeFailure returns the pending error once and resets the decoder, so
// that the caller can go on with a keyframe.
func (d *decoder) takeFailure() error {
	err := d.failure
	if err == nil {
		return nil
	}
	d.failure = nil
	d.abandon()
	return err
}

// streamConfig is the configuration a keyframe asks for.
type streamConfig struct {
	names         []string // codec strings, most fitting first
	width, height int      // coded size
	// color is set when the frames are converted to RGB by the browser
	// and the stream does not name its matrix: the description to convert
	// with, instead of the browser's own guess.
	color *pixconv.Color
	// depth is the bit depth of the samples and subsampled reports
	// 4:2:0 (or monochrome) pictures: what an NV12 frame can hold.
	depth      int
	subsampled bool
	// fullRange reports a stream whose samples use the full range.
	fullRange bool
}

func (c streamConfig) key() string {
	k := fmt.Sprintf("%s %dx%d", c.names[0], c.width, c.height)
	if c.color != nil {
		k += fmt.Sprintf(" %s full=%v", c.color.Matrix, c.color.FullRange)
	}
	return k
}

// matrixUnspecified is matrix_coeffs 2: the stream does not name a matrix.
const matrixUnspecified = 2

// undeclaredColor returns the colour description for a stream that names
// no matrix, when the browser converts its frames to RGB, and nil
// otherwise. Browsers guess differently there (Chromium takes BT.709 for
// every such AV1 stream and goes by the height for H.264); the rule of
// pixconv.ColorFor makes the colours those of the other backends.
func (d *decoder) undeclaredColor(described bool, matrix uint8, fullRange bool, width, height int) *pixconv.Color {
	if d.cfg.OutputFormat == codec.NV12 || (described && matrix != matrixUnspecified) {
		return nil
	}
	c := pixconv.ColorFor(matrixUnspecified, fullRange, width, height)
	return &c
}

// h26xConfig derives the configuration from the newest SPS.
func (d *decoder) h26xConfig() (streamConfig, error) {
	switch d.cfg.Codec {
	case codec.H264:
		s, err := h264.ParseSPS(d.ps.lastSPS)
		if err != nil {
			return streamConfig{}, err
		}
		signal := s.VUIPresent && s.VUI.VideoSignalTypePresent
		return streamConfig{
			names: []string{h264StreamCodecString(s)}, width: s.CodedWidth(), height: s.CodedHeight(),
			depth: int(max(s.BitDepthLuma, s.BitDepthChroma)), subsampled: s.ChromaFormatIDC <= 1,
			fullRange: signal && s.VUI.VideoFullRange,
			color:     d.undeclaredColor(signal && s.VUI.ColourDescriptionPresent, s.VUI.MatrixCoefficients, signal && s.VUI.VideoFullRange, s.Width(), s.Height()),
		}, nil
	default:
		// The colour description and the range are in the VUI, at the end
		// of the SPS.
		s, err := hevc.ParseSPS(d.ps.lastSPS)
		if err != nil {
			return streamConfig{}, err
		}
		signal := s.VUIPresent && s.VUI.VideoSignalTypePresent
		_, _, w, h := s.Crop()
		return streamConfig{
			names: hevcCodecStrings(&s.PTL), width: s.Width, height: s.Height,
			depth: int(max(s.BitDepthLuma, s.BitDepthChroma)), subsampled: s.ChromaFormatIDC <= 1,
			fullRange: signal && s.VUI.VideoFullRange,
			color:     d.undeclaredColor(signal && s.VUI.ColourDescriptionPresent, s.VUI.MatrixCoeffs, signal && s.VUI.VideoFullRange, w, h),
		}, nil
	}
}

// colorSpaceInit is the VideoColorSpaceInit of a colour description.
// Primaries and transfer function follow the matrix, as they do in every
// stream that is not HDR.
func colorSpaceInit(c pixconv.Color) map[string]any {
	name := "smpte170m"
	switch c.Matrix {
	case pixconv.BT709:
		name = "bt709"
	case pixconv.BT2020:
		return map[string]any{"primaries": "bt2020", "transfer": "bt709", "matrix": "bt2020-ncl", "fullRange": c.FullRange}
	}
	return map[string]any{"primaries": name, "transfer": name, "matrix": name, "fullRange": c.FullRange}
}

// configure makes the VideoDecoder ready for a stream with the given
// configuration. A decoder that is configured for another one is drained
// first, so that the frames of the old stream are not lost.
func (d *decoder) configure(ctx context.Context, want streamConfig) error {
	if err := d.open(); err != nil {
		return err
	}
	key := want.key()
	if key == d.configKey {
		return nil
	}
	if d.configKey != "" {
		if err := d.drain(ctx); err != nil {
			return err
		}
	}
	name, err := supportedDecodeCodec(ctx, d.class, want.names, d.cfg.AllowSoftware)
	if err != nil {
		return err
	}
	if name == "" {
		reason := fmt.Sprintf("the browser reports no hardware decoder for %s at %dx%d (use WithSoftwareFallback to allow its software decoder)", want.names[0], want.width, want.height)
		if d.cfg.AllowSoftware {
			reason = fmt.Sprintf("the browser has no decoder for %s at %dx%d", want.names[0], want.width, want.height)
		}
		return unsupported(d.cfg.Codec, codec.Decode, reason)
	}
	config := decoderConfig(name, d.cfg.AllowSoftware)
	config["codedWidth"], config["codedHeight"] = want.width, want.height
	if want.color != nil {
		// Overrides what the bitstream says, which is nothing.
		config["colorSpace"] = colorSpaceInit(*want.color)
	}
	if err := try("VideoDecoder.configure", func() { d.dec.Call("configure", config) }); err != nil {
		return err
	}
	d.configKey = key
	d.fullRange = want.fullRange
	return nil
}

func (d *decoder) Send(ctx context.Context, p codec.Packet) error {
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.takeFailure(); err != nil {
		return err
	}
	if len(d.slots) >= maxPending {
		return codec.ErrAgain
	}
	d.flushed = false

	data := p.Data
	var (
		want streamConfig
		key  bool
	)
	if d.cfg.Codec == codec.AV1 {
		tu, err := av1.ParseTemporalUnit(p.Data, d.av1Seq)
		if err != nil {
			return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
		}
		if tu.Sequence != nil {
			d.av1Seq = tu.Sequence
			d.av1SeqOBU = append(d.av1SeqOBU[:0], tu.SequenceHeader.Raw...)
		}
		if !tu.HasFrame {
			return nil
		}
		key = tu.Keyframe
		if d.waitKeyframe {
			if !key || d.av1Seq == nil {
				return nil
			}
			if tu.SequenceHeader == nil {
				data = append(append([]byte{}, d.av1SeqOBU...), p.Data...)
			}
		}
		if key {
			sh := d.av1Seq
			want = streamConfig{
				names: []string{av1CodecString(sh)}, width: sh.MaxFrameWidth, height: sh.MaxFrameHeight,
				depth: sh.BitDepth, subsampled: sh.MonoChrome || (sh.SubsamplingX == 1 && sh.SubsamplingY == 1),
				fullRange: sh.FullRange,
				color:     d.undeclaredColor(sh.ColorDescriptionPresent, sh.MatrixCoefficients, sh.FullRange, sh.MaxFrameWidth, sh.MaxFrameHeight),
			}
		}
	} else {
		info := d.ps.inspect(p.Data)
		if info.nals == 0 {
			return codec.ErrInvalidData
		}
		if !info.vcl {
			return nil // parameter sets on their own: remembered
		}
		key = info.keyframe
		if d.waitKeyframe && !key {
			return nil
		}
		if key {
			if d.ps.lastSPS == nil {
				// A keyframe without any parameter sets cannot be
				// decoded; wait for them.
				return nil
			}
			var err error
			if want, err = d.h26xConfig(); err != nil {
				return fmt.Errorf("%w: %v", codec.ErrInvalidData, err)
			}
			if !d.ps.covers(info.kinds) {
				// Without a description the decoder finds the
				// parameter sets in the keyframes only.
				data = append(d.ps.annexB(), p.Data...)
			}
		}
	}
	if key {
		if d.cfg.OutputFormat == codec.NV12 && (want.depth > 8 || !want.subsampled) {
			// The browser hands such pictures over in planar formats of
			// their own depth and sampling, and converts to RGB only.
			return unsupported(d.cfg.Codec, codec.Decode, fmt.Sprintf("the stream's %d-bit pictures (4:2:0: %v) have no NV12 form; ask for RGBA or BGRA output, which the browser converts to", want.depth, want.subsampled))
		}
		if err := d.configure(ctx, want); err != nil {
			return err
		}
	}
	if !d.opened || d.configKey == "" {
		return nil
	}

	ts := d.nextTS
	d.nextTS += timestampStep
	err := try("VideoDecoder.decode", func() {
		buf := uint8Array.New(len(data))
		js.CopyBytesToJS(buf, data)
		typ := "delta"
		if key {
			typ = "key"
		}
		chunk := global.Get("EncodedVideoChunk").New(map[string]any{"type": typ, "timestamp": ts, "data": buf})
		d.dec.Call("decode", chunk)
	})
	if err != nil {
		return err
	}
	d.inflight[ts] = inflightFrame{pts: p.PTS, order: codec.PacketOrder(p)}
	d.waitKeyframe = false
	return d.waitQueue(ctx)
}

// waitQueue blocks while the browser's decoder is saturated: it reports
// the chunks it has not taken yet in decodeQueueSize. This is where the
// page's event loop gets to run, and with it the output callback.
func (d *decoder) waitQueue(ctx context.Context) error {
	deadline := time.Now().Add(stallTimeout)
	for d.failure == nil && d.opened && len(d.slots) < maxPending {
		if d.dec.Get("decodeQueueSize").Int() == 0 {
			return nil
		}
		if err := d.wake.wait(ctx, "VideoDecoder.decode", deadline); err != nil {
			return err
		}
	}
	// A failure is reported by the next call, which is then free to go
	// on with a keyframe.
	return nil
}

// output is the VideoDecoder's output callback: it starts the copy of the
// frame into CPU memory and closes the frame when the copy is done. A
// browser's hardware decoder owns few pictures, and one that is not
// closed is not returned to it.
func (d *decoder) output(vf js.Value) {
	gen := d.gen
	s := &slot{}
	d.slots = append(d.slots, s)
	d.copying++
	finish := func(f *codec.Frame, err error) {
		try("VideoFrame.close", func() { vf.Call("close") })
		if gen != d.gen {
			f.Release()
			return
		}
		s.frame, s.err, s.done = f, err, true
		d.copying--
		d.wake.signal()
	}

	rgb := d.cfg.OutputFormat != codec.NV12
	var (
		ts     int64
		w, h   int
		format string
		size   int
		opts   js.Value
		// widened: the frame is full range and the stream is not.
		widened bool
	)
	err := try("VideoFrame", func() {
		ts = int64(vf.Get("timestamp").Float())
		if cs := vf.Get("colorSpace"); cs.Type() == js.TypeObject {
			widened = cs.Get("fullRange").Type() == js.TypeBoolean && cs.Get("fullRange").Bool() && !d.fullRange
		}
		rect := vf.Get("visibleRect")
		w, h = rect.Get("width").Int(), rect.Get("height").Int()
		if v := vf.Get("format"); v.Type() == js.TypeString {
			format = v.String()
		}
		switch {
		case rgb && format == "":
			// An opaque frame: no copyTo.
		case rgb:
			name := "RGBA"
			if d.cfg.OutputFormat == codec.BGRA {
				name = "BGRA"
			}
			opts = js.ValueOf(map[string]any{"format": name})
			size = vf.Call("allocationSize", opts).Int()
		case format == "NV12" || format == "I420" || format == "I420A":
			size = vf.Call("allocationSize").Int()
		}
	})
	in := d.take(ts)
	switch {
	case err != nil:
		finish(nil, err)
		return
	case rgb && format == "":
		finish(d.drawFrame(vf, w, h, in))
		return
	case !rgb && size == 0:
		// Not NV12 or I420: an opaque frame, or a browser whose decoders
		// hand over RGB (Firefox on macOS returns BGRX). Turning RGB
		// back into YUV would need the matrix the browser converted
		// with, which it does not tell.
		if format == "" {
			format = "opaque"
		}
		finish(nil, unsupported(d.cfg.Codec, codec.Decode, fmt.Sprintf("this browser hands decoded pictures over as %s, which has no NV12 form; ask for RGBA or BGRA output", format)))
		return
	}

	buf := d.jsBufs.get(size)
	var promise js.Value
	err = try("VideoFrame.copyTo", func() {
		if rgb {
			promise = vf.Call("copyTo", buf, opts)
		} else {
			promise = vf.Call("copyTo", buf)
		}
	})
	if err != nil {
		finish(nil, err)
		return
	}
	then(promise, func(layout js.Value) {
		var (
			f    *codec.Frame
			ferr error
		)
		if err := try("VideoFrame.copyTo", func() { f, ferr = d.frame(buf, size, layout, format, w, h, in) }); err != nil {
			ferr = err
		}
		if ferr == nil && widened && f.Format == codec.NV12 {
			widenedFrames = true
			if !keepBrowserForm {
				narrowRange(f.Planes)
				if webKit() {
					rows, rowBytes := codec.NV12.PlaneLayout(1, f.Width, f.Height)
					unshiftChroma(f.Planes[1], f.Strides[1], rowBytes, rows)
				}
			}
		}
		d.jsBufs.put(buf)
		finish(f, ferr)
	}, func(e js.Value) {
		d.jsBufs.put(buf)
		finish(nil, jsError("VideoFrame.copyTo", e))
	})
}

// drawFrame reads a frame whose pixels the browser does not expose
// (VideoFrame.format is null: pictures that live in a GPU texture only,
// which is what some hardware decoders return, on Android and for 10-bit
// content elsewhere). Such a frame can still be drawn, so it goes through
// a canvas, which converts it to 8-bit sRGB on the way.
func (d *decoder) drawFrame(vf js.Value, w, h int, in inflightFrame) (*codec.Frame, error) {
	var mem []byte
	err := try("OffscreenCanvas", func() {
		if d.canvas.IsUndefined() || d.canvasW != w || d.canvasH != h {
			d.canvas = global.Get("OffscreenCanvas").New(w, h)
			d.context2D = d.canvas.Call("getContext", "2d", map[string]any{"willReadFrequently": true})
			d.canvasW, d.canvasH = w, h
		}
		d.context2D.Call("drawImage", vf, 0, 0, w, h)
		pixels := d.context2D.Call("getImageData", 0, 0, w, h).Get("data")
		mem = d.goBufs.get(w * h * 4)
		js.CopyBytesToGo(mem, pixels)
	})
	if err != nil {
		return nil, err
	}
	if d.cfg.OutputFormat == codec.BGRA {
		for i := 0; i+3 < len(mem); i += 4 {
			mem[i], mem[i+2] = mem[i+2], mem[i]
		}
	}
	f := &codec.Frame{Width: w, Height: h, Format: d.cfg.OutputFormat, PTS: in.pts, Planes: [][]byte{mem}, Strides: []int{w * 4}}
	codec.SetFrameOrder(f, in.order)
	codec.SetRelease(f, func() { d.goBufs.put(mem) })
	return f, nil
}

var (
	// widenedFrames records that the browser handed over full-range NV12
	// frames for a video-range stream (WebKit does). Such frames are
	// brought back to the stream's form, which is close to but not always
	// exactly what was decoded.
	widenedFrames bool
	// keepBrowserForm makes the decoder return such frames as the browser
	// handed them over. The tests set it to compare them with a fixture
	// in the browser's own form.
	keepBrowserForm bool
)

// WidenedFrames reports whether this browser has handed over full-range
// frames for a video-range stream so far, which means that the NV12 frames
// of its decoders are restored rather than exact. The conformance tests
// ask before they insist on checksums.
func WidenedFrames() bool { return widenedFrames }

// take returns what Send recorded for the chunk with the given timestamp.
func (d *decoder) take(ts int64) inflightFrame {
	in, ok := d.inflight[ts]
	if !ok {
		// A browser that took the timestamp through a coarser clock.
		best := int64(-1)
		for k := range d.inflight {
			if best < 0 || abs(k-ts) < abs(best-ts) {
				best = k
			}
		}
		if best < 0 || abs(best-ts) >= timestampStep/2 {
			return inflightFrame{}
		}
		ts, in = best, d.inflight[best]
	}
	delete(d.inflight, ts)
	// Packets whose picture never came out (dropped leading pictures)
	// would stay for good.
	const keep = 256 * timestampStep
	if len(d.inflight) > 256 {
		for k := range d.inflight {
			if k < ts-keep {
				delete(d.inflight, k)
			}
		}
	}
	return in
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// frame builds the frame from the bytes copyTo wrote into buf.
func (d *decoder) frame(buf js.Value, size int, layoutValue js.Value, format string, w, h int, in inflightFrame) (*codec.Frame, error) {
	layout := make([]planeLayout, layoutValue.Length())
	for i := range layout {
		e := layoutValue.Index(i)
		layout[i] = planeLayout{offset: e.Get("offset").Int(), stride: e.Get("stride").Int()}
	}
	f := &codec.Frame{Width: w, Height: h, Format: d.cfg.OutputFormat, PTS: in.pts}
	var (
		mem []byte
		err error
	)
	switch {
	case d.cfg.OutputFormat != codec.NV12:
		mem = d.goBufs.get(size)
		js.CopyBytesToGo(mem, buf)
		var pix []byte
		if pix, err = packedRGB(mem, layout, w, h); err == nil {
			f.Planes, f.Strides = [][]byte{pix}, []int{w * 4}
		}
	case format == "NV12":
		mem = d.goBufs.get(size)
		js.CopyBytesToGo(mem, buf)
		f.Planes, f.Strides, err = nv12Planes(mem, layout, w, h)
	default: // I420, I420A: the browser's software decoders
		if cap(d.scratch) < size {
			d.scratch = make([]byte, size)
		}
		src := d.scratch[:size]
		js.CopyBytesToGo(src, buf)
		mem = d.goBufs.get(codec.NV12.FrameSize(w, h))
		if err = i420ToNV12(mem, src, layout, w, h); err == nil {
			_, uvRow := codec.NV12.PlaneLayout(1, w, h)
			f.Planes, f.Strides = [][]byte{mem[:w*h], mem[w*h:]}, []int{w, uvRow}
		}
	}
	if err != nil {
		d.goBufs.put(mem)
		return nil, backendErr("VideoFrame.copyTo", err.Error())
	}
	codec.SetFrameOrder(f, in.order)
	codec.SetRelease(f, func() { d.goBufs.put(mem) })
	return f, nil
}

func (d *decoder) Receive(ctx context.Context) (*codec.Frame, error) {
	if d.closed {
		return nil, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(stallTimeout)
	for len(d.slots) > 0 {
		s := d.slots[0]
		if !s.done {
			// The picture exists; its copy is on the way.
			if err := d.wake.wait(ctx, "VideoFrame.copyTo", deadline); err != nil {
				return nil, err
			}
			continue
		}
		d.slots[0] = nil
		d.slots = d.slots[1:]
		if s.err != nil {
			return nil, s.err
		}
		return s.frame, nil
	}
	if err := d.takeFailure(); err != nil {
		return nil, err
	}
	if d.flushed {
		return nil, io.EOF
	}
	return nil, codec.ErrAgain
}

// drain makes the browser emit every frame it holds and waits until all
// of them have been copied.
func (d *decoder) drain(ctx context.Context) error {
	if d.opened && d.configKey != "" && d.failure == nil {
		var promise js.Value
		if err := try("VideoDecoder.flush", func() { promise = d.dec.Call("flush") }); err != nil {
			return err
		}
		if _, err := await(ctx, "VideoDecoder.flush", promise); err != nil {
			if ctx.Err() == nil && d.failure != nil {
				// The decoder failed on the way; the failure says why.
				return d.takeFailure()
			}
			// Nothing the decoder still holds will be asked for again.
			d.abandon()
			return err
		}
	}
	deadline := time.Now().Add(stallTimeout)
	for d.copying > 0 {
		if err := d.wake.wait(ctx, "VideoFrame.copyTo", deadline); err != nil {
			d.abandon()
			return err
		}
	}
	// A VideoDecoder wants a key chunk after a flush.
	d.waitKeyframe = true
	clear(d.inflight)
	return nil
}

func (d *decoder) Flush(ctx context.Context) error {
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.takeFailure(); err != nil {
		return err
	}
	if err := d.drain(ctx); err != nil {
		return err
	}
	d.flushed = true
	return nil
}

func (d *decoder) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	d.abandon()
	d.failure = nil
	d.onOutput.Release()
	d.onError.Release()
	d.onDequeue.Release()
	return nil
}
