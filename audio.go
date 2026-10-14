package hwmediacodec

import (
	"context"
	"errors"
	"fmt"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// Re-exported audio types. See the documentation on each in package codec.
type (
	SampleFormat        = codec.SampleFormat
	AudioFormat         = codec.AudioFormat
	AudioFrame          = codec.AudioFrame
	AudioDecoder        = codec.AudioDecoder
	AudioEncoder        = codec.AudioEncoder
	AudioDecoderOption  = codec.AudioDecoderOption
	AudioEncoderOption  = codec.AudioEncoderOption
	AudioOption         = codec.AudioOption
	SharedEncoderOption = codec.SharedEncoderOption
)

const (
	AAC  = codec.AAC
	Opus = codec.Opus

	S16 = codec.S16
	F32 = codec.F32
)

// S16Frame wraps interleaved 16-bit samples as an encoder input frame
// without copying: the frame shares the slice, which must stay untouched
// until AudioEncoder.Send has returned. The opposite direction is
// AudioFrame.Int16s.
func S16Frame(samples []int16, sampleRate, channels int, pts int64) *AudioFrame {
	return codec.S16Frame(samples, sampleRate, channels, pts)
}

// F32Frame wraps interleaved floating point samples as an encoder input
// frame without copying, for an encoder opened with WithSampleFormat(F32).
// The opposite direction is AudioFrame.Float32s.
func F32Frame(samples []float32, sampleRate, channels int, pts int64) *AudioFrame {
	return codec.F32Frame(samples, sampleRate, channels, pts)
}

// NewAudioDecoder opens a decoder for the stream format describes on the
// first backend that supports it. A container track reports its format
// (mp4.AudioTrack.Format, adts.Reader.Format) and an AudioEncoder reports
// the one of its output; for a stream from elsewhere fill in the codec,
// the sample rate and the channel count. The error matches ErrUnsupported
// (and is an *UnsupportedError) when no backend can serve the request.
//
// The codecs are the operating system's own, which run in software on
// every platform: WithSoftwareFallback is not needed, and nothing is
// bundled with the library.
//
// Every packet yields one frame of interleaved samples (S16 unless
// WithSampleFormat says otherwise) that carries the packet's PTS. The
// decoder returns all the samples a stream codes: the AudioFormat.Delay
// samples at its start, which lie before PTS 0 in a stream that starts
// there, and the padding of its last packet are for the caller to drop.
func NewAudioDecoder(ctx context.Context, format AudioFormat, opts ...AudioDecoderOption) (AudioDecoder, error) {
	cfg := codec.AudioDecoderConfig{Format: format, SampleFormat: S16}
	for _, o := range opts {
		o.ApplyAudioDecoder(&cfg)
	}
	switch {
	case !format.Codec.IsAudio():
		return nil, &UnsupportedError{Codec: format.Codec, Direction: Decode, Reason: "not an audio codec; use NewDecoder"}
	case format.SampleRate < 0 || format.Channels < 0:
		return nil, fmt.Errorf("hwmediacodec: sample rate and channel count must not be negative, got %d Hz and %d channels", format.SampleRate, format.Channels)
	}
	var unsupported error
	for _, b := range audioBackends() {
		d, err := b.NewAudioDecoder(ctx, cfg)
		if err == nil {
			return d, nil
		}
		if !errors.Is(err, ErrUnsupported) {
			return nil, err
		}
		unsupported = codec.PreferUnsupported(unsupported, err)
	}
	if unsupported != nil {
		return nil, codec.PlainUnsupported(unsupported)
	}
	return nil, &UnsupportedError{Codec: format.Codec, Direction: Decode, Reason: "no audio backend is available on this platform"}
}

// NewAudioEncoder opens an encoder for c that takes interleaved samples at
// the given rate and channel count (S16 unless WithSampleFormat says
// otherwise) on the first backend that supports it. The error matches
// ErrUnsupported (and is an *UnsupportedError) when no backend can serve
// the request, including a sample rate or a bitrate the codec does not
// offer.
//
// Without options the encoder uses the backend's default bitrate. PTS and
// Packet.Duration count in samples (the time scale is the sample rate)
// unless WithTimeScale sets another unit. The first packets of a stream
// have a PTS before that of the first frame: an encoder starts
// AudioFormat.Delay samples early (see AudioEncoder.Format).
func NewAudioEncoder(ctx context.Context, c Codec, sampleRate, channels int, opts ...AudioEncoderOption) (AudioEncoder, error) {
	cfg := codec.AudioEncoderConfig{
		Codec:        c,
		SampleRate:   sampleRate,
		Channels:     channels,
		SampleFormat: S16,
		RateControl:  VBR,
	}
	for _, o := range opts {
		o.ApplyAudioEncoder(&cfg)
	}
	if cfg.TimeScale == 0 {
		cfg.TimeScale = int32(sampleRate)
	}
	switch {
	case !c.IsAudio():
		return nil, &UnsupportedError{Codec: c, Direction: Encode, Reason: "not an audio codec; use NewEncoder"}
	case sampleRate <= 0 || channels <= 0:
		return nil, fmt.Errorf("hwmediacodec: sample rate and channel count must be positive, got %d Hz and %d channels", sampleRate, channels)
	case cfg.TimeScale < 0:
		return nil, fmt.Errorf("hwmediacodec: time scale must be positive, got %d", cfg.TimeScale)
	case cfg.Bitrate < 0:
		return nil, fmt.Errorf("hwmediacodec: bitrate must not be negative, got %d", cfg.Bitrate)
	case cfg.RateControl != VBR && cfg.RateControl != CBR:
		return nil, fmt.Errorf("hwmediacodec: unknown rate control %s", cfg.RateControl)
	}
	var unsupported error
	for _, b := range audioBackends() {
		e, err := b.NewAudioEncoder(ctx, cfg)
		if err == nil {
			return e, nil
		}
		if !errors.Is(err, ErrUnsupported) {
			return nil, err
		}
		unsupported = codec.PreferUnsupported(unsupported, err)
	}
	if unsupported != nil {
		return nil, codec.PlainUnsupported(unsupported)
	}
	return nil, &UnsupportedError{Codec: c, Direction: Encode, Reason: "no audio backend is available on this platform"}
}

type audioOption struct {
	dec func(*codec.AudioDecoderConfig)
	enc func(*codec.AudioEncoderConfig)
}

func (o audioOption) ApplyAudioDecoder(c *codec.AudioDecoderConfig) { o.dec(c) }
func (o audioOption) ApplyAudioEncoder(c *codec.AudioEncoderConfig) { o.enc(c) }

// WithSampleFormat sets the format of the samples a decoder returns and an
// encoder is given: S16 (the default) or F32.
func WithSampleFormat(f SampleFormat) AudioOption {
	return audioOption{
		dec: func(c *codec.AudioDecoderConfig) { c.SampleFormat = f },
		enc: func(c *codec.AudioEncoderConfig) { c.SampleFormat = f },
	}
}

// sharedEncoderOption is an option of both the video and the audio
// encoders.
type sharedEncoderOption struct {
	video func(*codec.EncoderConfig)
	audio func(*codec.AudioEncoderConfig)
}

func (o sharedEncoderOption) ApplyEncoder(c *codec.EncoderConfig)           { o.video(c) }
func (o sharedEncoderOption) ApplyAudioEncoder(c *codec.AudioEncoderConfig) { o.audio(c) }

// probeAudio lists what the audio backends offer.
func probeAudio(ctx context.Context) ([]Capability, error) {
	var out []Capability
	for _, b := range audioBackends() {
		caps, err := b.ProbeAudio(ctx)
		if err != nil {
			return out, fmt.Errorf("hwmediacodec: probe %s: %w", b.Name(), err)
		}
		out = append(out, caps...)
	}
	return out, nil
}
