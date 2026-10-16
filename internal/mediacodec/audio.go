//go:build android || mediacodec_typecheck

package mediacodec

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
	"unsafe"

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/audioutil"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/mediacodec/sys"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// The audio codecs of MediaCodec are the framework's software codecs on
// every device (c2.android.aac.decoder and the like), so nothing is
// refused for being software and Probe lists them with Hardware false.
// Their PCM side is 16-bit; float frames are converted here.

func audioMime(c codec.Codec) (string, bool) {
	switch c {
	case codec.AAC:
		return sys.MimeAAC, true
	case codec.Opus:
		return sys.MimeOpus, true
	}
	return "", false
}

// ProbeAudio implements codec.AudioBackend: AAC and Opus where MediaCodec
// has a decoder or an encoder for them.
func (Backend) ProbeAudio(ctx context.Context) ([]codec.Capability, error) {
	if err := sys.Load(); err != nil {
		return nil, nil
	}
	var caps []codec.Capability
	for _, c := range []codec.Codec{codec.AAC, codec.Opus} {
		if ctx.Err() != nil {
			return caps, ctx.Err()
		}
		mime, _ := audioMime(c)
		if _, ok := probeCodec(mime, false); ok {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Decode})
		}
		if _, ok := probeCodec(mime, true); ok {
			caps = append(caps, codec.Capability{Backend: Name, Codec: c, Direction: codec.Encode})
		}
	}
	return caps, nil
}

// NewAudioDecoder implements codec.AudioBackend.
func (Backend) NewAudioDecoder(ctx context.Context, cfg codec.AudioDecoderConfig) (codec.AudioDecoder, error) {
	c := cfg.Format.Codec
	mime, ok := audioMime(c)
	if !ok {
		return nil, unsupported(c, codec.Decode, "only aac and opus decoding are implemented")
	}
	if cfg.SampleFormat.BytesPerSample() == 0 {
		return nil, unsupported(c, codec.Decode, "sample format "+cfg.SampleFormat.String()+" is not supported; use S16 or F32")
	}
	if err := sys.Load(); err != nil {
		return nil, unsupported(c, codec.Decode, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return newAudioDecoder(cfg, mime)
}

type audioDecoder struct {
	mu   sync.Mutex
	cfg  codec.AudioDecoderConfig
	mime string
	// csd are the codec configuration buffers of the format: the
	// AudioSpecificConfig, or the Opus header, delay and pre-roll.
	csd [][]byte
	// frame is the number of samples a packet decodes to, or 0 for Opus,
	// whose packets say it themselves.
	frame int

	codec  uintptr
	frames audioutil.Frames
	// position counts the samples sent, for the input timestamps.
	position int64

	eosQueued bool
	eosSeen   bool
	flushed   bool
	closed    bool
}

func newAudioDecoder(cfg codec.AudioDecoderConfig, mime string) (*audioDecoder, error) {
	d := &audioDecoder{cfg: cfg, mime: mime}
	d.frames.Format = cfg.SampleFormat
	f := cfg.Format
	switch f.Codec {
	case codec.AAC:
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
		d.frames.SampleRate, d.frames.Channels, d.frame = c.OutputSampleRate(), c.OutputChannels(), c.OutputFrameSamples()
		if d.frames.Channels == 0 {
			d.frames.Channels = f.Channels
		}
		if d.frames.SampleRate <= 0 || d.frames.Channels <= 0 {
			return nil, unsupported(f.Codec, codec.Decode, "the AudioSpecificConfig does not give the sample rate and channel count")
		}
		if d.frames.Channels > 6 {
			return nil, unsupported(f.Codec, codec.Decode, fmt.Sprintf("%d channels are not supported; mono to 5.1 are", d.frames.Channels))
		}
		d.csd = [][]byte{asc}
	case codec.Opus:
		head := opus.Head{Channels: f.Channels, InputSampleRate: opus.SampleRate}
		if len(f.Config) > 0 {
			var err error
			if head, err = opus.ParseHead(f.Config); err != nil {
				return nil, fmt.Errorf("hwmediacodec: AudioFormat.Config: %w: %w", err, codec.ErrInvalidData)
			}
			if head.MappingFamily != 0 {
				return nil, unsupported(f.Codec, codec.Decode, "only mono and stereo Opus streams (channel mapping family 0) are supported")
			}
		}
		if head.Channels == 0 {
			head.Channels = 2
		}
		if head.Channels > 2 {
			return nil, unsupported(f.Codec, codec.Decode, "only mono and stereo Opus streams are supported")
		}
		d.frames.SampleRate, d.frames.Channels = opus.SampleRate, head.Channels
		// MediaCodec's Opus decoder drops the samples its second and third
		// configuration buffers name: the codec delay at the start and
		// the seek pre-roll after every flush, both in nanoseconds. The
		// library's decoders return every sample a packet codes, so both
		// are zero here, and so is the pre-skip in the header.
		head.PreSkip = 0
		d.csd = [][]byte{head.Marshal(), make([]byte, 8), make([]byte, 8)}
	}
	if err := d.open(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// open creates, configures and starts the codec.
func (d *audioDecoder) open() error {
	c := sys.AMediaCodec_createDecoderByType(d.mime)
	if c == 0 {
		return unsupported(d.cfg.Format.Codec, codec.Decode, "MediaCodec has no decoder for "+d.mime)
	}
	d.codec = c
	format := sys.AMediaFormat_new()
	defer sys.AMediaFormat_delete(format)
	sys.AMediaFormat_setString(format, sys.KeyMime, d.mime)
	sys.AMediaFormat_setInt32(format, sys.KeySampleRate, int32(d.frames.SampleRate))
	sys.AMediaFormat_setInt32(format, sys.KeyChannelCount, int32(d.frames.Channels))
	for i, b := range d.csd {
		sys.SetBuffer(format, fmt.Sprintf("csd-%d", i), b)
	}
	if st := sys.AMediaCodec_configure(c, format, 0, 0, 0); st != sys.OK {
		return unsupported(d.cfg.Format.Codec, codec.Decode, fmt.Sprintf("%s does not take the stream (%s): %s", sys.CodecName(c), sys.FormatString(format), sys.StatusString(st)))
	}
	if st := sys.AMediaCodec_start(c); st != sys.OK {
		return backendErr("AMediaCodec_start", st)
	}
	return nil
}

func (d *audioDecoder) Send(ctx context.Context, p codec.Packet) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := audioutil.CheckPacket(p.Data); err != nil {
		return err
	}
	if d.frames.Ready() >= maxPending {
		return codec.ErrAgain
	}
	samples := d.frame
	if d.cfg.Format.Codec == codec.Opus {
		var err error
		if samples, err = opus.PacketSamples(p.Data); err != nil {
			return fmt.Errorf("hwmediacodec: %w: %w", err, codec.ErrInvalidData)
		}
	}
	if d.flushed {
		// After the end of a stream the codec takes input again once it
		// has been flushed.
		if st := sys.AMediaCodec_flush(d.codec); st != sys.OK {
			return backendErr("AMediaCodec_flush", st)
		}
		d.flushed, d.eosQueued, d.eosSeen, d.position = false, false, false, 0
	}
	idx, err := dequeueInput(ctx, d.codec, d.drainReady)
	if err != nil {
		return err
	}
	var size uintptr
	buf := sys.AMediaCodec_getInputBuffer(d.codec, uintptr(idx), &size)
	if buf == nil || int(size) < len(p.Data) {
		return &codec.BackendError{Backend: Name, Op: "AMediaCodec_getInputBuffer", Message: fmt.Sprintf("input buffer of %d bytes cannot take a %d byte packet", size, len(p.Data))}
	}
	copy(unsafe.Slice(buf, size), p.Data)
	us := d.position * 1_000_000 / int64(d.frames.SampleRate)
	if st := sys.AMediaCodec_queueInputBuffer(d.codec, uintptr(idx), 0, uintptr(len(p.Data)), uint64(us), 0); st != sys.OK {
		return backendErr("AMediaCodec_queueInputBuffer", st)
	}
	d.position += int64(samples)
	d.frames.Expect(p.PTS, samples)
	return d.drainReady()
}

func (d *audioDecoder) drainReady() error { return d.drain(0) }

// drain dequeues output until none is ready, waiting up to timeoutUs for
// the first one.
func (d *audioDecoder) drain(timeoutUs int64) error {
	for {
		var info sys.BufferInfo
		idx := sys.AMediaCodec_dequeueOutputBuffer(d.codec, &info, timeoutUs)
		switch {
		case idx >= 0:
			var size uintptr
			buf := sys.AMediaCodec_getOutputBuffer(d.codec, uintptr(idx), &size)
			start, end, ok := outputRange(size, info.Offset, info.Size)
			if buf != nil && info.Size > 0 && !ok {
				sys.AMediaCodec_releaseOutputBuffer(d.codec, uintptr(idx), false)
				return &codec.BackendError{Backend: Name, Op: "AMediaCodec_getOutputBuffer", Message: fmt.Sprintf("buffer of %d bytes, samples at %d+%d", size, info.Offset, info.Size)}
			}
			if buf != nil && info.Size > 0 {
				pcm := unsafe.Slice(buf, size)[start:end]
				// The frames keep the samples: converted, or copied.
				d.frames.Append(audioutil.Convert(pcm, codec.S16, d.cfg.SampleFormat))
			}
			sys.AMediaCodec_releaseOutputBuffer(d.codec, uintptr(idx), false)
			if info.Flags&sys.BufferFlagEndOfStream != 0 {
				d.eosSeen = true
				return nil
			}
			timeoutUs = 0
		case idx == sys.InfoOutputFormatChanged:
			d.readOutputFormat()
		case idx == sys.InfoOutputBuffersChanged:
		case idx == sys.InfoTryAgainLater:
			return nil
		default:
			return backendErr("AMediaCodec_dequeueOutputBuffer", int32(idx))
		}
	}
}

// readOutputFormat takes the sample rate and channel count the decoder
// settled on, which say more than the configuration where the stream
// signals its extensions in the packets alone.
func (d *audioDecoder) readOutputFormat() {
	format := sys.AMediaCodec_getOutputFormat(d.codec)
	if format == 0 {
		return
	}
	defer sys.AMediaFormat_delete(format)
	if rate, ok := sys.FormatInt32(format, sys.KeySampleRate); ok && rate > 0 && int(rate) != d.frames.SampleRate {
		d.frames.Scale(int(rate), d.frames.SampleRate)
		d.frame = d.frame * int(rate) / d.frames.SampleRate
		d.frames.SampleRate = int(rate)
	}
	if channels, ok := sys.FormatInt32(format, sys.KeyChannelCount); ok && channels > 0 {
		d.frames.Channels = int(channels)
	}
}

func (d *audioDecoder) Receive(ctx context.Context) (*codec.AudioFrame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f := d.frames.Next(); f != nil {
		return f, nil
	}
	if d.flushed {
		return nil, io.EOF
	}
	if err := d.drainReady(); err != nil {
		return nil, err
	}
	if f := d.frames.Next(); f != nil {
		return f, nil
	}
	return nil, codec.ErrAgain
}

// Flush implements codec.AudioDecoder: the end of the stream is sent
// through the codec, which brings out the samples it is still holding.
func (d *audioDecoder) Flush(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !d.flushed && d.frames.Waiting() > 0 {
		if !d.eosQueued {
			if err := queueEOS(ctx, d.codec, d.drainReady); err != nil {
				return err
			}
			d.eosQueued = true
		}
		deadline := time.Now().Add(eosTimeout)
		for !d.eosSeen {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := d.drain(int64(inputPoll / time.Microsecond)); err != nil {
				return err
			}
			if time.Now().After(deadline) {
				return &codec.BackendError{Backend: Name, Op: "AMediaCodec_dequeueOutputBuffer", Message: "the " + d.mime + " decoder did not deliver its end of stream"}
			}
		}
	}
	// Send flushes the codec before the next packet, which is also what
	// makes it forget the packets before a jump.
	d.frames.End()
	d.flushed = true
	return nil
}

func (d *audioDecoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if d.codec != 0 {
		sys.AMediaCodec_stop(d.codec)
		sys.AMediaCodec_delete(d.codec)
		d.codec = 0
	}
	d.frames.Reset()
	d.frames.Drop()
	return nil
}
