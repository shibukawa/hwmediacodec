package codec

import (
	"context"
	"fmt"
	"math/bits"
	"sync"
	"time"
	"unsafe"
)

// SampleFormat describes the memory layout of the samples of an AudioFrame.
// Both formats are interleaved (the samples of one moment for every
// channel, then the next moment) and little-endian.
type SampleFormat uint8

const (
	// S16 is signed 16-bit integer PCM.
	S16 SampleFormat = iota + 1
	// F32 is 32-bit floating point PCM with full scale at -1 and 1.
	F32
)

func (f SampleFormat) String() string {
	switch f {
	case S16:
		return "s16"
	case F32:
		return "f32"
	}
	return fmt.Sprintf("sampleformat(%d)", uint8(f))
}

// BytesPerSample is the size of one sample of one channel, or 0 for an
// unknown format.
func (f SampleFormat) BytesPerSample() int {
	switch f {
	case S16:
		return 2
	case F32:
		return 4
	}
	return 0
}

// AudioFormat describes a compressed audio stream: what a decoder has to
// know before the first packet, and what an encoder produces. A container
// track reports one (mp4.AudioTrack.Format), and AudioEncoder.Format
// returns the one to hand to a muxer.
type AudioFormat struct {
	Codec Codec
	// SampleRate is the rate of the decoded samples in Hz. Opus always
	// decodes to 48000 whatever the rate of the original recording.
	SampleRate int
	// Channels is the number of channels of the decoded samples.
	Channels int
	// Config is the codec configuration record. For AAC it is the
	// AudioSpecificConfig (the payload of an MP4 esds box); when it is
	// empty a decoder assumes AAC-LC at SampleRate and Channels. For Opus
	// it is the identification header ("OpusHead", RFC 7845 section 5.1)
	// and optional: without it a decoder assumes Channels channels.
	Config []byte
	// FrameSamples is the number of samples per channel one packet
	// decodes to (1024 for AAC-LC, 960 for 20 ms of Opus), or 0 when it
	// is not known or varies.
	FrameSamples int
	// Delay is the number of samples at the start of the decoded stream
	// that are not part of the recording: the priming of an AAC encoder,
	// the pre-skip of Opus. Packet timestamps account for it (the first
	// packet of a stream that starts at 0 has PTS -Delay), so a player
	// drops what lies before the start of the presentation and a muxer
	// records Delay in the track's edit list.
	Delay int
}

// AudioFrame is a run of raw audio samples in CPU memory. Unlike a video
// Frame it is plain Go memory: frames returned by a decoder belong to the
// caller and need no Release, and an encoder copies what it is given.
type AudioFrame struct {
	SampleRate int
	Channels   int
	Format     SampleFormat
	// Data holds Samples()*Channels samples in Format.
	Data []byte
	// PTS is the presentation timestamp of the first sample.
	PTS int64
}

// Samples is the number of samples per channel in the frame.
func (f *AudioFrame) Samples() int {
	if f == nil || f.Channels <= 0 || f.Format.BytesPerSample() == 0 {
		return 0
	}
	return len(f.Data) / f.Format.BytesPerSample() / f.Channels
}

// Validate checks the sample format, rate, channel count and interleaved
// data length. An empty frame is valid.
func (f *AudioFrame) Validate() error {
	if f == nil {
		return fmt.Errorf("%w: nil audio frame", ErrInvalidData)
	}
	size := f.Format.BytesPerSample()
	if f.SampleRate <= 0 || f.Channels <= 0 || size == 0 || f.Channels > int(^uint(0)>>1)/size {
		return fmt.Errorf("%w: invalid audio format", ErrInvalidData)
	}
	if len(f.Data)%(size*f.Channels) != 0 {
		return fmt.Errorf("%w: %d bytes are not a whole number of interleaved samples", ErrInvalidData, len(f.Data))
	}
	return nil
}

// Duration is the play time of the frame.
func (f *AudioFrame) Duration() time.Duration {
	if f == nil || f.SampleRate <= 0 {
		return 0
	}
	return time.Duration(RescaleTrunc(int64(f.Samples()), int64(f.SampleRate), int64(time.Second)))
}

// Int16s returns the samples of an S16 frame as numbers, and nil for a
// frame in another format. The slice is for reading: it shares the
// frame's memory when that is aligned and is a copy otherwise.
func (f *AudioFrame) Int16s() []int16 {
	if f.Format != S16 {
		return nil
	}
	return samplesOf[int16](f.Data)
}

// Float32s returns the samples of an F32 frame as numbers, and nil for a
// frame in another format. The slice is for reading: it shares the
// frame's memory when that is aligned and is a copy otherwise.
func (f *AudioFrame) Float32s() []float32 {
	if f.Format != F32 {
		return nil
	}
	return samplesOf[float32](f.Data)
}

func samplesOf[T int16 | float32](b []byte) []T {
	var zero T
	size := int(unsafe.Sizeof(zero))
	n := len(b) / size
	if n == 0 {
		return []T{}
	}
	if uintptr(unsafe.Pointer(&b[0]))%unsafe.Alignof(zero) == 0 {
		return unsafe.Slice((*T)(unsafe.Pointer(&b[0])), n)
	}
	out := make([]T, n)
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&out[0])), n*size), b)
	return out
}

// S16Frame wraps interleaved 16-bit samples as an encoder input frame
// without copying: the frame shares the slice, which must stay untouched
// until AudioEncoder.Send has returned.
func S16Frame(samples []int16, sampleRate, channels int, pts int64) *AudioFrame {
	return &AudioFrame{SampleRate: sampleRate, Channels: channels, Format: S16, Data: bytesOf(samples), PTS: pts}
}

// F32Frame wraps interleaved floating point samples as an encoder input
// frame without copying: the frame shares the slice, which must stay
// untouched until AudioEncoder.Send has returned.
func F32Frame(samples []float32, sampleRate, channels int, pts int64) *AudioFrame {
	return &AudioFrame{SampleRate: sampleRate, Channels: channels, Format: F32, Data: bytesOf(samples), PTS: pts}
}

func bytesOf[T int16 | float32](s []T) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*int(unsafe.Sizeof(s[0])))
}

// AudioDecoder turns Packets into AudioFrames.
//
// Methods are safe to call from one goroutine at a time. The loop is the
// Decoder's: Send a packet, then call Receive until it returns ErrAgain,
// repeat; at end of stream call Flush and Receive until io.EOF.
type AudioDecoder interface {
	// Send feeds one packet. It returns ErrAgain when the decoder cannot
	// take more input until Receive has drained output.
	Send(ctx context.Context, p Packet) error
	// Receive returns the next run of decoded samples. Every packet yields
	// one frame, which carries the packet's PTS and all the samples the
	// packet codes, the encoder's delay and padding included. It returns
	// ErrAgain when no frame is ready and more input is needed, and io.EOF
	// after Flush once all frames have been returned.
	Receive(ctx context.Context) (*AudioFrame, error)
	// Flush drains the decoder and resets it, which is also what a seek
	// needs. After Flush the decoder can be reused.
	Flush(ctx context.Context) error
	// Close releases backend resources.
	Close() error
}

// AudioEncoder turns AudioFrames into Packets.
//
// Methods are safe to call from one goroutine at a time. The loop mirrors
// the AudioDecoder: Send a frame, then call Receive until it returns
// ErrAgain, repeat; at end of stream call Flush and Receive until io.EOF.
type AudioEncoder interface {
	// Format describes the stream the encoder produces: hand it to a muxer
	// or to NewAudioDecoder.
	Format() AudioFormat
	// Send encodes a run of samples. The frame's Format, SampleRate and
	// Channels must match the encoder configuration; it may hold any number
	// of samples, which the encoder cuts into packets. The samples are
	// taken as one continuous signal: the PTS of the first frame (and of
	// the first frame after a Flush) places it in time and the PTS of the
	// later ones is not looked at. The data is copied before Send returns.
	// Send returns ErrAgain when too many packets are waiting to be
	// received.
	Send(ctx context.Context, f *AudioFrame) error
	// Receive returns the next packet. It returns ErrAgain when no packet
	// is ready and more input is needed, and io.EOF after Flush once every
	// packet has been returned.
	Receive(ctx context.Context) (Packet, error)
	// Flush makes the encoder emit the samples it is holding, padding the
	// last packet with silence. After Flush the encoder can be reused for a
	// new signal.
	Flush(ctx context.Context) error
	// Close releases backend resources. Packets already returned stay valid.
	Close() error
}

// AudioDecoderConfig is the resolved set of audio decoder options handed to
// a backend.
type AudioDecoderConfig struct {
	Format AudioFormat
	// SampleFormat is the format of the returned frames.
	SampleFormat SampleFormat
}

// AudioEncoderConfig is the resolved set of audio encoder options handed to
// a backend.
type AudioEncoderConfig struct {
	Codec      Codec
	SampleRate int
	Channels   int
	// SampleFormat is the format of the frames given to Send.
	SampleFormat SampleFormat
	// TimeScale is the number of PTS units per second.
	TimeScale int32
	// Bitrate is the target in bits per second, or 0 for the backend
	// default.
	Bitrate int
	// RateControl selects VBR (default) or CBR.
	RateControl RateControl
}

// AudioDecoderOption adjusts an AudioDecoderConfig.
type AudioDecoderOption interface {
	ApplyAudioDecoder(*AudioDecoderConfig)
}

// AudioEncoderOption adjusts an AudioEncoderConfig.
type AudioEncoderOption interface {
	ApplyAudioEncoder(*AudioEncoderConfig)
}

// AudioOption is accepted by both NewAudioDecoder and NewAudioEncoder.
type AudioOption interface {
	AudioDecoderOption
	AudioEncoderOption
}

// SharedEncoderOption is accepted by both NewEncoder and NewAudioEncoder.
type SharedEncoderOption interface {
	EncoderOption
	AudioEncoderOption
}

// AudioBackend is implemented by each platform package that offers audio
// codecs.
type AudioBackend interface {
	Name() string
	// ProbeAudio lists the audio capabilities. A backend whose native
	// library is absent returns (nil, nil); an error means the probe
	// itself failed.
	ProbeAudio(ctx context.Context) ([]Capability, error)
	// NewAudioDecoder returns a decoder or an error matching
	// ErrUnsupported.
	NewAudioDecoder(ctx context.Context, cfg AudioDecoderConfig) (AudioDecoder, error)
	// NewAudioEncoder returns an encoder or an error matching
	// ErrUnsupported.
	NewAudioEncoder(ctx context.Context, cfg AudioEncoderConfig) (AudioEncoder, error)
}

var (
	audioRegistryMu sync.RWMutex
	audioRegistry   []AudioBackend
)

// RegisterAudio adds an audio backend. Backends register themselves from
// init functions of the platform-specific files in the root package.
func RegisterAudio(b AudioBackend) {
	audioRegistryMu.Lock()
	defer audioRegistryMu.Unlock()
	audioRegistry = append(audioRegistry, b)
}

// AudioBackends returns the registered audio backends in registration
// order.
func AudioBackends() []AudioBackend {
	audioRegistryMu.RLock()
	defer audioRegistryMu.RUnlock()
	out := make([]AudioBackend, len(audioRegistry))
	copy(out, audioRegistry)
	return out
}

// Rescale converts v from units of 1/from to units of 1/to, rounding to
// the nearest unit (halves away from zero).
// Results outside the int64 range saturate at its nearest endpoint.
func Rescale(v, from, to int64) int64 {
	return rescale(v, from, to, true)
}

// RescaleTrunc converts timestamp units like Rescale, truncating toward
// zero. Results outside the int64 range saturate at its nearest endpoint.
func RescaleTrunc(v, from, to int64) int64 {
	return rescale(v, from, to, false)
}

func rescale(v, from, to int64, round bool) int64 {
	if from == to || from <= 0 {
		return v
	}
	negative := (v < 0) != (to < 0)
	abs := func(n int64) uint64 {
		if n < 0 {
			return uint64(-(n + 1)) + 1
		}
		return uint64(n)
	}
	hi, lo := bits.Mul64(abs(v), abs(to))
	limit := uint64(1<<63 - 1)
	if negative {
		limit++
	}
	var q uint64
	if hi >= uint64(from) {
		q = limit
	} else {
		var rem uint64
		q, rem = bits.Div64(hi, lo, uint64(from))
		if q >= limit {
			q = limit
		} else if round && rem >= uint64(from)/2+uint64(from)%2 {
			q++
		}
	}
	if negative {
		return -int64(q)
	}
	return int64(q)
}
