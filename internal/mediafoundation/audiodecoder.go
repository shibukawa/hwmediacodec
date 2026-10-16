//go:build windows && (amd64 || arm64)

package mediafoundation

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/audioutil"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediafoundation/sys"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// NewAudioDecoder implements codec.AudioBackend.
func (Backend) NewAudioDecoder(ctx context.Context, cfg codec.AudioDecoderConfig) (codec.AudioDecoder, error) {
	f := cfg.Format
	subtype, ok := audioSubtype(f.Codec)
	if !ok {
		return nil, unsupportedAudio(f.Codec, codec.Decode, "only aac and opus decoding are implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupportedAudio(f.Codec, codec.Decode, "sample format "+cfg.SampleFormat.String()+" is not supported; use S16 or F32")
	}
	// What the input type says: the rate and channel count of the decoded
	// samples, and the codec's configuration as the user data of the
	// type.
	var sampleRate, channels, frame int
	var user []byte
	switch f.Codec {
	case codec.AAC:
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
		sampleRate, channels, frame = c.OutputSampleRate(), c.OutputChannels(), c.OutputFrameSamples()
		if channels == 0 {
			channels = f.Channels
		}
		if sampleRate <= 0 || channels <= 0 {
			return nil, unsupportedAudio(f.Codec, codec.Decode, "the AudioSpecificConfig does not give the sample rate and channel count")
		}
		if channels > 6 {
			return nil, unsupportedAudio(f.Codec, codec.Decode, fmt.Sprintf("%d channels are not supported; mono to 5.1 are", channels))
		}
		// The part of HEAACWAVEINFO behind its WAVEFORMATEX (payload
		// type 0 for raw access units, an unknown profile and level, no
		// structure type, reserved fields), then the AudioSpecificConfig.
		user = append([]byte{0, 0, 0xFE, 0, 0, 0, 0, 0, 0, 0, 0, 0}, asc...)
	case codec.Opus:
		sampleRate, channels = opus.SampleRate, f.Channels
		if len(f.Config) > 0 {
			h, err := opus.ParseHead(f.Config)
			if err != nil {
				return nil, fmt.Errorf("hwmediacodec: AudioFormat.Config: %w: %w", err, codec.ErrInvalidData)
			}
			if h.MappingFamily != 0 {
				return nil, unsupportedAudio(f.Codec, codec.Decode, "only mono and stereo Opus streams (channel mapping family 0) are supported")
			}
			channels = h.Channels
		}
		if channels == 0 {
			channels = 2
		}
		if channels > 2 {
			return nil, unsupportedAudio(f.Codec, codec.Decode, "only mono and stereo Opus streams are supported")
		}
		// An identification header without pre-skip, so that the decoder
		// drops nothing: the caller gets every sample.
		user = opus.Head{Channels: channels, InputSampleRate: opus.SampleRate}.Marshal()
	}
	if err := sys.Load(); err != nil {
		return nil, unsupportedAudio(f.Codec, codec.Decode, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	done, err := comThread()
	if err != nil {
		return nil, err
	}
	defer done()
	cands, err := audioDecoders(subtype)
	if err != nil {
		return nil, err
	}
	x, why := openAudioTransform(cands)
	releaseCandidates(cands)
	if x == nil {
		return nil, unsupportedAudio(f.Codec, codec.Decode, "no Media Foundation decoder for "+f.Codec.String()+": "+why)
	}
	d := &audioDecoder{cfg: cfg, x: x, frame: frame}
	d.frames = audioutil.Frames{SampleRate: sampleRate, Channels: channels, Format: cfg.SampleFormat}

	mt, err := newAudioType(subtype, sampleRate, channels)
	if err != nil {
		x.release()
		return nil, err
	}
	a := mt.Attributes()
	a.SetUINT32(&sys.MF_MT_AUDIO_BITS_PER_SAMPLE, 16)
	if f.Codec == codec.AAC {
		a.SetUINT32(&sys.MF_MT_AAC_PAYLOAD_TYPE, 0)
		a.SetUINT32(&sys.MF_MT_AAC_AUDIO_PROFILE_LEVEL_INDICATION, 0xFE)
	}
	a.SetBlob(&sys.MF_MT_USER_DATA, user)
	hr := x.t.SetInputType(x.in, mt, 0)
	mt.Release()
	if hr.Failed() {
		x.release()
		return nil, unsupportedAudio(f.Codec, codec.Decode, fmt.Sprintf("%s does not take the stream: %s", x.name, hr))
	}
	if err := d.negotiate(channels); err != nil {
		x.release()
		return nil, err
	}
	return d, nil
}

type audioDecoder struct {
	mu  sync.Mutex
	cfg codec.AudioDecoderConfig
	x   *audioTransform
	// frame is the number of samples an AAC packet decodes to; an Opus
	// packet says its own.
	frame int
	// float is set when the transform returns 32-bit float samples
	// instead of 16-bit integers.
	float  bool
	frames audioutil.Frames
	hns    int64 // the time of the next input sample

	flushed bool
	closed  bool
}

// negotiate chooses the output type: 16-bit integer samples where the
// transform offers them, float samples otherwise, and of the layouts it
// lists (a decoder offers a stereo mix of 5.1 as well) the one with want
// channels, or the first when want is 0. It runs at the start and whenever
// the transform finds the stream to be something else than announced.
func (d *audioDecoder) negotiate(want int) error {
	c := d.cfg.Format.Codec
	var best *sys.IMFMediaType
	bestScore := -1
	for i := uint32(0); ; i++ {
		var mt *sys.IMFMediaType
		hr := d.x.t.GetOutputAvailableType(d.x.out, i, &mt)
		if hr == sys.MF_E_NO_MORE_TYPES {
			break
		}
		if hr.Failed() {
			if best != nil {
				best.Release()
			}
			return backendErr("IMFTransform::GetOutputAvailableType", hr)
		}
		a := mt.Attributes()
		sub, _ := a.GUID(&sys.MF_MT_SUBTYPE)
		bits, _ := a.UINT32(&sys.MF_MT_AUDIO_BITS_PER_SAMPLE)
		ch, _ := a.UINT32(&sys.MF_MT_AUDIO_NUM_CHANNELS)
		score := -1
		switch {
		case sub == sys.MFAudioFormat_PCM && bits == 16:
			score = 2
		case sub == sys.MFAudioFormat_Float && bits == 32:
			score = 1
		}
		if score >= 0 && want > 0 && int(ch) == want {
			score += 4
		}
		if score > bestScore {
			if best != nil {
				best.Release()
			}
			best, bestScore = mt, score
		} else {
			mt.Release()
		}
	}
	if best == nil {
		return unsupportedAudio(c, codec.Decode, d.x.name+" offers no PCM output for this stream")
	}
	defer best.Release()
	if hr := d.x.t.SetOutputType(d.x.out, best, 0); hr.Failed() {
		return backendErr("IMFTransform::SetOutputType", hr)
	}
	a := best.Attributes()
	sub, _ := a.GUID(&sys.MF_MT_SUBTYPE)
	d.float = sub == sys.MFAudioFormat_Float
	if ch, ok := a.UINT32(&sys.MF_MT_AUDIO_NUM_CHANNELS); ok && ch > 0 {
		d.frames.Channels = int(ch)
	}
	if rate, ok := a.UINT32(&sys.MF_MT_AUDIO_SAMPLES_PER_SECOND); ok && rate > 0 && int(rate) != d.frames.SampleRate {
		// Band replication that only the packets signal.
		d.frames.Scale(int(rate), d.frames.SampleRate)
		d.frame = d.frame * int(rate) / d.frames.SampleRate
		d.frames.SampleRate = int(rate)
	}
	return d.x.readOutputInfo()
}

func (d *audioDecoder) Send(ctx context.Context, p codec.Packet) error {
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
	if d.frames.Ready() >= maxAudioPending {
		return codec.ErrAgain
	}
	samples := d.frame
	if d.cfg.Format.Codec == codec.Opus {
		n, err := opus.PacketSamples(p.Data)
		if err != nil {
			return fmt.Errorf("hwmediacodec: %w: %w", err, codec.ErrInvalidData)
		}
		samples = n
	}
	if err := d.x.begin(); err != nil {
		return err
	}
	d.flushed = false
	duration := samplesHNS(int64(samples), d.frames.SampleRate)
	hr := d.x.input(p.Data, d.hns, duration)
	if hr == sys.MF_E_NOTACCEPTING {
		if err := d.fetch(); err != nil {
			return d.invalid(err)
		}
		if hr = d.x.input(p.Data, d.hns, duration); hr == sys.MF_E_NOTACCEPTING {
			return codec.ErrAgain
		}
	}
	if hr.Failed() {
		return d.invalid(backendErr("IMFTransform::ProcessInput", hr))
	}
	d.hns += duration
	d.frames.Expect(p.PTS, samples)
	if err := d.fetch(); err != nil {
		return d.invalid(err)
	}
	return nil
}

// invalid reports a packet the transform refused. The packets waiting for
// their samples are forgotten and the transform starts afresh, so that
// the packets that follow decode.
func (d *audioDecoder) invalid(err error) error {
	d.frames.Reset()
	d.x.reset()
	d.hns = 0
	return fmt.Errorf("%w: %w", err, codec.ErrInvalidData)
}

// fetch takes the output the transform has ready.
func (d *audioDecoder) fetch() error {
	for changes := 0; changes < 4; {
		data, state, err := d.x.output()
		if err != nil {
			return err
		}
		switch state {
		case outputEmpty:
			return nil
		case outputChange:
			changes++
			if err := d.negotiate(0); err != nil {
				return err
			}
		default:
			from := codec.S16
			if d.float {
				from = codec.F32
			}
			d.frames.Append(audioutil.Convert(data, from, d.cfg.SampleFormat))
		}
	}
	return &codec.BackendError{Backend: Name, Op: "IMFTransform::ProcessOutput", Message: "repeated output format changes"}
}

func (d *audioDecoder) Receive(ctx context.Context) (*codec.AudioFrame, error) {
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

// Flush implements codec.AudioDecoder: the transform gives out what it
// holds back and forgets the stream, so that the next packet does not
// lean on the ones before.
func (d *audioDecoder) Flush(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	var err error
	if d.x.started && !d.flushed {
		if err = d.x.drain(); err == nil {
			err = d.fetch()
		}
		d.x.reset()
		d.hns = 0
	}
	d.frames.End()
	d.flushed = true
	return err
}

func (d *audioDecoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.x.release()
	d.frames.Reset()
	d.frames.Drop()
	return nil
}
