package container

import "github.com/shibukawa/hwmediacodec"

// VideoFile is an MP4 file with a single video track fed with encoder
// packets. It satisfies screencast.Sink, so a Recorder can write straight
// into it.
type VideoFile struct {
	m  *Muxer
	vw *VideoWriter
}

// CreateVideoFile creates the file at path with one H.264 or HEVC track.
// The time scale is the unit of Packet.PTS and Packet.DTS
// (screencast.TimeScale for a Recorder).
func CreateVideoFile(path string, c hwmediacodec.Codec, timeScale uint32) (*VideoFile, error) {
	m, err := Create(path)
	if err != nil {
		return nil, err
	}
	vw, err := m.AddVideoTrack(c, timeScale)
	if err != nil {
		m.Close()
		return nil, err
	}
	return &VideoFile{m: m, vw: vw}, nil
}

// WritePacket appends one packet to the track.
func (f *VideoFile) WritePacket(p hwmediacodec.Packet) error { return f.vw.WritePacket(p) }

// Close writes the sample tables and closes the file.
func (f *VideoFile) Close() error { return f.m.Close() }
