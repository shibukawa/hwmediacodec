//go:build linux || (windows && amd64)

// Package nvidia implements the NVIDIA backend on Linux and on Windows
// (amd64): decoding through NVDEC (the cuvid parser and decoder in
// libnvcuvid / nvcuvid.dll) and encoding through NVENC (libnvidia-encode /
// nvEncodeAPI64.dll), both on top of the CUDA driver API in libcuda /
// nvcuda.dll. The three libraries are loaded at run time and called through
// purego; a machine without the proprietary driver yields no capabilities
// and ErrUnsupported.
//
// NVDEC is a full decoder: the driver's parser handles parameter sets,
// picture boundaries and reference management, so unlike the VA-API
// backend no slice-level bookkeeping runs in Go. Frames are returned in
// decode order; the public API's reorder layer turns them into display
// order.
package nvidia

import (
	"context"
	"errors"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/nvidia/sys"
)

// Name is the backend identifier reported in Capability.Backend.
const Name = "nvidia"

// Backend is the NVIDIA backend.
type Backend struct{}

// Name implements codec.Backend.
func (Backend) Name() string { return Name }

var probeCodecs = []codec.Codec{codec.H264, codec.HEVC}

func cuvidCodec(c codec.Codec) (uint32, bool) {
	switch c {
	case codec.H264:
		return sys.CodecH264, true
	case codec.HEVC:
		return sys.CodecHEVC, true
	}
	return 0, false
}

func encodeGUID(c codec.Codec) (sys.GUID, bool) {
	switch c {
	case codec.H264:
		return sys.CodecH264GUID, true
	case codec.HEVC:
		return sys.CodecHEVCGUID, true
	}
	return sys.GUID{}, false
}

func unsupported(c codec.Codec, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: codec.Decode, Reason: reason}
}

func unsupportedEncode(c codec.Codec, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: codec.Encode, Reason: reason}
}

// notAvailable reports whether err means "no NVIDIA driver or GPU here",
// which Probe turns into an empty list and the constructors into
// ErrUnsupported.
func notAvailable(err error) bool {
	return errors.Is(err, sys.ErrNotAvailable) || errors.Is(err, errNoDevice) || errors.Is(err, sys.ErrNVENCVersion)
}

// decodeCaps queries NVDEC support for 8-bit 4:2:0 streams of codec c. The
// device context must be current.
func decodeCaps(c codec.Codec) (sys.DecodeCaps, error) {
	ct, _ := cuvidCodec(c)
	caps := sys.DecodeCaps{CodecType: ct, ChromaFormat: sys.Chroma420}
	if st := sys.CuvidGetDecoderCaps(&caps); st != sys.CUDASuccess {
		return caps, cuError("cuvidGetDecoderCaps", st)
	}
	return caps, nil
}

// Probe implements codec.Backend. It reports NVDEC decode and NVENC encode
// support for H.264 and HEVC. Without the driver, or without a GPU, it
// yields (nil, nil).
func (Backend) Probe(ctx context.Context) ([]codec.Capability, error) {
	dev, err := openDevice()
	if err != nil {
		if notAvailable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer dev.close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var caps []codec.Capability
	if err := sys.LoadCuvid(); err == nil {
		err := dev.run(func() error {
			for _, c := range probeCodecs {
				dc, err := decodeCaps(c)
				if err != nil {
					return err
				}
				if dc.IsSupported != 0 {
					caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Decode, Hardware: true,
						MaxWidth: int(dc.MaxWidth), MaxHeight: int(dc.MaxHeight)})
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else if !notAvailable(err) {
		return nil, err
	}
	if api, err := sys.LoadNVENC(); err == nil {
		ecaps, err := probeEncoder(dev, api)
		if err != nil {
			return nil, err
		}
		caps = append(caps, ecaps...)
	} else if !notAvailable(err) {
		return nil, err
	}
	return caps, nil
}

// NewDecoder implements codec.Backend.
func (Backend) NewDecoder(ctx context.Context, cfg codec.DecoderConfig) (codec.Decoder, error) {
	ct, ok := cuvidCodec(cfg.Codec)
	if !ok {
		return nil, unsupported(cfg.Codec, "only h264 and hevc decoding are implemented on the nvidia backend")
	}
	if cfg.OutputFormat != codec.NV12 {
		return nil, unsupported(cfg.Codec, "output format "+cfg.OutputFormat.String()+" is not available on the nvidia backend yet; use NV12")
	}
	if err := sys.LoadCuvid(); err != nil {
		if notAvailable(err) {
			return nil, unsupported(cfg.Codec, err.Error())
		}
		return nil, err
	}
	dev, err := openDevice()
	if err != nil {
		if notAvailable(err) {
			return nil, unsupported(cfg.Codec, err.Error())
		}
		return nil, err
	}
	var caps sys.DecodeCaps
	err = dev.run(func() error {
		var err error
		caps, err = decodeCaps(cfg.Codec)
		return err
	})
	if err == nil && caps.IsSupported == 0 {
		err = unsupported(cfg.Codec, "the GPU ("+dev.name+") has no NVDEC engine for "+cfg.Codec.String()+" 8-bit 4:2:0")
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		dev.close()
		return nil, err
	}
	return newDecoder(cfg, dev, ct, caps), nil
}

// NewEncoder implements codec.Backend.
func (Backend) NewEncoder(ctx context.Context, cfg codec.EncoderConfig) (codec.Encoder, error) {
	if _, ok := encodeGUID(cfg.Codec); !ok {
		return nil, unsupportedEncode(cfg.Codec, "only h264 and hevc encoding are implemented on the nvidia backend")
	}
	switch cfg.InputFormat {
	case codec.NV12, codec.RGBA, codec.BGRA:
	default:
		return nil, unsupportedEncode(cfg.Codec, "input format "+cfg.InputFormat.String()+" is not supported; use NV12, RGBA or BGRA")
	}
	api, err := sys.LoadNVENC()
	if err != nil {
		if notAvailable(err) {
			return nil, unsupportedEncode(cfg.Codec, err.Error())
		}
		return nil, err
	}
	dev, err := openDevice()
	if err != nil {
		if notAvailable(err) {
			return nil, unsupportedEncode(cfg.Codec, err.Error())
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		dev.close()
		return nil, err
	}
	e, err := newEncoder(cfg, dev, api)
	if err != nil {
		dev.close()
		return nil, err
	}
	return e, nil
}
