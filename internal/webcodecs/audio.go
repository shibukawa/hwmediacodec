//go:build js && wasm

package webcodecs

import (
	"context"
	"fmt"
	"syscall/js"

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// The audio half of the backend sits on AudioDecoder, AudioEncoder,
// AudioData and EncodedAudioChunk. Which codecs exist is the browser's
// answer, and behind it stands the platform or the browser's own build:
// Opus everywhere, AAC where the operating system has a codec for it.
//
// What the browser's codecs do and the API does not say:
//
//   - A decoder stamps its output itself (the first chunk's timestamp
//     plus the samples so far), so frames are matched to packets by
//     order, not by timestamp.
//   - An Opus decoder that is given the identification header as its
//     description drops the pre-skip itself. The library's decoders
//     return every sample a packet codes, so the header is kept back.
//   - An encoder's chunks are stamped from 0 with no word about the
//     samples it starts early by (2112 for the AAC encoder Chrome and
//     Safari use on a Mac, 312 for Opus). The delay is taken from the
//     identification header an Opus encoder reports and measured for AAC:
//     see encoderTraits.

// sampleFormatName is the AudioSampleFormat of interleaved samples in f.
func sampleFormatName(f codec.SampleFormat) string {
	if f == codec.F32 {
		return "f32"
	}
	return "s16"
}

// audioCodecString names the stream for a decoder configuration: "opus",
// or "mp4a.40." and the audio object type the config signals (2 for
// AAC-LC, 5 with band replication, 29 with parametric stereo on top).
func audioCodecString(c codec.Codec, cfg aac.Config) string {
	if c == codec.Opus {
		return "opus"
	}
	switch {
	case cfg.PS:
		return "mp4a.40.29"
	case cfg.SBR:
		return "mp4a.40.5"
	}
	return fmt.Sprintf("mp4a.40.%d", cfg.ObjectType)
}

func bufferOf(b []byte) js.Value {
	v := uint8Array.New(len(b))
	js.CopyBytesToJS(v, b)
	return v
}

// audioDecoderConfig builds the AudioDecoderConfig for a stream and
// returns with it the sample rate, the channel count and the number of
// samples per packet (0 for Opus, whose packets say it themselves).
func audioDecoderConfig(f codec.AudioFormat) (config map[string]any, sampleRate, channels, frame int, err error) {
	switch f.Codec {
	case codec.AAC:
		asc := f.Config
		if len(asc) == 0 {
			if asc, err = aac.LCConfig(f.SampleRate, f.Channels); err != nil {
				return nil, 0, 0, 0, fmt.Errorf("hwmediacodec: an AAC decoder needs AudioFormat.Config, or a sample rate and channel count to assume AAC-LC with: %w", err)
			}
		}
		c, err := aac.ParseConfig(asc)
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("hwmediacodec: AudioFormat.Config: %w: %w", err, codec.ErrInvalidData)
		}
		sampleRate, channels, frame = c.OutputSampleRate(), c.OutputChannels(), c.OutputFrameSamples()
		if channels == 0 {
			channels = f.Channels
		}
		if sampleRate <= 0 || channels <= 0 {
			return nil, 0, 0, 0, unsupported(f.Codec, codec.Decode, "the AudioSpecificConfig does not give the sample rate and channel count")
		}
		if channels > 6 {
			return nil, 0, 0, 0, unsupported(f.Codec, codec.Decode, fmt.Sprintf("%d channels are not supported; mono to 5.1 are", channels))
		}
		return map[string]any{
			"codec":            audioCodecString(codec.AAC, c),
			"sampleRate":       sampleRate,
			"numberOfChannels": channels,
			// With the config the chunks are bare access units; without
			// one a browser expects ADTS frames.
			"description": bufferOf(asc),
		}, sampleRate, channels, frame, nil
	case codec.Opus:
		channels = f.Channels
		if len(f.Config) > 0 {
			h, err := opus.ParseHead(f.Config)
			if err != nil {
				return nil, 0, 0, 0, fmt.Errorf("hwmediacodec: AudioFormat.Config: %w: %w", err, codec.ErrInvalidData)
			}
			if h.MappingFamily != 0 {
				return nil, 0, 0, 0, unsupported(f.Codec, codec.Decode, "only mono and stereo Opus streams (channel mapping family 0) are supported")
			}
			channels = h.Channels
		}
		if channels == 0 {
			channels = 2
		}
		if channels > 2 {
			return nil, 0, 0, 0, unsupported(f.Codec, codec.Decode, "only mono and stereo Opus streams are supported")
		}
		return map[string]any{"codec": "opus", "sampleRate": opus.SampleRate, "numberOfChannels": channels}, opus.SampleRate, channels, 0, nil
	}
	return nil, 0, 0, 0, unsupported(f.Codec, codec.Decode, "only aac and opus decoding are implemented")
}

// opusFrameMicros is the packet length asked of an Opus encoder: 20 ms,
// what every other backend produces.
const opusFrameMicros = 20000

// audioEncoderConfig builds the AudioEncoderConfig for cfg.
func audioEncoderConfig(cfg *codec.AudioEncoderConfig) map[string]any {
	config := map[string]any{
		"sampleRate":       cfg.SampleRate,
		"numberOfChannels": cfg.Channels,
	}
	switch cfg.Codec {
	case codec.AAC:
		config["codec"] = "mp4a.40.2"
		// Bare access units with the AudioSpecificConfig beside them.
		config["aac"] = map[string]any{"format": "aac"}
	case codec.Opus:
		config["codec"] = "opus"
		config["opus"] = map[string]any{"format": "opus", "frameDuration": opusFrameMicros, "application": "audio"}
	}
	if cfg.Bitrate > 0 {
		config["bitrate"] = cfg.Bitrate
	}
	// WebKit's AAC encoder leaves the bitrate out of account in its
	// variable mode (it codes to a quality of its own choice) and holds
	// it on average in its constant one, which is what a bitrate without
	// WithRateControl asks for.
	if cfg.RateControl == codec.CBR || (cfg.Codec == codec.AAC && cfg.Bitrate > 0 && webKit()) {
		config["bitrateMode"] = "constant"
	}
	return config
}

// probeAudioFormats are the streams that stand for "this codec" in Probe.
var probeAudioFormats = []codec.AudioFormat{
	{Codec: codec.AAC, SampleRate: 48000, Channels: 2},
	{Codec: codec.Opus, SampleRate: 48000, Channels: 2},
}

// ProbeAudio implements codec.AudioBackend: AAC and Opus where the browser
// reports a decoder or an encoder for 48 kHz stereo.
func (Backend) ProbeAudio(ctx context.Context) ([]codec.Capability, error) {
	dec, hasDec := class("AudioDecoder")
	enc, hasEnc := class("AudioEncoder")
	var caps []codec.Capability
	for _, f := range probeAudioFormats {
		if hasDec {
			if config, _, _, _, err := audioDecoderConfig(f); err == nil {
				ok, err := supported(ctx, dec, config)
				if err != nil {
					return caps, err
				}
				if ok {
					caps = append(caps, codec.Capability{Backend: Name, Codec: f.Codec, Direction: codec.Decode})
				}
			}
		}
		if hasEnc {
			cfg := codec.AudioEncoderConfig{Codec: f.Codec, SampleRate: f.SampleRate, Channels: f.Channels}
			ok, err := supported(ctx, enc, audioEncoderConfig(&cfg))
			if err != nil {
				return caps, err
			}
			if ok {
				caps = append(caps, codec.Capability{Backend: Name, Codec: f.Codec, Direction: codec.Encode})
			}
		}
	}
	return caps, nil
}

// NewAudioDecoder implements codec.AudioBackend.
func (Backend) NewAudioDecoder(ctx context.Context, cfg codec.AudioDecoderConfig) (codec.AudioDecoder, error) {
	c := cfg.Format.Codec
	if c != codec.AAC && c != codec.Opus {
		return nil, unsupported(c, codec.Decode, "only aac and opus decoding are implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupported(c, codec.Decode, "sample format "+cfg.SampleFormat.String()+" is not supported; use S16 or F32")
	}
	cls, ok := class("AudioDecoder")
	if !ok {
		return nil, unsupported(c, codec.Decode, noWebCodecs)
	}
	config, sampleRate, channels, frame, err := audioDecoderConfig(cfg.Format)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ok, err = supported(ctx, cls, config)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, unsupported(c, codec.Decode, fmt.Sprintf("the browser has no decoder for %s at %d Hz with %d channels", config["codec"], sampleRate, channels))
	}
	return newAudioDecoder(cfg, cls, config, sampleRate, channels, frame), nil
}

// NewAudioEncoder implements codec.AudioBackend.
func (Backend) NewAudioEncoder(ctx context.Context, cfg codec.AudioEncoderConfig) (codec.AudioEncoder, error) {
	c := cfg.Codec
	if c != codec.AAC && c != codec.Opus {
		return nil, unsupported(c, codec.Encode, "only aac and opus encoding are implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupported(c, codec.Encode, "sample format "+cfg.SampleFormat.String()+" is not supported; use S16 or F32")
	}
	if cfg.Channels < 1 || cfg.Channels > 2 {
		return nil, unsupported(c, codec.Encode, fmt.Sprintf("%d channels are not supported; mono and stereo are", cfg.Channels))
	}
	if c == codec.Opus && cfg.SampleRate != opus.SampleRate {
		// The browser would resample; the library leaves that to the
		// caller on every platform.
		return nil, unsupported(c, codec.Encode, fmt.Sprintf("the Opus encoder takes %d Hz input, not %d Hz; resample first", opus.SampleRate, cfg.SampleRate))
	}
	cls, ok := class("AudioEncoder")
	if !ok {
		return nil, unsupported(c, codec.Encode, noWebCodecs)
	}
	if _, ok := class("AudioData"); !ok {
		return nil, unsupported(c, codec.Encode, noWebCodecs)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config := audioEncoderConfig(&cfg)
	ok, err := supported(ctx, cls, config)
	if err != nil {
		return nil, err
	}
	if !ok {
		reason := fmt.Sprintf("the browser has no %s encoder for %d Hz with %d channels", c, cfg.SampleRate, cfg.Channels)
		if cfg.Bitrate > 0 || cfg.RateControl == codec.CBR {
			reason += fmt.Sprintf(" at %d bit/s (%s)", cfg.Bitrate, cfg.RateControl)
		}
		return nil, unsupported(c, codec.Encode, reason)
	}
	traits, err := encoderTraits(ctx, &cfg, cls, config)
	if err != nil {
		return nil, err
	}
	return newAudioEncoder(cfg, cls, config, traits), nil
}
