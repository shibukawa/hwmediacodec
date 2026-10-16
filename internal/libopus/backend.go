//go:build linux && !android

// Package libopus implements Opus decoding and encoding on Linux on top
// of the system's libopus, loaded at run time with purego. Linux has no
// codec API of its own; libopus is the reference implementation of the
// royalty-free codec and part of practically every distribution. Nothing
// is bundled: without the library the backend is absent.
package libopus

import (
	"context"
	"fmt"
	"io"
	"math"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/audioutil"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// Name is the backend identifier reported in Capability.Backend.
const Name = "libopus"

// maxPending bounds the frames and packets held for Receive; Send reports
// ErrAgain beyond it.
const maxPending = 256

// frameSamples is the packet length the encoder makes: 20 ms.
const frameSamples = 960

// maxPacket is the output buffer the libopus documentation recommends.
const maxPacket = 4000

// Backend is the libopus backend.
type Backend struct{}

// Name implements codec.AudioBackend.
func (Backend) Name() string { return Name }

func unsupported(dir codec.Direction, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: codec.Opus, Direction: dir, Reason: reason}
}

func backendErr(op string, code int32) error {
	return &codec.BackendError{Backend: Name, Op: op, Status: int64(code), Message: errorText(code)}
}

// ProbeAudio implements codec.AudioBackend.
func (Backend) ProbeAudio(ctx context.Context) ([]codec.Capability, error) {
	if load() != nil {
		return nil, nil
	}
	return []codec.Capability{
		{Backend: Name, Codec: codec.Opus, Direction: codec.Decode},
		{Backend: Name, Codec: codec.Opus, Direction: codec.Encode},
	}, ctx.Err()
}

// NewAudioDecoder implements codec.AudioBackend.
func (Backend) NewAudioDecoder(ctx context.Context, cfg codec.AudioDecoderConfig) (codec.AudioDecoder, error) {
	f := cfg.Format
	if f.Codec != codec.Opus {
		return nil, codec.NotImplemented(Name, f.Codec, codec.Decode, "only opus is implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupported(codec.Decode, "sample format "+cfg.SampleFormat.String()+" is not supported; use S16 or F32")
	}
	channels := f.Channels
	if len(f.Config) > 0 {
		h, err := opus.ParseHead(f.Config)
		if err != nil {
			return nil, fmt.Errorf("hwmediacodec: AudioFormat.Config: %w: %w", err, codec.ErrInvalidData)
		}
		if h.MappingFamily != 0 {
			return nil, unsupported(codec.Decode, "only mono and stereo Opus streams (channel mapping family 0) are supported")
		}
		channels = h.Channels
	}
	if channels == 0 {
		channels = 2
	}
	if channels > 2 {
		return nil, unsupported(codec.Decode, "only mono and stereo Opus streams are supported")
	}
	if err := load(); err != nil {
		return nil, unsupported(codec.Decode, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := &decoder{format: cfg.SampleFormat, channels: channels}
	if err := d.create(); err != nil {
		return nil, err
	}
	return d, nil
}

type decoder struct {
	mu       sync.Mutex
	format   codec.SampleFormat
	channels int
	st       uintptr
	out      []*codec.AudioFrame
	flushed  bool
	closed   bool
}

func (d *decoder) create() error {
	var code int32
	st := decoderCreate(opus.SampleRate, int32(d.channels), &code)
	if st == 0 || code != 0 {
		return backendErr("opus_decoder_create", code)
	}
	d.st = st
	return nil
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
	if len(d.out) >= maxPending {
		return codec.ErrAgain
	}
	d.flushed = false
	unit := d.channels * d.format.BytesPerSample()
	buf := make([]byte, opus.MaxPacketSamples*unit)
	var n int32
	if d.format == codec.F32 {
		n = decodeFloat(d.st, &p.Data[0], int32(len(p.Data)), (*float32)(unsafe.Pointer(&buf[0])), opus.MaxPacketSamples, 0)
	} else {
		n = decode(d.st, &p.Data[0], int32(len(p.Data)), (*int16)(unsafe.Pointer(&buf[0])), opus.MaxPacketSamples, 0)
	}
	if n < 0 {
		return fmt.Errorf("%w: %w", backendErr("opus_decode", n), codec.ErrInvalidData)
	}
	if n > opus.MaxPacketSamples {
		return backendErr("opus_decode", 0) // more samples than the buffer holds
	}
	d.out = append(d.out, &codec.AudioFrame{
		SampleRate: opus.SampleRate,
		Channels:   d.channels,
		Format:     d.format,
		Data:       buf[: int(n)*unit : int(n)*unit],
		PTS:        p.PTS,
	})
	return nil
}

func (d *decoder) Receive(ctx context.Context) (*codec.AudioFrame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, codec.ErrClosed
	}
	if len(d.out) > 0 {
		f := d.out[0]
		d.out[0] = nil
		d.out = d.out[1:]
		return f, nil
	}
	if d.flushed {
		return nil, io.EOF
	}
	return nil, codec.ErrAgain
}

// Flush implements codec.AudioDecoder. Every packet is decoded by the
// time Send returns; the state is replaced by a new one so that the next
// packet does not lean on the ones before.
func (d *decoder) Flush(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	d.flushed = true
	decoderDestroy(d.st)
	d.st = 0
	return d.create()
}

func (d *decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if d.st != 0 {
		decoderDestroy(d.st)
		d.st = 0
	}
	d.out = nil
	return nil
}

// NewAudioEncoder implements codec.AudioBackend.
func (Backend) NewAudioEncoder(ctx context.Context, cfg codec.AudioEncoderConfig) (codec.AudioEncoder, error) {
	if cfg.Codec != codec.Opus {
		return nil, codec.NotImplemented(Name, cfg.Codec, codec.Encode, "only opus is implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupported(codec.Encode, "sample format "+cfg.SampleFormat.String()+" is not supported; use S16 or F32")
	}
	if cfg.Channels < 1 || cfg.Channels > 2 {
		return nil, unsupported(codec.Encode, fmt.Sprintf("%d channels are not supported; mono and stereo are", cfg.Channels))
	}
	if cfg.SampleRate != opus.SampleRate {
		// libopus also codes 8, 12, 16 and 24 kHz input; the library keeps
		// to the one rate every backend takes.
		return nil, unsupported(codec.Encode, fmt.Sprintf("the Opus encoder takes %d Hz input, not %d Hz; resample first", opus.SampleRate, cfg.SampleRate))
	}
	if err := load(); err != nil {
		return nil, unsupported(codec.Encode, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e := &encoder{cfg: cfg, unit: cfg.Channels * cfg.SampleFormat.BytesPerSample(), out: make([]byte, maxPacket)}
	if err := e.create(); err != nil {
		return nil, err
	}
	return e, nil
}

type encoder struct {
	mu     sync.Mutex
	cfg    codec.AudioEncoderConfig
	format codec.AudioFormat
	unit   int // one sample of every channel
	st     uintptr
	times  audioutil.PacketTimes
	// fifo holds the samples not encoded yet: less than a packet's worth,
	// or the rest of the frame Send accepted last while pending is full
	// (Send refuses more until Receive has made room).
	fifo []byte
	out  []byte

	started bool
	pending []codec.Packet
	flushed bool
	closed  bool
}

// create makes an encoder state with the options of the configuration
// and reads its delay.
func (e *encoder) create() error {
	var code int32
	st := encoderCreate(opus.SampleRate, int32(e.cfg.Channels), applicationAudio, &code)
	if st == 0 || code != 0 {
		return backendErr("opus_encoder_create", code)
	}
	e.st = st
	if e.cfg.Bitrate > 0 {
		if code := encoderCtl(st, ctlSetBitrate, uintptr(e.cfg.Bitrate)); code != 0 {
			e.release()
			return unsupported(codec.Encode, fmt.Sprintf("libopus does not take %d bit/s: %s", e.cfg.Bitrate, errorText(code)))
		}
	}
	if e.cfg.RateControl == codec.CBR {
		if code := encoderCtl(st, ctlSetVBR, 0); code != 0 {
			e.release()
			return backendErr("opus_encoder_ctl(OPUS_SET_VBR)", code)
		}
	}
	var lookahead int32
	if code := encoderCtl(st, ctlGetLookahead, uintptr(unsafe.Pointer(&lookahead))); code != 0 {
		e.release()
		return backendErr("opus_encoder_ctl(OPUS_GET_LOOKAHEAD)", code)
	}
	e.format = codec.AudioFormat{
		Codec:        codec.Opus,
		SampleRate:   opus.SampleRate,
		Channels:     e.cfg.Channels,
		Config:       opus.Head{Channels: e.cfg.Channels, PreSkip: int(lookahead), InputSampleRate: opus.SampleRate}.Marshal(),
		FrameSamples: frameSamples,
		Delay:        int(lookahead),
	}
	e.times = audioutil.PacketTimes{SampleRate: opus.SampleRate, TimeScale: e.cfg.TimeScale, Frame: frameSamples, Delay: int(lookahead)}
	return nil
}

func (e *encoder) release() {
	if e.st != 0 {
		encoderDestroy(e.st)
		e.st = 0
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
	e.flushed = false
	if !e.started {
		e.started = true
		e.times.Start(f.PTS)
	}
	e.times.Sent(len(f.Data) / e.unit)
	e.fifo = append(e.fifo, f.Data...)
	return e.pump(maxPending)
}

// pump encodes whole packets from the fifo until fewer than a packet's
// worth of samples is left or limit packets are queued. A frame longer
// than the queue is encoded in parts, the rest as Receive makes room.
func (e *encoder) pump(limit int) error {
	size := frameSamples * e.unit
	for len(e.pending) < limit && len(e.fifo) >= size {
		if err := e.encodeFrame(e.fifo[:size]); err != nil {
			return err
		}
		e.fifo = e.fifo[size:]
	}
	if len(e.fifo) == 0 {
		e.fifo = nil
	}
	return nil
}

// hasInput reports whether a packet's worth of samples waits in the fifo.
func (e *encoder) hasInput() bool { return len(e.fifo) >= frameSamples*e.unit }

// encodeFrame codes one packet's worth of samples and queues the packet,
// unless it codes nothing of the signal.
func (e *encoder) encodeFrame(pcm []byte) error {
	var n int32
	if e.cfg.SampleFormat == codec.F32 {
		n = encodeFloat(e.st, (*float32)(unsafe.Pointer(&pcm[0])), frameSamples, &e.out[0], int32(len(e.out)))
	} else {
		n = encode(e.st, (*int16)(unsafe.Pointer(&pcm[0])), frameSamples, &e.out[0], int32(len(e.out)))
	}
	if n < 0 {
		return backendErr("opus_encode", n)
	}
	if p, keep := e.times.Next(append([]byte(nil), e.out[:n]...)); keep {
		e.pending = append(e.pending, p)
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

// Flush implements codec.AudioEncoder: what Send left unencoded is
// encoded at once (so pending may exceed maxPending by what one frame
// codes to), the samples that did not fill a packet go out padded with
// silence, followed by as many packets of silence as it takes to bring
// out what the encoder's look-ahead holds.
func (e *encoder) Flush(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	if e.started {
		if err := e.pump(math.MaxInt); err != nil {
			return err
		}
		size := frameSamples * e.unit
		// Packets needed for the signal and the delay in front of it.
		need := (e.times.Samples() + int64(e.format.Delay) + frameSamples - 1) / frameSamples
		for coded := e.times.Samples() / frameSamples; coded < need; coded++ {
			frame := make([]byte, size)
			copy(frame, e.fifo)
			e.fifo = nil
			if err := e.encodeFrame(frame); err != nil {
				return err
			}
		}
		// A new state for the next signal, so that it starts like the
		// first.
		e.release()
		if err := e.create(); err != nil {
			return err
		}
	}
	e.fifo = nil
	e.started, e.flushed = false, true
	return nil
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
	e.fifo = nil
	return nil
}
