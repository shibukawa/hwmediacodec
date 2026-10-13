//go:build darwin

// Package audiotoolbox implements the audio codecs of macOS and iOS on top
// of the AudioConverter API of Apple's AudioToolbox framework, loaded at run
// time with purego. The codecs are the operating system's software codecs;
// nothing is bundled with the library.
package audiotoolbox

import (
	"context"
	"fmt"
	"slices"

	"github.com/shibukawa/hwmediacodec/internal/audiotoolbox/sys"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// Name is the backend identifier reported in Capability.Backend.
const Name = "audiotoolbox"

// Backend is the AudioToolbox backend.
type Backend struct{}

// Name implements codec.AudioBackend.
func (Backend) Name() string { return Name }

func formatID(c codec.Codec) (uint32, bool) {
	switch c {
	case codec.AAC:
		return sys.FormatMPEG4AAC, true
	case codec.Opus:
		return sys.FormatOpus, true
	}
	return 0, false
}

// ProbeAudio implements codec.AudioBackend: the codecs of this backend that
// AudioToolbox lists among its decoders and encoders on this system.
func (Backend) ProbeAudio(ctx context.Context) ([]codec.Capability, error) {
	if err := sys.Load(); err != nil {
		// The framework is part of the system; failing to load it means
		// this backend is absent, which is not a probe error.
		return nil, nil
	}
	decoders, encoders := sys.FormatIDs(false), sys.FormatIDs(true)
	var caps []codec.Capability
	for _, c := range []codec.Codec{codec.AAC, codec.Opus} {
		id, _ := formatID(c)
		if slices.Contains(decoders, id) {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Decode})
		}
		if slices.Contains(encoders, id) {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Encode})
		}
	}
	return caps, ctx.Err()
}

// NewAudioDecoder implements codec.AudioBackend.
func (Backend) NewAudioDecoder(ctx context.Context, cfg codec.AudioDecoderConfig) (codec.AudioDecoder, error) {
	c := cfg.Format.Codec
	unsupported := func(reason string) error {
		return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: codec.Decode, Reason: reason}
	}
	if _, ok := formatID(c); !ok {
		return nil, unsupported("only aac and opus decoding are implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupported("sample format " + cfg.SampleFormat.String() + " is not supported; use S16 or F32")
	}
	if err := sys.Load(); err != nil {
		return nil, unsupported(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return newDecoder(cfg)
}

// NewAudioEncoder implements codec.AudioBackend.
func (Backend) NewAudioEncoder(ctx context.Context, cfg codec.AudioEncoderConfig) (codec.AudioEncoder, error) {
	unsupported := func(reason string) error {
		return &codec.UnsupportedError{Backend: Name, Codec: cfg.Codec, Direction: codec.Encode, Reason: reason}
	}
	if _, ok := formatID(cfg.Codec); !ok {
		return nil, unsupported("only aac and opus encoding are implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupported("sample format " + cfg.SampleFormat.String() + " is not supported; use S16 or F32")
	}
	if cfg.Channels < 1 || cfg.Channels > 2 {
		return nil, unsupported(fmt.Sprintf("%d channels are not supported; mono and stereo are", cfg.Channels))
	}
	if err := sys.Load(); err != nil {
		return nil, unsupported(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return newEncoder(cfg)
}
