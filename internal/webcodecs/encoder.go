//go:build js && wasm

package webcodecs

import (
	"context"
	"fmt"
	"io"
	"math"
	"syscall/js"
	"time"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// describe summarises the requested controls for error messages.
func describe(cfg *codec.EncoderConfig) string {
	s := fmt.Sprintf("%s %dx%d", cfg.Codec, cfg.Width, cfg.Height)
	switch {
	case cfg.Quality > 0:
		s += fmt.Sprintf(" quality %.2f", cfg.Quality)
	case cfg.Bitrate > 0:
		s += fmt.Sprintf(" %d bit/s %s", cfg.Bitrate, cfg.RateControl)
	}
	if cfg.Profile != codec.ProfileDefault {
		s += " profile " + cfg.Profile.String()
	}
	if cfg.LowLatency {
		s += " low-latency"
	}
	return s
}

// codecMember names the codec-specific member of the encoder
// configuration and of the per-frame options.
func codecMember(c codec.Codec) string {
	if c == codec.HEVC {
		return "hevc"
	}
	return "avc"
}

// encoderConfig builds the VideoEncoderConfig for one codec string.
func encoderConfig(codecString string, cfg *codec.EncoderConfig) map[string]any {
	m := map[string]any{
		"codec":                codecString,
		"width":                cfg.Width,
		"height":               cfg.Height,
		"hardwareAcceleration": acceleration(cfg.AllowSoftware),
		// Access units with start codes and the parameter sets in front
		// of every keyframe: the form a Packet has.
		codecMember(cfg.Codec): map[string]any{"format": "annexb"},
	}
	fps := cfg.FrameRate
	if fps > 0 {
		m["framerate"] = fps
	} else {
		fps = 30
	}
	if cfg.LowLatency {
		m["latencyMode"] = "realtime"
	} else {
		m["latencyMode"] = "quality"
	}
	if cfg.Quality > 0 {
		// Constant quality: every frame carries its quantiser.
		m["bitrateMode"] = "quantizer"
		return m
	}
	// Without a target, 0.1 bit per pixel per frame (at least 200 kbit/s)
	// as on Windows and Android, so that the rate does not depend on the
	// browser's own default.
	bitrate := cfg.Bitrate
	if bitrate <= 0 {
		bitrate = max(int(float64(cfg.Width*cfg.Height)*fps*0.1), 200_000)
	}
	m["bitrate"] = bitrate
	if cfg.RateControl == codec.CBR {
		m["bitrateMode"] = "constant"
	} else {
		m["bitrateMode"] = "variable"
	}
	return m
}

// supportedEncoderConfig returns the first configuration for cfg the
// browser accepts, or nil when it accepts none.
func supportedEncoderConfig(ctx context.Context, cls js.Value, cfg codec.EncoderConfig) (map[string]any, error) {
	names, _ := encodeCodecStrings(cfg.Codec, cfg.Profile, cfg.Width, cfg.Height, cfg.FrameRate)
	for _, name := range names {
		config := encoderConfig(name, &cfg)
		ok, err := supported(ctx, cls, config)
		if err != nil {
			return nil, err
		}
		if ok {
			return config, nil
		}
	}
	return nil, nil
}

type encoder struct {
	cfg        codec.EncoderConfig
	class      js.Value // the VideoEncoder constructor
	frameClass js.Value // the VideoFrame constructor
	config     map[string]any

	// enc is the VideoEncoder. The browser closes one for good when it
	// reports an error, so a new one is created on the next Send.
	enc       js.Value
	opened    bool
	onOutput  js.Func
	onError   js.Func
	onDequeue js.Func
	wake      waiter

	qp       int   // quantiser of every frame in constant-quality mode, else 0
	frameDur int64 // one frame in microseconds, or 0 when the rate is unknown

	ps *paramSets // from the chunks, prepended to keyframes that lack them
	// lengthSize is non-zero when the browser ignored the request for
	// Annex-B output and returns length-prefixed NAL units with a decoder
	// configuration record: the size of the length fields.
	lengthSize int

	inflight map[int64]int64 // chunk timestamp -> caller PTS
	lastTS   int64
	hasTS    bool
	pending  []codec.Packet
	failure  error

	frameIndex int
	forceNext  bool
	flushed    bool
	closed     bool

	input   js.Value // one frame of NV12 on the JavaScript side
	scratch []byte
	out     js.Value // a chunk on its way to Go
	outSize int
}

func newEncoder(cfg codec.EncoderConfig, cls js.Value, config map[string]any) (*encoder, error) {
	size := codec.NV12.FrameSize(cfg.Width, cfg.Height)
	e := &encoder{
		cfg:        cfg,
		class:      cls,
		frameClass: global.Get("VideoFrame"),
		config:     config,
		wake:       newWaiter(),
		ps:         newParamSets(cfg.Codec),
		inflight:   map[int64]int64{},
		forceNext:  true,
		input:      uint8Array.New(size),
		scratch:    make([]byte, size),
	}
	if cfg.Quality > 0 {
		e.qp = quantizer(cfg.Quality)
	}
	if cfg.FrameRate > 0 {
		e.frameDur = int64(math.Round(1_000_000 / cfg.FrameRate))
	}
	e.onOutput = js.FuncOf(func(_ js.Value, args []js.Value) any {
		var meta js.Value
		if len(args) > 1 {
			meta = args[1]
		}
		e.output(first(args), meta)
		return nil
	})
	e.onError = js.FuncOf(func(_ js.Value, args []js.Value) any {
		e.fail(first(args))
		return nil
	})
	e.onDequeue = js.FuncOf(func(js.Value, []js.Value) any {
		e.wake.signal()
		return nil
	})
	if err := e.open(); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

// open creates and configures the VideoEncoder if there is none.
func (e *encoder) open() error {
	if e.opened {
		return nil
	}
	err := try("new VideoEncoder", func() {
		e.enc = e.class.New(map[string]any{"output": e.onOutput, "error": e.onError})
		e.enc.Call("addEventListener", "dequeue", e.onDequeue)
		e.enc.Call("configure", e.config)
	})
	if err != nil {
		return err
	}
	e.opened = true
	e.forceNext = true
	return nil
}

// abandon drops what is in flight and the VideoEncoder itself.
func (e *encoder) abandon() {
	clear(e.inflight)
	if e.opened {
		try("VideoEncoder.close", func() {
			e.enc.Call("removeEventListener", "dequeue", e.onDequeue)
			if e.enc.Get("state").String() != "closed" {
				e.enc.Call("close")
			}
		})
	}
	e.opened = false
}

// fail is the VideoEncoder's error callback. The browser has closed the
// encoder; the error waits for the next method call.
func (e *encoder) fail(v js.Value) {
	if errorName(v) == "NotSupportedError" {
		e.failure = unsupported(e.cfg.Codec, codec.Encode, errorText(v))
	} else {
		e.failure = jsError("VideoEncoder", v)
	}
	e.wake.signal()
}

// takeFailure returns the pending error once and drops the encoder, so
// that the next frame starts a new one with a keyframe.
func (e *encoder) takeFailure() error {
	err := e.failure
	if err == nil {
		return nil
	}
	e.failure = nil
	e.abandon()
	return err
}

func (e *encoder) Send(ctx context.Context, f *codec.Frame) error {
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkNV12(f, e.cfg.Width, e.cfg.Height); err != nil {
		return err
	}
	if err := e.takeFailure(); err != nil {
		return err
	}
	if len(e.pending) >= maxPending {
		return codec.ErrAgain
	}
	if err := e.open(); err != nil {
		return err
	}
	e.flushed = false

	key := f.ForceKeyframe || e.forceNext
	if e.cfg.KeyframeInterval > 0 && e.frameIndex%e.cfg.KeyframeInterval == 0 {
		key = true
	}
	// The chunk brings the timestamp back; it must be new.
	ts := toMicros(f.PTS, e.cfg.TimeScale)
	if e.hasTS && ts <= e.lastTS {
		ts = e.lastTS + 1
	}
	err := try("VideoEncoder.encode", func() {
		n := packNV12(e.scratch, f)
		js.CopyBytesToJS(e.input, e.scratch[:n])
		init := map[string]any{
			"format":      "NV12",
			"codedWidth":  e.cfg.Width,
			"codedHeight": e.cfg.Height,
			"timestamp":   ts,
		}
		if e.frameDur > 0 {
			init["duration"] = e.frameDur
		}
		if e.cfg.BT709 {
			init["colorSpace"] = map[string]any{"primaries": "bt709", "transfer": "bt709", "matrix": "bt709", "fullRange": false}
		}
		// The VideoFrame copies the bytes, so the buffer is free again.
		frame := e.frameClass.New(e.input, init)
		defer frame.Call("close")
		opts := map[string]any{"keyFrame": key}
		if e.qp > 0 {
			opts[codecMember(e.cfg.Codec)] = map[string]any{"quantizer": e.qp}
		}
		e.enc.Call("encode", frame, opts)
	})
	if err != nil {
		return err
	}
	e.inflight[ts] = f.PTS
	e.lastTS, e.hasTS = ts, true
	e.frameIndex++
	e.forceNext = false
	return e.waitQueue(ctx)
}

// waitQueue blocks while the browser's encoder is saturated (frames it
// has not taken yet are counted in encodeQueueSize); the page's event
// loop runs meanwhile, and with it the output callback.
func (e *encoder) waitQueue(ctx context.Context) error {
	deadline := time.Now().Add(stallTimeout)
	for e.failure == nil && e.opened && len(e.pending) < maxPending {
		if e.enc.Get("encodeQueueSize").Int() == 0 {
			return nil
		}
		if err := e.wake.wait(ctx, "VideoEncoder.encode", deadline); err != nil {
			return err
		}
	}
	return nil
}

// bytesOf copies a BufferSource (an ArrayBuffer or a view on one).
func bytesOf(v js.Value) []byte {
	var view js.Value
	if v.InstanceOf(global.Get("ArrayBuffer")) {
		view = uint8Array.New(v)
	} else {
		view = uint8Array.New(v.Get("buffer"), v.Get("byteOffset"), v.Get("byteLength"))
	}
	b := make([]byte, view.Length())
	js.CopyBytesToGo(b, view)
	return b
}

// output is the VideoEncoder's output callback.
func (e *encoder) output(chunk, meta js.Value) {
	var perr error
	err := try("EncodedVideoChunk", func() {
		if meta.Type() == js.TypeObject {
			if dc := meta.Get("decoderConfig"); dc.Type() == js.TypeObject {
				if desc := dc.Get("description"); desc.Type() == js.TypeObject {
					// Not Annex-B after all: see lengthSize.
					if n, err := e.ps.addDescription(bytesOf(desc)); err == nil {
						e.lengthSize = n
					}
				}
			}
		}
		n := chunk.Get("byteLength").Int()
		if e.outSize < n {
			e.outSize = n + n/2
			e.out = uint8Array.New(e.outSize)
		}
		chunk.Call("copyTo", e.out)
		data := make([]byte, n)
		js.CopyBytesToGo(data, e.out)
		if e.lengthSize > 0 {
			// Bytes that do not parse as length-prefixed NAL units are
			// Annex-B and stay as they are.
			if converted, err := annexb.FromLengthPrefixed(nil, data, e.lengthSize); err == nil {
				data = converted
			}
		}
		info := e.ps.inspect(data)
		if info.nals == 0 {
			perr = backendErr("EncodedVideoChunk", "a chunk holds no NAL unit")
			return
		}
		key := chunk.Get("type").String() == "key" || info.keyframe
		if key && !e.ps.covers(info.kinds) {
			data = append(e.ps.annexB(), data...)
		}
		ts := int64(chunk.Get("timestamp").Float())
		pts, ok := e.inflight[ts]
		if ok {
			delete(e.inflight, ts)
		} else {
			pts = (ts*int64(e.cfg.TimeScale) + 500_000) / 1_000_000
		}
		// WebCodecs encoders do not reorder frames: decode order is
		// presentation order.
		e.pending = append(e.pending, codec.Packet{Data: data, PTS: pts, DTS: pts, Keyframe: key})
	})
	if err == nil {
		err = perr
	}
	if err != nil && e.failure == nil {
		e.failure = err
	}
	e.wake.signal()
}

func (e *encoder) Receive(ctx context.Context) (codec.Packet, error) {
	if e.closed {
		return codec.Packet{}, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return codec.Packet{}, err
	}
	if len(e.pending) > 0 {
		p := e.pending[0]
		e.pending[0] = codec.Packet{}
		e.pending = e.pending[1:]
		return p, nil
	}
	if err := e.takeFailure(); err != nil {
		return codec.Packet{}, err
	}
	if e.flushed {
		return codec.Packet{}, io.EOF
	}
	return codec.Packet{}, codec.ErrAgain
}

func (e *encoder) Flush(ctx context.Context) error {
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.takeFailure(); err != nil {
		return err
	}
	if e.opened {
		var promise js.Value
		if err := try("VideoEncoder.flush", func() { promise = e.enc.Call("flush") }); err != nil {
			return err
		}
		if _, err := await(ctx, "VideoEncoder.flush", promise); err != nil {
			if ctx.Err() == nil && e.failure != nil {
				// The encoder failed on the way; the failure says why.
				return e.takeFailure()
			}
			e.abandon()
			return err
		}
		if err := e.takeFailure(); err != nil {
			return err
		}
	}
	e.flushed = true
	e.forceNext = true
	e.frameIndex = 0
	clear(e.inflight)
	return nil
}

func (e *encoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	e.abandon()
	e.failure = nil
	e.pending = nil
	e.onOutput.Release()
	e.onError.Release()
	e.onDequeue.Release()
	return nil
}
