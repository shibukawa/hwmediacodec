// Package hwmediacodec drives the operating system's hardware video decode and
// encode engines from Go without cgo.
//
// Native libraries are loaded at run time (dlopen on macOS and Linux,
// LoadLibrary on Windows), so CGO_ENABLED=0 builds and cross-compilation work.
// A platform without a usable engine reports an empty Probe result and
// ErrUnsupported from NewDecoder; it never fails at link time.
//
// # Status
//
// H.264 and HEVC decoding on macOS (Apple Silicon) through VideoToolbox, and
// H.264 decoding on Linux through VA-API (AMD Mesa and Intel drivers).
// Decoded frames are returned in NV12 in CPU memory, in decode order.
// Encoding and the Windows backends are not implemented yet.
//
// On Linux the VA-API backend opens the first usable DRM render node; set
// HWMEDIACODEC_VAAPI_DEVICE to a /dev/dri/renderD* path to choose a GPU.
//
// # Usage
//
//	dec, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.H264)
//	...
//	r := annexb.NewReader(file, hwmediacodec.H264)
//	for {
//		au, err := r.Next()
//		if err == io.EOF { break }
//		if err := dec.Send(ctx, hwmediacodec.Packet{Data: au}); err != nil { ... }
//		for {
//			f, err := dec.Receive(ctx)
//			if errors.Is(err, hwmediacodec.ErrAgain) { break }
//			... use f ...
//			f.Release()
//		}
//	}
//	dec.Flush(ctx)
//	// Receive until io.EOF, then Close.
package hwmediacodec

import (
	"context"
	"errors"
	"fmt"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// Re-exported core types. See the documentation on each in package codec.
type (
	Codec            = codec.Codec
	Direction        = codec.Direction
	PixelFormat      = codec.PixelFormat
	Capability       = codec.Capability
	Packet           = codec.Packet
	Frame            = codec.Frame
	Decoder          = codec.Decoder
	DecoderOption    = codec.DecoderOption
	UnsupportedError = codec.UnsupportedError
	BackendError     = codec.BackendError
)

const (
	H264 = codec.H264
	HEVC = codec.HEVC
	AV1  = codec.AV1

	Decode = codec.Decode
	Encode = codec.Encode

	NV12 = codec.NV12
)

var (
	ErrUnsupported = codec.ErrUnsupported
	ErrAgain       = codec.ErrAgain
	ErrClosed      = codec.ErrClosed
	ErrInvalidData = codec.ErrInvalidData
)

// DefaultTimeScale is the PTS unit used when WithTimeScale is not given.
const DefaultTimeScale int32 = 90000

// Probe reports every codec/direction pair the registered backends can serve
// on this machine. It returns an empty (non-nil) slice, not an error, when no
// hardware engine is present.
func Probe(ctx context.Context) ([]Capability, error) {
	out := []Capability{}
	for _, b := range codec.Backends() {
		caps, err := b.Probe(ctx)
		if err != nil {
			return out, fmt.Errorf("hwmediacodec: probe %s: %w", b.Name(), err)
		}
		out = append(out, caps...)
	}
	return out, nil
}

// NewDecoder opens a hardware decoder for c on the first backend that
// supports it. The error matches ErrUnsupported (and is an *UnsupportedError)
// when no backend can serve the request.
func NewDecoder(ctx context.Context, c Codec, opts ...DecoderOption) (Decoder, error) {
	cfg := codec.DecoderConfig{Codec: c, OutputFormat: NV12, TimeScale: DefaultTimeScale}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.TimeScale <= 0 {
		return nil, fmt.Errorf("hwmediacodec: time scale must be positive, got %d", cfg.TimeScale)
	}
	var unsupported error
	for _, b := range codec.Backends() {
		d, err := b.NewDecoder(ctx, cfg)
		if err == nil {
			return d, nil
		}
		if errors.Is(err, ErrUnsupported) {
			unsupported = err
			continue
		}
		return nil, err
	}
	if unsupported != nil {
		return nil, unsupported
	}
	return nil, &UnsupportedError{Codec: c, Direction: Decode, Reason: "no backend is available on this platform"}
}

// WithSoftwareFallback lets a backend use the operating system's software
// decoder when no hardware engine exists for the codec. By default the
// request is rejected with ErrUnsupported instead.
func WithSoftwareFallback() DecoderOption {
	return func(c *codec.DecoderConfig) { c.AllowSoftware = true }
}

// WithTimeScale sets the number of PTS units per second (default 90000).
func WithTimeScale(unitsPerSecond int32) DecoderOption {
	return func(c *codec.DecoderConfig) { c.TimeScale = unitsPerSecond }
}

// WithOutputFormat requests a pixel format for decoded frames. Only NV12 is
// supported in this release.
func WithOutputFormat(f PixelFormat) DecoderOption {
	return func(c *codec.DecoderConfig) { c.OutputFormat = f }
}
