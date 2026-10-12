//go:build js && wasm

package webcodecs

import (
	"context"
	"syscall/js"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// Backend is the WebCodecs backend.
type Backend struct{}

// Name implements codec.Backend.
func (Backend) Name() string { return Name }

// decoderConfig is a VideoDecoderConfig without a description: the chunks
// are Annex-B (or AV1 temporal units) with their parameter sets in-band.
func decoderConfig(codecString string, allowSoftware bool) map[string]any {
	return map[string]any{
		"codec":                codecString,
		"hardwareAcceleration": acceleration(allowSoftware),
	}
}

// supportedDecodeCodec returns the first of the codec strings the browser
// can decode, or "".
func supportedDecodeCodec(ctx context.Context, cls js.Value, names []string, allowSoftware bool) (string, error) {
	for _, name := range names {
		ok, err := supported(ctx, cls, decoderConfig(name, allowSoftware))
		if err != nil {
			return "", err
		}
		if ok {
			return name, nil
		}
	}
	return "", nil
}

// probeEncoderConfig is the configuration that stands for "this codec"
// when no encoder has been asked for: 720p at 30 frames per second.
func probeEncoderConfig(c codec.Codec) codec.EncoderConfig {
	return codec.EncoderConfig{Codec: c, Width: 1280, Height: 720, InputFormat: codec.NV12, TimeScale: 90000, FrameRate: 30, RateControl: codec.VBR}
}

// Probe implements codec.Backend. It lists what the browser reports a
// hardware codec for: H.264, HEVC and AV1 decoding and H.264 and HEVC
// encoding. Codecs the browser only has in software are left out;
// WithSoftwareFallback opens them.
func (Backend) Probe(ctx context.Context) ([]codec.Capability, error) {
	dec, hasDec := class("VideoDecoder")
	enc, hasEnc := class("VideoEncoder")
	var caps []codec.Capability
	for _, c := range []codec.Codec{codec.H264, codec.HEVC, codec.AV1} {
		if hasDec {
			name, err := supportedDecodeCodec(ctx, dec, probeDecodeCodecs(c), false)
			if err != nil {
				return caps, err
			}
			if name != "" {
				caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Decode, Hardware: true})
			}
		}
		if hasEnc && c != codec.AV1 {
			config, err := supportedEncoderConfig(ctx, enc, probeEncoderConfig(c))
			if err != nil {
				return caps, err
			}
			if config != nil {
				caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Encode, Hardware: true})
			}
		}
	}
	return caps, nil
}

// rgbCopy caches whether VideoFrame.copyTo converts to packed RGB.
var rgbCopy struct {
	known, ok bool
}

// rgbCopySupported reports whether VideoFrame.copyTo honours the format
// member of its options (RGBA, BGRA), which later browser versions added.
// A browser that does not know the member ignores it, and the allocation
// size it reports for a tiny NV12 frame stays the NV12 one.
func rgbCopySupported() bool {
	if rgbCopy.known {
		return rgbCopy.ok
	}
	rgbCopy.known = true
	cls, ok := class("VideoFrame")
	if !ok {
		return false
	}
	const w, h = 4, 4
	try("VideoFrame", func() {
		frame := cls.New(uint8Array.New(w*h*3/2), map[string]any{
			"format": "NV12", "codedWidth": w, "codedHeight": h, "timestamp": 0,
		})
		defer frame.Call("close")
		rgbCopy.ok = frame.Call("allocationSize", map[string]any{"format": "RGBA"}).Int() == w*h*4
	})
	return rgbCopy.ok
}

// NewDecoder implements codec.Backend. Whether the browser has a decoder
// for the codec at all is settled here; the VideoDecoder is configured
// when the stream's parameter sets give the exact codec string.
func (Backend) NewDecoder(ctx context.Context, cfg codec.DecoderConfig) (codec.Decoder, error) {
	names := probeDecodeCodecs(cfg.Codec)
	if names == nil {
		return nil, unsupported(cfg.Codec, codec.Decode, "only h264, hevc and av1 decoding are implemented")
	}
	cls, ok := class("VideoDecoder")
	if !ok {
		return nil, unsupported(cfg.Codec, codec.Decode, noWebCodecs)
	}
	switch cfg.OutputFormat {
	case codec.NV12:
	case codec.RGBA, codec.BGRA:
		if !rgbCopySupported() {
			return nil, unsupported(cfg.Codec, codec.Decode, "this browser's VideoFrame.copyTo does not convert to "+cfg.OutputFormat.String()+"; use NV12")
		}
	default:
		return nil, unsupported(cfg.Codec, codec.Decode, "output format "+cfg.OutputFormat.String()+" is not supported")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := supportedDecodeCodec(ctx, cls, names, cfg.AllowSoftware)
	if err != nil {
		return nil, err
	}
	if name == "" {
		if cfg.AllowSoftware {
			return nil, unsupported(cfg.Codec, codec.Decode, "the browser has no decoder for it")
		}
		return nil, unsupported(cfg.Codec, codec.Decode, "the browser reports no hardware decoder for it (use WithSoftwareFallback to allow its software decoder)")
	}
	return newDecoder(cfg, cls), nil
}

// NewEncoder implements codec.Backend.
func (Backend) NewEncoder(ctx context.Context, cfg codec.EncoderConfig) (codec.Encoder, error) {
	if cfg.Codec != codec.H264 && cfg.Codec != codec.HEVC {
		return nil, unsupported(cfg.Codec, codec.Encode, "only h264 and hevc encoding are implemented")
	}
	cls, ok := class("VideoEncoder")
	if !ok {
		return nil, unsupported(cfg.Codec, codec.Encode, noWebCodecs)
	}
	if _, ok := class("VideoFrame"); !ok {
		return nil, unsupported(cfg.Codec, codec.Encode, noWebCodecs)
	}
	if cfg.InputFormat != codec.NV12 {
		return nil, unsupported(cfg.Codec, codec.Encode, "input format "+cfg.InputFormat.String()+" is not supported; use NV12")
	}
	if _, ok := encodeCodecStrings(cfg.Codec, cfg.Profile, cfg.Width, cfg.Height, cfg.FrameRate); !ok {
		return nil, unsupported(cfg.Codec, codec.Encode, "profile "+cfg.Profile.String()+" is not defined for "+cfg.Codec.String())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := supportedEncoderConfig(ctx, cls, cfg)
	if err != nil {
		return nil, err
	}
	if config == nil {
		reason := "the browser reports no hardware encoder for " + describe(&cfg) + " (use WithSoftwareFallback to allow its software encoder)"
		if cfg.AllowSoftware {
			reason = "the browser has no encoder for " + describe(&cfg)
		}
		return nil, unsupported(cfg.Codec, codec.Encode, reason)
	}
	return newEncoder(ctx, cfg, cls, config)
}
