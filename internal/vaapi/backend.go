//go:build linux

// Package vaapi implements the Linux backend on top of VA-API (libva),
// loaded at run time with purego. VA-API is a slice-level interface: the
// bitstream is parsed and the decoded picture buffer is managed in Go
// (packages h264, hevc and av1) and the driver only accelerates the slice
// data (for AV1, the tile data).
package vaapi

import (
	"context"
	"errors"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
)

// Name is the backend identifier reported in Capability.Backend.
const Name = "vaapi"

// Backend is the VA-API backend.
type Backend struct{}

// Name implements codec.Backend.
func (Backend) Name() string { return Name }

// h264Profiles lists the VA profiles that can decode H.264, most capable
// first. A High-profile decoder handles Main and Constrained Baseline
// streams too.
var h264Profiles = []int32{sys.ProfileH264High, sys.ProfileH264Main, sys.ProfileH264ConstrainedBaseline}

func unsupported(c codec.Codec, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: codec.Decode, Reason: reason}
}

// Probe implements codec.Backend. It reports hardware decode support for
// the codecs this backend implements. A machine without libva or without a
// usable render node yields (nil, nil).
func (Backend) Probe(ctx context.Context) ([]codec.Capability, error) {
	d, err := openDisplay()
	if err != nil {
		if errors.Is(err, sys.ErrNotAvailable) || errors.Is(err, errNoDevice) {
			return nil, nil
		}
		return nil, err
	}
	defer d.close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var caps []codec.Capability
	for _, c := range []codec.Codec{codec.H264, codec.HEVC, codec.AV1} {
		for _, p := range decodeProfiles(c) {
			if d.decodeProfiles[p] {
				w, h := d.maxPictureSize(p, sys.EntrypointVLD)
				caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Decode, Hardware: true, MaxWidth: w, MaxHeight: h})
				break
			}
		}
		for _, p := range encodeProfiles(c, codec.ProfileDefault) {
			if e, ok := d.encodeEntrypoint(p); ok {
				w, h := d.maxPictureSize(p, e)
				caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Encode, Hardware: true, MaxWidth: w, MaxHeight: h})
				break
			}
		}
	}
	return caps, nil
}

// decodeProfiles lists the VA profiles that decode the streams of a codec
// this backend handles (8-bit 4:2:0).
func decodeProfiles(c codec.Codec) []int32 {
	switch c {
	case codec.H264:
		return h264Profiles
	case codec.HEVC:
		// A driver with only the Main 10 profile still decodes Main
		// streams, but none is known to omit Main; Probe keys on it.
		return hevcProfiles[:1]
	case codec.AV1:
		return av1Profiles
	}
	return nil
}

// decodeCandidates lists every VA profile a decoder for the codec can use,
// or nil for codecs the backend does not decode.
func decodeCandidates(c codec.Codec) []int32 {
	switch c {
	case codec.H264:
		return h264Profiles
	case codec.HEVC:
		return hevcProfiles
	case codec.AV1:
		return av1Profiles
	}
	return nil
}

// NewDecoder implements codec.Backend.
func (Backend) NewDecoder(ctx context.Context, cfg codec.DecoderConfig) (codec.Decoder, error) {
	candidates := decodeCandidates(cfg.Codec)
	if candidates == nil {
		return nil, unsupported(cfg.Codec, "only h264, hevc and av1 decoding are implemented on the vaapi backend")
	}
	if cfg.OutputFormat != codec.NV12 {
		return nil, unsupported(cfg.Codec, "output format "+cfg.OutputFormat.String()+" is not available on the vaapi backend yet; use NV12")
	}
	d, err := openDisplay()
	if err != nil {
		if errors.Is(err, sys.ErrNotAvailable) || errors.Is(err, errNoDevice) {
			return nil, unsupported(cfg.Codec, err.Error())
		}
		return nil, err
	}
	found := false
	for _, p := range candidates {
		if d.decodeProfiles[p] {
			found = true
			break
		}
	}
	if !found {
		vendor := d.vendor
		d.close()
		return nil, unsupported(cfg.Codec, "the VA-API driver ("+vendor+") offers no "+cfg.Codec.String()+" decode entrypoint")
	}
	return newDecoder(cfg, d), nil
}

func unsupportedEncode(c codec.Codec, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: codec.Encode, Reason: reason}
}

// NewEncoder implements codec.Backend.
func (Backend) NewEncoder(ctx context.Context, cfg codec.EncoderConfig) (codec.Encoder, error) {
	if cfg.Codec != codec.H264 && cfg.Codec != codec.HEVC {
		return nil, unsupportedEncode(cfg.Codec, "only h264 and hevc encoding are implemented on the vaapi backend")
	}
	if cfg.InputFormat != codec.NV12 {
		return nil, unsupportedEncode(cfg.Codec, "input format "+cfg.InputFormat.String()+" is not available on the vaapi backend yet; use NV12")
	}
	d, err := openDisplay()
	if err != nil {
		if errors.Is(err, sys.ErrNotAvailable) || errors.Is(err, errNoDevice) {
			return nil, unsupportedEncode(cfg.Codec, err.Error())
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		d.close()
		return nil, err
	}
	e, err := newEncoder(cfg, d)
	if err != nil {
		d.close()
		return nil, err
	}
	return e, nil
}
