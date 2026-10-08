//go:build darwin

// Package videotoolbox implements the macOS backend on top of Apple's
// VideoToolbox framework, loaded at run time with purego.
package videotoolbox

import (
	"context"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/videotoolbox/sys"
)

// Name is the backend identifier reported in Capability.Backend.
const Name = "videotoolbox"

// Backend is the VideoToolbox backend.
type Backend struct{}

// Name implements codec.Backend.
func (Backend) Name() string { return Name }

func vtCodecType(c codec.Codec) (uint32, bool) {
	switch c {
	case codec.H264:
		return sys.CodecTypeH264, true
	case codec.HEVC:
		return sys.CodecTypeHEVC, true
	}
	return 0, false
}

// Probe implements codec.Backend. It lists hardware decode support for the
// codecs this backend implements.
func (Backend) Probe(ctx context.Context) ([]codec.Capability, error) {
	if err := sys.Load(); err != nil {
		// The frameworks are part of macOS; failing to load them means this
		// backend is absent on this system, which is not a probe error.
		return nil, nil
	}
	var caps []codec.Capability
	for _, c := range []codec.Codec{codec.H264, codec.HEVC} {
		if ctx.Err() != nil {
			return caps, ctx.Err()
		}
		vt, _ := vtCodecType(c)
		if sys.VTIsHardwareDecodeSupported(vt) != 0 {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Decode, Hardware: true})
		}
	}
	return caps, nil
}

// NewDecoder implements codec.Backend.
func (Backend) NewDecoder(ctx context.Context, cfg codec.DecoderConfig) (codec.Decoder, error) {
	vt, ok := vtCodecType(cfg.Codec)
	if !ok {
		return nil, &codec.UnsupportedError{Backend: Name, Codec: cfg.Codec, Direction: codec.Decode, Reason: "only h264 and hevc decoding are implemented"}
	}
	if cfg.OutputFormat != codec.NV12 {
		return nil, &codec.UnsupportedError{Backend: Name, Codec: cfg.Codec, Direction: codec.Decode, Reason: "output format " + cfg.OutputFormat.String() + " is not supported; use NV12"}
	}
	if err := sys.Load(); err != nil {
		return nil, &codec.UnsupportedError{Backend: Name, Codec: cfg.Codec, Direction: codec.Decode, Reason: err.Error()}
	}
	if !cfg.AllowSoftware && sys.VTIsHardwareDecodeSupported(vt) == 0 {
		return nil, &codec.UnsupportedError{Backend: Name, Codec: cfg.Codec, Direction: codec.Decode, Reason: "no hardware decoder on this machine (use WithSoftwareFallback to allow software)"}
	}
	return newDecoder(cfg, vt), nil
}
