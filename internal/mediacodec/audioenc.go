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

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/audioutil"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediacodec/sys"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// Bitrates used when none is asked for: MediaCodec's encoders want one.
const (
	defaultAACBitrate  = 64000 // per channel
	defaultOpusBitrate = 48000 // per channel
)

// Delays to assume where the trial cannot measure one: the ones measured
// with the framework's encoders on the Android 14 emulator.
const (
	assumedAACDelay  = 2048
	assumedOpusDelay = 312
)

// knownAudioDelays remembers the measured delay of the encoder for a
// codec, sample rate and channel count.
var (
	knownAudioDelaysMu sync.Mutex
	knownAudioDelays   = map[string]int{}
)

// NewAudioEncoder implements codec.AudioBackend.
func (b Backend) NewAudioEncoder(ctx context.Context, cfg codec.AudioEncoderConfig) (codec.AudioEncoder, error) {
	c := cfg.Codec
	mime, ok := audioMime(c)
	if !ok {
		return nil, unsupported(c, codec.Encode, "only aac and opus encoding are implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupported(c, codec.Encode, "sample format "+cfg.SampleFormat.String()+" is not supported; use S16 or F32")
	}
	if cfg.Channels < 1 || cfg.Channels > 2 {
		return nil, unsupported(c, codec.Encode, fmt.Sprintf("%d channels are not supported; mono and stereo are", cfg.Channels))
	}
	if c == codec.AAC && !aac.StandardRate(cfg.SampleRate) {
		// MediaCodec configures for any rate and then codes something else.
		return nil, unsupported(c, codec.Encode, fmt.Sprintf("the AAC encoder does not take %d Hz; it takes the rates of the AAC table (8000 to 96000 Hz)", cfg.SampleRate))
	}
	if c == codec.Opus && cfg.SampleRate != opus.SampleRate {
		return nil, unsupported(c, codec.Encode, fmt.Sprintf("the Opus encoder takes %d Hz input, not %d Hz; resample first", opus.SampleRate, cfg.SampleRate))
	}
	if err := sys.Load(); err != nil {
		return nil, unsupported(c, codec.Encode, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e, err := newAudioEncoder(cfg, mime, 0)
	if err != nil {
		return nil, err
	}
	// MediaCodec does not say by how many samples its encoder starts
	// early, so the first encoder for a kind of stream measures it: a
	// trial signal goes through an encoder of its own and through the
	// decoder (see audioutil.MeasureDelay).
	key := fmt.Sprintf("%s %d %d", c, cfg.SampleRate, cfg.Channels)
	knownAudioDelaysMu.Lock()
	delay, known := knownAudioDelays[key]
	knownAudioDelaysMu.Unlock()
	if !known {
		delay = assumedAACDelay
		if c == codec.Opus {
			delay = assumedOpusDelay
		}
		if measured, ok := b.measureDelay(ctx, cfg, mime, e.format); ok {
			delay = measured
			knownAudioDelaysMu.Lock()
			knownAudioDelays[key] = delay
			knownAudioDelaysMu.Unlock()
		}
	}
	e.setDelay(delay)
	return e, nil
}

// measureDelay runs the trial for cfg with an encoder and a decoder of
// its own.
func (b Backend) measureDelay(ctx context.Context, cfg codec.AudioEncoderConfig, mime string, format codec.AudioFormat) (int, bool) {
	trial, err := newAudioEncoder(cfg, mime, 0)
	if err != nil {
		return 0, false
	}
	defer trial.Close()
	// The decoder is given the stream without its delay: it must return
	// every sample.
	format.Delay = 0
	if cfg.Codec == codec.Opus {
		format.Config = nil
	}
	dec, err := b.NewAudioDecoder(ctx, codec.AudioDecoderConfig{Format: format, SampleFormat: codec.F32})
	if err != nil {
		return 0, false
	}
	defer dec.Close()
	return audioutil.MeasureDelay(ctx, trial, dec, cfg.SampleFormat, cfg.SampleRate, cfg.Channels, format.FrameSamples)
}

type audioEncoder struct {
	mu     sync.Mutex
	cfg    codec.AudioEncoderConfig
	mime   string
	format codec.AudioFormat
	// unit is the size of one sample of every channel as it is sent.
	unit int

	codec   uintptr
	times   audioutil.PacketTimes
	started bool // a signal is being encoded
	// sent counts the samples handed to the codec, the silence of Flush
	// included, for the input timestamps.
	sent int64
	// input holds 16-bit samples not handed to the codec yet: the rest of
	// the frame Send accepted last while pending is full (Send refuses
	// more until Receive has made room).
	input []byte

	pending   []codec.Packet
	eosQueued bool
	eosSeen   bool
	flushed   bool
	closed    bool
}

func newAudioEncoder(cfg codec.AudioEncoderConfig, mime string, delay int) (*audioEncoder, error) {
	e := &audioEncoder{
		cfg:  cfg,
		mime: mime,
		unit: cfg.Channels * cfg.SampleFormat.BytesPerSample(),
		format: codec.AudioFormat{
			Codec:      cfg.Codec,
			SampleRate: cfg.SampleRate,
			Channels:   cfg.Channels,
		},
	}
	switch cfg.Codec {
	case codec.AAC:
		asc, err := aac.LCConfig(cfg.SampleRate, cfg.Channels)
		if err != nil {
			return nil, unsupported(cfg.Codec, codec.Encode, err.Error())
		}
		e.format.Config, e.format.FrameSamples = asc, 1024
	case codec.Opus:
		e.format.FrameSamples = 960
	}
	scale := cfg.TimeScale
	if scale <= 0 {
		scale = int32(cfg.SampleRate)
	}
	e.times = audioutil.PacketTimes{SampleRate: cfg.SampleRate, TimeScale: scale, Frame: e.format.FrameSamples}
	e.setDelay(delay)
	if err := e.open(); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

// setDelay records the number of samples the encoder starts early by.
func (e *audioEncoder) setDelay(delay int) {
	e.format.Delay, e.times.Delay = delay, delay
	if e.cfg.Codec == codec.Opus {
		e.format.Config = opus.Head{Channels: e.cfg.Channels, PreSkip: delay, InputSampleRate: e.cfg.SampleRate}.Marshal()
	}
}

// open creates, configures and starts a codec. Every signal gets one of
// its own, so that it starts like the first.
func (e *audioEncoder) open() error {
	c := sys.AMediaCodec_createEncoderByType(e.mime)
	if c == 0 {
		return unsupported(e.cfg.Codec, codec.Encode, "MediaCodec has no encoder for "+e.mime)
	}
	e.codec = c
	format := sys.AMediaFormat_new()
	defer sys.AMediaFormat_delete(format)
	bitrate := e.cfg.Bitrate
	if bitrate == 0 {
		bitrate = defaultAACBitrate * e.cfg.Channels
		if e.cfg.Codec == codec.Opus {
			bitrate = defaultOpusBitrate * e.cfg.Channels
		}
	}
	sys.AMediaFormat_setString(format, sys.KeyMime, e.mime)
	sys.AMediaFormat_setInt32(format, sys.KeySampleRate, int32(e.cfg.SampleRate))
	sys.AMediaFormat_setInt32(format, sys.KeyChannelCount, int32(e.cfg.Channels))
	sys.AMediaFormat_setInt32(format, sys.KeyBitrate, int32(bitrate))
	if e.cfg.Codec == codec.AAC {
		sys.AMediaFormat_setInt32(format, sys.KeyAACProfile, sys.AACObjectLC)
	}
	if e.cfg.RateControl == codec.CBR {
		sys.AMediaFormat_setInt32(format, sys.KeyBitrateMode, sys.BitrateModeCBR)
	}
	if st := sys.AMediaCodec_configure(c, format, 0, 0, sys.ConfigureFlagEncode); st != sys.OK {
		return unsupported(e.cfg.Codec, codec.Encode, fmt.Sprintf("%s does not take the configuration (%s): %s", sys.CodecName(c), sys.FormatString(format), sys.StatusString(st)))
	}
	if st := sys.AMediaCodec_start(c); st != sys.OK {
		return backendErr("AMediaCodec_start", st)
	}
	e.eosQueued, e.eosSeen, e.sent = false, false, 0
	return nil
}

func (e *audioEncoder) release() {
	if e.codec != 0 {
		sys.AMediaCodec_stop(e.codec)
		sys.AMediaCodec_delete(e.codec)
		e.codec = 0
	}
}

func (e *audioEncoder) Format() codec.AudioFormat {
	e.mu.Lock()
	defer e.mu.Unlock()
	f := e.format
	f.Config = append([]byte(nil), f.Config...)
	return f
}

func (e *audioEncoder) Send(ctx context.Context, f *codec.AudioFrame) error {
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
	if e.codec == 0 {
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
	return e.pump(ctx, maxPending)
}

// hasInput reports whether samples wait to be handed to the codec.
func (e *audioEncoder) hasInput() bool { return len(e.input) > 0 }

// pump hands the waiting 16-bit samples to the codec, as many input
// buffers as they take, while fewer than limit packets are queued. A frame
// longer than the queue is handed over in parts, the rest as Receive makes
// room; the queue can pass limit only by what the codec's few buffers in
// flight hold.
func (e *audioEncoder) pump(ctx context.Context, limit int) error {
	unit := e.cfg.Channels * 2
	for len(e.input) > 0 && len(e.pending) < limit {
		pcm := e.input
		idx, err := dequeueInput(ctx, e.codec, e.drainReady)
		if err != nil {
			return err
		}
		var size uintptr
		buf := sys.AMediaCodec_getInputBuffer(e.codec, uintptr(idx), &size)
		n := min(len(pcm), int(size)/unit*unit)
		if buf == nil || n == 0 {
			return &codec.BackendError{Backend: Name, Op: "AMediaCodec_getInputBuffer", Message: fmt.Sprintf("input buffer of %d bytes", size)}
		}
		copy(unsafe.Slice(buf, size), pcm[:n])
		us := e.sent * 1_000_000 / int64(e.cfg.SampleRate)
		if st := sys.AMediaCodec_queueInputBuffer(e.codec, uintptr(idx), 0, uintptr(n), uint64(us), 0); st != sys.OK {
			return backendErr("AMediaCodec_queueInputBuffer", st)
		}
		e.sent += int64(n / unit)
		e.input = pcm[n:]
	}
	if len(e.input) == 0 {
		e.input = nil
	}
	return e.drainReady()
}

func (e *audioEncoder) drainReady() error { return e.drain(0) }

// drain dequeues output until none is ready, waiting up to timeoutUs for
// the first one.
func (e *audioEncoder) drain(timeoutUs int64) error {
	for {
		var info sys.BufferInfo
		idx := sys.AMediaCodec_dequeueOutputBuffer(e.codec, &info, timeoutUs)
		switch {
		case idx >= 0:
			var size uintptr
			buf := sys.AMediaCodec_getOutputBuffer(e.codec, uintptr(idx), &size)
			start, end, ok := outputRange(size, info.Offset, info.Size)
			if buf != nil && info.Size > 0 && !ok {
				sys.AMediaCodec_releaseOutputBuffer(e.codec, uintptr(idx), false)
				return &codec.BackendError{Backend: Name, Op: "AMediaCodec_getOutputBuffer", Message: fmt.Sprintf("buffer of %d bytes, packet at %d+%d", size, info.Offset, info.Size)}
			}
			// The configuration buffers repeat what Format already says.
			if buf != nil && info.Size > 0 && info.Flags&sys.BufferFlagCodecConfig == 0 {
				data := append([]byte(nil), unsafe.Slice(buf, size)[start:end]...)
				if p, keep := e.times.Next(data); keep {
					e.pending = append(e.pending, p)
				}
			}
			sys.AMediaCodec_releaseOutputBuffer(e.codec, uintptr(idx), false)
			if info.Flags&sys.BufferFlagEndOfStream != 0 {
				e.eosSeen = true
				return nil
			}
			timeoutUs = 0
		case idx == sys.InfoOutputFormatChanged, idx == sys.InfoOutputBuffersChanged:
		case idx == sys.InfoTryAgainLater:
			return nil
		default:
			return backendErr("AMediaCodec_dequeueOutputBuffer", int32(idx))
		}
	}
}

func (e *audioEncoder) Receive(ctx context.Context) (codec.Packet, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.Packet{}, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return codec.Packet{}, err
	}
	if len(e.pending) < maxPending && e.hasInput() && e.codec != 0 {
		// Room for the samples Send left unencoded.
		if err := e.pump(ctx, maxPending); err != nil {
			return codec.Packet{}, err
		}
	}
	if len(e.pending) == 0 && e.codec != 0 && !e.flushed {
		if err := e.drainReady(); err != nil {
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
// to push its last samples out whatever it does at the end of a stream;
// the packets that code nothing but that silence are dropped as they
// arrive. The codec is then given up: the next signal gets a new one.
func (e *audioEncoder) Flush(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.started && e.codec != 0 {
		// What Send left unencoded goes first, so pending may exceed
		// maxPending by what one frame codes to.
		e.input = append(e.input, make([]byte, (e.format.Delay+2*e.format.FrameSamples)*e.cfg.Channels*2)...)
		if err := e.pump(ctx, math.MaxInt); err != nil {
			return err
		}
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
				return &codec.BackendError{Backend: Name, Op: "AMediaCodec_dequeueOutputBuffer", Message: "the " + e.mime + " encoder did not deliver its end of stream"}
			}
		}
		e.release()
	}
	e.input = nil
	e.started, e.flushed = false, true
	return nil
}

func (e *audioEncoder) Close() error {
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
