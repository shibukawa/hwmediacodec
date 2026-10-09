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
	// RGBA is 8-bit packed RGB with alpha in one plane, R first in memory,
	// 4 bytes per pixel and rows of exactly 4*Width bytes (no padding), so
	// that Planes[0] can be handed to ebiten.Image.WritePixels or wrapped
	// in an image.RGBA as is. Decoders emit alpha 255; encoders ignore
	// alpha.
	RGBA
	// BGRA is RGBA with B first in memory (the native 32-bit format of
	// Metal, Direct3D and Core Video).
	BGRA
)

func (p PixelFormat) String() string {
	switch p {
	case NV12:
		return "nv12"
	case RGBA:
		return "rgba"
	case BGRA:
		return "bgra"
	}
	return fmt.Sprintf("pixelformat(%d)", uint8(p))
}

// PlaneCount returns the number of planes of a frame in format p, or 0 for
// an unknown format.
func (p PixelFormat) PlaneCount() int {
	switch p {
	case NV12:
		return 2
	case RGBA, BGRA:
		return 1
	}
	return 0
}

// PlaneLayout returns the number of rows and the number of meaningful bytes
// per row of plane i of a picture of the given size, or (0, 0) when the
// plane does not exist.
func (p PixelFormat) PlaneLayout(i, width, height int) (rows, rowBytes int) {
	switch p {
	case NV12:
		switch i {
		case 0:
			return height, width
		case 1:
			return (height + 1) / 2, (width + 1) / 2 * 2
		}
	case RGBA, BGRA:
		if i == 0 {
			return height, width * 4
		}
	}
	return 0, 0
}

// FrameSize returns the number of bytes of a tightly packed picture of the
// given size in format p.
func (p PixelFormat) FrameSize(width, height int) int {
	n := 0
	for i := 0; i < p.PlaneCount(); i++ {
		rows, rowBytes := p.PlaneLayout(i, width, height)
		n += rows * rowBytes
	}
	return n
}

// RateControl selects how an encoder spends its bit budget.
type RateControl uint8

const (
	// VBR targets the configured bitrate on average and lets the size of
	// individual frames vary with content.
	VBR RateControl = iota + 1
	// CBR holds the bitrate constant, padding frames when necessary. It is
	// meant for streaming paths that require a steady rate.
	CBR
)

func (r RateControl) String() string {
	switch r {
	case VBR:
		return "vbr"
	case CBR:
		return "cbr"
	}
	return fmt.Sprintf("ratecontrol(%d)", uint8(r))
}

// Profile selects the coding profile of an encoder.
type Profile uint8

const (
	// ProfileDefault lets the backend choose.
	ProfileDefault Profile = iota
	// ProfileBaseline is H.264 Baseline (no B-frames, CAVLC).
	ProfileBaseline
	// ProfileMain is H.264 Main or HEVC Main.
	ProfileMain
	// ProfileHigh is H.264 High.
	ProfileHigh
)

func (p Profile) String() string {
	switch p {
	case ProfileDefault:
		return "default"
	case ProfileBaseline:
		return "baseline"
	case ProfileMain:
		return "main"
	case ProfileHigh:
		return "high"
	}
	return fmt.Sprintf("profile(%d)", uint8(p))
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

// Packet is one compressed access unit (one picture). Decoders consume
// Packets and encoders produce them.
//
// For H.264 and HEVC, Data holds a complete access unit in Annex-B
// byte-stream form (start codes). Parameter sets (SPS/PPS/VPS) may be
// included in-band; encoders put them in front of every keyframe. For AV1,
// Data holds one temporal unit in the low-overhead OBU format (the content
// of an IVF frame or an ISOBMFF sample), with or without temporal delimiter
// OBUs; the sequence header travels in-band the same way.
type Packet struct {
	Data []byte
	// PTS is the presentation timestamp in TimeScale units (see the
	// options). Decoders pass it through to the resulting Frame; encoders
	// copy it from the input Frame.
	PTS int64
	// DTS is the decode timestamp. Decoders treat it as informational.
	// Encoders set it; it equals PTS unless B-frames are enabled, in which
	// case packets arrive in decode order and DTS may lag PTS.
	DTS int64
	// Keyframe reports whether the access unit is a random access point. For
	// decoder input it is a hint only; decoders inspect the bitstream
	// themselves. Encoders set it authoritatively.
	Keyframe bool

	order Order
}

// Order is the display-order key the reorder layer derives for a picture
// from its slice header. Decoders copy it from the Packet to the Frame they
// produce for it.
type Order struct {
	// Seq counts coded video sequences: it increases whenever the picture
	// order count restarts (IDR and BLA pictures). Frames of an older
	// sequence are always displayed before frames of a newer one.
	Seq uint32
	// POC is the picture order count within the sequence.
	POC int32
	// Reorder is the number of frames that may precede this frame in decode
	// order and follow it in display order (num_reorder_frames).
	Reorder uint8
}

// SetPacketOrder attaches the display-order key to a packet.
func SetPacketOrder(p *Packet, o Order) { p.order = o }

// PacketOrder returns the display-order key attached to a packet.
func PacketOrder(p Packet) Order { return p.order }

// Frame is one raw picture in CPU memory.
//
// Planes and Strides follow Format. Frames returned by a decoder are owned
// by the library and must be returned with Release once the caller is done
// with them. Frames handed to an encoder are owned by the caller; the encoder
// copies them before Send returns.
type Frame struct {
	Width   int
	Height  int
	Format  PixelFormat
	Planes  [][]byte
	Strides []int
	PTS     int64
	// ForceKeyframe asks an encoder to code this frame as a keyframe. It is
	// an input to Encoder.Send only; decoders leave it false.
	ForceKeyframe bool

	release func()
	native  any
	order   Order
}

// Release returns the frame memory to the decoder. The frame must not be
// used afterwards. Release is idempotent and a no-op for caller-owned frames.
func (f *Frame) Release() {
	if f == nil || f.release == nil {
		return
	}
	r := f.release
	f.release = nil
	r()
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

// SetFrameOrder attaches the display-order key of the packet the frame was
// decoded from. Backends call it when they construct a Frame.
func SetFrameOrder(f *Frame, o Order) { f.order = o }

// FrameOrder returns the display-order key attached to a frame.
func FrameOrder(f *Frame) Order { return f.order }

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

// Encoder turns Frames into Packets.
//
// Methods are safe to call from one goroutine at a time. The typical loop
// mirrors the Decoder: Send a frame, then call Receive until it returns
// ErrAgain, repeat; at end of stream call Flush and Receive until io.EOF.
type Encoder interface {
	// Send encodes one raw frame. The frame's Format, Width and Height must
	// match the encoder configuration and PTS must increase from frame to
	// frame. The pixel data is copied before Send returns, so the caller may
	// reuse the frame memory. Send returns ErrAgain when too many packets
	// are waiting to be received.
	Send(ctx context.Context, f *Frame) error
	// Receive returns the next encoded access unit in decode order. It
	// returns ErrAgain when no packet is ready and more input is needed, and
	// io.EOF after Flush once every packet has been returned.
	Receive(ctx context.Context) (Packet, error)
	// Flush makes the encoder emit every frame it is holding. It blocks
	// until the backend has produced them. After Flush the encoder can be
	// reused; the next frame is coded as a keyframe.
	Flush(ctx context.Context) error
	// Close releases backend resources. Packets already returned stay valid.
	Close() error
}

var (
	// ErrUnsupported is matched by errors.Is for every unsupported codec,
	// direction, format or platform. The concrete error is *UnsupportedError.
	ErrUnsupported = errors.New("hwmediacodec: unsupported")
	// ErrAgain means "nothing to do right now": Receive has no output and the
	// caller should Send more input, or Send cannot accept input until
	// Receive has drained output.
	ErrAgain = errors.New("hwmediacodec: try again")
	// ErrClosed is returned after Close.
	ErrClosed = errors.New("hwmediacodec: closed")
	// ErrInvalidData is returned for input that is not a usable bitstream or
	// frame.
	ErrInvalidData = errors.New("hwmediacodec: invalid data")
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
	// DisplayOrder asks for frames in display order. Backends that emit
	// decode order are wrapped in the reorder layer by the public API;
	// a backend that reorders natively should implement DisplayOrderer.
	DisplayOrder bool
}

// DisplayOrderer is implemented by decoders that already emit frames in
// display order, so the public API does not wrap them in the reorder layer.
type DisplayOrderer interface {
	OutputsDisplayOrder() bool
}

// EncoderConfig is the resolved set of encoder options handed to a backend.
type EncoderConfig struct {
	Codec  Codec
	Width  int
	Height int
	// AllowSoftware permits a backend to use the OS software encoder when no
	// hardware engine exists. Default false: unsupported is reported instead.
	AllowSoftware bool
	// InputFormat is the pixel format of frames given to Send.
	InputFormat PixelFormat
	// TimeScale is the number of PTS units per second.
	TimeScale int32
	// FrameRate is the expected frames per second, or 0 when unknown. Rate
	// control is more accurate when it is set.
	FrameRate float64
	// Bitrate is the target in bits per second, or 0 for the backend default.
	Bitrate int
	// RateControl selects VBR (default) or CBR when Bitrate is set.
	RateControl RateControl
	// Quality is a constant-quality target in (0, 1], or 0 when bitrate
	// control is used instead.
	Quality float64
	// KeyframeInterval is the number of frames from one keyframe to the
	// next, or 0 for the backend default.
	KeyframeInterval int
	// BFrames allows the encoder to reorder frames (use B-frames).
	BFrames bool
	// LowLatency configures the encoder for live streaming.
	LowLatency bool
	// Profile selects the coding profile; ProfileDefault lets the backend
	// choose.
	Profile Profile
	// BT709 asks the backend to declare BT.709 primaries, transfer
	// function and matrix in video range in the stream. The public API
	// sets it when it converts packed RGB input to NV12 itself (package
	// pixconv), so that decoders convert back with the same matrix.
	BT709 bool
}

// DecoderOption adjusts a DecoderConfig.
type DecoderOption interface {
	ApplyDecoder(*DecoderConfig)
}

// EncoderOption adjusts an EncoderConfig.
type EncoderOption interface {
	ApplyEncoder(*EncoderConfig)
}

// Option is accepted by both NewDecoder and NewEncoder.
type Option interface {
	DecoderOption
	EncoderOption
}

// Backend is implemented by each platform package.
type Backend interface {
	Name() string
	// Probe lists hardware capabilities. A backend whose native library is
	// absent returns (nil, nil); an error means the probe itself failed.
	Probe(ctx context.Context) ([]Capability, error)
	// NewDecoder returns a decoder or an error matching ErrUnsupported.
	NewDecoder(ctx context.Context, cfg DecoderConfig) (Decoder, error)
	// NewEncoder returns an encoder or an error matching ErrUnsupported.
	NewEncoder(ctx context.Context, cfg EncoderConfig) (Encoder, error)
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
