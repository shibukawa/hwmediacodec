package mp4

import (
	"fmt"
	"io"
	"time"

	"github.com/shibukawa/hwmediacodec"
)

// PacketSource reads a video track as a stream of packets with
// presentation times, which is what ebitenvideo.NewPlayerFromSource
// consumes; it also satisfies ebitenvideo.Seeker through the sync sample
// table, so the player can seek and loop. The package does not import
// ebitenvideo: the interfaces are matched structurally.
type PacketSource struct {
	v    *VideoTrack
	next int
}

// PacketSource returns a packet source positioned at the first sample.
func (v *VideoTrack) PacketSource() *PacketSource { return &PacketSource{v: v} }

// Codec implements ebitenvideo.Source.
func (s *PacketSource) Codec() hwmediacodec.Codec { return s.v.Codec }

// ReadPacket implements ebitenvideo.Source: the next access unit in decode
// order with its presentation time (edit list applied).
func (s *PacketSource) ReadPacket() ([]byte, time.Duration, error) {
	if s.next >= s.v.SampleCount() {
		return nil, 0, io.EOF
	}
	au, err := s.v.AccessUnit(s.next)
	if err != nil {
		return nil, 0, err
	}
	s.next++
	return au.Data, s.v.PresentationTime(au.PTS), nil
}

// SeekKeyframe implements ebitenvideo.Seeker.
func (s *PacketSource) SeekKeyframe(t time.Duration) (time.Duration, error) {
	i := s.v.KeyframeAtOrBefore(t)
	if i < 0 {
		return 0, fmt.Errorf("mp4: track %d has no sync sample", s.v.ID)
	}
	s.next = i
	_, pts, _ := s.v.Info(i)
	return s.v.PresentationTime(pts), nil
}

// Length implements ebitenvideo.Seeker.
func (s *PacketSource) Length() time.Duration { return s.v.PresentationLength() }
