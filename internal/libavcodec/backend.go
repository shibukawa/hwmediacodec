//go:build linux && !android

// Package libavcodec implements AAC decoding on Linux on top of the
// system's libavcodec (FFmpeg), loaded at run time with purego. Linux has
// no codec API of its own, and libavcodec is what the players of a
// desktop distribution decode AAC with; without it the backend is absent.
// Nothing is bundled.
//
// Only decoding is implemented, and only through the part of the
// library's interface that has kept its shape across its major versions:
// the functions, and the first fields of three structures (see sys.go).
// Encoding would mean filling in an AVFrame's channel layout, which has
// changed between them.
package libavcodec

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/audioutil"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// Name is the backend identifier reported in Capability.Backend.
const Name = "libavcodec"

// maxPending bounds the frames held for Receive; Send reports ErrAgain
// beyond it.
const maxPending = 256

// Backend is the libavcodec backend.
type Backend struct{}

// Name implements codec.AudioBackend.
func (Backend) Name() string { return Name }

func unsupported(c codec.Codec, dir codec.Direction, reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: c, Direction: dir, Reason: reason}
}

func backendErr(op string, code int32) error {
	return &codec.BackendError{Backend: Name, Op: op, Status: int64(code), Message: fmt.Sprintf("error %d", code)}
}

// ProbeAudio implements codec.AudioBackend.
func (Backend) ProbeAudio(ctx context.Context) ([]codec.Capability, error) {
	if load() != nil || findDecoder(codecIDAAC) == nil {
		return nil, nil
	}
	return []codec.Capability{{Backend: Name, Codec: codec.AAC, Direction: codec.Decode}}, ctx.Err()
}

// NewAudioEncoder implements codec.AudioBackend: there is none.
func (Backend) NewAudioEncoder(ctx context.Context, cfg codec.AudioEncoderConfig) (codec.AudioEncoder, error) {
	return nil, codec.NotImplemented(Name, cfg.Codec, codec.Encode, "the libavcodec backend only decodes")
}

// NewAudioDecoder implements codec.AudioBackend.
func (Backend) NewAudioDecoder(ctx context.Context, cfg codec.AudioDecoderConfig) (codec.AudioDecoder, error) {
	f := cfg.Format
	if f.Codec != codec.AAC {
		return nil, codec.NotImplemented(Name, f.Codec, codec.Decode, "only aac is implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupported(codec.AAC, codec.Decode, "sample format "+cfg.SampleFormat.String()+" is not supported; use S16 or F32")
	}
	asc := f.Config
	if len(asc) == 0 {
		var err error
		if asc, err = aac.LCConfig(f.SampleRate, f.Channels); err != nil {
			return nil, fmt.Errorf("hwmediacodec: an AAC decoder needs AudioFormat.Config, or a sample rate and channel count to assume AAC-LC with: %w", err)
		}
	}
	c, err := aac.ParseConfig(asc)
	if err != nil {
		return nil, fmt.Errorf("hwmediacodec: AudioFormat.Config: %w: %w", err, codec.ErrInvalidData)
	}
	channels := c.OutputChannels()
	if channels == 0 {
		channels = f.Channels
	}
	if c.OutputSampleRate() <= 0 || channels <= 0 {
		return nil, unsupported(codec.AAC, codec.Decode, "the AudioSpecificConfig does not give the sample rate and channel count")
	}
	if channels > 6 {
		return nil, unsupported(codec.AAC, codec.Decode, fmt.Sprintf("%d channels are not supported; mono to 5.1 are", channels))
	}
	if err := load(); err != nil {
		return nil, unsupported(codec.AAC, codec.Decode, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := &decoder{cfg: cfg, asc: append([]byte(nil), asc...), frame: c.OutputFrameSamples(), coreRate: c.SampleRate}
	d.frames = audioutil.Frames{SampleRate: c.OutputSampleRate(), Channels: channels, Format: cfg.SampleFormat}
	if err := d.open(); err != nil {
		return nil, err
	}
	return d, nil
}

type decoder struct {
	mu  sync.Mutex
	cfg codec.AudioDecoderConfig
	asc []byte
	// frame is the number of samples a packet decodes to and coreRate the
	// rate of the AAC-LC core, which band replication doubles.
	frame    int
	coreRate int
	ctx      unsafe.Pointer
	pkt      unsafe.Pointer
	out      unsafe.Pointer
	frames   audioutil.Frames
	// started is set once the decoder has returned samples, and lagged
	// when it runs aac.SBRDecoderDelay behind.
	started bool
	lagged  bool

	flushed bool
	closed  bool
}

// open makes a decoder context for the stream's configuration.
func (d *decoder) open() error {
	dec := findDecoder(codecIDAAC)
	if dec == nil {
		return unsupported(codec.AAC, codec.Decode, "this libavcodec was built without an AAC decoder")
	}
	par := parametersAlloc()
	if par == nil {
		return backendErr("avcodec_parameters_alloc", 0)
	}
	defer parametersFree(&par)
	// The parameters own the configuration, which the library wants with
	// zeroed bytes behind it.
	extra := mallocz(uintptr(len(d.asc)) + inputBufferPadding)
	if extra == nil {
		return backendErr("av_mallocz", 0)
	}
	copy(unsafe.Slice((*byte)(extra), len(d.asc)), d.asc)
	p := (*parameters)(par)
	p.CodecType, p.CodecID = mediaTypeAudio, codecIDAAC
	p.Extradata, p.ExtradataSize = extra, int32(len(d.asc))

	d.started, d.lagged = false, false
	d.ctx = allocContext(dec)
	if d.ctx == nil {
		return backendErr("avcodec_alloc_context3", 0)
	}
	if code := parametersToContext(d.ctx, par); code < 0 {
		d.release()
		return backendErr("avcodec_parameters_to_context", code)
	}
	if code := open2(d.ctx, dec, nil); code < 0 {
		d.release()
		return unsupported(codec.AAC, codec.Decode, fmt.Sprintf("libavcodec does not take the stream configuration (error %d)", code))
	}
	if d.pkt = packetAlloc(); d.pkt == nil {
		d.release()
		return backendErr("av_packet_alloc", 0)
	}
	if d.out = frameAlloc(); d.out == nil {
		d.release()
		return backendErr("av_frame_alloc", 0)
	}
	return nil
}

func (d *decoder) release() {
	if d.out != nil {
		frameFree(&d.out)
		d.out = nil
	}
	if d.pkt != nil {
		packetFree(&d.pkt)
		d.pkt = nil
	}
	if d.ctx != nil {
		freeContext(&d.ctx)
		d.ctx = nil
	}
}

func (d *decoder) Send(ctx context.Context, p codec.Packet) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(p.Data) == 0 {
		return fmt.Errorf("hwmediacodec: empty audio packet: %w", codec.ErrInvalidData)
	}
	if d.frames.Ready() >= maxPending {
		return codec.ErrAgain
	}
	d.flushed = false
	// The packet borrows the caller's bytes for the call: without a
	// buffer of its own the library copies them, padding included.
	pkt := (*packet)(d.pkt)
	pkt.Data, pkt.Size = unsafe.Pointer(&p.Data[0]), int32(len(p.Data))
	code := sendPacket(d.ctx, d.pkt)
	pkt.Data, pkt.Size = nil, 0
	if code < 0 {
		return d.invalid("avcodec_send_packet", code)
	}
	d.frames.Expect(p.PTS, d.frame)
	for {
		code := receiveFrame(d.ctx, d.out)
		if code == errAgain {
			return nil
		}
		if code < 0 {
			return d.invalid("avcodec_receive_frame", code)
		}
		err := d.take((*frame)(d.out))
		frameUnref(d.out)
		if err != nil {
			return err
		}
	}
}

// invalid reports a packet the decoder refused. The packets waiting for
// their samples are forgotten and the decoder starts afresh, so that the
// packets that follow decode.
func (d *decoder) invalid(op string, code int32) error {
	d.frames.Reset()
	d.release()
	if err := d.open(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %w", backendErr(op, code), codec.ErrInvalidData)
}

// take appends the samples of a decoded frame.
func (d *decoder) take(f *frame) error {
	samples := int(f.NbSamples)
	if samples <= 0 {
		return nil
	}
	planar := sampleFmtIsPlanar(f.Format) == 1
	packed := getPackedSampleFmt(f.Format)
	size := int(getBytesPerSample(f.Format))
	if packed != sampleFmtS16 && packed != sampleFmtS32 && packed != sampleFmtFLT {
		return unsupported(codec.AAC, codec.Decode, fmt.Sprintf("libavcodec's decoder returns sample format %d", f.Format))
	}
	// The number of channels has moved inside AVFrame and AVCodecContext
	// between the versions; the frame's buffers tell it as well. A planar
	// frame has one plane per channel (the decoders give AAC's six at
	// most, which fit the frame's own pointers), a packed one a single
	// buffer of all of them.
	var channels int
	var planes [][]byte
	if planar {
		for channels < len(f.Data) && f.Data[channels] != nil {
			planes = append(planes, unsafe.Slice((*byte)(f.Data[channels]), samples*size))
			channels++
		}
	} else if f.Data[0] != nil {
		var ac int64
		name := [...]byte{'a', 'c', 0}
		if optGetInt(d.ctx, &name[0], 0, &ac) < 0 || ac <= 0 {
			ac = int64(int(f.Linesize[0]) / (samples * size))
		}
		channels = int(ac)
		if channels > 0 && int(f.Linesize[0]) >= samples*size*channels {
			planes = [][]byte{unsafe.Slice((*byte)(f.Data[0]), samples*size*channels)}
		}
	}
	if channels <= 0 || len(planes) == 0 {
		return backendErr("avcodec_receive_frame", 0)
	}
	var rate int64
	name := [...]byte{'a', 'r', 0}
	if optGetInt(d.ctx, &name[0], 0, &rate) >= 0 && rate > 0 && int(rate) != d.frames.SampleRate {
		// Band replication that only the packets signal.
		d.frames.Scale(int(rate), d.frames.SampleRate)
		d.frame = d.frame * int(rate) / d.frames.SampleRate
		d.frames.SampleRate = int(rate)
	}
	d.frames.Channels = channels
	if !d.started {
		d.started = true
		// With band replication the output starts with the delay of its
		// filter banks, which belongs to no packet.
		if d.lagged = d.frames.SampleRate != d.coreRate; d.lagged {
			d.frames.Skip(aac.SBRDecoderDelay)
		}
	}

	// Interleave into the format asked for.
	out := make([]byte, samples*channels*d.cfg.SampleFormat.BytesPerSample())
	at := func(i, ch int) []byte {
		if planar {
			return planes[ch][i*size:]
		}
		return planes[0][(i*channels+ch)*size:]
	}
	for i := 0; i < samples; i++ {
		for ch := 0; ch < channels; ch++ {
			src := at(i, ch)
			var v float64 // full scale at 1
			switch packed {
			case sampleFmtS16:
				v = float64(int16(binary.LittleEndian.Uint16(src))) / 32768
			case sampleFmtS32:
				v = float64(int32(binary.LittleEndian.Uint32(src))) / 2147483648
			default:
				v = float64(math.Float32frombits(binary.LittleEndian.Uint32(src)))
			}
			n := i*channels + ch
			if d.cfg.SampleFormat == codec.F32 {
				binary.LittleEndian.PutUint32(out[n*4:], math.Float32bits(float32(v)))
			} else {
				s := math.Round(v * 32768)
				binary.LittleEndian.PutUint16(out[n*2:], uint16(int16(max(-32768, min(32767, s)))))
			}
		}
	}
	d.frames.Append(out)
	return nil
}

func (d *decoder) Receive(ctx context.Context) (*codec.AudioFrame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, codec.ErrClosed
	}
	if f := d.frames.Next(); f != nil {
		return f, nil
	}
	if d.flushed {
		return nil, io.EOF
	}
	return nil, codec.ErrAgain
}

// Flush implements codec.AudioDecoder. The decoder has no way to give out
// what its band replication still holds, so the last packet of HE-AAC ends
// in silence (which is its padding in a stream that an encoder closed
// properly). The context is replaced by a new one, so that the next packet
// does not lean on the ones before.
func (d *decoder) Flush(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if d.lagged && d.frames.Waiting() > 0 {
		d.frames.Pad(aac.SBRDecoderDelay)
	}
	d.frames.End()
	d.flushed = true
	d.release()
	return d.open()
}

func (d *decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.release()
	d.frames.Reset()
	d.frames.Drop()
	return nil
}
