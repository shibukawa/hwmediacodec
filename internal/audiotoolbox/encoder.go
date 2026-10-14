//go:build darwin

package audiotoolbox

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/audiotoolbox/sys"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

type encoder struct {
	mu     sync.Mutex
	cfg    codec.AudioEncoderConfig
	format codec.AudioFormat

	in     *input
	handle uintptr
	conv   uintptr
	// outBuf receives one packet at a time.
	outBuf []byte

	// started is set by the first frame of a signal, whose PTS becomes
	// basePTS. samplesIn and packetsOut count from there.
	started    bool
	basePTS    int64
	samplesIn  int64
	packetsOut int64

	pending []codec.Packet
	flushed bool
	closed  bool
}

func newEncoder(cfg codec.AudioEncoderConfig) (*encoder, error) {
	e := &encoder{cfg: cfg, in: &input{unit: cfg.Channels * cfg.SampleFormat.BytesPerSample()}}
	if cfg.Codec == codec.Opus && cfg.SampleRate != opus.SampleRate {
		return nil, e.unsupported(fmt.Sprintf("the Opus encoder takes %d Hz input, not %d Hz; resample first", opus.SampleRate, cfg.SampleRate))
	}
	e.handle = registerInput(e.in)
	if err := e.createConverter(); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *encoder) unsupported(reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: e.cfg.Codec, Direction: codec.Encode, Reason: reason}
}

func (e *encoder) createConverter() error {
	cfg := e.cfg
	id, _ := formatID(cfg.Codec)
	src := pcmDescription(cfg.SampleFormat, cfg.SampleRate, cfg.Channels)
	dst := sys.AudioStreamBasicDescription{
		SampleRate:       float64(cfg.SampleRate),
		FormatID:         id,
		ChannelsPerFrame: uint32(cfg.Channels),
	}
	var conv uintptr
	if st := sys.AudioConverterNew(&src, &dst, &conv); st != 0 {
		return e.unsupported(fmt.Sprintf("AudioToolbox has no %s encoder for %d Hz with %d channels: %s", cfg.Codec, cfg.SampleRate, cfg.Channels, sys.StatusString(st)))
	}
	e.conv = conv

	if cfg.Codec == codec.Opus {
		e.setOpusApplication()
	}
	if cfg.RateControl == codec.CBR {
		if st := sys.SetUint32(conv, sys.PropBitRateControlMode, sys.BitRateControlModeConstant); st != 0 {
			return e.unsupported("the encoder has no constant bitrate mode: " + sys.StatusString(st))
		}
	}
	if cfg.Bitrate > 0 {
		// Asked before the attempt: a converter that has refused a bitrate
		// no longer answers.
		offers := e.applicableBitrates()
		if st := sys.SetUint32(conv, sys.PropEncodeBitRate, uint32(cfg.Bitrate)); st != 0 {
			return e.unsupported(fmt.Sprintf("the encoder does not offer %d bit/s for %d Hz with %d channels%s", cfg.Bitrate, cfg.SampleRate, cfg.Channels, offers))
		}
	}

	// What the encoder settled on.
	size := uint32(unsafe.Sizeof(dst))
	if st := sys.AudioConverterGetProperty(conv, sys.PropCurrentOutputStreamDesc, &size, unsafe.Pointer(&dst)); st != 0 {
		return backendError("AudioConverterGetProperty(output format)", st)
	}
	maxPacket, st := sys.GetUint32(conv, sys.PropMaximumOutputPacketSize)
	if st != 0 || maxPacket == 0 {
		return backendError("AudioConverterGetProperty(maximum packet size)", st)
	}
	e.outBuf = make([]byte, maxPacket)
	var prime sys.AudioConverterPrimeInfo
	size = uint32(unsafe.Sizeof(prime))
	if st := sys.AudioConverterGetProperty(conv, sys.PropPrimeInfo, &size, unsafe.Pointer(&prime)); st != 0 {
		return backendError("AudioConverterGetProperty(prime info)", st)
	}
	e.format = codec.AudioFormat{
		Codec:        cfg.Codec,
		SampleRate:   cfg.SampleRate,
		Channels:     cfg.Channels,
		FrameSamples: int(dst.FramesPerPacket),
		Delay:        int(prime.LeadingFrames),
	}
	if e.format.FrameSamples <= 0 {
		return e.unsupported("the encoder does not produce packets of a fixed duration")
	}
	switch cfg.Codec {
	case codec.AAC:
		e.format.Config = aac.ConfigFromESDescriptor(sys.GetBytes(conv, sys.PropCompressionMagicCookie))
		if e.format.Config == nil {
			asc, err := aac.LCConfig(cfg.SampleRate, cfg.Channels)
			if err != nil {
				return e.unsupported(err.Error())
			}
			e.format.Config = asc
		}
	case codec.Opus:
		e.format.Config = opus.Head{Channels: cfg.Channels, PreSkip: e.format.Delay, InputSampleRate: cfg.SampleRate}.Marshal()
	}
	return nil
}

// Opus application modes (opus_defines.h).
const (
	opusApplicationVoIP  = 2048
	opusApplicationAudio = 2049
)

// setOpusApplication switches the Opus encoder from the mode AudioToolbox
// starts it in, which is the one for telephony (it filters out the low end
// and leans towards the speech coder), to the one for general audio.
//
// AudioToolbox has no property for the mode. It is the first field of the
// encoder's magic cookie, seven big-endian 32-bit numbers that start with
// the application, the sample rate and the packet length in samples, and
// the cookie can be written back. That layout is observed, not documented
// (macOS 15 to 27), so it is only touched when it looks as expected; an
// encoder with another cookie keeps its default mode.
func (e *encoder) setOpusApplication() {
	c := sys.GetBytes(e.conv, sys.PropCompressionMagicCookie)
	if len(c) != 28 ||
		binary.BigEndian.Uint32(c[0:]) != opusApplicationVoIP ||
		binary.BigEndian.Uint32(c[4:]) != uint32(e.cfg.SampleRate) ||
		binary.BigEndian.Uint32(c[16:]) != uint32(e.cfg.Channels) {
		return
	}
	binary.BigEndian.PutUint32(c[0:], opusApplicationAudio)
	sys.SetBytes(e.conv, sys.PropCompressionMagicCookie, c)
}

// applicableBitrates describes the bitrates the encoder offers for its
// current formats, for an error message.
func (e *encoder) applicableBitrates() string {
	type valueRange struct{ min, max float64 }
	var size uint32
	if sys.AudioConverterGetPropertyInfo(e.conv, sys.PropApplicableEncodeBitRates, &size, nil) != 0 || size < 16 {
		return ""
	}
	ranges := make([]valueRange, size/16)
	if sys.AudioConverterGetProperty(e.conv, sys.PropApplicableEncodeBitRates, &size, unsafe.Pointer(&ranges[0])) != 0 {
		return ""
	}
	var parts []string
	for _, r := range ranges[:size/16] {
		if r.min == r.max {
			parts = append(parts, fmt.Sprint(int(r.min)))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", int(r.min), int(r.max)))
		}
	}
	return " (it offers " + strings.Join(parts, ", ") + ")"
}

func (e *encoder) Format() codec.AudioFormat {
	f := e.format
	f.Config = append([]byte(nil), f.Config...)
	return f
}

func (e *encoder) Send(ctx context.Context, f *codec.AudioFrame) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch {
	case f == nil:
		return fmt.Errorf("hwmediacodec: nil audio frame: %w", codec.ErrInvalidData)
	case f.Format != e.cfg.SampleFormat:
		return fmt.Errorf("hwmediacodec: the frame is %s but the encoder was opened for %s: %w", f.Format, e.cfg.SampleFormat, codec.ErrInvalidData)
	case f.SampleRate != e.cfg.SampleRate || f.Channels != e.cfg.Channels:
		return fmt.Errorf("hwmediacodec: the frame is %d Hz with %d channels but the encoder was opened for %d Hz with %d: %w", f.SampleRate, f.Channels, e.cfg.SampleRate, e.cfg.Channels, codec.ErrInvalidData)
	}
	if err := f.Validate(); err != nil {
		return err
	}
	if uint64(len(f.Data)) > math.MaxUint32 {
		return fmt.Errorf("hwmediacodec: audio frame exceeds AudioToolbox's buffer size: %w", codec.ErrInvalidData)
	}
	if len(e.pending) >= maxPending {
		return codec.ErrAgain
	}
	e.flushed = false
	if !e.started {
		e.started, e.basePTS, e.samplesIn, e.packetsOut = true, f.PTS, 0, 0
	}
	if len(f.Data) == 0 {
		return nil
	}
	e.in.chunks = append(e.in.chunks, append([]byte(nil), f.Data...))
	e.samplesIn += int64(len(f.Data) / e.in.unit)
	return e.pump()
}

// pump runs the converter until it wants input that is not there (or, at
// the end of the stream, until it has nothing more to give) and queues the
// packets it produces.
func (e *encoder) pump() error {
	for {
		list := sys.AudioBufferList{NumberBuffers: 1, Buffer: sys.AudioBuffer{
			NumberChannels: uint32(e.cfg.Channels),
			DataByteSize:   uint32(len(e.outBuf)),
			Data:           unsafe.Pointer(&e.outBuf[0]),
		}}
		n := uint32(1)
		var desc sys.AudioStreamPacketDescription
		st := sys.AudioConverterFillComplexBuffer(e.conv, inputCallback(), e.handle, &n, &list, &desc)
		if n > 1 || uint64(list.Buffer.DataByteSize) > uint64(len(e.outBuf)) {
			return backendError("AudioConverterFillComplexBuffer(output size)", sys.StatusInvalidOutputSize)
		}
		if n > 0 {
			e.emit(e.outBuf[:list.Buffer.DataByteSize])
		}
		switch {
		case st == statusNoData:
			return nil
		case st != 0:
			return backendError("AudioConverterFillComplexBuffer", st)
		case n == 0:
			return nil
		}
	}
}

// emit queues one packet. Packet k codes the samples from k*FrameSamples -
// Delay on, counted from the first sample sent; what lies past the last
// sample sent is padding and not part of its duration.
func (e *encoder) emit(data []byte) {
	frame, delay := int64(e.format.FrameSamples), int64(e.format.Delay)
	start := e.packetsOut*frame - delay
	end := min(start+frame, e.samplesIn)
	e.packetsOut++
	rate, scale := int64(e.cfg.SampleRate), int64(e.cfg.TimeScale)
	pts := e.basePTS + codec.Rescale(start, rate, scale)
	p := codec.Packet{Data: append([]byte(nil), data...), PTS: pts, DTS: pts, Keyframe: true}
	if end > start {
		p.Duration = e.basePTS + codec.Rescale(end, rate, scale) - pts
	}
	e.pending = append(e.pending, p)
}

func (e *encoder) Receive(ctx context.Context) (codec.Packet, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.Packet{}, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return codec.Packet{}, err
	}
	if len(e.pending) > 0 {
		p := e.pending[0]
		e.pending[0] = codec.Packet{}
		e.pending = e.pending[1:]
		return p, nil
	}
	if e.flushed {
		return codec.Packet{}, io.EOF
	}
	return codec.Packet{}, codec.ErrAgain
}

func (e *encoder) Flush(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	if e.started {
		e.in.eof = true
		err = e.pump()
		e.in.eof = false
	}
	e.in.chunks, e.in.held = nil, nil
	e.started, e.flushed = false, true
	if st := sys.AudioConverterReset(e.conv); st != 0 && err == nil {
		err = backendError("AudioConverterReset", st)
	}
	return err
}

func (e *encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.conv != 0 {
		sys.AudioConverterDispose(e.conv)
		e.conv = 0
	}
	if e.handle != 0 {
		unregisterInput(e.handle)
	}
	e.pending = nil
	e.in.chunks, e.in.held = nil, nil
	return nil
}
