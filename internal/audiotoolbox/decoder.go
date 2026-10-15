//go:build darwin

package audiotoolbox

import (
	"context"
	"fmt"
	"io"
	"math"
	"sync"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/audiotoolbox/sys"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// channelLayouts names the order of the decoded channels for streams with
// more than two: the order of WAV files and of most audio APIs (front left
// and right, centre, LFE, then the surrounds) instead of the order the AAC
// bitstream stores them in. 7.1 is left out: its AAC form has two more
// front channels where the common 7.1 has two rear ones, and which of the
// orders in use a caller expects is not settled by a test here.
var channelLayouts = map[int]uint32{
	3: sys.LayoutMPEG_3_0A,
	4: sys.LayoutMPEG_4_0A,
	5: sys.LayoutMPEG_5_0A,
	6: sys.LayoutMPEG_5_1A,
}

type decoder struct {
	mu  sync.Mutex
	cfg codec.AudioDecoderConfig

	in     *input
	handle uintptr
	conv   uintptr
	// source describes the compressed input; cookie is its magic cookie
	// (AAC only).
	source sys.AudioStreamBasicDescription
	cookie []byte

	sampleRate int
	channels   int
	// frame is the number of samples per channel one packet decodes to.
	frame int

	// pts holds the timestamps of the packets whose frames are still to
	// come and pcm the samples decoded for them so far. Most decoders give
	// a packet's samples at once; the one for HE-AAC runs 962 samples
	// behind (the delay of the band replication) and gives the last of
	// them at the end of the stream.
	pts []int64
	pcm []byte

	out     []*codec.AudioFrame
	flushed bool
	closed  bool
}

func newDecoder(cfg codec.AudioDecoderConfig) (*decoder, error) {
	d := &decoder{cfg: cfg, in: &input{}}
	var err error
	switch cfg.Format.Codec {
	case codec.AAC:
		err = d.describeAAC()
	case codec.Opus:
		err = d.describeOpus()
	}
	if err != nil {
		return nil, err
	}
	d.handle = registerInput(d.in)
	if err := d.createConverter(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func (d *decoder) unsupported(reason string) error {
	return &codec.UnsupportedError{Backend: Name, Codec: d.cfg.Format.Codec, Direction: codec.Decode, Reason: reason}
}

func (d *decoder) describeAAC() error {
	f := d.cfg.Format
	asc := f.Config
	if len(asc) == 0 {
		var err error
		if asc, err = aac.LCConfig(f.SampleRate, f.Channels); err != nil {
			return fmt.Errorf("hwmediacodec: an AAC decoder needs AudioFormat.Config, or a sample rate and channel count to assume AAC-LC with: %w", err)
		}
	}
	c, err := aac.ParseConfig(asc)
	if err != nil {
		return fmt.Errorf("hwmediacodec: AudioFormat.Config: %w: %w", err, codec.ErrInvalidData)
	}
	d.cookie = aacCookie(asc)
	// The extensions are formats of their own to AudioToolbox: a decoder
	// for plain AAC plays the AAC-LC core of an HE-AAC stream alone, at
	// half the sample rate (and in mono under parametric stereo).
	id := sys.FormatMPEG4AAC
	switch {
	case c.PS:
		id = sys.FormatMPEG4AACHEV2
	case c.SBR:
		id = sys.FormatMPEG4AACHE
	}
	d.source = sys.AudioStreamBasicDescription{
		SampleRate:       float64(c.OutputSampleRate()),
		FormatID:         id,
		FramesPerPacket:  uint32(c.OutputFrameSamples()),
		ChannelsPerFrame: uint32(c.OutputChannels()),
	}
	// Let AudioToolbox read the config itself where the parse above left
	// something open (a channel layout given by a program config element).
	size := uint32(unsafe.Sizeof(d.source))
	info := d.source
	if st := sys.AudioFormatGetProperty(sys.FormatPropFormatInfo, uint32(len(d.cookie)), unsafe.Pointer(&d.cookie[0]), &size, unsafe.Pointer(&info)); st == 0 && info.FormatID == id {
		d.source = info
	}
	d.sampleRate = int(d.source.SampleRate)
	d.channels = int(d.source.ChannelsPerFrame)
	d.frame = int(d.source.FramesPerPacket)
	if d.sampleRate <= 0 || d.channels <= 0 || d.frame <= 0 {
		return d.unsupported("the AudioSpecificConfig does not give the sample rate and channel count")
	}
	return nil
}

func (d *decoder) describeOpus() error {
	f := d.cfg.Format
	d.channels = f.Channels
	if len(f.Config) > 0 {
		h, err := opus.ParseHead(f.Config)
		if err != nil {
			return fmt.Errorf("hwmediacodec: AudioFormat.Config: %w: %w", err, codec.ErrInvalidData)
		}
		if h.MappingFamily != 0 {
			return d.unsupported("only mono and stereo Opus streams (channel mapping family 0) are supported")
		}
		d.channels = h.Channels
	}
	if d.channels == 0 {
		d.channels = 2
	}
	if d.channels > 2 {
		return d.unsupported("only mono and stereo Opus streams are supported")
	}
	d.sampleRate = opus.SampleRate
	// The packet duration is part of the converter's input description;
	// 20 ms is what almost every stream uses, and Send makes a new
	// converter when a packet is of another length.
	d.setOpusFrame(960)
	return nil
}

func (d *decoder) setOpusFrame(samples int) {
	d.frame = samples
	d.source = sys.AudioStreamBasicDescription{
		SampleRate:       opus.SampleRate,
		FormatID:         sys.FormatOpus,
		FramesPerPacket:  uint32(samples),
		ChannelsPerFrame: uint32(d.channels),
	}
}

func (d *decoder) createConverter() (err error) {
	d.disposeConverter()
	defer func() {
		if err != nil {
			d.disposeConverter()
		}
	}()
	dst := pcmDescription(d.cfg.SampleFormat, d.sampleRate, d.channels)
	var conv uintptr
	if st := sys.AudioConverterNew(&d.source, &dst, &conv); st != 0 {
		return d.unsupported("AudioToolbox has no decoder for this stream: " + sys.StatusString(st))
	}
	d.conv = conv
	if len(d.cookie) > 0 {
		if st := sys.SetBytes(conv, sys.PropDecompressionMagicCookie, d.cookie); st != 0 {
			return d.unsupported("AudioToolbox rejects the stream configuration: " + sys.StatusString(st))
		}
	}
	if d.channels > 2 {
		tag, ok := channelLayouts[d.channels]
		if !ok {
			return d.unsupported(fmt.Sprintf("%d channels are not supported; mono to 5.1 are", d.channels))
		}
		layout := sys.AudioChannelLayout{Tag: tag}
		if st := sys.AudioConverterSetProperty(conv, sys.PropOutputChannelLayout, uint32(unsafe.Sizeof(layout)), unsafe.Pointer(&layout)); st != 0 {
			return d.unsupported(fmt.Sprintf("AudioToolbox cannot lay out %d channels: %s", d.channels, sys.StatusString(st)))
		}
	}
	return nil
}

func (d *decoder) disposeConverter() {
	if d.conv != 0 {
		sys.AudioConverterDispose(d.conv)
		d.conv = 0
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
	if uint64(len(p.Data)) > math.MaxUint32 {
		return fmt.Errorf("hwmediacodec: audio packet exceeds AudioToolbox's buffer size: %w", codec.ErrInvalidData)
	}
	if len(d.out)+len(d.pts) >= maxPending {
		return codec.ErrAgain
	}
	if d.cfg.Format.Codec == codec.Opus {
		samples, err := opus.PacketSamples(p.Data)
		if err != nil {
			return fmt.Errorf("hwmediacodec: %w: %w", err, codec.ErrInvalidData)
		}
		if samples != d.frame || d.conv == 0 {
			d.setOpusFrame(samples)
			if err := d.createConverter(); err != nil {
				return err
			}
		}
	}
	d.flushed = false

	bytesPerFrame := d.channels * d.cfg.SampleFormat.BytesPerSample()
	buf := make([]byte, d.frame*bytesPerFrame)
	// The packet is consumed before the call returns, so it is handed
	// over without a copy.
	d.in.chunks = append(d.in.chunks[:0], p.Data)
	n, st := d.fill(buf)
	d.in.pinner.Unpin()
	d.in.chunks, d.in.held = d.in.chunks[:0], nil
	if st != 0 && st != statusNoData {
		// Leave the converter usable for the packets that follow.
		sys.AudioConverterReset(d.conv)
		d.pts, d.pcm = nil, nil
		return fmt.Errorf("%w: %w", backendError("AudioConverterFillComplexBuffer", st), codec.ErrInvalidData)
	}
	data := buf[:n*bytesPerFrame]
	if n > 0 && n < d.frame && d.cfg.Format.Codec == codec.Opus {
		// The Opus decoder withholds its first 2.5 ms for good. Those
		// samples lie inside the pre-skip, which players drop; they are
		// given back as silence so that every packet yields all the
		// samples it codes and timestamps stay exact.
		pad := (d.frame - n) * bytesPerFrame
		copy(buf[pad:], data)
		clear(buf[:pad])
		data = buf
	}
	d.pts = append(d.pts, p.PTS)
	if len(d.pcm) == 0 && len(data) == len(buf) {
		// The usual case: the packet's samples, all of them, right away.
		d.push(data)
		return nil
	}
	d.pcm = append(d.pcm, data...)
	d.emit(len(buf))
	return nil
}

// emit turns the samples waiting in pcm into frames of size bytes, one per
// packet in pts.
func (d *decoder) emit(size int) {
	for len(d.pcm) >= size && len(d.pts) > 0 {
		d.push(append([]byte(nil), d.pcm[:size]...))
		d.pcm = d.pcm[size:]
	}
	if len(d.pcm) == 0 {
		d.pcm = nil
	}
}

// push queues data as the frame of the oldest waiting packet.
func (d *decoder) push(data []byte) {
	d.out = append(d.out, &codec.AudioFrame{
		SampleRate: d.sampleRate,
		Channels:   d.channels,
		Format:     d.cfg.SampleFormat,
		Data:       data,
		PTS:        d.pts[0],
	})
	d.pts = d.pts[1:]
}

// fill asks the converter for up to one packet's worth of samples into buf
// and returns the number of samples per channel written.
func (d *decoder) fill(buf []byte) (int, int32) {
	list := sys.AudioBufferList{NumberBuffers: 1, Buffer: sys.AudioBuffer{
		NumberChannels: uint32(d.channels),
		DataByteSize:   uint32(len(buf)),
		Data:           unsafe.Pointer(&buf[0]),
	}}
	n := uint32(d.frame)
	st := sys.AudioConverterFillComplexBuffer(d.conv, inputCallback(), d.handle, &n, &list, nil)
	if n > uint32(d.frame) || uint64(list.Buffer.DataByteSize) > uint64(len(buf)) || uint64(n)*uint64(d.channels*d.cfg.SampleFormat.BytesPerSample()) > uint64(list.Buffer.DataByteSize) {
		return 0, sys.StatusInvalidOutputSize
	}
	return int(n), st
}

func (d *decoder) Receive(ctx context.Context) (*codec.AudioFrame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(d.out) > 0 {
		f := d.out[0]
		d.out[0] = nil
		d.out = d.out[1:]
		return f, nil
	}
	if d.flushed {
		return nil, io.EOF
	}
	return nil, codec.ErrAgain
}

// Flush implements codec.AudioDecoder: the samples the converter is still
// holding are fetched, and the converter is reset so that the next packet
// does not lean on the ones before.
func (d *decoder) Flush(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	d.flushed = true
	if len(d.pts) > 0 {
		size := d.frame * d.channels * d.cfg.SampleFormat.BytesPerSample()
		d.in.eof = true
		for {
			buf := make([]byte, size)
			n, st := d.fill(buf)
			if st != 0 && st != statusNoData {
				err = backendError("AudioConverterFillComplexBuffer", st)
			}
			if st != 0 || n == 0 {
				break
			}
			d.pcm = append(d.pcm, buf[:n*size/d.frame]...)
		}
		d.in.eof = false
		d.emit(size)
		if len(d.pts) > 0 && len(d.pcm) > 0 {
			// The stream ended short of a whole frame.
			d.push(d.pcm)
		}
	}
	d.pts, d.pcm = nil, nil
	if st := sys.AudioConverterReset(d.conv); st != 0 && err == nil {
		return backendError("AudioConverterReset", st)
	}
	return err
}

func (d *decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.disposeConverter()
	if d.handle != 0 {
		unregisterInput(d.handle)
	}
	d.out = nil
	d.pts, d.pcm = nil, nil
	d.in.release()
	return nil
}
