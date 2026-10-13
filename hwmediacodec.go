// Package hwmediacodec drives the operating system's hardware video decode and
// encode engines, and its audio codecs, from Go without cgo.
//
// Native libraries are loaded at run time (dlopen on macOS, iOS, Linux and
// Android, LoadLibrary on Windows), so CGO_ENABLED=0 builds and
// cross-compilation work on the desktop platforms. On iOS and Android
// purego needs the cgo runtime, so mobile builds (as made by ebitenmobile)
// set CGO_ENABLED=1 with the platform's clang; the library itself still
// contains no cgo code. A platform without a usable engine reports an empty
// Probe result and ErrUnsupported from NewDecoder and NewEncoder; it never
// fails at link time.
//
// In a browser (GOOS=js GOARCH=wasm, the target Ebitengine builds for the
// web) the engine is the WebCodecs API, with the platform's codecs behind
// it. Its results arrive on the page's event loop, which a WebAssembly
// program shares: Send blocks while the browser's codec is saturated and
// Flush until everything is delivered, and that is when the frames and
// packets Receive returns come in. A loop that only polls Receive, without
// ever blocking, starves the browser; the loops below do not.
//
// # Status
//
// H.264 and HEVC decoding and encoding are implemented on macOS (Apple
// Silicon) and iOS through VideoToolbox, on Android through the NDK
// MediaCodec API (the vendor's hardware codecs), on Linux and Windows (amd64) with the NVIDIA
// driver through NVDEC and NVENC, on Linux with an Intel GPU through Intel
// VPL (a VPL or Media SDK runtime), on Linux through VA-API (AMD Mesa and
// Intel drivers), and on Windows (amd64 and arm64) through Media Foundation
// (decoding with the Microsoft decoder transforms accelerated by Direct3D 11,
// encoding with the vendor's hardware encoder transforms), and in browsers
// through WebCodecs (the codecs the browser reports as hardware ones; RGBA
// and BGRA output converted by the browser, and the Native format for
// frames that stay on the GPU). Where the NVIDIA
// driver is installed its backend is tried first. Raw frames are NV12, RGBA
// or BGRA in CPU memory. VideoToolbox converts to and from the RGB formats
// itself and NVENC takes them as input; everywhere else the library converts
// in Go, so both formats work for decoding and encoding on every backend.
// Decoded frames are returned in display order (see NewDecoder and
// WithDecodeOrder; the Media Foundation and Intel VPL decoders and the
// browsers' reorder natively, so WithDecodeOrder has no effect there).
// AV1 decoding is available where the GPU has an AV1 decoder: on macOS with
// an Apple M3 or newer and on iOS from the A17 Pro on (8-bit and 10-bit
// Main profile), and on Linux and
// Windows through every backend (8-bit Main profile; NVIDIA RTX 30 series,
// Intel Tiger Lake, AMD RX 6000 series and newer; Media Foundation needs the
// "AV1 Video Extension" package), on Android where the SoC has an AV1
// decoder, and in a browser that reports a hardware AV1 decoder. A Packet
// is then one temporal unit (an IVF
// frame or ISOBMFF sample; see package mediacontainer/ivf), each yielding one frame in
// presentation order. No backend encodes AV1 yet.
// The separate module github.com/shibukawa/hwmediacodec/ebitenvideo plays
// streams in Ebitengine.
//
// AAC and Opus audio is decoded and encoded on macOS and iOS through
// AudioToolbox and in the browser through WebCodecs, as far as the browser
// has the codecs (see NewAudioDecoder and NewAudioEncoder); the other
// platforms report ErrUnsupported for audio so far. The audio codecs are
// the operating system's or the browser's own and run in software; the
// library contains no codec and bundles none.
//
// On Linux the VA-API backend opens the first usable DRM render node and the
// Intel VPL backend the first Intel one; set HWMEDIACODEC_VAAPI_DEVICE or
// HWMEDIACODEC_VPL_DEVICE to a /dev/dri/renderD* path to choose a GPU.
// The NVIDIA backend uses CUDA device 0; set HWMEDIACODEC_NVIDIA_DEVICE to
// another device ordinal on machines with several NVIDIA GPUs.
//
// When several backends can serve a request the first registered one wins
// (on Linux: nvidia, vpl, vaapi; on Windows: nvidia, mediafoundation; one
// backend each on macOS, iOS, Android and in the browser); set
// HWMEDIACODEC_BACKENDS to a comma-separated list of backend names, for
// example "vaapi" or "vaapi,vpl", to restrict and reorder them.
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
//
// # Audio
//
//	dec, err := hwmediacodec.NewAudioDecoder(ctx, track.Format()) // *mp4.AudioTrack
//	...
//	if err := dec.Send(ctx, packet); err != nil { ... }
//	f, err := dec.Receive(ctx) // interleaved samples in f.Data, see f.Int16s
//
//	enc, err := hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.AAC, 48000, 2,
//		hwmediacodec.WithBitrate(128_000))
//	...
//	if err := enc.Send(ctx, hwmediacodec.S16Frame(samples, 48000, 2, pts)); err != nil { ... }
//	p, err := enc.Receive(ctx) // one raw AAC access unit; enc.Format() describes the stream
package hwmediacodec

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"strings"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/pixconv"
	"github.com/shibukawa/hwmediacodec/internal/reorder"
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

	NV12   = codec.NV12
	RGBA   = codec.RGBA
	BGRA   = codec.BGRA
	Native = codec.Native

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

// RGBAFrame wraps img as an encoder input frame without copying: the frame
// shares the image's pixels, which must stay untouched until Encoder.Send
// has returned. Open the encoder with WithInputFormat(RGBA). The opposite
// direction is Frame.RGBAImage.
func RGBAFrame(img *image.RGBA, pts int64) *Frame { return codec.RGBAFrame(img, pts) }

// DefaultTimeScale is the PTS unit used when WithTimeScale is not given.
const DefaultTimeScale int32 = 90000

// backendsEnv names the environment variable that restricts the backends
// Probe, NewDecoder and NewEncoder consider and sets their order: a
// comma-separated list of backend names as reported in Capability.Backend
// (for example "vaapi" to bypass Intel VPL on an Intel GPU). Unset or empty
// means every backend in its built-in order.
const backendsEnv = "HWMEDIACODEC_BACKENDS"

// backends returns the backends to consult, honouring backendsEnv.
func backends() []codec.Backend {
	return selectBackends(codec.Backends(), os.Getenv(backendsEnv))
}

// selectBackends picks the backends named in list, in the order given
// there. Names that match no backend are ignored.
func selectBackends(all []codec.Backend, list string) []codec.Backend {
	if strings.TrimSpace(list) == "" {
		return all
	}
	out := []codec.Backend{}
	seen := map[string]bool{}
	for _, name := range strings.Split(list, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if seen[name] {
			continue
		}
		seen[name] = true
		for _, b := range all {
			if b.Name() == name {
				out = append(out, b)
			}
		}
	}
	return out
}

// Probe reports every codec/direction pair the registered backends can serve
// on this machine: the hardware video engines first, then the audio codecs
// of the operating system (Capability.Hardware is false for those; test
// Codec.IsAudio to tell the two apart). It returns an empty (non-nil)
// slice, not an error, when nothing is present.
func Probe(ctx context.Context) ([]Capability, error) {
	out := []Capability{}
	for _, b := range backends() {
		caps, err := b.Probe(ctx)
		if err != nil {
			return out, fmt.Errorf("hwmediacodec: probe %s: %w", b.Name(), err)
		}
		out = append(out, caps...)
	}
	audio, err := probeAudio(ctx)
	return append(out, audio...), err
}

// NewDecoder opens a hardware decoder for c on the first backend that
// supports it. The error matches ErrUnsupported (and is an *UnsupportedError)
// when no backend can serve the request.
//
// Frames come back in display order: pictures are reordered by their
// picture order count, derived from the slice headers in Go, and held back
// no longer than the stream's reorder bound (num_reorder_frames in the SPS
// or, for H.264 streams without that VUI field, the DPB size of the level).
// WithDecodeOrder turns the reordering off.
func NewDecoder(ctx context.Context, c Codec, opts ...DecoderOption) (Decoder, error) {
	cfg := codec.DecoderConfig{Codec: c, OutputFormat: NV12, TimeScale: DefaultTimeScale, DisplayOrder: true}
	for _, o := range opts {
		o.ApplyDecoder(&cfg)
	}
	if c.IsAudio() {
		return nil, &UnsupportedError{Codec: c, Direction: Decode, Reason: "an audio codec; use NewAudioDecoder"}
	}
	if cfg.TimeScale <= 0 {
		return nil, fmt.Errorf("hwmediacodec: time scale must be positive, got %d", cfg.TimeScale)
	}
	return newDecoder(ctx, backends(), cfg)
}

// newDecoder opens the decoder on the first candidate that supports cfg. A
// backend that rejects packed RGB output is asked again for NV12 and, when
// it accepts, gets the conversion in Go behind it.
func newDecoder(ctx context.Context, candidates []codec.Backend, cfg codec.DecoderConfig) (Decoder, error) {
	rgb := cfg.OutputFormat == RGBA || cfg.OutputFormat == BGRA
	var unsupported error
	for _, b := range candidates {
		d, err := b.NewDecoder(ctx, cfg)
		convert := false
		if rgb && errors.Is(err, ErrUnsupported) {
			nv12 := cfg
			nv12.OutputFormat = NV12
			d, err = b.NewDecoder(ctx, nv12)
			convert = err == nil
		}
		if err == nil {
			if cfg.DisplayOrder {
				if o, ok := d.(codec.DisplayOrderer); !ok || !o.OutputsDisplayOrder() {
					d = reorder.Wrap(d, cfg.Codec)
				}
			}
			if convert {
				// Outermost, so that only frames that leave are converted.
				d = pixconv.WrapDecoder(d, cfg.Codec, cfg.OutputFormat)
			}
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
	return nil, &UnsupportedError{Codec: cfg.Codec, Direction: Decode, Reason: "no backend is available on this platform"}
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
	case c.IsAudio():
		return nil, &UnsupportedError{Codec: c, Direction: Encode, Reason: "an audio codec; use NewAudioEncoder"}
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
	return newEncoder(ctx, backends(), cfg)
}

// newEncoder opens the encoder on the first candidate that supports cfg. A
// backend that rejects packed RGB input is asked again for NV12 and, when it
// accepts, gets the conversion in Go in front of it.
func newEncoder(ctx context.Context, candidates []codec.Backend, cfg codec.EncoderConfig) (Encoder, error) {
	rgb := cfg.InputFormat == RGBA || cfg.InputFormat == BGRA
	var unsupported error
	for _, b := range candidates {
		e, err := b.NewEncoder(ctx, cfg)
		if rgb && errors.Is(err, ErrUnsupported) {
			nv12 := cfg
			nv12.InputFormat = NV12
			nv12.BT709 = true
			if e, err = b.NewEncoder(ctx, nv12); err == nil {
				return pixconv.WrapEncoder(e, cfg.InputFormat, cfg.Width, cfg.Height), nil
			}
		}
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
	return nil, &UnsupportedError{Codec: cfg.Codec, Direction: Encode, Reason: "no backend is available on this platform"}
}

type decoderOption func(*codec.DecoderConfig)

func (f decoderOption) ApplyDecoder(c *codec.DecoderConfig) { f(c) }

type encoderOption func(*codec.EncoderConfig)

func (f encoderOption) ApplyEncoder(c *codec.EncoderConfig) { f(c) }

// sharedOption is an option every constructor accepts. The audio halves
// are nil where the option means nothing for audio.
type sharedOption struct {
	dec      func(*codec.DecoderConfig)
	enc      func(*codec.EncoderConfig)
	audioEnc func(*codec.AudioEncoderConfig)
}

func (o sharedOption) ApplyDecoder(c *codec.DecoderConfig) { o.dec(c) }
func (o sharedOption) ApplyEncoder(c *codec.EncoderConfig) { o.enc(c) }

func (o sharedOption) ApplyAudioDecoder(*codec.AudioDecoderConfig) {}
func (o sharedOption) ApplyAudioEncoder(c *codec.AudioEncoderConfig) {
	if o.audioEnc != nil {
		o.audioEnc(c)
	}
}

// WithSoftwareFallback lets a backend use the operating system's software
// codec when no hardware engine exists for the codec. By default the request
// is rejected with ErrUnsupported instead. It applies to the video decoders
// and encoders; the audio codecs run in software everywhere and take the
// option without needing it.
func WithSoftwareFallback() Option {
	return sharedOption{
		dec: func(c *codec.DecoderConfig) { c.AllowSoftware = true },
		enc: func(c *codec.EncoderConfig) { c.AllowSoftware = true },
	}
}

// WithTimeScale sets the number of PTS units per second. The default is
// 90000 for video and the sample rate for an audio encoder, so that audio
// timestamps count samples. An audio decoder hands the PTS of each packet
// on unchanged, whatever its unit, and takes the option without effect.
func WithTimeScale(unitsPerSecond int32) Option {
	return sharedOption{
		dec:      func(c *codec.DecoderConfig) { c.TimeScale = unitsPerSecond },
		enc:      func(c *codec.EncoderConfig) { c.TimeScale = unitsPerSecond },
		audioEnc: func(c *codec.AudioEncoderConfig) { c.TimeScale = unitsPerSecond },
	}
}

// WithOutputFormat requests a pixel format for decoded frames: NV12 (the
// default), RGBA or BGRA, or Native for frames that stay with the decoder
// (see Native; browsers only, ErrUnsupported elsewhere). RGBA and BGRA frames have one plane with rows of
// exactly 4*Width bytes, ready for ebiten.Image.WritePixels or image.RGBA.
// The backend converts where it can; for a backend that returns NV12 only
// the frames are converted in Go, with the colour matrix and range the
// stream declares (for a stream that declares none, BT.709 for pictures
// larger than 704x576 and BT.601 otherwise).
func WithOutputFormat(f PixelFormat) DecoderOption {
	return decoderOption(func(c *codec.DecoderConfig) { c.OutputFormat = f })
}

// WithDecodeOrder makes Receive return frames in decode order, as the
// backend produces them, instead of display order. It avoids the latency of
// the reorder buffer for streams without B-frames whose headers do not say
// so, and is what a transcoder that preserves timestamps wants.
func WithDecodeOrder() DecoderOption {
	return decoderOption(func(c *codec.DecoderConfig) { c.DisplayOrder = false })
}

// WithInputFormat sets the pixel format of frames given to Encoder.Send:
// NV12 (the default), RGBA or BGRA. The alpha byte of RGBA and BGRA input
// is ignored. A backend whose encoder takes NV12 only gets RGBA and BGRA
// frames converted in Go (BT.709, video range) and declares that matrix in
// the stream where the encoder lets it.
func WithInputFormat(f PixelFormat) EncoderOption {
	return encoderOption(func(c *codec.EncoderConfig) { c.InputFormat = f })
}

// WithFrameRate tells the encoder the expected frame rate. It sets the
// duration of every frame and lets rate control plan for the rate.
func WithFrameRate(fps float64) EncoderOption {
	return encoderOption(func(c *codec.EncoderConfig) { c.FrameRate = fps })
}

// WithBitrate sets the target bitrate in bits per second, for a video or an
// audio encoder. The rate control mode is VBR unless WithRateControl(CBR)
// is also given.
func WithBitrate(bitsPerSecond int) SharedEncoderOption {
	return sharedEncoderOption{
		video: func(c *codec.EncoderConfig) { c.Bitrate = bitsPerSecond },
		audio: func(c *codec.AudioEncoderConfig) { c.Bitrate = bitsPerSecond },
	}
}

// WithRateControl selects VBR (default) or CBR for the bitrate given with
// WithBitrate, for a video or an audio encoder. Backends that cannot do CBR
// report ErrUnsupported.
func WithRateControl(rc RateControl) SharedEncoderOption {
	return sharedEncoderOption{
		video: func(c *codec.EncoderConfig) { c.RateControl = rc },
		audio: func(c *codec.AudioEncoderConfig) { c.RateControl = rc },
	}
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
