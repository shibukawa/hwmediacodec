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

// hasHardwareEncoder reports whether VideoToolbox can select a hardware
// encoder for the codec. It asks for the supported-property dictionary with
// the hardware requirement set, which fails when only software is available.
func hasHardwareEncoder(vt uint32) bool {
	spec := sys.NewDictionary()
	defer sys.Release(spec)
	sys.CFDictionarySetValue(spec, sys.KVTRequireHardwareEncoder, sys.KCFBooleanTrue)
	var id, props uintptr
	st := sys.VTCopySupportedPropertyDictionaryForEncoder(1920, 1080, vt, spec, &id, &props)
	sys.Release(id)
	sys.Release(props)
	return st == 0
}

// Probe implements codec.Backend. It lists hardware decode and encode
// support for the codecs this backend implements.
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
		if hasHardwareEncoder(vt) {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Encode, Hardware: true})
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

// NewEncoder implements codec.Backend.
func (Backend) NewEncoder(ctx context.Context, cfg codec.EncoderConfig) (codec.Encoder, error) {
	vt, ok := vtCodecType(cfg.Codec)
	if !ok {
		return nil, &codec.UnsupportedError{Backend: Name, Codec: cfg.Codec, Direction: codec.Encode, Reason: "only h264 and hevc encoding are implemented"}
	}
	if cfg.InputFormat != codec.NV12 {
		return nil, &codec.UnsupportedError{Backend: Name, Codec: cfg.Codec, Direction: codec.Encode, Reason: "input format " + cfg.InputFormat.String() + " is not supported; use NV12"}
	}
	if err := sys.Load(); err != nil {
		return nil, &codec.UnsupportedError{Backend: Name, Codec: cfg.Codec, Direction: codec.Encode, Reason: err.Error()}
	}
	if !cfg.AllowSoftware && !hasHardwareEncoder(vt) {
		return nil, &codec.UnsupportedError{Backend: Name, Codec: cfg.Codec, Direction: codec.Encode, Reason: "no hardware encoder on this machine (use WithSoftwareFallback to allow software)"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return newEncoder(cfg, vt)
}
