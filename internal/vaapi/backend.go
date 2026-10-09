//go:build linux

// Package vaapi implements the Linux backend on top of VA-API (libva),
// loaded at run time with purego. VA-API is a slice-level interface: the
// bitstream is parsed and the decoded picture buffer is managed in Go
// (package h264) and the driver only accelerates the slice data.
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
	for _, p := range h264Profiles {
		if d.decodeProfiles[p] {
			w, h := d.maxPictureSize(p)
			caps = append(caps, codec.Capability{Backend: Name, Codec: codec.H264, Direction: codec.Decode, Hardware: true, MaxWidth: w, MaxHeight: h})
			break
		}
	}
	return caps, nil
}

// NewDecoder implements codec.Backend.
func (Backend) NewDecoder(ctx context.Context, cfg codec.DecoderConfig) (codec.Decoder, error) {
	if cfg.Codec != codec.H264 {
		return nil, unsupported(cfg.Codec, "only h264 decoding is implemented on the vaapi backend")
	}
	if cfg.OutputFormat != codec.NV12 {
		return nil, unsupported(cfg.Codec, "output format "+cfg.OutputFormat.String()+" is not supported; use NV12")
	}
	d, err := openDisplay()
	if err != nil {
		if errors.Is(err, sys.ErrNotAvailable) || errors.Is(err, errNoDevice) {
			return nil, unsupported(cfg.Codec, err.Error())
		}
		return nil, err
	}
	hasH264 := false
	for _, p := range h264Profiles {
		if d.decodeProfiles[p] {
			hasH264 = true
			break
		}
	}
	if !hasH264 {
		vendor := d.vendor
		d.close()
		return nil, unsupported(cfg.Codec, "the VA-API driver ("+vendor+") offers no H.264 decode entrypoint")
	}
	return newDecoder(cfg, d), nil
}

// NewEncoder implements codec.Backend. Encoding on VA-API is not implemented
// yet.
func (Backend) NewEncoder(ctx context.Context, cfg codec.EncoderConfig) (codec.Encoder, error) {
	return nil, &codec.UnsupportedError{Backend: Name, Codec: cfg.Codec, Direction: codec.Encode, Reason: "encoding is not implemented on the vaapi backend yet"}
}
