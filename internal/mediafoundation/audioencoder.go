//go:build windows && (amd64 || arm64)

package mediafoundation

import (
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"sync"

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/audioutil"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediafoundation/sys"
)

// What the AAC encoder of Media Foundation offers (the "AAC Encoder"
// page of its documentation): AAC-LC at 44100 or 48000 Hz, and for mono
// and stereo four bitrates. It has one rate control, which holds the
// bitrate.
var (
	aacEncoderRates    = []int{44100, 48000}
	aacEncoderBitrates = []int{96000, 128000, 160000, 192000}
)

const (
	aacFrameSamples = 1024
	// aacProfileLC is the profile and level the encoder is asked for: the
	// AAC profile at level 2.
	aacProfileLC = 0x29
	// assumedAACDelay is the encoder's delay where the trial cannot
	// measure it: one packet.
	assumedAACDelay = 1024
)

// knownAudioDelays remembers the measured delay of the encoder for a
// sample rate and channel count.
var (
	knownAudioDelaysMu sync.Mutex
	knownAudioDelays   = map[string]int{}
)

// NewAudioEncoder implements codec.AudioBackend.
func (b Backend) NewAudioEncoder(ctx context.Context, cfg codec.AudioEncoderConfig) (codec.AudioEncoder, error) {
	c := cfg.Codec
	switch {
	case c == codec.Opus:
		return nil, unsupportedAudio(c, codec.Encode, "Media Foundation has no Opus encoder")
	case c != codec.AAC:
		return nil, unsupportedAudio(c, codec.Encode, "only aac encoding is implemented")
	case cfg.SampleFormat.BytesPerSample() == 0:
		return nil, unsupportedAudio(c, codec.Encode, "sample format "+cfg.SampleFormat.String()+" is not supported; use S16 or F32")
	case cfg.Channels < 1 || cfg.Channels > 2:
		return nil, unsupportedAudio(c, codec.Encode, fmt.Sprintf("%d channels are not supported; mono and stereo are", cfg.Channels))
	case !slices.Contains(aacEncoderRates, cfg.SampleRate):
		return nil, unsupportedAudio(c, codec.Encode, fmt.Sprintf("the AAC encoder of Media Foundation does not take %d Hz; it takes 44100 and 48000 Hz", cfg.SampleRate))
	}
	if cfg.Bitrate == 0 {
		cfg.Bitrate = 64000 + 32000*cfg.Channels
	}
	if !slices.Contains(aacEncoderBitrates, cfg.Bitrate) {
		return nil, unsupportedAudio(c, codec.Encode, fmt.Sprintf("the AAC encoder of Media Foundation does not offer %d bit/s; it offers 96000, 128000, 160000 and 192000", cfg.Bitrate))
	}
	if err := sys.Load(); err != nil {
		return nil, unsupportedAudio(c, codec.Encode, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e, err := newAudioEncoder(cfg)
	if err != nil {
		return nil, err
	}
	// Media Foundation does not say how many samples its encoder starts
	// early, so the first encoder for a kind of stream measures it: a
	// trial signal goes through an encoder of its own and through the
	// decoder (see audioutil.MeasureDelay).
	key := fmt.Sprintf("%d %d", cfg.SampleRate, cfg.Channels)
	knownAudioDelaysMu.Lock()
	delay, known := knownAudioDelays[key]
	knownAudioDelaysMu.Unlock()
	if !known {
		delay = assumedAACDelay
		if measured, ok := b.measureAudioDelay(ctx, cfg, e.format); ok {
			delay = measured
			knownAudioDelaysMu.Lock()
			knownAudioDelays[key] = delay
			knownAudioDelaysMu.Unlock()
		}
	}
	e.format.Delay, e.times.Delay = delay, delay
	return e, nil
}

func (b Backend) measureAudioDelay(ctx context.Context, cfg codec.AudioEncoderConfig, format codec.AudioFormat) (int, bool) {
	trial, err := newAudioEncoder(cfg)
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

type audioEncoder struct {
	mu     sync.Mutex
	cfg    codec.AudioEncoderConfig
	format codec.AudioFormat
	unit   int // one sample of every channel as it is sent
	// x is the transform of the signal under way, or nil between two.
	x     *audioTransform
	times audioutil.PacketTimes
	// fifo holds the 16-bit samples not encoded yet: less than a packet's
	// worth, or the rest of the frame Send accepted last while pending is
	// full (Send refuses more until Receive has made room); fed
	// counts the samples handed to the transform.
	fifo []byte
	fed  int64

	started bool
	pending []codec.Packet
	flushed bool
	closed  bool
}

func newAudioEncoder(cfg codec.AudioEncoderConfig) (*audioEncoder, error) {
	e := &audioEncoder{
		cfg:  cfg,
		unit: cfg.Channels * cfg.SampleFormat.BytesPerSample(),
		format: codec.AudioFormat{
			Codec:        codec.AAC,
			SampleRate:   cfg.SampleRate,
			Channels:     cfg.Channels,
			FrameSamples: aacFrameSamples,
		},
	}
	e.times = audioutil.PacketTimes{SampleRate: cfg.SampleRate, TimeScale: cfg.TimeScale, Frame: aacFrameSamples}
	if err := e.open(); err != nil {
		return nil, err
	}
	return e, nil
}

// open activates the encoder and sets its types: 16-bit samples in, raw
// AAC-LC access units out.
func (e *audioEncoder) open() error {
	done, err := comThread()
	if err != nil {
		return err
	}
	defer done()
	cands, err := audioEncoders(&sys.MFAudioFormat_AAC)
	if err != nil {
		return err
	}
	x, why := openAudioTransform(cands)
	releaseCandidates(cands)
	if x == nil {
		return unsupportedAudio(codec.AAC, codec.Encode, "no Media Foundation encoder for aac: "+why)
	}
	rate, channels := e.cfg.SampleRate, e.cfg.Channels
	in, err := newAudioType(&sys.MFAudioFormat_PCM, rate, channels)
	if err != nil {
		x.release()
		return err
	}
	defer in.Release()
	a := in.Attributes()
	a.SetUINT32(&sys.MF_MT_AUDIO_BITS_PER_SAMPLE, 16)
	a.SetUINT32(&sys.MF_MT_AUDIO_BLOCK_ALIGNMENT, uint32(channels*2))
	a.SetUINT32(&sys.MF_MT_AUDIO_AVG_BYTES_PER_SECOND, uint32(rate*channels*2))
	out, err := newAudioType(&sys.MFAudioFormat_AAC, rate, channels)
	if err != nil {
		x.release()
		return err
	}
	defer out.Release()
	a = out.Attributes()
	a.SetUINT32(&sys.MF_MT_AUDIO_BITS_PER_SAMPLE, 16)
	a.SetUINT32(&sys.MF_MT_AUDIO_AVG_BYTES_PER_SECOND, uint32(e.cfg.Bitrate/8))
	a.SetUINT32(&sys.MF_MT_AAC_PAYLOAD_TYPE, 0)
	a.SetUINT32(&sys.MF_MT_AAC_AUDIO_PROFILE_LEVEL_INDICATION, aacProfileLC)

	// An encoder wants to know what it makes before what it is given;
	// should this one see it the other way round, that order is tried
	// too.
	hrOut := x.t.SetOutputType(x.out, out, 0)
	hrIn := x.t.SetInputType(x.in, in, 0)
	if hrOut.Failed() && !hrIn.Failed() {
		hrOut = x.t.SetOutputType(x.out, out, 0)
	}
	if hrIn.Failed() || hrOut.Failed() {
		x.release()
		hr := hrOut
		if !hr.Failed() {
			hr = hrIn
		}
		return unsupportedAudio(codec.AAC, codec.Encode, fmt.Sprintf("%s does not take %d Hz with %d channels at %d bit/s: %s", x.name, rate, channels, e.cfg.Bitrate, hr))
	}
	if err := x.readOutputInfo(); err != nil {
		x.release()
		return err
	}
	// The AudioSpecificConfig follows the twelve bytes of HEAACWAVEINFO
	// in the user data of the output type.
	asc, _ := aac.LCConfig(rate, channels)
	var cur *sys.IMFMediaType
	if hr := x.t.GetOutputCurrentType(x.out, &cur); !hr.Failed() && cur != nil {
		if user, ok := cur.Attributes().Blob(&sys.MF_MT_USER_DATA); ok && len(user) >= 14 {
			if _, err := aac.ParseConfig(user[12:]); err == nil {
				asc = user[12:]
			}
		}
		cur.Release()
	}
	if len(asc) == 0 {
		x.release()
		return unsupportedAudio(codec.AAC, codec.Encode, "no AudioSpecificConfig for this stream")
	}
	e.format.Config = asc
	e.x, e.fifo, e.fed = x, e.fifo[:0], 0
	return nil
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
	if len(e.pending) >= maxAudioPending {
		return codec.ErrAgain
	}
	if e.x == nil {
		if err := e.open(); err != nil {
			return err
		}
	}
	e.flushed = false
	if !e.started {
		e.started = true
		e.times.Start(f.PTS)
		// Priming: the encoder codes the signal from its first sample
		// without the frame of silence before it that AAC encoders
		// usually add, so the first half-window (about 512 samples) came
		// out of every decoder faded in from nothing. One frame of silence
		// first lets the overlap rebuild it; the delay measurement counts
		// it, so Format().Delay tells callers to drop it.
		e.fifo = append(e.fifo, make([]byte, aacFrameSamples*e.cfg.Channels*2)...)
	}
	if len(f.Data) == 0 {
		return nil
	}
	e.times.Sent(len(f.Data) / e.unit)
	e.fifo = append(e.fifo, audioutil.Convert(f.Data, e.cfg.SampleFormat, codec.S16)...)
	return e.encode(false, maxAudioPending)
}

// hasInput reports whether a packet's worth of samples waits in the fifo.
func (e *audioEncoder) hasInput() bool { return len(e.fifo) >= aacFrameSamples*e.cfg.Channels*2 }

// encode hands the transform the samples of the queue one packet's worth
// at a time (all of them when the signal ends) and collects the access
// units it returns, until fewer than limit packets are queued. A frame
// longer than the queue is encoded in parts, the rest as Receive makes
// room.
func (e *audioEncoder) encode(all bool, limit int) error {
	if err := e.x.begin(); err != nil {
		return err
	}
	block := aacFrameSamples * e.cfg.Channels * 2
	for len(e.pending) < limit && (len(e.fifo) >= block || (all && len(e.fifo) > 0)) {
		n := min(block, len(e.fifo))
		samples := int64(n / (e.cfg.Channels * 2))
		at := samplesHNS(e.fed, e.cfg.SampleRate)
		duration := samplesHNS(e.fed+samples, e.cfg.SampleRate) - at
		hr := e.x.input(e.fifo[:n], at, duration)
		if hr == sys.MF_E_NOTACCEPTING {
			if err := e.fetch(); err != nil {
				return err
			}
			hr = e.x.input(e.fifo[:n], at, duration)
		}
		if hr.Failed() {
			return backendErr("IMFTransform::ProcessInput", hr)
		}
		e.fed += samples
		e.fifo = e.fifo[n:]
		if err := e.fetch(); err != nil {
			return err
		}
	}
	if len(e.fifo) == 0 {
		e.fifo = nil
	}
	return nil
}

// fetch takes the access units the transform has ready, one to a sample.
func (e *audioEncoder) fetch() error {
	for {
		data, state, err := e.x.output()
		if err != nil {
			return err
		}
		switch state {
		case outputEmpty:
			return nil
		case outputChange:
			return &codec.BackendError{Backend: Name, Op: "IMFTransform::ProcessOutput", Message: "the encoder changed its output type"}
		}
		if len(data) == 0 {
			continue
		}
		if p, keep := e.times.Next(data); keep {
			e.pending = append(e.pending, p)
		}
	}
}

func (e *audioEncoder) Receive(ctx context.Context) (codec.Packet, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.Packet{}, codec.ErrClosed
	}
	if len(e.pending) < maxAudioPending && e.hasInput() && e.x != nil {
		// Room for the samples Send left unencoded.
		if err := e.encode(false, maxAudioPending); err != nil {
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
// to push its last samples out and then drained; the packets that code
// nothing but that silence are dropped as they arrive. The next signal
// gets a new transform, so that it starts like the first.
func (e *audioEncoder) Flush(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	var err error
	if e.started && e.x != nil {
		silence := (e.format.Delay + 2*aacFrameSamples) * e.cfg.Channels * 2
		e.fifo = append(e.fifo, make([]byte, silence)...)
		// What Send left unencoded goes too, so pending may exceed
		// maxAudioPending by what one frame codes to.
		if err = e.encode(true, math.MaxInt); err == nil {
			if err = e.x.drain(); err == nil {
				err = e.fetch()
			}
		}
		e.x.release()
		e.x = nil
	}
	e.fifo = nil
	e.started, e.flushed = false, true
	return err
}

func (e *audioEncoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.x != nil {
		e.x.release()
		e.x = nil
	}
	e.pending = nil
	e.fifo = nil
	return nil
}
