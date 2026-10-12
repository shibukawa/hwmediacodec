//go:build android || mediacodec_typecheck

package mediacodec

import (
	"context"
	"fmt"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediacodec/sys"
)

// Backend is the Android MediaCodec backend.
type Backend struct{}

// Name implements codec.Backend.
func (Backend) Name() string { return Name }

func unsupported(c codec.Codec, dir codec.Direction, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: dir, Reason: reason}
}

func backendErr(op string, st int32) error {
	return &codec.BackendError{Backend: Name, Op: op, Status: int64(st), Message: sys.StatusString(st)}
}

// probeCodec creates the default codec for the MIME type and reports whether
// one exists and whether it is a hardware one.
func probeCodec(mime string, encoder bool) (hardware, exists bool) {
	var c uintptr
	if encoder {
		c = sys.AMediaCodec_createEncoderByType(mime)
	} else {
		c = sys.AMediaCodec_createDecoderByType(mime)
	}
	if c == 0 {
		return false, false
	}
	name := sys.CodecName(c)
	sys.AMediaCodec_delete(c)
	return !isSoftwareCodec(name), true
}

// Probe implements codec.Backend. It lists the codecs whose default
// MediaCodec implementation is a hardware one: H.264, HEVC and AV1 decoding
// and H.264 and HEVC encoding. Software codecs (c2.android.*) are left out;
// WithSoftwareFallback opens them.
func (Backend) Probe(ctx context.Context) ([]codec.Capability, error) {
	if err := sys.Load(); err != nil {
		// libmediandk.so is part of Android; without it, or with one too
		// old for the symbols used, this backend is absent.
		return nil, nil
	}
	var caps []codec.Capability
	for _, c := range []codec.Codec{codec.H264, codec.HEVC, codec.AV1} {
		if ctx.Err() != nil {
			return caps, ctx.Err()
		}
		mime, _ := mimeOf(c)
		if hw, ok := probeCodec(mime, false); ok && hw {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Decode, Hardware: true})
		}
		if c == codec.AV1 {
			continue
		}
		if hw, ok := probeCodec(mime, true); ok && hw {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Encode, Hardware: true})
		}
	}
	return caps, nil
}

// NewDecoder implements codec.Backend. The MediaCodec decoder is created
// here (so that a software-only codec is refused at once) and configured
// when the stream's parameter sets arrive, since MediaCodec needs the
// picture size.
func (Backend) NewDecoder(ctx context.Context, cfg codec.DecoderConfig) (codec.Decoder, error) {
	mime, ok := mimeOf(cfg.Codec)
	if !ok {
		return nil, unsupported(cfg.Codec, codec.Decode, "only h264, hevc and av1 decoding are implemented")
	}
	if cfg.OutputFormat != codec.NV12 {
		return nil, unsupported(cfg.Codec, codec.Decode, "output format "+cfg.OutputFormat.String()+" is not supported; use NV12")
	}
	if err := sys.Load(); err != nil {
		return nil, unsupported(cfg.Codec, codec.Decode, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := sys.AMediaCodec_createDecoderByType(mime)
	if c == 0 {
		return nil, unsupported(cfg.Codec, codec.Decode, "MediaCodec has no decoder for "+mime)
	}
	name := sys.CodecName(c)
	if isSoftwareCodec(name) && !cfg.AllowSoftware {
		sys.AMediaCodec_delete(c)
		return nil, unsupported(cfg.Codec, codec.Decode, fmt.Sprintf("the only %s decoder is the software codec %s (use WithSoftwareFallback to allow it)", mime, name))
	}
	return newDecoder(cfg, mime, name, c), nil
}

// NewEncoder implements codec.Backend.
func (Backend) NewEncoder(ctx context.Context, cfg codec.EncoderConfig) (codec.Encoder, error) {
	mime, ok := mimeOf(cfg.Codec)
	if !ok || cfg.Codec == codec.AV1 {
		return nil, unsupported(cfg.Codec, codec.Encode, "only h264 and hevc encoding are implemented")
	}
	if cfg.InputFormat != codec.NV12 {
		return nil, unsupported(cfg.Codec, codec.Encode, "input format "+cfg.InputFormat.String()+" is not supported; use NV12")
	}
	if err := sys.Load(); err != nil {
		return nil, unsupported(cfg.Codec, codec.Encode, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := sys.AMediaCodec_createEncoderByType(mime)
	if c == 0 {
		return nil, unsupported(cfg.Codec, codec.Encode, "MediaCodec has no encoder for "+mime)
	}
	name := sys.CodecName(c)
	if isSoftwareCodec(name) && !cfg.AllowSoftware {
		sys.AMediaCodec_delete(c)
		return nil, unsupported(cfg.Codec, codec.Encode, fmt.Sprintf("the only %s encoder is the software codec %s (use WithSoftwareFallback to allow it)", mime, name))
	}
	return newEncoder(cfg, mime, name, c)
}
