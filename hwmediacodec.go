// Package hwmediacodec drives the operating system's hardware video decode and
// encode engines from Go without cgo.
//
// Native libraries are loaded at run time (dlopen on macOS and Linux,
// LoadLibrary on Windows), so CGO_ENABLED=0 builds and cross-compilation work.
// A platform without a usable engine reports an empty Probe result and
// ErrUnsupported from NewDecoder and NewEncoder; it never fails at link time.
//
// # Status
//
// H.264 and HEVC decoding and encoding are implemented on macOS (Apple
// Silicon) through VideoToolbox, and H.264 and HEVC decoding on Windows
// (amd64 and arm64) through Media Foundation decoder transforms accelerated
// with Direct3D 11 (DXVA). Raw frames are NV12 in CPU memory. Decoded frames
// come back in decode order on macOS and in display order on Windows. The
// Windows encoder and the Linux backends are not implemented yet.
//
// # Decoding
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
//
// # Encoding
//
//	enc, err := hwmediacodec.NewEncoder(ctx, hwmediacodec.H264, 1920, 1080,
//		hwmediacodec.WithFrameRate(30), hwmediacodec.WithBitrate(4_000_000))
//	...
//	for i, f := range frames { // *hwmediacodec.Frame in NV12
//		f.PTS = int64(i) * int64(hwmediacodec.DefaultTimeScale) / 30
//		if err := enc.Send(ctx, f); err != nil { ... }
//		for {
//			p, err := enc.Receive(ctx)
//			if errors.Is(err, hwmediacodec.ErrAgain) { break }
//			out.Write(p.Data) // Annex-B access unit
//		}
//	}
//	enc.Flush(ctx)
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
	RateControl      = codec.RateControl
	Profile          = codec.Profile
	Capability       = codec.Capability
	Packet           = codec.Packet
	Frame            = codec.Frame
	Decoder          = codec.Decoder
	Encoder          = codec.Encoder
	DecoderOption    = codec.DecoderOption
	EncoderOption    = codec.EncoderOption
	Option           = codec.Option
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

	VBR = codec.VBR
	CBR = codec.CBR

	ProfileDefault  = codec.ProfileDefault
	ProfileBaseline = codec.ProfileBaseline
	ProfileMain     = codec.ProfileMain
	ProfileHigh     = codec.ProfileHigh
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
		o.ApplyDecoder(&cfg)
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

// NewEncoder opens a hardware encoder for c at the given picture size on the
// first backend that supports it. The error matches ErrUnsupported (and is an
// *UnsupportedError) when no backend can serve the request, including when a
// requested control (profile, rate control mode) is not available.
//
// Without options the encoder uses the backend's default rate control, no
// B-frames and the backend's keyframe placement. Set WithFrameRate so that
// rate control knows the frame rate, and give every frame an increasing PTS.
func NewEncoder(ctx context.Context, c Codec, width, height int, opts ...EncoderOption) (Encoder, error) {
	cfg := codec.EncoderConfig{
		Codec:       c,
		Width:       width,
		Height:      height,
		InputFormat: NV12,
		TimeScale:   DefaultTimeScale,
		RateControl: VBR,
	}
	for _, o := range opts {
		o.ApplyEncoder(&cfg)
	}
	switch {
	case width <= 0 || height <= 0:
		return nil, fmt.Errorf("hwmediacodec: picture size must be positive, got %dx%d", width, height)
	case cfg.TimeScale <= 0:
		return nil, fmt.Errorf("hwmediacodec: time scale must be positive, got %d", cfg.TimeScale)
	case cfg.FrameRate < 0:
		return nil, fmt.Errorf("hwmediacodec: frame rate must not be negative, got %g", cfg.FrameRate)
	case cfg.Bitrate < 0:
		return nil, fmt.Errorf("hwmediacodec: bitrate must not be negative, got %d", cfg.Bitrate)
	case cfg.Quality < 0 || cfg.Quality > 1:
		return nil, fmt.Errorf("hwmediacodec: quality must be within [0, 1], got %g", cfg.Quality)
	case cfg.Quality > 0 && cfg.Bitrate > 0:
		return nil, errors.New("hwmediacodec: WithQuality and WithBitrate are mutually exclusive")
	case cfg.KeyframeInterval < 0:
		return nil, fmt.Errorf("hwmediacodec: keyframe interval must not be negative, got %d", cfg.KeyframeInterval)
	case cfg.RateControl != VBR && cfg.RateControl != CBR:
		return nil, fmt.Errorf("hwmediacodec: unknown rate control %s", cfg.RateControl)
	}
	var unsupported error
	for _, b := range codec.Backends() {
		e, err := b.NewEncoder(ctx, cfg)
		if err == nil {
			return e, nil
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
	return nil, &UnsupportedError{Codec: c, Direction: Encode, Reason: "no backend is available on this platform"}
}

type decoderOption func(*codec.DecoderConfig)

func (f decoderOption) ApplyDecoder(c *codec.DecoderConfig) { f(c) }

type encoderOption func(*codec.EncoderConfig)

func (f encoderOption) ApplyEncoder(c *codec.EncoderConfig) { f(c) }

type sharedOption struct {
	dec func(*codec.DecoderConfig)
	enc func(*codec.EncoderConfig)
}

func (o sharedOption) ApplyDecoder(c *codec.DecoderConfig) { o.dec(c) }
func (o sharedOption) ApplyEncoder(c *codec.EncoderConfig) { o.enc(c) }

// WithSoftwareFallback lets a backend use the operating system's software
// codec when no hardware engine exists for the codec. By default the request
// is rejected with ErrUnsupported instead. It applies to decoders and
// encoders.
func WithSoftwareFallback() Option {
	return sharedOption{
		dec: func(c *codec.DecoderConfig) { c.AllowSoftware = true },
		enc: func(c *codec.EncoderConfig) { c.AllowSoftware = true },
	}
}

// WithTimeScale sets the number of PTS units per second (default 90000). It
// applies to decoders and encoders.
func WithTimeScale(unitsPerSecond int32) Option {
	return sharedOption{
		dec: func(c *codec.DecoderConfig) { c.TimeScale = unitsPerSecond },
		enc: func(c *codec.EncoderConfig) { c.TimeScale = unitsPerSecond },
	}
}

// WithOutputFormat requests a pixel format for decoded frames. Only NV12 is
// supported in this release.
func WithOutputFormat(f PixelFormat) DecoderOption {
	return decoderOption(func(c *codec.DecoderConfig) { c.OutputFormat = f })
}

// WithInputFormat sets the pixel format of frames given to Encoder.Send.
// Only NV12 is supported in this release.
func WithInputFormat(f PixelFormat) EncoderOption {
	return encoderOption(func(c *codec.EncoderConfig) { c.InputFormat = f })
}

// WithFrameRate tells the encoder the expected frame rate. It sets the
// duration of every frame and lets rate control plan for the rate.
func WithFrameRate(fps float64) EncoderOption {
	return encoderOption(func(c *codec.EncoderConfig) { c.FrameRate = fps })
}

// WithBitrate sets the target bitrate in bits per second. The rate control
// mode is VBR unless WithRateControl(CBR) is also given.
func WithBitrate(bitsPerSecond int) EncoderOption {
	return encoderOption(func(c *codec.EncoderConfig) { c.Bitrate = bitsPerSecond })
}

// WithRateControl selects VBR (default) or CBR for the bitrate given with
// WithBitrate. Backends that cannot do CBR report ErrUnsupported.
func WithRateControl(rc RateControl) EncoderOption {
	return encoderOption(func(c *codec.EncoderConfig) { c.RateControl = rc })
}

// WithQuality requests constant-quality coding with q in (0, 1] (higher is
// better) instead of a bitrate target. It cannot be combined with
// WithBitrate. Backends without a quality mode report ErrUnsupported.
func WithQuality(q float64) EncoderOption {
	return encoderOption(func(c *codec.EncoderConfig) { c.Quality = q })
}

// WithKeyframeInterval makes every n-th frame a keyframe, counted from the
// first frame and from every Flush. The encoder may insert extra keyframes.
// 0 (the default) leaves keyframe placement to the backend.
func WithKeyframeInterval(frames int) EncoderOption {
	return encoderOption(func(c *codec.EncoderConfig) { c.KeyframeInterval = frames })
}

// WithBFrames allows the encoder to use B-frames. Packets then arrive in
// decode order with DTS lagging PTS. B-frames are off by default.
func WithBFrames() EncoderOption {
	return encoderOption(func(c *codec.EncoderConfig) { c.BFrames = true })
}

// WithLowLatency configures the encoder for live streaming: real-time rate
// control, no frame reordering and the backend's low-latency mode where it
// has one. WithKeyframeInterval and Frame.ForceKeyframe keep working.
func WithLowLatency() EncoderOption {
	return encoderOption(func(c *codec.EncoderConfig) { c.LowLatency = true })
}

// WithProfile selects the coding profile. Combinations the codec does not
// define (ProfileBaseline or ProfileHigh with HEVC) report ErrUnsupported.
func WithProfile(p Profile) EncoderOption {
	return encoderOption(func(c *codec.EncoderConfig) { c.Profile = p })
}
