//go:build js && wasm

package webcodecs

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"syscall/js"
	"time"

	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// maxAudioPending bounds the audio frames and packets held for Receive;
// Send reports ErrAgain beyond it.
const maxAudioPending = 64

// audioPacket is a packet whose frame is still to come.
type audioPacket struct {
	pts     int64
	samples int
}

type audioDecoder struct {
	cfg    codec.AudioDecoderConfig
	class  js.Value
	config map[string]any

	// sampleRate and channels are what the stream's configuration says,
	// replaced by what the browser's first AudioData says. frame is the
	// number of samples per packet, or 0 for Opus, whose packets differ.
	sampleRate int
	channels   int
	frame      int

	dec       js.Value
	opened    bool
	onOutput  js.Func
	onError   js.Func
	onDequeue js.Func
	wake      waiter

	// waiting are the packets sent and not yet answered, oldest first,
	// and pcm the samples decoded for them so far. The browser stamps its
	// output itself, so the two are matched by order: every packet gets
	// the samples it codes. started is set by the first output after a
	// start or a Flush, and position counts the samples sent since then.
	waiting  []audioPacket
	pcm      []byte
	started  bool
	position int64

	// interleaved is cleared when the browser refuses to convert to the
	// interleaved sample format in AudioData.copyTo; planar float, which
	// every browser converts to, is then interleaved here.
	interleaved bool
	copyBuf     js.Value
	copySize    int
	scratch     []byte

	out     []*codec.AudioFrame
	failure error
	flushed bool
	closed  bool
}

func newAudioDecoder(cfg codec.AudioDecoderConfig, cls js.Value, config map[string]any, sampleRate, channels, frame int) *audioDecoder {
	d := &audioDecoder{
		cfg:         cfg,
		class:       cls,
		config:      config,
		sampleRate:  sampleRate,
		channels:    channels,
		frame:       frame,
		wake:        newWaiter(),
		interleaved: true,
	}
	// Callbacks never block and never let a JavaScript exception escape:
	// either would take the whole program down.
	d.onOutput = js.FuncOf(func(_ js.Value, args []js.Value) any {
		d.output(first(args))
		return nil
	})
	d.onError = js.FuncOf(func(_ js.Value, args []js.Value) any {
		d.fail(first(args))
		return nil
	})
	d.onDequeue = js.FuncOf(func(js.Value, []js.Value) any {
		d.wake.signal()
		return nil
	})
	return d
}

// open creates and configures the AudioDecoder if there is none.
func (d *audioDecoder) open() error {
	if d.opened {
		return nil
	}
	err := try("new AudioDecoder", func() {
		d.dec = d.class.New(map[string]any{"output": d.onOutput, "error": d.onError})
		d.dec.Call("addEventListener", "dequeue", d.onDequeue)
		d.dec.Call("configure", d.config)
	})
	if err != nil {
		d.abandon()
		return err
	}
	d.opened = true
	return nil
}

// abandon drops what is in flight and the AudioDecoder itself.
func (d *audioDecoder) abandon() {
	d.waiting, d.pcm, d.started, d.position = nil, nil, false, 0
	if d.dec.Type() == js.TypeObject {
		try("AudioDecoder.close", func() {
			d.dec.Call("removeEventListener", "dequeue", d.onDequeue)
			if d.dec.Get("state").String() != "closed" {
				d.dec.Call("close")
			}
		})
	}
	d.dec, d.opened = js.Undefined(), false
}

// fail is the AudioDecoder's error callback. The browser has closed the
// decoder; the error waits for the next method call.
func (d *audioDecoder) fail(e js.Value) {
	switch errorName(e) {
	case "EncodingError", "DataError":
		d.failure = fmt.Errorf("%w: %s: %s", codec.ErrInvalidData, Name, errorText(e))
	case "NotSupportedError":
		d.failure = unsupported(d.cfg.Format.Codec, codec.Decode, errorText(e))
	default:
		d.failure = jsError("AudioDecoder", e)
	}
	d.wake.signal()
}

// takeFailure returns the pending error once and drops the decoder, so
// that the next packet starts a new one.
func (d *audioDecoder) takeFailure() error {
	err := d.failure
	if err == nil {
		return nil
	}
	d.failure = nil
	d.abandon()
	return err
}

func (d *audioDecoder) Send(ctx context.Context, p codec.Packet) error {
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(p.Data) == 0 {
		return fmt.Errorf("hwmediacodec: empty audio packet: %w", codec.ErrInvalidData)
	}
	if err := d.takeFailure(); err != nil {
		return err
	}
	if len(d.out) >= maxAudioPending {
		return codec.ErrAgain
	}
	samples := d.frame
	if d.cfg.Format.Codec == codec.Opus {
		var err error
		if samples, err = opus.PacketSamples(p.Data); err != nil {
			return fmt.Errorf("hwmediacodec: %w: %w", err, codec.ErrInvalidData)
		}
	}
	if err := d.open(); err != nil {
		return err
	}
	d.flushed = false
	err := try("AudioDecoder.decode", func() {
		chunk := global.Get("EncodedAudioChunk").New(map[string]any{
			"type": "key",
			// Where the packet lies in the samples sent so far: the
			// browser derives its own stamps from the first one.
			"timestamp": float64(d.position) * 1e6 / float64(d.sampleRate),
			"data":      bufferOf(p.Data),
		})
		d.dec.Call("decode", chunk)
	})
	if err != nil {
		return err
	}
	d.position += int64(samples)
	d.waiting = append(d.waiting, audioPacket{pts: p.PTS, samples: samples})
	return d.waitQueue(ctx)
}

// waitQueue blocks while the browser's decoder has chunks it has not
// taken yet; the page's event loop runs meanwhile, and with it the output
// callback.
func (d *audioDecoder) waitQueue(ctx context.Context) error {
	deadline := time.Now().Add(stallTimeout)
	for d.failure == nil && d.opened && len(d.out) < maxAudioPending {
		if d.dec.Get("decodeQueueSize").Int() == 0 {
			return nil
		}
		if err := d.wake.wait(ctx, "AudioDecoder.decode", deadline); err != nil {
			return err
		}
	}
	// A failure is reported by the next call.
	return nil
}

// output is the AudioDecoder's output callback: the samples are copied
// out and the AudioData closed.
func (d *audioDecoder) output(ad js.Value) {
	var data []byte
	err := try("AudioData", func() {
		defer ad.Call("close")
		n := ad.Get("numberOfFrames").Int()
		channels := ad.Get("numberOfChannels").Int()
		rate := int(math.Round(ad.Get("sampleRate").Float()))
		if rate > 0 && rate != d.sampleRate {
			// The stream carries more than its configuration told: band
			// replication signalled in the packets alone doubles the rate
			// and with it the samples per packet.
			d.frame = d.frame * rate / d.sampleRate
			for i := range d.waiting {
				d.waiting[i].samples = d.waiting[i].samples * rate / d.sampleRate
			}
			d.sampleRate = rate
		}
		if channels > 0 {
			d.channels = channels
		}
		data = d.copyOut(ad, n, channels)
	})
	if err != nil {
		if d.failure == nil {
			d.failure = err
		}
		d.wake.signal()
		return
	}
	size := d.channels * d.cfg.SampleFormat.BytesPerSample()
	if !d.started && d.cfg.Format.Codec == codec.Opus && len(d.waiting) > 0 {
		// An Opus decoder that withholds its first samples for good
		// (AudioToolbox's does, 2.5 ms) gets them back as silence: they
		// lie inside the pre-skip, and every packet keeps all the samples
		// it codes.
		if missing := d.waiting[0].samples*size - len(data); missing > 0 && len(data) > 0 {
			d.pcm = append(d.pcm, make([]byte, missing)...)
		}
	}
	d.started = true
	d.pcm = append(d.pcm, data...)
	d.emit()
	d.wake.signal()
}

// bitstreamOrder maps the place of a channel in the order of a WAV file
// (left, right, centre, LFE, surrounds) to its place in the order an AAC
// stream stores it in (centre first, LFE last), by channel count.
var bitstreamOrder = map[int][]int{
	3: {1, 2, 0},
	4: {1, 2, 0, 3},
	5: {1, 2, 0, 3, 4},
	6: {1, 2, 0, 5, 3, 4},
}

// channelOrder returns where the browser has each channel of the output,
// or nil when it has them in output order already. The API names no
// order. Chrome and Firefox give AAC with more than two channels in the
// order of a WAV file, which is the library's; WebKit gives it as the
// stream stores it (measured with Safari 27 on macOS), so there the
// channels are put in order here.
func (d *audioDecoder) channelOrder(channels int) []int {
	if d.cfg.Format.Codec != codec.AAC || !webKit() {
		return nil
	}
	return bitstreamOrder[channels]
}

// copyOut returns the n samples of every channel of ad, interleaved in
// the decoder's sample format.
func (d *audioDecoder) copyOut(ad js.Value, n, channels int) []byte {
	bps := d.cfg.SampleFormat.BytesPerSample()
	out := make([]byte, n*channels*bps)
	if len(out) == 0 {
		return out
	}
	if order := d.channelOrder(channels); order != nil {
		defer func() {
			var moment [6 * 4]byte
			for i := 0; i < len(out); i += channels * bps {
				copy(moment[:], out[i:i+channels*bps])
				for c, from := range order {
					copy(out[i+c*bps:i+(c+1)*bps], moment[from*bps:])
				}
			}
		}()
	}
	buffer := func(size int) js.Value {
		if d.copySize < size {
			d.copyBuf, d.copySize = uint8Array.New(size), size
		}
		return d.copyBuf.Call("subarray", 0, size)
	}
	if d.interleaved {
		err := try("AudioData.copyTo", func() {
			buf := buffer(len(out))
			ad.Call("copyTo", buf, map[string]any{"planeIndex": 0, "format": sampleFormatName(d.cfg.SampleFormat)})
			js.CopyBytesToGo(out, buf)
		})
		if err == nil {
			return out
		}
		d.interleaved = false
	}
	// Planar float is the one conversion every browser has to offer.
	if len(d.scratch) < n*4 {
		d.scratch = make([]byte, n*4)
	}
	plane := d.scratch[:n*4]
	for c := range channels {
		buf := buffer(len(plane))
		ad.Call("copyTo", buf, map[string]any{"planeIndex": c, "format": "f32-planar"})
		js.CopyBytesToGo(plane, buf)
		for i := range n {
			at := (i*channels + c) * bps
			if d.cfg.SampleFormat == codec.F32 {
				copy(out[at:at+4], plane[i*4:])
				continue
			}
			v := math.Float32frombits(binary.LittleEndian.Uint32(plane[i*4:]))
			s := math.Round(float64(v) * 32768)
			binary.LittleEndian.PutUint16(out[at:], uint16(int16(max(-32768, min(32767, s)))))
		}
	}
	return out
}

// emit turns the samples waiting in pcm into frames, one per packet.
func (d *audioDecoder) emit() {
	size := d.channels * d.cfg.SampleFormat.BytesPerSample()
	for len(d.waiting) > 0 {
		need := d.waiting[0].samples * size
		if need == 0 || len(d.pcm) < need {
			break
		}
		d.push(append([]byte(nil), d.pcm[:need]...))
		d.pcm = d.pcm[need:]
	}
	if len(d.pcm) == 0 {
		d.pcm = nil
	}
}

// push queues data as the frame of the oldest waiting packet.
func (d *audioDecoder) push(data []byte) {
	d.out = append(d.out, &codec.AudioFrame{
		SampleRate: d.sampleRate,
		Channels:   d.channels,
		Format:     d.cfg.SampleFormat,
		Data:       data,
		PTS:        d.waiting[0].pts,
	})
	d.waiting = d.waiting[1:]
}

func (d *audioDecoder) Receive(ctx context.Context) (*codec.AudioFrame, error) {
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
	if err := d.takeFailure(); err != nil {
		return nil, err
	}
	if d.flushed {
		return nil, io.EOF
	}
	return nil, codec.ErrAgain
}

// Flush implements codec.AudioDecoder: it waits until the browser has
// delivered the samples of every packet sent, which also leaves its
// decoder ready for a packet from anywhere in the stream.
func (d *audioDecoder) Flush(ctx context.Context) error {
	if d.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.takeFailure(); err != nil {
		return err
	}
	if d.opened {
		var promise js.Value
		if err := try("AudioDecoder.flush", func() { promise = d.dec.Call("flush") }); err != nil {
			return err
		}
		if _, err := await(ctx, "AudioDecoder.flush", promise); err != nil {
			if ctx.Err() == nil && d.failure != nil {
				// The decoder failed on the way; the failure says why.
				return d.takeFailure()
			}
			d.abandon()
			return err
		}
		if err := d.takeFailure(); err != nil {
			return err
		}
		d.emit()
		if len(d.waiting) > 0 && len(d.pcm) > 0 {
			// The stream ended short of a whole frame.
			d.push(d.pcm)
		}
	}
	d.waiting, d.pcm, d.started, d.position = nil, nil, false, 0
	d.flushed = true
	return nil
}

func (d *audioDecoder) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	d.abandon()
	d.failure = nil
	d.out = nil
	d.onOutput.Release()
	d.onError.Release()
	d.onDequeue.Release()
	return nil
}
