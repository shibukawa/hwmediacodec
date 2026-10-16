// Package audioutil holds what the audio backends share: the trial that
// measures an encoder's delay, the queue that hands a decoder's samples
// out one frame per packet, and sample format conversion. It contains no
// codec.
package audioutil

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// An encoder starts a few samples early (its delay, or priming) and the
// stream it makes is that much longer in front. Some codec APIs say by how
// much (AudioToolbox, libopus, libfdk-aac); others do not (WebCodecs,
// MediaCodec, Media Foundation), and the number differs from one
// implementation to the next. For those the delay is measured: a short
// signal is encoded, decoded again by the same platform, and looked for in
// the result.

// TrialSamples is the length of the trial signal, and TrialTail the
// silence sent after it so that the encoder gives all of it back.
const (
	TrialSamples = 6144
	TrialTail    = 8192
)

// TrialSignal is noise with most of its energy in the low end, which
// every codec keeps at every bitrate: one channel, in [-0.25, 0.25].
func TrialSignal() []float32 {
	out := make([]float32, TrialSamples)
	state := uint32(0x9E3779B9)
	var window [4]float32
	var sum float32
	for i := range out {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		v := float32(int32(state)) / (1 << 31)
		sum += v - window[i&3]
		window[i&3] = v
		out[i] = sum / 16
	}
	return out
}

// SignalDelay returns where the trial signal starts in the decoded
// samples (one channel): the lag at which a stretch of it correlates best
// with them. frame is the number of samples per packet, which bounds the
// search. ok is false when the signal is not found clearly.
func SignalDelay(signal, decoded []float32, frame int) (delay int, ok bool) {
	// The stretch starts a good way into the signal, clear of the codec's
	// onset and of samples an encoder may lose at the start.
	const from, length = 2048, 2048
	ref := signal[from : from+length]
	var refEnergy float64
	for _, v := range ref {
		refEnergy += float64(v) * float64(v)
	}
	best, bestScore := -1, 0.0
	for lag := 0; from+lag+length <= len(decoded) && lag <= 3*frame+1024; lag++ {
		got := decoded[from+lag : from+lag+length]
		var dot, energy float64
		for i, v := range ref {
			dot += float64(v) * float64(got[i])
			energy += float64(got[i]) * float64(got[i])
		}
		if energy == 0 {
			continue
		}
		if score := dot / math.Sqrt(refEnergy*energy); score > bestScore {
			best, bestScore = lag, score
		}
	}
	if best < 0 || bestScore < 0.6 {
		return 0, false
	}
	return best, true
}

// LostLead returns how many samples at the start of the trial signal came
// back as silence, rounded up to a multiple of 128 (WebKit's AAC encoder
// replaces the first 1024 samples of every signal with silence).
func LostLead(signal, decoded []float32, delay int) int {
	const block, limit = 64, 4096
	for at := 0; at < limit && delay+at+block <= len(decoded); at += block {
		var in, out float64
		for i := at; i < at+block; i++ {
			in += float64(signal[i]) * float64(signal[i])
			out += float64(decoded[delay+i]) * float64(decoded[delay+i])
		}
		if out >= in/16 {
			return (at + 127) / 128 * 128
		}
	}
	return 0
}

// MeasureDelay runs the trial on an encoder and a decoder of the same
// platform: the trial signal goes through enc (on every channel, in the
// sample format and at the rate and channel count given, which must be
// the encoder's), its packets through dec, and the place of the signal in
// the result is the encoder's delay. dec must return float samples
// (codec.F32) and all the samples of every packet. Both are used up by the
// call; the caller closes them.
//
// ok is false when the measurement cannot be trusted: the decoder did not
// return frame samples for every packet, or the signal was not found.
func MeasureDelay(ctx context.Context, enc codec.AudioEncoder, dec codec.AudioDecoder, format codec.SampleFormat, sampleRate, channels, frame int) (delay int, ok bool) {
	signal := TrialSignal()
	total := len(signal) + TrialTail
	pcm := make([]byte, total*channels*format.BytesPerSample())
	for i, v := range signal {
		for c := range channels {
			at := i*channels + c
			if format == codec.F32 {
				binary.LittleEndian.PutUint32(pcm[at*4:], math.Float32bits(v))
			} else {
				binary.LittleEndian.PutUint16(pcm[at*2:], uint16(int16(math.Round(float64(v)*32768))))
			}
		}
	}
	var packets []codec.Packet
	drain := func() bool {
		for {
			p, err := enc.Receive(ctx)
			if errors.Is(err, codec.ErrAgain) || err == io.EOF {
				return true
			}
			if err != nil {
				return false
			}
			packets = append(packets, p)
		}
	}
	// In pieces, as a caller would send them: an encoder's queue of
	// packets is bounded.
	const piece = 2048
	unit := channels * format.BytesPerSample()
	for at := 0; at < len(pcm); {
		end := min(at+piece*unit, len(pcm))
		err := enc.Send(ctx, &codec.AudioFrame{SampleRate: sampleRate, Channels: channels, Format: format, Data: pcm[at:end]})
		if errors.Is(err, codec.ErrAgain) {
			if !drain() {
				return 0, false
			}
			continue
		}
		if err != nil || !drain() {
			return 0, false
		}
		at = end
	}
	if enc.Flush(ctx) != nil || !drain() || len(packets) == 0 {
		return 0, false
	}

	var decoded []float32
	take := func() bool {
		for {
			f, err := dec.Receive(ctx)
			if errors.Is(err, codec.ErrAgain) || err == io.EOF {
				return true
			}
			if err != nil {
				return false
			}
			s := f.Float32s()
			for i := 0; i+f.Channels <= len(s); i += f.Channels {
				decoded = append(decoded, s[i])
			}
		}
	}
	for _, p := range packets {
		for {
			err := dec.Send(ctx, codec.Packet{Data: p.Data})
			if errors.Is(err, codec.ErrAgain) {
				if !take() {
					return 0, false
				}
				continue
			}
			if err != nil {
				return 0, false
			}
			break
		}
		if !take() {
			return 0, false
		}
	}
	if dec.Flush(ctx) != nil || !take() || len(decoded) != len(packets)*frame {
		return 0, false
	}
	return SignalDelay(signal, decoded, frame)
}
