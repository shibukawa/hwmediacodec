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

	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/audioutil"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// audioTraits is what the API does not tell about the browser's encoder
// for a codec, sample rate and channel count, and a trial run does.
type audioTraits struct {
	// config is the codec configuration record of the encoder's stream:
	// the AudioSpecificConfig, or the OpusHead.
	config []byte
	// frame is the number of samples per packet and delay the number of
	// samples the encoder starts early by.
	frame int
	delay int
	// measured reports that delay was found in this browser (read from
	// the identification header or measured through its decoder), not
	// assumed.
	measured bool
	// lead is the number of samples at the start of a signal that the
	// encoder replaces with silence (WebKit's AAC encoder does that to
	// its first 1024). That much silence is sent ahead of every signal,
	// so that it is silence that is lost; delay includes it.
	lead int
}

// Delays to assume where the trial cannot find one: those of the encoders
// the browsers use most (AudioToolbox's AAC, libopus).
const (
	assumedAACDelay  = 2112
	assumedOpusDelay = 312
)

// knownAudioTraits remembers the trial results for the page, by
// configuration, and knownAudioDelays the delays found, by codec, sample
// rate and channel count: the delay is a property of the encoder, not of
// its bitrate.
var (
	knownAudioTraits = map[string]audioTraits{}
	knownAudioDelays = map[string]audioTraits{}
)

// encoderTraits returns the traits of the browser's encoder for cfg, from
// a trial the first time a configuration is asked for: a short signal is
// encoded, which gives the configuration record and the packet length and
// shows whether the encoder works at all (Chrome's AAC encoder on a Mac
// accepts some bitrates in isConfigSupported and then fails on the first
// packets). Where the API does not give the delay (AAC) the packets are
// decoded by the browser again and the signal is looked for in the
// result.
func encoderTraits(ctx context.Context, cfg *codec.AudioEncoderConfig, cls js.Value, config map[string]any) (audioTraits, error) {
	stream := fmt.Sprintf("%s %d %d", cfg.Codec, cfg.SampleRate, cfg.Channels)
	key := fmt.Sprintf("%s %d %s", stream, cfg.Bitrate, cfg.RateControl)
	if t, ok := knownAudioTraits[key]; ok {
		return t, nil
	}
	signal := audioutil.TrialSignal()
	chunks, description, micros, err := trialEncode(ctx, cfg, cls, config, signal)
	if err != nil {
		if ctx.Err() != nil {
			return audioTraits{}, ctx.Err()
		}
		reason := fmt.Sprintf("the browser's encoder does not work for %d Hz with %d channels", cfg.SampleRate, cfg.Channels)
		if cfg.Bitrate > 0 {
			reason += fmt.Sprintf(" at %d bit/s", cfg.Bitrate)
		}
		return audioTraits{}, unsupported(cfg.Codec, codec.Encode, reason+": "+err.Error())
	}
	if cfg.Codec == codec.AAC {
		// Safari hands out the whole ES_Descriptor, not the config in it.
		if asc := aac.ConfigFromESDescriptor(description); asc != nil {
			description = asc
		}
	}
	t := audioTraits{config: description}
	if micros > 0 {
		t.frame = int(math.Round(micros * float64(cfg.SampleRate) / 1e6))
	}
	switch cfg.Codec {
	case codec.AAC:
		if t.frame == 0 {
			t.frame = 1024
		}
		t.delay = assumedAACDelay
		if len(t.config) == 0 {
			if t.config, err = aac.LCConfig(cfg.SampleRate, cfg.Channels); err != nil {
				return audioTraits{}, unsupported(cfg.Codec, codec.Encode, err.Error())
			}
		}
	case codec.Opus:
		if t.frame == 0 {
			t.frame = opusFrameMicros * opus.SampleRate / 1_000_000
		}
		t.delay = assumedOpusDelay
		if h, err := opus.ParseHead(t.config); err == nil {
			t.delay, t.measured = h.PreSkip, true
		}
	}
	if known, ok := knownAudioDelays[stream]; ok {
		t.delay, t.lead, t.measured = known.delay, known.lead, true
	} else if len(chunks) > 0 {
		// The decoder is given no Opus header: with one it would drop the
		// very samples the measurement looks at.
		format := codec.AudioFormat{Codec: cfg.Codec, SampleRate: cfg.SampleRate, Channels: cfg.Channels}
		if cfg.Codec == codec.AAC {
			format.Config = t.config
		}
		if decoded, ok := trialDecode(ctx, format, chunks, t.frame); ok {
			if !t.measured {
				if delay, ok := audioutil.SignalDelay(signal, decoded, t.frame); ok {
					t.delay, t.measured = delay, true
				}
			}
			if t.measured {
				t.lead = audioutil.LostLead(signal, decoded, t.delay)
				t.delay += t.lead
			}
		}
		if t.measured {
			knownAudioDelays[stream] = t
		}
	}
	if cfg.Codec == codec.Opus {
		t.config = opus.Head{Channels: cfg.Channels, PreSkip: t.delay, InputSampleRate: cfg.SampleRate}.Marshal()
	}
	knownAudioTraits[key] = t
	return t, nil
}

// trialEncode sends the signal (on every channel) and the tail of silence
// through an encoder of its own and returns the chunks, the description
// the encoder reports and the duration of a chunk in microseconds.
func trialEncode(ctx context.Context, cfg *codec.AudioEncoderConfig, cls js.Value, config map[string]any, signal []float32) (chunks [][]byte, description []byte, micros float64, err error) {
	var failure error
	onOutput := js.FuncOf(func(_ js.Value, args []js.Value) any {
		try("EncodedAudioChunk", func() {
			chunk := first(args)
			if len(args) > 1 && description == nil {
				description = chunkDescription(args[1])
			}
			if d := chunk.Get("duration"); d.Type() == js.TypeNumber && micros == 0 {
				micros = d.Float()
			}
			buf := uint8Array.New(chunk.Get("byteLength").Int())
			chunk.Call("copyTo", buf)
			data := make([]byte, buf.Length())
			js.CopyBytesToGo(data, buf)
			chunks = append(chunks, data)
		})
		return nil
	})
	onError := js.FuncOf(func(_ js.Value, args []js.Value) any {
		failure = audioEncoderError(cfg.Codec, first(args))
		return nil
	})
	defer onOutput.Release()
	defer onError.Release()

	total := len(signal) + audioutil.TrialTail
	pcm := make([]byte, total*cfg.Channels*4)
	for i, v := range signal {
		for c := range cfg.Channels {
			binary.LittleEndian.PutUint32(pcm[(i*cfg.Channels+c)*4:], math.Float32bits(v))
		}
	}
	var enc, promise js.Value
	err = try("AudioEncoder", func() {
		enc = cls.New(map[string]any{"output": onOutput, "error": onError})
		enc.Call("configure", config)
		data := global.Get("AudioData").New(map[string]any{
			"format": "f32", "sampleRate": cfg.SampleRate, "numberOfFrames": total,
			"numberOfChannels": cfg.Channels, "timestamp": 0, "data": bufferOf(pcm),
		})
		defer data.Call("close")
		enc.Call("encode", data)
		promise = enc.Call("flush")
	})
	defer try("AudioEncoder.close", func() {
		if enc.Type() == js.TypeObject && enc.Get("state").String() != "closed" {
			enc.Call("close")
		}
	})
	if err == nil {
		_, err = await(ctx, "AudioEncoder.flush", promise)
	}
	if failure != nil {
		err = failure
	}
	if err != nil {
		return nil, nil, 0, err
	}
	return chunks, description, micros, nil
}

// chunkDescription returns the description of the decoder configuration
// in a chunk's metadata, or nil.
func chunkDescription(meta js.Value) []byte {
	if meta.Type() != js.TypeObject {
		return nil
	}
	dc := meta.Get("decoderConfig")
	if dc.Type() != js.TypeObject {
		return nil
	}
	desc := dc.Get("description")
	if desc.Type() != js.TypeObject {
		return nil
	}
	return bytesOf(desc)
}

// trialDecode decodes the trial's chunks with the browser's decoder and
// returns the first channel. ok is false when the browser's decoder does
// not return a frame of samples for every chunk: it trims something
// itself, so its output says nothing about the encoder.
func trialDecode(ctx context.Context, format codec.AudioFormat, chunks [][]byte, frame int) (decoded []float32, ok bool) {
	dec, err := Backend{}.NewAudioDecoder(ctx, codec.AudioDecoderConfig{Format: format, SampleFormat: codec.F32})
	if err != nil {
		return nil, false
	}
	defer dec.Close()
	take := func() {
		for {
			f, err := dec.Receive(ctx)
			if err != nil {
				return
			}
			s := f.Float32s()
			for i := 0; i < len(s); i += f.Channels {
				decoded = append(decoded, s[i])
			}
		}
	}
	for _, c := range chunks {
		if dec.Send(ctx, codec.Packet{Data: c}) != nil {
			return nil, false
		}
		take()
	}
	if dec.Flush(ctx) != nil {
		return nil, false
	}
	take()
	return decoded, len(decoded) == len(chunks)*frame
}

func audioEncoderError(c codec.Codec, v js.Value) error {
	if errorName(v) == "NotSupportedError" {
		return unsupported(c, codec.Encode, errorText(v))
	}
	return jsError("AudioEncoder", v)
}

type audioEncoder struct {
	cfg    codec.AudioEncoderConfig
	class  js.Value
	config map[string]any
	format codec.AudioFormat
	// unit is the size of one sample of every channel.
	unit int
	// lead is the silence to send ahead of a signal (see audioTraits).
	lead int

	enc       js.Value
	opened    bool
	onOutput  js.Func
	onError   js.Func
	onDequeue js.Func
	wake      waiter
	out       js.Value
	outSize   int

	// started is set by the first frame of a signal, whose PTS becomes
	// basePTS. samplesIn counts the samples of the signal, samplesSent
	// those handed to the browser (the silence Flush adds included) and
	// packetsOut the chunks that came back.
	started     bool
	basePTS     int64
	samplesIn   int64
	samplesSent int64
	packetsOut  int64

	// input holds the samples Send accepted but has not handed to the
	// browser yet: a frame is fed in pieces, as far as the packets it
	// will produce fit maxAudioPending, and the rest goes as Receive
	// makes room (at most one frame waits, since Send refuses more while
	// the queue is full).
	input []byte

	pending []codec.Packet
	failure error
	flushed bool
	closed  bool
}

// feedPackets is the number of packets' worth of samples handed to the
// browser in one AudioData while a frame is fed in pieces.
const feedPackets = 16

func newAudioEncoder(cfg codec.AudioEncoderConfig, cls js.Value, config map[string]any, traits audioTraits) *audioEncoder {
	e := &audioEncoder{
		cfg:    cfg,
		class:  cls,
		config: config,
		unit:   cfg.Channels * cfg.SampleFormat.BytesPerSample(),
		wake:   newWaiter(),
		format: codec.AudioFormat{
			Codec:        cfg.Codec,
			SampleRate:   cfg.SampleRate,
			Channels:     cfg.Channels,
			Config:       traits.config,
			FrameSamples: traits.frame,
			Delay:        traits.delay,
		},
		lead: traits.lead,
	}
	e.onOutput = js.FuncOf(func(_ js.Value, args []js.Value) any {
		e.output(first(args))
		return nil
	})
	e.onError = js.FuncOf(func(_ js.Value, args []js.Value) any {
		e.failure = audioEncoderError(e.cfg.Codec, first(args))
		e.wake.signal()
		return nil
	})
	e.onDequeue = js.FuncOf(func(js.Value, []js.Value) any {
		e.wake.signal()
		return nil
	})
	return e
}

func (e *audioEncoder) Format() codec.AudioFormat {
	f := e.format
	f.Config = append([]byte(nil), f.Config...)
	return f
}

// open creates and configures the AudioEncoder if there is none. Every
// signal gets an encoder of its own: one that has been flushed is closed,
// so that the next signal starts like the first.
func (e *audioEncoder) open() error {
	if e.opened {
		return nil
	}
	err := try("new AudioEncoder", func() {
		e.enc = e.class.New(map[string]any{"output": e.onOutput, "error": e.onError})
		e.enc.Call("addEventListener", "dequeue", e.onDequeue)
		e.enc.Call("configure", e.config)
	})
	if err != nil {
		e.abandon()
		return err
	}
	e.opened = true
	return nil
}

// abandon drops the AudioEncoder; the packets already received stay.
func (e *audioEncoder) abandon() {
	if e.enc.Type() == js.TypeObject {
		try("AudioEncoder.close", func() {
			e.enc.Call("removeEventListener", "dequeue", e.onDequeue)
			if e.enc.Get("state").String() != "closed" {
				e.enc.Call("close")
			}
		})
	}
	e.enc, e.opened, e.started, e.input = js.Undefined(), false, false, nil
}

// takeFailure returns the pending error once and drops the encoder.
func (e *audioEncoder) takeFailure() error {
	err := e.failure
	if err == nil {
		return nil
	}
	e.failure = nil
	e.abandon()
	return err
}

func (e *audioEncoder) Send(ctx context.Context, f *codec.AudioFrame) error {
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
	if err := e.takeFailure(); err != nil {
		return err
	}
	if e.queued() >= maxAudioPending {
		return codec.ErrAgain
	}
	if err := e.open(); err != nil {
		return err
	}
	e.flushed = false
	if !e.started {
		e.started, e.basePTS, e.samplesIn, e.samplesSent, e.packetsOut = true, f.PTS, 0, 0, 0
		if e.lead > 0 {
			if err := e.encode(make([]byte, e.lead*e.unit)); err != nil {
				return err
			}
		}
	}
	if len(f.Data) == 0 {
		return nil
	}
	e.samplesIn += int64(len(f.Data) / e.unit)
	e.input = append(e.input, f.Data...)
	if err := e.feed(); err != nil {
		return err
	}
	return e.waitQueue(ctx)
}

// queued is the number of packets held for Receive plus those the browser
// still owes for the samples it was handed.
func (e *audioEncoder) queued() int {
	frame := int64(e.format.FrameSamples)
	owed := (e.samplesSent+frame-1)/frame - e.packetsOut
	return len(e.pending) + int(max(owed, 0))
}

// feed hands the browser as much of input as keeps queued within
// maxAudioPending, feedPackets packets at a time.
func (e *audioEncoder) feed() error {
	piece := e.format.FrameSamples * feedPackets * e.unit
	for len(e.input) > 0 && e.queued() < maxAudioPending {
		n := min(len(e.input), piece)
		if err := e.encode(e.input[:n]); err != nil {
			return err
		}
		e.input = e.input[n:]
	}
	if len(e.input) == 0 {
		e.input = nil
	}
	return nil
}

// encode hands interleaved samples in the encoder's format to the
// browser, which cuts them into packets itself.
func (e *audioEncoder) encode(pcm []byte) error {
	n := len(pcm) / e.unit
	err := try("AudioEncoder.encode", func() {
		data := global.Get("AudioData").New(map[string]any{
			"format":           sampleFormatName(e.cfg.SampleFormat),
			"sampleRate":       e.cfg.SampleRate,
			"numberOfFrames":   n,
			"numberOfChannels": e.cfg.Channels,
			"timestamp":        float64(e.samplesSent) * 1e6 / float64(e.cfg.SampleRate),
			"data":             bufferOf(pcm),
		})
		// The encoder takes its own reference; this one is done.
		defer data.Call("close")
		e.enc.Call("encode", data)
	})
	if err == nil {
		e.samplesSent += int64(n)
	}
	return err
}

// waitQueue blocks while the browser's encoder has input it has not
// taken yet; the page's event loop runs meanwhile, and with it the output
// callback.
func (e *audioEncoder) waitQueue(ctx context.Context) error {
	deadline := time.Now().Add(stallTimeout)
	for e.failure == nil && e.opened && len(e.pending) < maxAudioPending {
		if e.enc.Get("encodeQueueSize").Int() == 0 {
			return nil
		}
		if err := e.wake.wait(ctx, "AudioEncoder.encode", deadline); err != nil {
			return err
		}
	}
	return nil
}

// output is the AudioEncoder's output callback. The browser stamps its
// chunks from the time of the first input; the packets are stamped here
// instead, like on every backend: packet k codes the samples from
// k*FrameSamples - Delay on, counted from the first sample sent, and what
// lies past the last sample sent is padding.
func (e *audioEncoder) output(chunk js.Value) {
	err := try("EncodedAudioChunk", func() {
		n := chunk.Get("byteLength").Int()
		if e.outSize < n {
			e.outSize = n + n/2
			e.out = uint8Array.New(e.outSize)
		}
		chunk.Call("copyTo", e.out)
		data := make([]byte, n)
		js.CopyBytesToGo(data, e.out)

		frame, delay := int64(e.format.FrameSamples), int64(e.format.Delay)
		start := e.packetsOut*frame - delay
		e.packetsOut++
		if start >= e.samplesIn {
			// Nothing but the silence Flush sent to get the last samples
			// out.
			return
		}
		end := min(start+frame, e.samplesIn)
		rate, scale := int64(e.cfg.SampleRate), int64(e.cfg.TimeScale)
		pts := e.basePTS + codec.Rescale(start, rate, scale)
		p := codec.Packet{Data: data, PTS: pts, DTS: pts, Keyframe: true}
		if end > start {
			p.Duration = e.basePTS + codec.Rescale(end, rate, scale) - pts
		}
		e.pending = append(e.pending, p)
	})
	if err != nil && e.failure == nil {
		e.failure = err
	}
	e.wake.signal()
}

func (e *audioEncoder) Receive(ctx context.Context) (codec.Packet, error) {
	if e.closed {
		return codec.Packet{}, codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return codec.Packet{}, err
	}
	if len(e.input) > 0 && e.failure == nil {
		// Room for the samples Send left with us.
		if err := e.feed(); err != nil {
			return codec.Packet{}, err
		}
	}
	if len(e.pending) > 0 {
		p := e.pending[0]
		e.pending[0] = codec.Packet{}
		e.pending = e.pending[1:]
		return p, nil
	}
	if err := e.takeFailure(); err != nil {
		return codec.Packet{}, err
	}
	if e.flushed {
		return codec.Packet{}, io.EOF
	}
	return codec.Packet{}, codec.ErrAgain
}

// Flush implements codec.AudioEncoder. The samples Send left unencoded go
// to the browser first, so pending may exceed maxAudioPending afterwards
// by what one frame codes to. Whether a browser's encoder gives back the
// samples it is still holding when it is flushed is not specified, so it
// is sent enough silence to push them out; the packets that code nothing
// but that silence are dropped as they arrive.
func (e *audioEncoder) Flush(ctx context.Context) error {
	if e.closed {
		return codec.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.takeFailure(); err != nil {
		return err
	}
	if e.opened && e.started {
		if len(e.input) > 0 {
			rest := e.input
			e.input = nil
			if err := e.encode(rest); err != nil {
				e.abandon()
				return err
			}
		}
		silence := make([]byte, (e.format.Delay+2*e.format.FrameSamples)*e.unit)
		if err := e.encode(silence); err != nil {
			e.abandon()
			return err
		}
		var promise js.Value
		if err := try("AudioEncoder.flush", func() { promise = e.enc.Call("flush") }); err != nil {
			e.abandon()
			return err
		}
		if _, err := await(ctx, "AudioEncoder.flush", promise); err != nil {
			if ctx.Err() == nil && e.failure != nil {
				// The encoder failed on the way; the failure says why.
				return e.takeFailure()
			}
			e.abandon()
			return err
		}
		if err := e.takeFailure(); err != nil {
			return err
		}
	}
	e.abandon()
	e.flushed = true
	return nil
}

func (e *audioEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	e.abandon()
	e.failure = nil
	e.pending = nil
	e.input = nil
	e.onOutput.Release()
	e.onError.Release()
	e.onDequeue.Release()
	return nil
}
