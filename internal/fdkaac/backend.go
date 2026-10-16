//go:build linux && !android

// Package fdkaac implements AAC decoding and encoding on Linux on top of
// the system's libfdk-aac (the Fraunhofer FDK AAC library), loaded at run
// time with purego. Linux has no codec API of its own, and the library is
// not part of every distribution (some ship it as fdk-aac-free, which is
// AAC-LC alone, some in their non-free section, some not at all); without
// it the backend is absent. Nothing is bundled.
//
// The backend is the AAC encoder of Linux. It decodes as well, which is
// what a machine without libavcodec uses and what the encoder measures its
// own delay with.
package fdkaac

import (
	"context"
	"fmt"
	"io"
	"math"
	"runtime"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/audioutil"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// Name is the backend identifier reported in Capability.Backend.
const Name = "fdk-aac"

// maxPending bounds the frames and packets held for Receive; Send reports
// ErrAgain beyond it.
const maxPending = 256

// assumedDelay is the encoder's delay where the trial cannot measure it:
// the figure of the library's AAC-LC encoder.
const assumedDelay = 2048

// Backend is the libfdk-aac backend.
type Backend struct{}

// Name implements codec.AudioBackend.
func (Backend) Name() string { return Name }

func unsupported(dir codec.Direction, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: codec.AAC, Direction: dir, Reason: reason}
}

func backendErr(op string, code int32) error {
	return &codec.BackendError{Backend: Name, Op: op, Status: int64(code), Message: fmt.Sprintf("error %#x", uint32(code))}
}

// ProbeAudio implements codec.AudioBackend.
func (Backend) ProbeAudio(ctx context.Context) ([]codec.Capability, error) {
	if load() != nil {
		return nil, nil
	}
	return []codec.Capability{
		{Backend: Name, Codec: codec.AAC, Direction: codec.Decode},
		{Backend: Name, Codec: codec.AAC, Direction: codec.Encode},
	}, ctx.Err()
}

// NewAudioDecoder implements codec.AudioBackend.
func (Backend) NewAudioDecoder(ctx context.Context, cfg codec.AudioDecoderConfig) (codec.AudioDecoder, error) {
	f := cfg.Format
	if f.Codec != codec.AAC {
		return nil, codec.NotImplemented(Name, f.Codec, codec.Decode, "only aac is implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupported(codec.Decode, "sample format "+cfg.SampleFormat.String()+" is not supported; use S16 or F32")
	}
	asc := f.Config
	if len(asc) == 0 {
		var err error
		if asc, err = aac.LCConfig(f.SampleRate, f.Channels); err != nil {
			return nil, fmt.Errorf("hwmediacodec: an AAC decoder needs AudioFormat.Config, or a sample rate and channel count to assume AAC-LC with: %w", err)
		}
	}
	c, err := aac.ParseConfig(asc)
	if err != nil {
		return nil, fmt.Errorf("hwmediacodec: AudioFormat.Config: %w: %w", err, codec.ErrInvalidData)
	}
	channels := c.OutputChannels()
	if channels == 0 {
		channels = f.Channels
	}
	if c.OutputSampleRate() <= 0 || channels <= 0 {
		return nil, unsupported(codec.Decode, "the AudioSpecificConfig does not give the sample rate and channel count")
	}
	if channels > 6 {
		return nil, unsupported(codec.Decode, fmt.Sprintf("%d channels are not supported; mono to 5.1 are", channels))
	}
	if err := load(); err != nil {
		return nil, unsupported(codec.Decode, err.Error())
	}
	if c.SBR && !hasSBR() {
		return nil, unsupported(codec.Decode, "this libfdk-aac is built without spectral band replication (fdk-aac-free), so it does not decode HE-AAC; libavcodec does")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := &decoder{cfg: cfg, asc: append([]byte(nil), asc...), frame: c.OutputFrameSamples(), coreRate: c.SampleRate}
	d.frames = audioutil.Frames{SampleRate: c.OutputSampleRate(), Channels: channels, Format: cfg.SampleFormat}
	if err := d.open(); err != nil {
		return nil, err
	}
	return d, nil
}

type decoder struct {
	mu  sync.Mutex
	cfg codec.AudioDecoderConfig
	asc []byte
	// frame is the number of samples a packet decodes to and coreRate the
	// rate of the AAC-LC core, which band replication doubles.
	frame    int
	coreRate int
	h        uintptr
	frames   audioutil.Frames
	// started is set once the decoder has returned samples, and lagged
	// when it runs aac.SBRDecoderDelay behind.
	started bool
	lagged  bool
	// pcm receives one decoded frame: 2048 samples of up to eight
	// channels.
	pcm     [2048 * 8]int16
	flushed bool
	closed  bool
}

// open makes a decoder for the stream's configuration.
func (d *decoder) open() error {
	h := decOpen(transportRaw, 1)
	if h == 0 {
		return unsupported(codec.Decode, "aacDecoder_Open failed")
	}
	d.h, d.started, d.lagged = h, false, false
	conf := unsafe.Pointer(&d.asc[0])
	length := uint32(len(d.asc))
	code := decConfigRaw(h, &conf, &length)
	runtime.KeepAlive(d.asc)
	if code != decOK {
		d.release()
		return unsupported(codec.Decode, fmt.Sprintf("libfdk-aac does not take the stream configuration (error %#x)", uint32(code)))
	}
	// The order of a WAV file, which is the library's default, and
	// nothing that holds the samples back: the limiter would by its
	// attack time, and the concealment the library picks for AAC-LC (it
	// interpolates across a lost packet) by a whole packet.
	decSetParam(h, decParamChannelMapping, 1)
	decSetParam(h, decParamLimiterEnable, 0)
	decSetParam(h, decParamConcealMethod, 1)
	return nil
}

func (d *decoder) release() {
	if d.h != 0 {
		decClose(d.h)
		d.h = 0
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
	if err := audioutil.CheckPacket(p.Data); err != nil {
		return err
	}
	if d.frames.Ready() >= maxPending {
		return codec.ErrAgain
	}
	d.flushed = false
	data := unsafe.Pointer(&p.Data[0])
	size, valid := uint32(len(p.Data)), uint32(len(p.Data))
	code := decFill(d.h, &data, &size, &valid)
	runtime.KeepAlive(p.Data)
	if code != decOK {
		return fmt.Errorf("%w: %w", backendErr("aacDecoder_Fill", code), codec.ErrInvalidData)
	}
	d.frames.Expect(p.PTS, d.frame)
	switch code := decFrame(d.h, &d.pcm[0], int32(len(d.pcm)), 0); code {
	case decOK:
	case decNotEnoughBits:
		// The decoder wants more than this packet; its samples come with
		// the next one.
		return nil
	default:
		// Forget the packet and start afresh, so that the ones that
		// follow decode.
		d.frames.Reset()
		d.release()
		if err := d.open(); err != nil {
			return err
		}
		return fmt.Errorf("%w: %w", backendErr("aacDecoder_DecodeFrame", code), codec.ErrInvalidData)
	}
	info := decInfo(d.h)
	if info == nil || info.FrameSize <= 0 || info.NumChannels <= 0 || int(info.FrameSize*info.NumChannels) > len(d.pcm) {
		return backendErr("aacDecoder_GetStreamInfo", 0)
	}
	if rate := int(info.SampleRate); rate > 0 && rate != d.frames.SampleRate {
		// Band replication that only the packets signal.
		d.frames.Scale(rate, d.frames.SampleRate)
		d.frame = d.frame * rate / d.frames.SampleRate
		d.frames.SampleRate = rate
	}
	d.frames.Channels = int(info.NumChannels)
	if !d.started {
		d.started = true
		// With band replication the output starts with the delay of its
		// filter banks, which belongs to no packet.
		if d.lagged = d.frames.SampleRate != d.coreRate; d.lagged {
			d.frames.Skip(aac.SBRDecoderDelay)
		}
	}
	d.append(int(info.FrameSize * info.NumChannels))
	return nil
}

// append takes n decoded samples out of pcm.
func (d *decoder) append(n int) {
	s16 := unsafe.Slice((*byte)(unsafe.Pointer(&d.pcm[0])), n*2)
	if d.cfg.SampleFormat == codec.F32 {
		d.frames.Append(audioutil.S16ToF32(s16))
	} else {
		d.frames.Append(s16)
	}
}

// drain fetches what a decoder that runs behind still holds, so that the
// last packet gets all of its samples.
func (d *decoder) drain() {
	if !d.lagged || d.frames.Waiting() == 0 {
		return
	}
	n := aac.SBRDecoderDelay * d.frames.Channels
	if decFrame(d.h, &d.pcm[0], int32(len(d.pcm)), decFlagFlush) == decOK {
		if info := decInfo(d.h); info != nil && int(info.FrameSize*info.NumChannels) >= n {
			d.append(n)
			return
		}
	}
	d.frames.Pad(aac.SBRDecoderDelay)
}

func (d *decoder) Receive(ctx context.Context) (*codec.AudioFrame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, codec.ErrClosed
	}
	if f := d.frames.Next(); f != nil {
		return f, nil
	}
	if d.flushed {
		return nil, io.EOF
	}
	return nil, codec.ErrAgain
}

// Flush implements codec.AudioDecoder: the decoder is replaced by a new
// one, so that the next packet does not lean on the ones before.
func (d *decoder) Flush(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	d.drain()
	d.frames.End()
	d.flushed = true
	d.release()
	return d.open()
}

func (d *decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.release()
	d.frames.Reset()
	d.frames.Drop()
	return nil
}

// knownDelays remembers the measured delay of the encoder for a sample
// rate and channel count.
var (
	knownDelaysMu sync.Mutex
	knownDelays   = map[string]int{}
)

// NewAudioEncoder implements codec.AudioBackend.
func (b Backend) NewAudioEncoder(ctx context.Context, cfg codec.AudioEncoderConfig) (codec.AudioEncoder, error) {
	if cfg.Codec != codec.AAC {
		return nil, codec.NotImplemented(Name, cfg.Codec, codec.Encode, "only aac is implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupported(codec.Encode, "sample format "+cfg.SampleFormat.String()+" is not supported; use S16 or F32")
	}
	if cfg.Channels < 1 || cfg.Channels > 2 {
		return nil, unsupported(codec.Encode, fmt.Sprintf("%d channels are not supported; mono and stereo are", cfg.Channels))
	}
	if !aac.StandardRate(cfg.SampleRate) {
		return nil, unsupported(codec.Encode, fmt.Sprintf("the AAC encoder does not take %d Hz; it takes the rates of the AAC table (8000 to 96000 Hz)", cfg.SampleRate))
	}
	if err := load(); err != nil {
		return nil, unsupported(codec.Encode, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e, err := newEncoder(cfg, 0)
	if err != nil {
		return nil, err
	}
	// The structure the library reports its delay in has changed between
	// its releases, so the delay is measured instead: a trial signal goes
	// through an encoder of its own and through the decoder.
	key := fmt.Sprintf("%d %d", cfg.SampleRate, cfg.Channels)
	knownDelaysMu.Lock()
	delay, known := knownDelays[key]
	knownDelaysMu.Unlock()
	if !known {
		delay = assumedDelay
		if measured, ok := b.measureDelay(ctx, cfg, e.format); ok {
			delay = measured
			knownDelaysMu.Lock()
			knownDelays[key] = delay
			knownDelaysMu.Unlock()
		}
	}
	e.format.Delay, e.times.Delay = delay, delay
	return e, nil
}

func (b Backend) measureDelay(ctx context.Context, cfg codec.AudioEncoderConfig, format codec.AudioFormat) (int, bool) {
	trial, err := newEncoder(cfg, 0)
	if err != nil {
		return 0, false
	}
	defer trial.Close()
	format.Delay = 0
	dec, err := b.NewAudioDecoder(ctx, codec.AudioDecoderConfig{Format: format, SampleFormat: codec.F32})
	if err != nil {
		return 0, false
	}
	defer dec.Close()
	return audioutil.MeasureDelay(ctx, trial, dec, cfg.SampleFormat, cfg.SampleRate, cfg.Channels, format.FrameSamples)
}

type encoder struct {
	mu     sync.Mutex
	cfg    codec.AudioEncoderConfig
	format codec.AudioFormat
	unit   int // one sample of every channel as it is sent
	h      uintptr
	times  audioutil.PacketTimes
	out    []byte
	// input holds 16-bit samples not handed to the encoder yet: the rest
	// of the frame Send accepted last while pending is full (Send refuses
	// more until Receive has made room).
	input []byte

	started bool
	pending []codec.Packet
	flushed bool
	closed  bool
}

func newEncoder(cfg codec.AudioEncoderConfig, delay int) (*encoder, error) {
	asc, err := aac.LCConfig(cfg.SampleRate, cfg.Channels)
	if err != nil {
		return nil, unsupported(codec.Encode, err.Error())
	}
	e := &encoder{
		cfg:  cfg,
		unit: cfg.Channels * cfg.SampleFormat.BytesPerSample(),
		// 6144 bits per channel is the largest access unit AAC allows.
		out: make([]byte, 768*cfg.Channels+64),
		format: codec.AudioFormat{
			Codec:        codec.AAC,
			SampleRate:   cfg.SampleRate,
			Channels:     cfg.Channels,
			Config:       asc,
			FrameSamples: 1024,
			Delay:        delay,
		},
	}
	e.times = audioutil.PacketTimes{SampleRate: cfg.SampleRate, TimeScale: cfg.TimeScale, Frame: 1024, Delay: delay}
	if err := e.open(); err != nil {
		return nil, err
	}
	return e, nil
}

// open makes an encoder with the options of the configuration.
func (e *encoder) open() error {
	var h uintptr
	if code := encOpen(&h, 0, uint32(e.cfg.Channels)); code != encOK || h == 0 {
		return backendErr("aacEncOpen", code)
	}
	e.h = h
	set := func(param, value uint32, what string) error {
		if code := encSetParam(h, param, value); code != encOK {
			return unsupported(codec.Encode, fmt.Sprintf("libfdk-aac does not take %s (error %#x)", what, uint32(code)))
		}
		return nil
	}
	steps := []struct {
		param, value uint32
		what         string
	}{
		{encParamAOT, aac.ObjectTypeLC, "AAC-LC"},
		{encParamSampleRate, uint32(e.cfg.SampleRate), fmt.Sprintf("%d Hz", e.cfg.SampleRate)},
		{encParamChannelMode, uint32(e.cfg.Channels), fmt.Sprintf("%d channels", e.cfg.Channels)},
		{encParamChannelOrder, 1, "the channel order of a WAV file"},
		// Mode 0 holds the bitrate on average with a bit reservoir, which
		// is what both of the library's rate controls ask for; its
		// numbered modes code to a quality and leave the bitrate aside.
		{encParamBitrateMode, 0, "its bitrate mode"},
		{encParamTransmux, 0, "raw access units"},
		{encParamAfterburner, 1, "the afterburner"},
	}
	if e.cfg.Bitrate > 0 {
		steps = append(steps, struct {
			param, value uint32
			what         string
		}{encParamBitrate, uint32(e.cfg.Bitrate), fmt.Sprintf("%d bit/s", e.cfg.Bitrate)})
	}
	for _, s := range steps {
		if err := set(s.param, s.value, s.what); err != nil {
			e.release()
			return err
		}
	}
	// A call without buffers makes the encoder take its parameters.
	if code := encEncode(h, nil, nil, nil, nil); code != encOK {
		e.release()
		return unsupported(codec.Encode, fmt.Sprintf("libfdk-aac does not take the configuration (error %#x)", uint32(code)))
	}
	return nil
}

func (e *encoder) release() {
	if e.h != 0 {
		encClose(&e.h)
		e.h = 0
	}
}

func (e *encoder) Format() codec.AudioFormat {
	e.mu.Lock()
	defer e.mu.Unlock()
	f := e.format
	f.Config = append([]byte(nil), f.Config...)
	return f
}

func (e *encoder) Send(ctx context.Context, f *codec.AudioFrame) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch {
	case f == nil:
		return fmt.Errorf("hwmediacodec: nil audio frame: %w", codec.ErrInvalidData)
	case f.Format != e.cfg.SampleFormat:
		return fmt.Errorf("hwmediacodec: the frame is %s but the encoder was opened for %s: %w", f.Format, e.cfg.SampleFormat, codec.ErrInvalidData)
	case f.SampleRate != e.cfg.SampleRate || f.Channels != e.cfg.Channels:
		return fmt.Errorf("hwmediacodec: the frame is %d Hz with %d channels but the encoder was opened for %d Hz with %d: %w", f.SampleRate, f.Channels, e.cfg.SampleRate, e.cfg.Channels, codec.ErrInvalidData)
	case len(f.Data)%e.unit != 0:
		return fmt.Errorf("hwmediacodec: %d bytes are not a whole number of samples: %w", len(f.Data), codec.ErrInvalidData)
	}
	if len(e.pending) >= maxPending {
		return codec.ErrAgain
	}
	if e.h == 0 {
		if err := e.open(); err != nil {
			return err
		}
	}
	e.flushed = false
	if !e.started {
		e.started = true
		e.times.Start(f.PTS)
	}
	if len(f.Data) == 0 {
		return nil
	}
	e.times.Sent(len(f.Data) / e.unit)
	e.input = append(e.input, audioutil.Convert(f.Data, e.cfg.SampleFormat, codec.S16)...)
	return e.pump(maxPending)
}

// hasInput reports whether samples wait to be handed to the encoder.
func (e *encoder) hasInput() bool { return len(e.input) > 0 }

// pump hands the waiting 16-bit samples to the encoder, which takes what
// fills its next access unit at each call, and queues the packets it
// returns, until the input is used up or limit packets are queued. A frame
// longer than the queue is encoded in parts, the rest as Receive makes
// room.
func (e *encoder) pump(limit int) error {
	for len(e.pending) < limit && len(e.input) > 0 {
		// At most 4096 samples per channel a call, so that the sizes fit
		// the library's 32-bit fields whatever the frame's length.
		pcm := e.input
		if n := 4096 * e.cfg.Channels * 2; len(pcm) > n {
			pcm = pcm[:n]
		}
		in, out := unsafe.Pointer(&pcm[0]), unsafe.Pointer(&e.out[0])
		inID, inSize, inEl := int32(bufInAudio), int32(len(pcm)), int32(2)
		outID, outSize, outEl := int32(bufOutBitstream), int32(len(e.out)), int32(1)
		inDesc := bufDesc{NumBufs: 1, Bufs: &in, Identifiers: &inID, Sizes: &inSize, ElSizes: &inEl}
		outDesc := bufDesc{NumBufs: 1, Bufs: &out, Identifiers: &outID, Sizes: &outSize, ElSizes: &outEl}
		inArgs := encInArgs{NumInSamples: int32(len(pcm) / 2)}
		var outArgs encOutArgs
		code := encEncode(e.h, &inDesc, &outDesc, &inArgs, &outArgs)
		runtime.KeepAlive(pcm)
		if code != encOK {
			return backendErr("aacEncEncode", code)
		}
		if outArgs.NumOutBytes < 0 || int(outArgs.NumOutBytes) > len(e.out) {
			return backendErr("aacEncEncode", 0) // wrote past the buffer it was given
		}
		if outArgs.NumOutBytes > 0 {
			if p, keep := e.times.Next(append([]byte(nil), e.out[:outArgs.NumOutBytes]...)); keep {
				e.pending = append(e.pending, p)
			}
		}
		if outArgs.NumInSamples <= 0 && outArgs.NumOutBytes <= 0 {
			return backendErr("aacEncEncode", 0) // no progress
		}
		if outArgs.NumInSamples < 0 || int(outArgs.NumInSamples) > len(pcm)/2 {
			return backendErr("aacEncEncode", 0) // took more than it was given
		}
		e.input = e.input[int(outArgs.NumInSamples)*2:]
	}
	if len(e.input) == 0 {
		e.input = nil
	}
	return nil
}

func (e *encoder) Receive(ctx context.Context) (codec.Packet, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.Packet{}, codec.ErrClosed
	}
	if len(e.pending) < maxPending && e.hasInput() {
		// Room for the samples Send left unencoded.
		if err := e.pump(maxPending); err != nil {
			return codec.Packet{}, err
		}
	}
	if len(e.pending) > 0 {
		p := e.pending[0]
		e.pending[0] = codec.Packet{}
		e.pending = e.pending[1:]
		return p, nil
	}
	if e.flushed {
		return codec.Packet{}, io.EOF
	}
	return codec.Packet{}, codec.ErrAgain
}

// Flush implements codec.AudioEncoder. The encoder is sent enough silence
// to push its last samples out; the packets that code nothing but that
// silence are dropped as they arrive. The next signal gets a new encoder,
// so that it starts like the first.
func (e *encoder) Flush(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	var err error
	if e.started && e.h != 0 {
		// What Send left unencoded goes first, so pending may exceed
		// maxPending by what one frame codes to.
		e.input = append(e.input, make([]byte, (e.format.Delay+2*e.format.FrameSamples)*e.cfg.Channels*2)...)
		err = e.pump(math.MaxInt)
		e.release()
	}
	e.input = nil
	e.started, e.flushed = false, true
	return err
}

func (e *encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	e.release()
	e.pending = nil
	e.input = nil
	return nil
}
