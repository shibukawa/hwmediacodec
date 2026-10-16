package webm

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/opus"
)

// PacketSource reads the AV1 track as a stream of packets: a
// hwmediacodec.PacketSeeker, which is what hwmediacodec.DecodeReader, the
// playback package and ebitenvideo.NewPlayerFromSource consume. Seeking
// goes through the keyframes of the index.
type PacketSource struct {
	v    *VideoTrack
	next int
}

// PacketSource returns a packet source positioned at the first frame.
func (v *VideoTrack) PacketSource() *PacketSource { return &PacketSource{v: v} }

// Codec implements hwmediacodec.PacketReader.
func (s *PacketSource) Codec() hwmediacodec.Codec { return s.v.Codec }

// TimeScale implements hwmediacodec.PacketReader (see VideoTrack.TimeScale).
func (s *PacketSource) TimeScale() int32 { return s.v.TimeScale() }

// ReadPacket implements hwmediacodec.PacketReader.
func (s *PacketSource) ReadPacket() (hwmediacodec.Packet, error) {
	if s.next >= s.v.FrameCount() {
		return hwmediacodec.Packet{}, io.EOF
	}
	p, err := s.v.Packet(s.next)
	if err != nil {
		return hwmediacodec.Packet{}, err
	}
	s.next++
	return p, nil
}

// SeekKeyframe implements hwmediacodec.PacketSeeker.
func (s *PacketSource) SeekKeyframe(t time.Duration) (time.Duration, error) {
	i := s.v.KeyframeAtOrBefore(t)
	if i < 0 {
		return 0, fmt.Errorf("webm: track %d has no keyframe", s.v.Number)
	}
	s.next = i
	return s.v.TimeOf(s.v.frames[i].ts), nil
}

// Length implements hwmediacodec.PacketSeeker.
func (s *PacketSource) Length() time.Duration { return s.v.Length() }

var _ hwmediacodec.PacketSeeker = (*PacketSource)(nil)

// AudioPacketSource reads the Opus track as a stream of packets: a
// hwmediacodec.PacketSeeker and a playback.AudioSource. The packets' PTS
// is in samples at 48 kHz with the codec delay taken off, so the
// presentation starts at 0 and the samples decoded before it are the ones
// to drop (see AudioTrack.Packet).
type AudioPacketSource struct {
	a    *AudioTrack
	next int
}

// PacketSource returns a packet source positioned at the first frame.
func (a *AudioTrack) PacketSource() *AudioPacketSource { return &AudioPacketSource{a: a} }

// Format describes the stream (see AudioTrack.Format).
func (s *AudioPacketSource) Format() hwmediacodec.AudioFormat { return s.a.Format() }

// Codec implements hwmediacodec.PacketReader.
func (s *AudioPacketSource) Codec() hwmediacodec.Codec { return hwmediacodec.Opus }

// TimeScale implements hwmediacodec.PacketReader: 48000.
func (s *AudioPacketSource) TimeScale() int32 { return opus.SampleRate }

// ReadPacket implements hwmediacodec.PacketReader.
func (s *AudioPacketSource) ReadPacket() (hwmediacodec.Packet, error) {
	if s.next >= s.a.FrameCount() {
		return hwmediacodec.Packet{}, io.EOF
	}
	p, err := s.a.Packet(s.next)
	if err != nil {
		return hwmediacodec.Packet{}, err
	}
	s.next++
	return p, nil
}

// SeekKeyframe implements hwmediacodec.PacketSeeker. Every Opus packet
// decodes on its own, but the decoder needs the track's SeekPreRoll
// (80 ms) to converge: the reader is positioned that much before t and
// the presentation time of the packet it stands at is returned. Flush the
// decoder, decode from there and drop the samples before t.
func (s *AudioPacketSource) SeekKeyframe(t time.Duration) (time.Duration, error) {
	a := s.a
	if len(a.frames) == 0 {
		return 0, fmt.Errorf("webm: track %d has no frames", a.Number)
	}
	preroll := time.Duration(a.seekPreRoll)
	if preroll <= 0 || preroll > time.Second {
		preroll = 80 * time.Millisecond
	}
	t -= preroll
	i := sort.Search(len(a.frames), func(i int) bool { return a.presentation(i) > t })
	s.next = max(i-1, 0)
	return a.presentation(s.next), nil
}

func (a *AudioTrack) presentation(i int) time.Duration {
	return time.Duration(a.pts(a.frames[i].ts) * int64(time.Second) / opus.SampleRate)
}

// Length implements hwmediacodec.PacketSeeker.
func (s *AudioPacketSource) Length() time.Duration { return s.a.Length() }

var _ hwmediacodec.PacketSeeker = (*AudioPacketSource)(nil)
