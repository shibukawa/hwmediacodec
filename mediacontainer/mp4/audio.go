package mp4

import (
	"fmt"
	"io"
	"math"
	"time"

	mp4ff "github.com/Eyevinn/mp4ff/mp4"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/aac"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// objectTypeAAC is the objectTypeIndication of MPEG-4 audio in an esds box.
const objectTypeAAC = 0x40

// opusPreroll is how far before a seek target an Opus decoder has to start
// for its output to have converged (RFC 7845, section 4.6).
const opusPreroll = 80 * time.Millisecond

// AudioTrack is the decodable audio track of a file: AAC ("mp4a") or Opus.
type AudioTrack struct {
	Track
	format hwmediacodec.AudioFormat
}

func (d *Demuxer) newAudioTrack(t *Track) *AudioTrack {
	stsd := t.Trak.Mdia.Minf.Stbl.Stsd
	if stsd == nil || len(stsd.Children) == 0 || t.TimeScale == 0 {
		return nil
	}
	entry, ok := stsd.Children[0].(*mp4ff.AudioSampleEntryBox)
	if !ok {
		return nil
	}
	a := &AudioTrack{Track: *t}
	f := &a.format
	switch entry.Type() {
	case "mp4a":
		if entry.Esds == nil || entry.Esds.DecConfigDescriptor == nil || entry.Esds.DecConfigDescriptor.DecSpecificInfo == nil ||
			entry.Esds.DecConfigDescriptor.ObjectType != objectTypeAAC {
			return nil
		}
		asc := entry.Esds.DecConfigDescriptor.DecSpecificInfo.DecConfig
		cfg, err := aac.ParseConfig(asc)
		if err != nil {
			return nil
		}
		f.Codec = hwmediacodec.AAC
		f.Config = append([]byte(nil), asc...)
		f.SampleRate = cfg.OutputSampleRate()
		f.Channels = cfg.OutputChannels()
		if f.Channels == 0 {
			f.Channels = int(entry.ChannelCount)
		}
		f.FrameSamples = cfg.OutputFrameSamples()
	case "Opus":
		if entry.Dops == nil {
			return nil
		}
		head := opus.Head{
			Channels:        int(entry.Dops.OutputChannelCount),
			PreSkip:         int(entry.Dops.PreSkip),
			InputSampleRate: int(entry.Dops.InputSampleRate),
			OutputGain:      entry.Dops.OutputGain,
			MappingFamily:   entry.Dops.ChannelMappingFamily,
		}
		if head.MappingFamily != 0 {
			head.Mapping = append([]byte{entry.Dops.StreamCount, entry.Dops.CoupledCount}, entry.Dops.ChannelMapping...)
		}
		f.Codec = hwmediacodec.Opus
		f.Config = head.Marshal()
		f.SampleRate = opus.SampleRate
		f.Channels = head.Channels
		f.Delay = head.PreSkip
		if len(a.samples) > 0 {
			f.FrameSamples = int(codec.Rescale(int64(a.dur(0)), int64(t.TimeScale), opus.SampleRate))
		}
	default:
		return nil
	}
	if t.MediaTimeOffset > 0 {
		// The edit list is where a muxer records the samples to drop.
		f.Delay = int(codec.Rescale(t.MediaTimeOffset, int64(t.TimeScale), int64(f.SampleRate)))
	}
	return a
}

// Format describes the stream: hand it to hwmediacodec.NewAudioDecoder.
// Its Delay is the start of the presentation according to the edit list
// (for Opus without one, the pre-skip of the stream).
func (a *AudioTrack) Format() hwmediacodec.AudioFormat {
	f := a.format
	f.Config = append([]byte(nil), f.Config...)
	return f
}

// Packet reads sample i as a decoder packet with the times as stored.
func (a *AudioTrack) Packet(i int) (hwmediacodec.Packet, error) {
	s, err := a.Sample(i)
	if err != nil {
		return hwmediacodec.Packet{}, err
	}
	return hwmediacodec.Packet{Data: s.Data, PTS: s.PTS, DTS: s.DTS, Duration: int64(s.Duration), Keyframe: true}, nil
}

// AudioPacketSource reads an audio track as a stream of packets: a
// hwmediacodec.PacketSeeker.
//
// The packets' PTS is in the track's time scale with the edit list
// applied, like PacketSource's: the presentation starts at 0, so the
// packets that carry the encoder's delay start before it and the samples
// decoded from them up to 0 are the ones to drop.
type AudioPacketSource struct {
	a    *AudioTrack
	next int
}

// PacketSource returns a packet source positioned at the first sample.
func (a *AudioTrack) PacketSource() *AudioPacketSource { return &AudioPacketSource{a: a} }

// Format describes the stream (see AudioTrack.Format).
func (s *AudioPacketSource) Format() hwmediacodec.AudioFormat { return s.a.Format() }

// Codec implements hwmediacodec.PacketReader.
func (s *AudioPacketSource) Codec() hwmediacodec.Codec { return s.a.format.Codec }

// TimeScale implements hwmediacodec.PacketReader: the track's time scale.
func (s *AudioPacketSource) TimeScale() int32 { return int32(s.a.TimeScale) }

// ReadPacket implements hwmediacodec.PacketReader.
func (s *AudioPacketSource) ReadPacket() (hwmediacodec.Packet, error) {
	if s.next >= s.a.SampleCount() {
		return hwmediacodec.Packet{}, io.EOF
	}
	p, err := s.a.Packet(s.next)
	if err != nil {
		return hwmediacodec.Packet{}, err
	}
	s.next++
	p.PTS -= s.a.MediaTimeOffset
	p.DTS -= s.a.MediaTimeOffset
	return p, nil
}

// SeekKeyframe implements hwmediacodec.PacketSeeker. Every audio packet
// decodes on its own, but the first samples after a jump are not right
// until the decoder has run in: the reader is positioned that much before
// t (one packet for AAC, 80 ms for Opus) and the presentation time of the
// packet it stands at is returned. Flush the decoder, decode from there
// and drop the samples before t.
func (s *AudioPacketSource) SeekKeyframe(t time.Duration) (time.Duration, error) {
	a := s.a
	if len(a.samples) == 0 {
		return 0, fmt.Errorf("mp4: track %d has no samples", a.ID)
	}
	if a.format.Codec == hwmediacodec.Opus {
		t -= opusPreroll
	}
	// The last sample that starts at or before t. Compare durations so
	// converting a long-running stream does not overflow t * TimeScale.
	lo, hi := 0, len(a.samples)
	for lo < hi {
		mid := (lo + hi) / 2
		if a.PresentationTime(a.samples[mid].dts) <= t {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	i := max(lo-1, 0)
	if a.format.Codec == hwmediacodec.AAC && i > 0 {
		i--
	}
	s.next = i
	return a.PresentationTime(a.samples[i].dts), nil
}

// Length implements hwmediacodec.PacketSeeker.
func (s *AudioPacketSource) Length() time.Duration { return s.a.PresentationLength() }

var _ hwmediacodec.PacketSeeker = (*AudioPacketSource)(nil)

// AudioWriter receives hwmediacodec packets for an audio track.
type AudioWriter struct {
	*trackWriter
	format hwmediacodec.AudioFormat
	// frame is the duration of a packet that does not give its own.
	frame uint32
}

// AddAudioTrack adds an AAC or Opus track whose samples are written with
// WritePacket: pass AudioEncoder.Format. The time scale is the unit of
// Packet.PTS and Packet.Duration, which for an encoder opened without
// WithTimeScale is the sample rate. The encoder's delay goes into the
// track's edit list, so that players start at the first real sample.
func (m *Muxer) AddAudioTrack(format hwmediacodec.AudioFormat, timeScale uint32) (*AudioWriter, error) {
	if format.Codec != hwmediacodec.AAC && format.Codec != hwmediacodec.Opus {
		return nil, fmt.Errorf("mp4: cannot write %s samples", format.Codec)
	}
	if format.SampleRate <= 0 || format.Channels <= 0 || timeScale == 0 || timeScale > math.MaxInt32 {
		return nil, fmt.Errorf("mp4: an audio track needs a sample rate, a channel count and a time scale, got %d Hz, %d channels and %d", format.SampleRate, format.Channels, timeScale)
	}
	a := &AudioWriter{format: format}
	a.format.Config = append([]byte(nil), format.Config...)
	if _, err := a.sampleEntry(); err != nil {
		return nil, err
	}
	a.frame = uint32(codec.Rescale(int64(format.FrameSamples), int64(format.SampleRate), int64(timeScale)))
	a.trackWriter = m.addTrack(timeScale, a.buildTrak)
	a.trackWriter.skip = codec.Rescale(int64(format.Delay), int64(format.SampleRate), int64(timeScale))
	return a, nil
}

// WritePacket writes one packet from an audio encoder.
func (a *AudioWriter) WritePacket(p hwmediacodec.Packet) error {
	if p.Duration > math.MaxUint32 {
		return fmt.Errorf("mp4: audio sample duration exceeds the 32-bit field")
	}
	dur := uint32(p.Duration)
	if p.Duration <= 0 {
		dur = a.frame
	}
	return a.m.write(a.trackWriter, p.Data, outSample{dts: p.PTS, pts: p.PTS, dur: dur, sync: true})
}

func (a *AudioWriter) sampleEntry() (*mp4ff.AudioSampleEntryBox, error) {
	f := a.format
	rate := uint16(0) // a rate that does not fit the field is left to the codec configuration
	if f.SampleRate < 1<<16 {
		rate = uint16(f.SampleRate)
	}
	switch f.Codec {
	case hwmediacodec.AAC:
		asc := f.Config
		if len(asc) == 0 {
			var err error
			if asc, err = aac.LCConfig(f.SampleRate, f.Channels); err != nil {
				return nil, fmt.Errorf("mp4: %w", err)
			}
		}
		return mp4ff.CreateAudioSampleEntryBox("mp4a", uint16(f.Channels), 16, rate, mp4ff.CreateEsdsBox(asc)), nil
	default:
		head := opus.Head{Channels: f.Channels, PreSkip: f.Delay, InputSampleRate: f.SampleRate}
		if len(f.Config) > 0 {
			var err error
			if head, err = opus.ParseHead(f.Config); err != nil {
				return nil, fmt.Errorf("mp4: %w", err)
			}
		}
		dops := &mp4ff.DopsBox{
			OutputChannelCount:   byte(head.Channels),
			PreSkip:              uint16(head.PreSkip),
			InputSampleRate:      uint32(head.InputSampleRate),
			OutputGain:           head.OutputGain,
			ChannelMappingFamily: head.MappingFamily,
		}
		if head.MappingFamily != 0 && len(head.Mapping) >= 2 {
			dops.StreamCount, dops.CoupledCount, dops.ChannelMapping = head.Mapping[0], head.Mapping[1], head.Mapping[2:]
		}
		return mp4ff.CreateAudioSampleEntryBox("Opus", uint16(head.Channels), 16, opus.SampleRate, dops), nil
	}
}

func (a *AudioWriter) buildTrak(stbl *mp4ff.StblBox) (*mp4ff.TrakBox, error) {
	entry, err := a.sampleEntry()
	if err != nil {
		return nil, err
	}
	stsd := mp4ff.NewStsdBox()
	stsd.AddChild(entry)
	hdlr, _ := mp4ff.CreateHdlr("audio")
	trak := newTrak(a.trackWriter, hdlr, mp4ff.CreateSmhd(), stsd, stbl, 0, 0, "und")
	trak.Tkhd.Volume = 0x0100
	return trak, nil
}
