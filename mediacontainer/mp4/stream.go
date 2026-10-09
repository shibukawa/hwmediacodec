package mp4

import (
	"errors"
	"io"
)

// elementaryStream serves the access units of a video track as one
// Annex-B byte stream. It rewinds on Seek(0, io.SeekStart), which is all
// ebitenvideo needs to loop, and rejects other seeks.
type elementaryStream struct {
	v    *VideoTrack
	next int
	buf  []byte
}

// ElementaryStream returns the track as an Annex-B elementary stream (or
// the sequence of temporal units for AV1), in decode order with the
// parameter sets in front of every keyframe. It is what
// ebitenvideo.NewPlayer and annexb.NewReader consume; pass FrameRate() as
// the player's frame rate.
func (v *VideoTrack) ElementaryStream() io.ReadSeeker {
	return &elementaryStream{v: v}
}

func (s *elementaryStream) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		if s.next >= s.v.SampleCount() {
			return 0, io.EOF
		}
		au, err := s.v.AccessUnit(s.next)
		if err != nil {
			return 0, err
		}
		s.next++
		s.buf = au.Data
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *elementaryStream) Seek(offset int64, whence int) (int64, error) {
	if offset != 0 || whence != io.SeekStart {
		return 0, errors.New("mp4: an elementary stream can only be rewound to its start")
	}
	s.next = 0
	s.buf = nil
	return 0, nil
}
