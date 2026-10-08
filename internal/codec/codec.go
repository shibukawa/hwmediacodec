// Package codec holds the core types shared by the public hwmediacodec API and
// its platform backends. It is internal; import github.com/shibukawa/hwmediacodec
// instead, which re-exports these types.
package codec

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Codec identifies a video coding format.
type Codec uint8

const (
	H264 Codec = iota + 1
	HEVC
	AV1
)

func (c Codec) String() string {
	switch c {
	case H264:
		return "h264"
	case HEVC:
		return "hevc"
	case AV1:
		return "av1"
	}
	return fmt.Sprintf("codec(%d)", uint8(c))
}

// Direction selects decoding or encoding.
type Direction uint8

const (
	Decode Direction = iota + 1
	Encode
)

func (d Direction) String() string {
	switch d {
	case Decode:
		return "decode"
	case Encode:
		return "encode"
	}
	return fmt.Sprintf("direction(%d)", uint8(d))
}

// PixelFormat describes the memory layout of a raw Frame.
type PixelFormat uint8

const (
	// NV12 is 8-bit 4:2:0 with a full-resolution Y plane followed by one
	// interleaved CbCr plane at half resolution.
	NV12 PixelFormat = iota + 1
)

func (p PixelFormat) String() string {
	switch p {
	case NV12:
		return "nv12"
	}
	return fmt.Sprintf("pixelformat(%d)", uint8(p))
}

// Capability is one codec/direction pair a backend can serve on this machine.
type Capability struct {
	Backend   string
	Codec     Codec
	Direction Direction
	// Hardware is true when the backend reports a hardware engine for this
	// capability. Software paths are only listed when a backend exposes them.
	Hardware bool
	// MaxWidth and MaxHeight are 0 when the backend does not report limits.
	MaxWidth  int
	MaxHeight int
}

// Packet is one compressed access unit (one picture) handed to a decoder.
//
// Data must hold a complete access unit in Annex-B byte-stream form (start
// codes). Parameter sets (SPS/PPS/VPS) may be included in-band.
type Packet struct {
	Data []byte
	// PTS is the presentation timestamp in TimeScale units (see the decoder
	// options). It is passed through to the resulting Frame.
	PTS int64
	// DTS is optional and currently informational.
	DTS int64
	// Keyframe is a hint only; decoders inspect the bitstream themselves.
	Keyframe bool
}

// Frame is one decoded picture in CPU memory.
//
// Planes and Strides follow Format. The memory is owned by the library and
// must be returned with Release once the caller is done with it.
type Frame struct {
	Width   int
	Height  int
	Format  PixelFormat
	Planes  [][]byte
	Strides []int
	PTS     int64

	release func()
	native  any
}

// Release returns the frame memory to the decoder. The frame must not be
// used afterwards. Release is idempotent.
func (f *Frame) Release() {
	if f == nil {
		return
	}
	if f.release != nil {
		r := f.release
		f.release = nil
		r()
	}
	f.Planes = nil
	f.Strides = nil
}

// Native returns a backend-specific handle for the frame, or nil. No backend
// exposes a handle yet; the accessor is reserved so that zero-copy paths can
// be added without changing the API.
func (f *Frame) Native() any { return f.native }

// SetRelease installs the release callback. Backends call it when they
// construct a Frame.
func SetRelease(f *Frame, fn func()) { f.release = fn }

// SetNative installs the backend-specific handle.
func SetNative(f *Frame, n any) { f.native = n }

// Decoder turns Packets into Frames.
//
// Methods are safe to call from one goroutine at a time. The typical loop is:
// Send a packet, then call Receive until it returns ErrAgain, repeat; at end
// of stream call Flush and Receive until io.EOF.
type Decoder interface {
	// Send feeds one access unit. It returns ErrAgain when the decoder cannot
	// take more input until Receive has drained output.
	Send(ctx context.Context, p Packet) error
	// Receive returns the next decoded frame. It returns ErrAgain when no
	// frame is ready and more input is needed, and io.EOF after Flush once
	// all frames have been returned.
	Receive(ctx context.Context) (*Frame, error)
	// Flush drains the decoder. After Flush the decoder can be reused; the
	// next Send must start at a keyframe (this doubles as a seek reset).
	Flush(ctx context.Context) error
	// Close releases backend resources. Frames already returned stay valid
	// until their own Release.
	Close() error
}

var (
	// ErrUnsupported is matched by errors.Is for every unsupported codec,
	// direction, format or platform. The concrete error is *UnsupportedError.
	ErrUnsupported = errors.New("hwmediacodec: unsupported")
	// ErrAgain means "nothing to do right now": Receive has no frame and the
	// caller should Send more input, or Send cannot accept input until
	// Receive has drained output.
	ErrAgain = errors.New("hwmediacodec: try again")
	// ErrClosed is returned after Close.
	ErrClosed = errors.New("hwmediacodec: decoder is closed")
	// ErrInvalidData is returned for input that is not a usable bitstream.
	ErrInvalidData = errors.New("hwmediacodec: invalid bitstream data")
)

// UnsupportedError explains which combination is unsupported and why.
type UnsupportedError struct {
	Backend   string
	Codec     Codec
	Direction Direction
	Reason    string
}

func (e *UnsupportedError) Error() string {
	b := e.Backend
	if b == "" {
		b = "any backend"
	}
	return fmt.Sprintf("hwmediacodec: %s %s is unsupported by %s: %s", e.Codec, e.Direction, b, e.Reason)
}

// Is reports true for ErrUnsupported so errors.Is(err, ErrUnsupported) works.
func (e *UnsupportedError) Is(target error) bool { return target == ErrUnsupported }

// BackendError carries a native status code from a backend call.
type BackendError struct {
	Backend string
	Op      string
	Status  int64
	Message string
}

func (e *BackendError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("hwmediacodec: %s: %s failed: %s (status %d)", e.Backend, e.Op, e.Message, e.Status)
	}
	return fmt.Sprintf("hwmediacodec: %s: %s failed with status %d", e.Backend, e.Op, e.Status)
}

// DecoderConfig is the resolved set of decoder options handed to a backend.
type DecoderConfig struct {
	Codec Codec
	// AllowSoftware permits a backend to use the OS software decoder when no
	// hardware engine exists. Default false: unsupported is reported instead.
	AllowSoftware bool
	// OutputFormat is the pixel format of returned frames.
	OutputFormat PixelFormat
	// TimeScale is the number of PTS units per second.
	TimeScale int32
}

// DecoderOption adjusts a DecoderConfig.
type DecoderOption func(*DecoderConfig)

// Backend is implemented by each platform package.
type Backend interface {
	Name() string
	// Probe lists hardware capabilities. A backend whose native library is
	// absent returns (nil, nil); an error means the probe itself failed.
	Probe(ctx context.Context) ([]Capability, error)
	// NewDecoder returns a decoder or an error matching ErrUnsupported.
	NewDecoder(ctx context.Context, cfg DecoderConfig) (Decoder, error)
}

var (
	registryMu sync.RWMutex
	registry   []Backend
)

// Register adds a backend. Backends register themselves from init functions
// of the platform-specific files in the root package.
func Register(b Backend) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, b)
}

// Backends returns the registered backends in registration order.
func Backends() []Backend {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Backend, len(registry))
	copy(out, registry)
	return out
}
