package mp4

import (
	"fmt"
	"io"
	"time"

	"github.com/shibukawa/hwmediacodec"
)

// PacketSource reads a video track as a stream of packets: a
// hwmediacodec.PacketSeeker, which is what hwmediacodec.DecodeReader, the
// playback package and ebitenvideo.NewPlayerFromSource consume. Seeking
// goes through the sync sample table.
//
// The packets' PTS and DTS are in the track's time scale with the edit
// list applied: PTS counts from the start of the presentation (the first
// picture shown is at 0 even when B-frames delay it in the media
// timeline), so DTS can be negative. VideoTrack.Packet returns the sample
// times as stored.
type PacketSource struct {
	v    *VideoTrack
	next int
}

// PacketSource returns a packet source positioned at the first sample.
func (v *VideoTrack) PacketSource() *PacketSource { return &PacketSource{v: v} }

// Codec implements hwmediacodec.PacketReader.
func (s *PacketSource) Codec() hwmediacodec.Codec { return s.v.Codec }

// TimeScale implements hwmediacodec.PacketReader: the track's time scale.
func (s *PacketSource) TimeScale() int32 { return int32(s.v.TimeScale) }

// ReadPacket implements hwmediacodec.PacketReader: the next access unit in
// decode order.
func (s *PacketSource) ReadPacket() (hwmediacodec.Packet, error) {
	if s.next >= s.v.SampleCount() {
		return hwmediacodec.Packet{}, io.EOF
	}
	au, err := s.v.AccessUnit(s.next)
	if err != nil {
		return hwmediacodec.Packet{}, err
	}
	s.next++
	off := s.v.MediaTimeOffset
	return hwmediacodec.Packet{Data: au.Data, PTS: au.PTS - off, DTS: au.DTS - off, Keyframe: au.Keyframe}, nil
}

// SeekKeyframe implements hwmediacodec.PacketSeeker.
func (s *PacketSource) SeekKeyframe(t time.Duration) (time.Duration, error) {
	i := s.v.KeyframeAtOrBefore(t)
	if i < 0 {
		return 0, fmt.Errorf("mp4: track %d has no sync sample", s.v.ID)
	}
	s.next = i
	_, pts, _ := s.v.Info(i)
	return s.v.PresentationTime(pts), nil
}

// Length implements hwmediacodec.PacketSeeker.
func (s *PacketSource) Length() time.Duration { return s.v.PresentationLength() }

var _ hwmediacodec.PacketSeeker = (*PacketSource)(nil)
