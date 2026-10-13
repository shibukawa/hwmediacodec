package audioutil

import (
	"encoding/binary"
	"math"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// Frames hands a decoder's samples out one frame per packet. Not every
// codec API returns a packet's samples at once, in one piece and stamped
// like the packet: some run behind, some cut their output into buffers of
// their own. The library's decoders promise that every packet yields one
// frame with its PTS and all its samples, so the packets wait here in
// order with the number of samples each codes, the decoded samples are
// appended as they come, and whenever the oldest packet has all of its
// samples a frame is cut for it.
type Frames struct {
	// SampleRate, Channels and Format describe the frames. They may be
	// changed between packets (a decoder that finds out more about the
	// stream than its configuration said).
	SampleRate int
	Channels   int
	Format     codec.SampleFormat

	waiting []packet
	pcm     []byte
	out     []*codec.AudioFrame
}

type packet struct {
	pts     int64
	samples int
}

// Expect notes a packet that was handed to the decoder: its PTS and the
// number of samples per channel it codes.
func (f *Frames) Expect(pts int64, samples int) {
	f.waiting = append(f.waiting, packet{pts: pts, samples: samples})
}

// Waiting is the number of packets whose frames are still to come.
func (f *Frames) Waiting() int { return len(f.waiting) }

// Scale multiplies the sample count of every waiting packet by num/den:
// for a decoder that turns out to return another rate than announced
// (band replication signalled in the packets alone doubles it).
func (f *Frames) Scale(num, den int) {
	for i := range f.waiting {
		f.waiting[i].samples = f.waiting[i].samples * num / den
	}
}

// Append adds decoded samples (interleaved, in Format) and cuts the frames
// that are complete with them.
func (f *Frames) Append(data []byte) {
	f.pcm = append(f.pcm, data...)
	f.cut()
}

// Pad adds silence in front of the samples still to come, for a decoder
// that withholds the first samples of a stream for good.
func (f *Frames) Pad(samples int) {
	f.pcm = append(f.pcm, make([]byte, samples*f.unit())...)
}

func (f *Frames) unit() int { return f.Channels * f.Format.BytesPerSample() }

func (f *Frames) cut() {
	unit := f.unit()
	for len(f.waiting) > 0 {
		need := f.waiting[0].samples * unit
		if need == 0 || len(f.pcm) < need {
			break
		}
		f.push(append([]byte(nil), f.pcm[:need]...))
		f.pcm = f.pcm[need:]
	}
	if len(f.pcm) == 0 {
		f.pcm = nil
	}
}

func (f *Frames) push(data []byte) {
	f.out = append(f.out, &codec.AudioFrame{
		SampleRate: f.SampleRate,
		Channels:   f.Channels,
		Format:     f.Format,
		Data:       data,
		PTS:        f.waiting[0].pts,
	})
	f.waiting = f.waiting[1:]
}

// End is called when the decoder has given everything it has: a last
// packet that is short of its samples gets what there is, and what is
// left over is dropped.
func (f *Frames) End() {
	f.cut()
	if len(f.waiting) > 0 && len(f.pcm) > 0 {
		f.push(f.pcm)
	}
	f.waiting, f.pcm = nil, nil
}

// Reset drops the packets and samples that have not become frames.
func (f *Frames) Reset() { f.waiting, f.pcm = nil, nil }

// Ready is the number of frames waiting for Next.
func (f *Frames) Ready() int { return len(f.out) }

// Next returns the oldest finished frame, or nil.
func (f *Frames) Next() *codec.AudioFrame {
	if len(f.out) == 0 {
		return nil
	}
	frame := f.out[0]
	f.out[0] = nil
	f.out = f.out[1:]
	return frame
}

// Drop discards the finished frames too.
func (f *Frames) Drop() { f.out = nil }

// S16ToF32 converts 16-bit samples to float samples of the same layout.
func S16ToF32(in []byte) []byte {
	out := make([]byte, len(in)*2)
	for i := 0; i+1 < len(in); i += 2 {
		v := float32(int16(binary.LittleEndian.Uint16(in[i:]))) / 32768
		binary.LittleEndian.PutUint32(out[i*2:], math.Float32bits(v))
	}
	return out
}

// F32ToS16 converts float samples to 16-bit samples of the same layout,
// clipping what lies outside [-1, 1).
func F32ToS16(in []byte) []byte {
	out := make([]byte, len(in)/2)
	for i := 0; i+3 < len(in); i += 4 {
		v := math.Float32frombits(binary.LittleEndian.Uint32(in[i:]))
		s := math.Round(float64(v) * 32768)
		binary.LittleEndian.PutUint16(out[i/2:], uint16(int16(max(-32768, min(32767, s)))))
	}
	return out
}

// Convert returns samples in the format wanted: in as it is when the
// formats agree, a converted copy otherwise.
func Convert(in []byte, from, to codec.SampleFormat) []byte {
	switch {
	case from == to:
		return in
	case to == codec.F32:
		return S16ToF32(in)
	}
	return F32ToS16(in)
}

// PacketTimes stamps an encoder's packets, which is the same arithmetic
// on every backend: packet k codes the samples from k*Frame - Delay on,
// counted from the first sample of the signal, and what lies past the
// last sample sent is padding and not part of its duration.
type PacketTimes struct {
	SampleRate int
	TimeScale  int32
	Frame      int
	Delay      int

	base    int64 // the PTS of the signal's first sample
	sent    int64 // samples of the signal so far
	packets int64
}

// Start begins a signal whose first sample has the given PTS.
func (t *PacketTimes) Start(pts int64) { t.base, t.sent, t.packets = pts, 0, 0 }

// Sent counts samples of the signal handed to the encoder.
func (t *PacketTimes) Sent(samples int) { t.sent += int64(samples) }

// Samples is the number of samples of the signal sent so far.
func (t *PacketTimes) Samples() int64 { return t.sent }

// Next stamps the next packet. keep is false for a packet that codes
// nothing of the signal: it starts at or behind its last sample, which
// happens where an encoder is given silence to push its last samples
// out.
func (t *PacketTimes) Next(data []byte) (p codec.Packet, keep bool) {
	frame, delay := int64(t.Frame), int64(t.Delay)
	start := t.packets*frame - delay
	t.packets++
	if start >= t.sent {
		return codec.Packet{}, false
	}
	end := min(start+frame, t.sent)
	rate, scale := int64(t.SampleRate), int64(t.TimeScale)
	pts := t.base + codec.Rescale(start, rate, scale)
	p = codec.Packet{Data: data, PTS: pts, DTS: pts, Keyframe: true}
	if end > start {
		p.Duration = t.base + codec.Rescale(end, rate, scale) - pts
	}
	return p, true
}
